package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/shopspring/decimal"

	"xr-trading/market-info-service/internal/application"
	"xr-trading/market-info-service/internal/database/sqlbuilder"
	"xr-trading/market-info-service/internal/domain"
)

// CatalogRepository reads entities from the core catalog and stores provider
// configuration in market_data.
type CatalogRepository struct {
	pool CatalogDatabase
}

// CatalogDatabase is the narrow pgx contract used by catalog reads and
// configuration writes. It keeps runtime assembly testable without exposing a
// concrete connection-pool implementation to the application entrypoint.
type CatalogDatabase interface {
	Exec(context.Context, string, ...any) (pgconn.CommandTag, error)
	QueryRow(context.Context, string, ...any) pgx.Row
	Query(context.Context, string, ...any) (pgx.Rows, error)
}

// catalogDatabase remains the package-local name used by the other
// PostgreSQL repositories that share this pgx contract.
type catalogDatabase = CatalogDatabase

// NewCatalogRepository constructs a catalog repository over pool.
func NewCatalogRepository(pool CatalogDatabase) (*CatalogRepository, error) {
	if pool == nil {
		return nil, errors.New("PostgreSQL pool is required")
	}
	return newCatalogRepository(pool)
}

func newCatalogRepository(pool CatalogDatabase) (*CatalogRepository, error) {
	if pool == nil {
		return nil, errors.New("PostgreSQL pool is required")
	}
	return &CatalogRepository{pool: pool}, nil
}

// ListInstrumentProviderOptions returns a flattened, read-optimized page. The
// CTE limits Instruments before joining Provider rows, so an Instrument is
// never split across pages.
func (repository *CatalogRepository) ListInstrumentProviderOptions(ctx context.Context, filter application.InstrumentOptionsFilter) ([]application.InstrumentProviderOptionRecord, error) {
	if filter.AssetID.IsZero() || filter.EffectiveAt.IsZero() || filter.InstrumentLimit <= 0 || filter.InstrumentLimit > application.MaximumInstrumentOptionsPageSize+1 {
		return nil, fmt.Errorf("list instrument provider options: %w", domain.ErrInvalidData)
	}
	afterCode := ""
	if filter.AfterInstrumentCode != nil {
		afterCode = filter.AfterInstrumentCode.String()
		if afterCode == "" {
			return nil, fmt.Errorf("list instrument provider options: %w", domain.ErrInvalidData)
		}
	}

	rows, err := repository.pool.Query(ctx, `
WITH selected_instruments AS (
    SELECT
        i.id,
        i.code,
        COALESCE(NULLIF(i.metadata ->> 'display_name', ''), i.symbol) AS display_name
    FROM core.instruments AS i
    WHERE i.asset_id = $1
      AND i.status = 'active'
      AND (i.valid_from IS NULL OR i.valid_from <= $2)
      AND (i.valid_to IS NULL OR i.valid_to > $2)
      AND ($3::text = '' OR i.code > $3)
      AND EXISTS (
          SELECT 1
          FROM market_data.provider_instruments AS available_mapping
          JOIN market_data.providers AS available_provider
            ON available_provider.id = available_mapping.provider_id
          WHERE available_mapping.instrument_id = i.id
            AND available_mapping.enabled = true
            AND (available_mapping.valid_from IS NULL OR available_mapping.valid_from <= $2)
            AND (available_mapping.valid_to IS NULL OR available_mapping.valid_to > $2)
            AND available_provider.status IN ('active', 'degraded')
      )
    ORDER BY i.code ASC
    LIMIT $4
)
SELECT
    selected.id,
    selected.code,
    selected.display_name,
    mapping.id,
    mapping.code,
    provider.code,
    provider.name,
    mapping.is_default,
    mapping.priority,
    mapping.capabilities
FROM selected_instruments AS selected
JOIN market_data.provider_instruments AS mapping
  ON mapping.instrument_id = selected.id
JOIN market_data.providers AS provider
  ON provider.id = mapping.provider_id
WHERE mapping.enabled = true
  AND (mapping.valid_from IS NULL OR mapping.valid_from <= $2)
  AND (mapping.valid_to IS NULL OR mapping.valid_to > $2)
  AND provider.status IN ('active', 'degraded')
ORDER BY
    selected.code ASC,
    mapping.is_default DESC,
    mapping.priority ASC,
    provider.code ASC,
    mapping.code ASC`, IDToDatabase(filter.AssetID), TimeToDatabase(filter.EffectiveAt), afterCode, filter.InstrumentLimit)
	if err != nil {
		return nil, fmt.Errorf("list instrument provider options: %w", MapError(err))
	}
	defer rows.Close()

	records := make([]application.InstrumentProviderOptionRecord, 0)
	for rows.Next() {
		record, scanErr := scanInstrumentProviderOptionRecord(rows)
		if scanErr != nil {
			return nil, fmt.Errorf("scan instrument provider option: %w", scanErr)
		}
		records = append(records, record)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate instrument provider options: %w", MapError(err))
	}
	return records, nil
}

func scanInstrumentProviderOptionRecord(row scanner) (application.InstrumentProviderOptionRecord, error) {
	var record application.InstrumentProviderOptionRecord
	var instrumentID, providerInstrumentID uuid.UUID
	var instrumentCode, providerInstrumentCode, providerCode string
	var capabilities []byte
	if err := row.Scan(
		&instrumentID, &instrumentCode, &record.InstrumentDisplayName,
		&providerInstrumentID, &providerInstrumentCode, &providerCode, &record.ProviderDisplayName,
		&record.IsDefault, &record.Priority, &capabilities,
	); err != nil {
		return application.InstrumentProviderOptionRecord{}, err
	}
	parsedInstrumentCode, err := domain.ParseCode(instrumentCode)
	if err != nil {
		return application.InstrumentProviderOptionRecord{}, err
	}
	parsedProviderInstrumentCode, err := domain.ParseCode(providerInstrumentCode)
	if err != nil {
		return application.InstrumentProviderOptionRecord{}, err
	}
	parsedProviderCode, err := domain.ParseCode(providerCode)
	if err != nil {
		return application.InstrumentProviderOptionRecord{}, err
	}
	parsedCapabilities, err := domain.ParseProviderCapabilities(capabilities)
	if err != nil {
		return application.InstrumentProviderOptionRecord{}, err
	}
	record.InstrumentID = IDFromDatabase(instrumentID)
	record.InstrumentCode = parsedInstrumentCode
	record.ProviderInstrumentID = IDFromDatabase(providerInstrumentID)
	record.ProviderInstrumentCode = parsedProviderInstrumentCode
	record.ProviderCode = parsedProviderCode
	record.Capabilities = parsedCapabilities
	return record, nil
}

// FindAssetByCode returns a core Asset identified by its stable readable code.
func (repository *CatalogRepository) FindAssetByCode(ctx context.Context, code string) (domain.Asset, error) {
	query, args, err := sqlbuilder.Select(assetColumns...).From("core.assets").Where(sqlbuilder.Eq("code", code)).Build()
	if err != nil {
		return domain.Asset{}, fmt.Errorf("build asset query: %w", err)
	}
	asset, err := scanAsset(repository.pool.QueryRow(ctx, query, args...))
	if err != nil {
		return domain.Asset{}, fmt.Errorf("find asset by code: %w", MapError(err))
	}
	return asset, nil
}

// FindAssetByID returns a core Asset for an Instrument relationship.
func (repository *CatalogRepository) FindAssetByID(ctx context.Context, id domain.ID) (domain.Asset, error) {
	if id.IsZero() {
		return domain.Asset{}, fmt.Errorf("find asset by ID: %w", domain.ErrInvalidData)
	}
	query, args, err := sqlbuilder.Select(assetColumns...).From("core.assets").Where(sqlbuilder.Eq("id", IDToDatabase(id))).Build()
	if err != nil {
		return domain.Asset{}, fmt.Errorf("build asset query: %w", err)
	}
	asset, err := scanAsset(repository.pool.QueryRow(ctx, query, args...))
	if err != nil {
		return domain.Asset{}, fmt.Errorf("find asset by ID: %w", MapError(err))
	}
	return asset, nil
}

// FindInstrumentByCode returns a core Instrument identified by its stable
// readable code.
func (repository *CatalogRepository) FindInstrumentByCode(ctx context.Context, code string) (domain.Instrument, error) {
	query, args, err := sqlbuilder.Select(instrumentColumns...).From("core.instruments").Where(sqlbuilder.Eq("code", code)).Build()
	if err != nil {
		return domain.Instrument{}, fmt.Errorf("build instrument query: %w", err)
	}
	instrument, err := scanInstrument(repository.pool.QueryRow(ctx, query, args...))
	if err != nil {
		return domain.Instrument{}, fmt.Errorf("find instrument by code: %w", MapError(err))
	}
	return instrument, nil
}

// FindInstrumentsByIDs returns every core Instrument matching one of ids.
// Absent ids are simply missing from the result; callers detect gaps by
// comparing the requested set against the returned set.
func (repository *CatalogRepository) FindInstrumentsByIDs(ctx context.Context, ids []domain.ID) ([]domain.Instrument, error) {
	if len(ids) == 0 {
		return nil, fmt.Errorf("find instruments by IDs: %w", domain.ErrInvalidData)
	}
	databaseIDs := make([]uuid.UUID, len(ids))
	for index, id := range ids {
		if id.IsZero() {
			return nil, fmt.Errorf("find instruments by IDs: %w", domain.ErrInvalidData)
		}
		databaseIDs[index] = IDToDatabase(id)
	}
	query, args, err := sqlbuilder.Select(instrumentColumns...).From("core.instruments").
		Where(sqlbuilder.Raw("id = ANY(?)", databaseIDs)).Build()
	if err != nil {
		return nil, fmt.Errorf("build instruments by IDs query: %w", err)
	}

	rows, err := repository.pool.Query(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("find instruments by IDs: %w", MapError(err))
	}
	defer rows.Close()

	instruments := make([]domain.Instrument, 0, len(ids))
	for rows.Next() {
		instrument, scanErr := scanInstrument(rows)
		if scanErr != nil {
			return nil, fmt.Errorf("scan instrument: %w", MapError(scanErr))
		}
		instruments = append(instruments, instrument)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate instruments by IDs: %w", MapError(err))
	}
	return instruments, nil
}

// CreateProvider stores a Provider in the market_data schema.
func (repository *CatalogRepository) CreateProvider(ctx context.Context, provider domain.Provider) error {
	provider, err := domain.NewProvider(provider)
	if err != nil {
		return fmt.Errorf("create provider: %w", err)
	}
	_, err = repository.pool.Exec(ctx, `
INSERT INTO market_data.providers (
    id, code, name, provider_type, status, metadata, created_at, updated_at
) VALUES ($1, $2, $3, $4, $5, $6::jsonb, $7, $8)`,
		IDToDatabase(provider.ID), provider.Code.String(), provider.Name, string(provider.ProviderType),
		string(provider.Status), jsonValue(provider.Metadata), TimeToDatabase(provider.CreatedAt), TimeToDatabase(provider.UpdatedAt),
	)
	if err != nil {
		return fmt.Errorf("create provider: %w", MapError(err))
	}
	return nil
}

// FindProviderByCode returns a Provider identified by its stable readable code.
func (repository *CatalogRepository) FindProviderByCode(ctx context.Context, code string) (domain.Provider, error) {
	query, args, err := sqlbuilder.Select(providerColumns...).From("market_data.providers").Where(sqlbuilder.Eq("code", code)).Build()
	if err != nil {
		return domain.Provider{}, fmt.Errorf("build provider query: %w", err)
	}
	provider, err := scanProvider(repository.pool.QueryRow(ctx, query, args...))
	if err != nil {
		return domain.Provider{}, fmt.Errorf("find provider by code: %w", MapError(err))
	}
	return provider, nil
}

// CreateProviderInstrument stores a provider-specific Instrument mapping.
func (repository *CatalogRepository) CreateProviderInstrument(ctx context.Context, mapping domain.ProviderInstrument) error {
	mapping, err := domain.NewProviderInstrument(mapping)
	if err != nil {
		return fmt.Errorf("create provider instrument: %w", err)
	}
	capabilities, err := json.Marshal(mapping.Capabilities)
	if err != nil {
		return fmt.Errorf("encode provider instrument capabilities: %w", err)
	}
	_, err = repository.pool.Exec(ctx, `
INSERT INTO market_data.provider_instruments (
    id, code, provider_id, instrument_id, external_symbol, provider_market,
    capabilities, priority, is_default, enabled, valid_from, valid_to, metadata,
    created_at, updated_at
) VALUES (
    $1, $2, $3, $4, $5, $6, $7::jsonb, $8, $9, $10, $11, $12, $13::jsonb, $14, $15
)`,
		IDToDatabase(mapping.ID), mapping.Code.String(), IDToDatabase(mapping.ProviderID), IDToDatabase(mapping.InstrumentID),
		mapping.ExternalSymbol, mapping.ProviderMarket, string(capabilities), mapping.Priority,
		mapping.IsDefault, mapping.Enabled, optionalTimeToDatabase(mapping.ValidFrom), optionalTimeToDatabase(mapping.ValidTo),
		jsonValue(mapping.Metadata), TimeToDatabase(mapping.CreatedAt), TimeToDatabase(mapping.UpdatedAt),
	)
	if err != nil {
		return fmt.Errorf("create provider instrument: %w", MapError(err))
	}
	return nil
}

// ListActiveProviderInstruments returns enabled, currently-valid provider
// mappings. The default mapping is returned first, then priority and code.
func (repository *CatalogRepository) ListActiveProviderInstruments(ctx context.Context, instrumentID domain.ID) ([]domain.ProviderInstrument, error) {
	if instrumentID.IsZero() {
		return nil, fmt.Errorf("list active provider instruments: %w", domain.ErrInvalidData)
	}
	query, args, err := sqlbuilder.Select(providerInstrumentColumns...).From("market_data.provider_instruments").
		Where(sqlbuilder.Eq("instrument_id", IDToDatabase(instrumentID))).
		And(sqlbuilder.Raw("enabled = true AND valid_to IS NULL")).
		OrderBy("is_default DESC", "priority ASC", "code ASC").Build()
	if err != nil {
		return nil, fmt.Errorf("build provider instrument query: %w", err)
	}

	rows, err := repository.pool.Query(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("list active provider instruments: %w", MapError(err))
	}
	defer rows.Close()

	mappings := make([]domain.ProviderInstrument, 0)
	for rows.Next() {
		mapping, err := scanProviderInstrument(rows)
		if err != nil {
			return nil, fmt.Errorf("scan provider instrument: %w", MapError(err))
		}
		mappings = append(mappings, mapping)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate provider instruments: %w", MapError(err))
	}
	return mappings, nil
}

var (
	assetColumns              = []string{"id", "code", "asset_type", "canonical_symbol", "name", "status", "metadata", "created_at", "updated_at"}
	instrumentColumns         = []string{"id", "code", "asset_id", "venue", "instrument_type", "symbol", "quote_asset_id", "quote_currency", "market_timezone", "price_scale", "quantity_scale", "lot_size", "min_quantity", "status", "valid_from", "valid_to", "metadata", "created_at", "updated_at"}
	providerColumns           = []string{"id", "code", "name", "provider_type", "status", "metadata", "created_at", "updated_at"}
	providerInstrumentColumns = []string{"id", "code", "provider_id", "instrument_id", "external_symbol", "provider_market", "capabilities", "priority", "is_default", "enabled", "valid_from", "valid_to", "metadata", "created_at", "updated_at"}
)

type scanner interface {
	Scan(...any) error
}

func scanAsset(row scanner) (domain.Asset, error) {
	var asset domain.Asset
	var id uuid.UUID
	var code, assetType, status string
	var metadata []byte
	if err := row.Scan(&id, &code, &assetType, &asset.CanonicalSymbol, &asset.Name, &status, &metadata, &asset.CreatedAt, &asset.UpdatedAt); err != nil {
		return domain.Asset{}, err
	}
	parsedCode, err := domain.ParseCode(code)
	if err != nil {
		return domain.Asset{}, err
	}
	parsedType, err := domain.ParseAssetType(assetType)
	if err != nil {
		return domain.Asset{}, err
	}
	parsedStatus, err := domain.ParseAssetStatus(status)
	if err != nil {
		return domain.Asset{}, err
	}
	asset.ID = IDFromDatabase(id)
	asset.Code = parsedCode
	asset.AssetType = parsedType
	asset.Status = parsedStatus
	asset.Metadata = copyJSON(metadata)
	asset.CreatedAt = TimeToDatabase(asset.CreatedAt)
	asset.UpdatedAt = TimeToDatabase(asset.UpdatedAt)
	return domain.NewAsset(asset)
}

func scanInstrument(row scanner) (domain.Instrument, error) {
	var instrument domain.Instrument
	var id, assetID uuid.UUID
	var quoteAssetID *uuid.UUID
	var code, instrumentType, status string
	var lotSize, minQuantity *decimal.Decimal
	var metadata []byte
	if err := row.Scan(
		&id, &code, &assetID, &instrument.Venue, &instrumentType, &instrument.Symbol,
		&quoteAssetID, &instrument.QuoteCurrency, &instrument.MarketTimezone,
		&instrument.PriceScale, &instrument.QuantityScale, &lotSize, &minQuantity,
		&status, &instrument.ValidFrom, &instrument.ValidTo, &metadata,
		&instrument.CreatedAt, &instrument.UpdatedAt,
	); err != nil {
		return domain.Instrument{}, err
	}
	parsedCode, err := domain.ParseCode(code)
	if err != nil {
		return domain.Instrument{}, err
	}
	parsedType, err := domain.ParseInstrumentType(instrumentType)
	if err != nil {
		return domain.Instrument{}, err
	}
	parsedStatus, err := domain.ParseInstrumentStatus(status)
	if err != nil {
		return domain.Instrument{}, err
	}
	instrument.ID = IDFromDatabase(id)
	instrument.Code = parsedCode
	instrument.AssetID = IDFromDatabase(assetID)
	instrument.InstrumentType = parsedType
	instrument.QuoteAssetID = optionalIDFromDatabase(quoteAssetID)
	instrument.LotSize = optionalDecimalFromDatabase(lotSize)
	instrument.MinQuantity = optionalDecimalFromDatabase(minQuantity)
	instrument.Status = parsedStatus
	instrument.ValidFrom = optionalTimeFromDatabase(instrument.ValidFrom)
	instrument.ValidTo = optionalTimeFromDatabase(instrument.ValidTo)
	instrument.Metadata = copyJSON(metadata)
	instrument.CreatedAt = TimeToDatabase(instrument.CreatedAt)
	instrument.UpdatedAt = TimeToDatabase(instrument.UpdatedAt)
	return domain.NewInstrument(instrument)
}

func scanProvider(row scanner) (domain.Provider, error) {
	var provider domain.Provider
	var id uuid.UUID
	var code, providerType, status string
	var metadata []byte
	if err := row.Scan(&id, &code, &provider.Name, &providerType, &status, &metadata, &provider.CreatedAt, &provider.UpdatedAt); err != nil {
		return domain.Provider{}, err
	}
	parsedCode, err := domain.ParseCode(code)
	if err != nil {
		return domain.Provider{}, err
	}
	parsedType, err := domain.ParseProviderType(providerType)
	if err != nil {
		return domain.Provider{}, err
	}
	parsedStatus, err := domain.ParseProviderStatus(status)
	if err != nil {
		return domain.Provider{}, err
	}
	provider.ID = IDFromDatabase(id)
	provider.Code = parsedCode
	provider.ProviderType = parsedType
	provider.Status = parsedStatus
	provider.Metadata = copyJSON(metadata)
	provider.CreatedAt = TimeToDatabase(provider.CreatedAt)
	provider.UpdatedAt = TimeToDatabase(provider.UpdatedAt)
	return domain.NewProvider(provider)
}

func scanProviderInstrument(row scanner) (domain.ProviderInstrument, error) {
	var mapping domain.ProviderInstrument
	var id, providerID, instrumentID uuid.UUID
	var code string
	var capabilities, metadata []byte
	if err := row.Scan(&id, &code, &providerID, &instrumentID, &mapping.ExternalSymbol, &mapping.ProviderMarket, &capabilities, &mapping.Priority, &mapping.IsDefault, &mapping.Enabled, &mapping.ValidFrom, &mapping.ValidTo, &metadata, &mapping.CreatedAt, &mapping.UpdatedAt); err != nil {
		return domain.ProviderInstrument{}, err
	}
	parsedCode, err := domain.ParseCode(code)
	if err != nil {
		return domain.ProviderInstrument{}, err
	}
	parsedCapabilities, err := domain.ParseProviderCapabilities(capabilities)
	if err != nil {
		return domain.ProviderInstrument{}, err
	}
	mapping.ID = IDFromDatabase(id)
	mapping.Code = parsedCode
	mapping.ProviderID = IDFromDatabase(providerID)
	mapping.InstrumentID = IDFromDatabase(instrumentID)
	mapping.Capabilities = parsedCapabilities
	mapping.Metadata = copyJSON(metadata)
	mapping.ValidFrom = optionalTimeFromDatabase(mapping.ValidFrom)
	mapping.ValidTo = optionalTimeFromDatabase(mapping.ValidTo)
	mapping.CreatedAt = TimeToDatabase(mapping.CreatedAt)
	mapping.UpdatedAt = TimeToDatabase(mapping.UpdatedAt)
	return domain.NewProviderInstrument(mapping)
}

func optionalIDFromDatabase(value *uuid.UUID) *domain.ID {
	if value == nil {
		return nil
	}
	converted := IDFromDatabase(*value)
	return &converted
}

func optionalTimeFromDatabase(value *time.Time) *time.Time {
	if value == nil {
		return nil
	}
	converted := TimeToDatabase(*value)
	return &converted
}

func optionalTimeToDatabase(value *time.Time) any {
	if value == nil {
		return nil
	}
	return TimeToDatabase(*value)
}

func optionalDecimalFromDatabase(value *decimal.Decimal) *domain.Decimal {
	if value == nil {
		return nil
	}
	converted := domain.DecimalFromExact(*value)
	return &converted
}

func jsonValue(value json.RawMessage) string {
	if len(value) == 0 {
		return "{}"
	}
	return string(value)
}

func copyJSON(value []byte) json.RawMessage {
	if len(value) == 0 {
		return json.RawMessage("{}")
	}
	return append(json.RawMessage(nil), value...)
}

var _ domain.CatalogRepository = (*CatalogRepository)(nil)
var _ domain.ProviderRepository = (*CatalogRepository)(nil)
