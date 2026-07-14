package postgres

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"

	"xr-trading/market-info-service/internal/application"
	"xr-trading/market-info-service/internal/database/sqlbuilder"
	"xr-trading/market-info-service/internal/domain"
)

// MarketBarQueryRepository resolves one active source and reads current bars.
type MarketBarQueryRepository struct {
	database CatalogDatabase
}

// NewMarketBarQueryRepository constructs the read-only K-line repository.
func NewMarketBarQueryRepository(database CatalogDatabase) (*MarketBarQueryRepository, error) {
	if database == nil {
		return nil, errors.New("PostgreSQL database is required")
	}
	return &MarketBarQueryRepository{database: database}, nil
}

// ResolveBarSource chooses the same mapping order used by the option query.
func (repository *MarketBarQueryRepository) ResolveBarSource(ctx context.Context, filter application.BarSourceFilter) (application.BarSourceRecord, error) {
	if filter.InstrumentID.IsZero() || filter.ProviderID.IsZero() || filter.EffectiveAt.IsZero() {
		return application.BarSourceRecord{}, fmt.Errorf("resolve bar source: %w", domain.ErrInvalidData)
	}
	row := repository.database.QueryRow(ctx, `
SELECT
    instrument.id,
    base_asset.code,
    quote_asset.code,
    instrument.quote_currency,
    provider.id,
    mapping.id,
    mapping.code,
    mapping.external_symbol,
    mapping.capabilities
FROM core.instruments AS instrument
JOIN core.assets AS base_asset
  ON base_asset.id = instrument.asset_id
LEFT JOIN core.assets AS quote_asset
  ON quote_asset.id = instrument.quote_asset_id
JOIN market_data.provider_instruments AS mapping
  ON mapping.instrument_id = instrument.id
JOIN market_data.providers AS provider
  ON provider.id = mapping.provider_id
WHERE instrument.id = $1
  AND provider.id = $2
  AND instrument.status = 'active'
  AND base_asset.status = 'active'
  AND (instrument.valid_from IS NULL OR instrument.valid_from <= $3)
  AND (instrument.valid_to IS NULL OR instrument.valid_to > $3)
  AND mapping.enabled = true
  AND (mapping.valid_from IS NULL OR mapping.valid_from <= $3)
  AND (mapping.valid_to IS NULL OR mapping.valid_to > $3)
  AND provider.status IN ('active', 'degraded')
ORDER BY mapping.is_default DESC, mapping.priority ASC, mapping.code ASC
LIMIT 1`, IDToDatabase(filter.InstrumentID), IDToDatabase(filter.ProviderID), TimeToDatabase(filter.EffectiveAt))
	source, err := scanBarSource(row)
	if err != nil {
		return application.BarSourceRecord{}, fmt.Errorf("resolve bar source: %w", MapError(err))
	}
	return source, nil
}

// ListBars returns current revisions using strict keyset pagination.
func (repository *MarketBarQueryRepository) ListBars(ctx context.Context, filter application.BarReadFilter) ([]domain.MarketBar, error) {
	if err := validateBarReadFilter(filter); err != nil {
		return nil, fmt.Errorf("list bars: %w", err)
	}
	builder := sqlbuilder.Select(marketBarColumns...).From("market_data.market_bars").
		Where(sqlbuilder.Eq("instrument_id", IDToDatabase(filter.InstrumentID))).
		And(sqlbuilder.Eq("provider_instrument_id", IDToDatabase(filter.ProviderInstrumentID))).
		And(sqlbuilder.Eq("interval", filter.Interval)).
		And(sqlbuilder.Raw("is_current = true"))
	if filter.Range.Start != nil {
		builder.And(sqlbuilder.Gte("open_time", TimeToDatabase(filter.Range.Start.Time())))
	}
	if filter.Range.End != nil {
		builder.And(sqlbuilder.Lt("open_time", TimeToDatabase(filter.Range.End.Time())))
	}
	if filter.CursorOpenTime != nil {
		if filter.Order == application.BarOrderAscending {
			builder.And(sqlbuilder.Gt("open_time", TimeToDatabase(filter.CursorOpenTime.Time())))
		} else {
			builder.And(sqlbuilder.Lt("open_time", TimeToDatabase(filter.CursorOpenTime.Time())))
		}
	}
	orderExpression := "open_time DESC"
	if filter.Order == application.BarOrderAscending {
		orderExpression = "open_time ASC"
	}
	query, args, err := builder.OrderBy(orderExpression).Limit(filter.Limit).Build()
	if err != nil {
		return nil, fmt.Errorf("build bar query: %w", err)
	}
	rows, err := repository.database.Query(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("list bars: %w", MapError(err))
	}
	defer rows.Close()
	bars := make([]domain.MarketBar, 0, filter.Limit)
	for rows.Next() {
		bar, scanErr := scanMarketBar(rows)
		if scanErr != nil {
			return nil, fmt.Errorf("scan bar: %w", scanErr)
		}
		bars = append(bars, bar)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate bars: %w", MapError(err))
	}
	return bars, nil
}

func validateBarReadFilter(filter application.BarReadFilter) error {
	if filter.InstrumentID.IsZero() || filter.ProviderInstrumentID.IsZero() || filter.Limit <= 0 || filter.Limit > application.MaximumBarsPageSize+1 {
		return domain.ErrInvalidData
	}
	if _, err := domain.ParseBarInterval(string(filter.Interval)); err != nil {
		return err
	}
	if err := filter.Range.Validate(); err != nil {
		return err
	}
	if filter.Order != application.BarOrderAscending && filter.Order != application.BarOrderDescending {
		return domain.ErrInvalidData
	}
	if filter.CursorOpenTime != nil && filter.CursorOpenTime.IsZero() {
		return domain.ErrInvalidData
	}
	return nil
}

func scanBarSource(row scanner) (application.BarSourceRecord, error) {
	var source application.BarSourceRecord
	var instrumentID, providerID, providerInstrumentID uuid.UUID
	var baseAssetCode, providerInstrumentCode string
	var quoteAssetCode *string
	var capabilities []byte
	if err := row.Scan(
		&instrumentID, &baseAssetCode, &quoteAssetCode, &source.QuoteCurrency,
		&providerID, &providerInstrumentID, &providerInstrumentCode, &source.ProviderSymbol, &capabilities,
	); err != nil {
		return application.BarSourceRecord{}, err
	}
	parsedBaseAssetCode, err := domain.ParseCode(baseAssetCode)
	if err != nil {
		return application.BarSourceRecord{}, err
	}
	parsedProviderInstrumentCode, err := domain.ParseCode(providerInstrumentCode)
	if err != nil {
		return application.BarSourceRecord{}, err
	}
	parsedCapabilities, err := domain.ParseProviderCapabilities(capabilities)
	if err != nil {
		return application.BarSourceRecord{}, err
	}
	source.InstrumentID = domain.IDFromUUID(instrumentID)
	source.BaseAssetCode = parsedBaseAssetCode
	source.ProviderID = domain.IDFromUUID(providerID)
	source.ProviderInstrumentID = domain.IDFromUUID(providerInstrumentID)
	source.ProviderInstrumentCode = parsedProviderInstrumentCode
	source.Capabilities = parsedCapabilities
	if quoteAssetCode != nil {
		parsed, parseErr := domain.ParseCode(*quoteAssetCode)
		if parseErr != nil {
			return application.BarSourceRecord{}, parseErr
		}
		source.QuoteAssetCode = &parsed
	}
	return source, nil
}

var _ application.BarReader = (*MarketBarQueryRepository)(nil)
