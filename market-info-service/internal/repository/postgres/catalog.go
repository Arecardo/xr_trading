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
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/shopspring/decimal"

	"xr-trading/market-info-service/internal/database/sqlbuilder"
	"xr-trading/market-info-service/internal/domain"
)

// CatalogRepository reads entities from the core catalog and stores provider
// configuration in market_data.
type CatalogRepository struct {
	pool catalogDatabase
}

type catalogDatabase interface {
	Exec(context.Context, string, ...any) (pgconn.CommandTag, error)
	QueryRow(context.Context, string, ...any) pgx.Row
	Query(context.Context, string, ...any) (pgx.Rows, error)
}

// NewCatalogRepository constructs a catalog repository over pool.
func NewCatalogRepository(pool *pgxpool.Pool) (*CatalogRepository, error) {
	if pool == nil {
		return nil, errors.New("PostgreSQL pool is required")
	}
	return newCatalogRepository(pool)
}

func newCatalogRepository(pool catalogDatabase) (*CatalogRepository, error) {
	if pool == nil {
		return nil, errors.New("PostgreSQL pool is required")
	}
	return &CatalogRepository{pool: pool}, nil
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
