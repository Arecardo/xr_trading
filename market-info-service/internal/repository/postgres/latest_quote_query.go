package postgres

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/shopspring/decimal"

	"xr-trading/market-info-service/internal/application"
	"xr-trading/market-info-service/internal/database/sqlbuilder"
	"xr-trading/market-info-service/internal/domain"
)

// LatestQuoteQueryRepository serves the public read model without exposing
// market-data write methods to the application query service.
type LatestQuoteQueryRepository struct {
	database CatalogDatabase
}

// NewLatestQuoteQueryRepository constructs the read-only latest quote query.
func NewLatestQuoteQueryRepository(database CatalogDatabase) (*LatestQuoteQueryRepository, error) {
	if database == nil {
		return nil, errors.New("PostgreSQL database is required")
	}
	return &LatestQuoteQueryRepository{database: database}, nil
}

// ListLatestQuoteRecords joins latest snapshots with their stable internal and
// Provider identities. Every ProviderInstrument row remains independent.
func (repository *LatestQuoteQueryRepository) ListLatestQuoteRecords(ctx context.Context, filter application.LatestQuoteFilter) ([]application.LatestQuoteRecord, error) {
	if err := validateLatestQuoteFilter(filter); err != nil {
		return nil, fmt.Errorf("list latest quote records: %w", err)
	}
	builder := sqlbuilder.Select(latestQuoteProjectionColumns...).From(`market_data.latest_quotes AS quote
JOIN core.instruments AS instrument
  ON instrument.id = quote.instrument_id
JOIN market_data.provider_instruments AS mapping
  ON mapping.id = quote.provider_instrument_id
 AND mapping.instrument_id = quote.instrument_id
JOIN market_data.providers AS provider
  ON provider.id = mapping.provider_id`).
		Where(sqlbuilder.Eq("instrument.asset_id", IDToDatabase(filter.AssetID))).
		And(sqlbuilder.Raw("instrument.status = 'active'")).
		And(sqlbuilder.Raw("(instrument.valid_from IS NULL OR instrument.valid_from <= ?)", TimeToDatabase(filter.EffectiveAt))).
		And(sqlbuilder.Raw("(instrument.valid_to IS NULL OR instrument.valid_to > ?)", TimeToDatabase(filter.EffectiveAt))).
		And(sqlbuilder.Raw("mapping.enabled = true")).
		And(sqlbuilder.Raw("(mapping.valid_from IS NULL OR mapping.valid_from <= ?)", TimeToDatabase(filter.EffectiveAt))).
		And(sqlbuilder.Raw("(mapping.valid_to IS NULL OR mapping.valid_to > ?)", TimeToDatabase(filter.EffectiveAt))).
		And(sqlbuilder.Raw("mapping.capabilities @> '{\"quote\": true}'::jsonb")).
		And(sqlbuilder.Raw("provider.status IN ('active', 'degraded')"))
	if filter.InstrumentID != nil {
		builder.And(sqlbuilder.Eq("quote.instrument_id", IDToDatabase(*filter.InstrumentID)))
	}
	if filter.ProviderID != nil {
		builder.And(sqlbuilder.Eq("provider.id", IDToDatabase(*filter.ProviderID)))
	}
	query, args, err := builder.OrderBy("instrument.code ASC", "provider.code ASC", "mapping.code ASC").Build()
	if err != nil {
		return nil, fmt.Errorf("build latest quote query: %w", err)
	}
	rows, err := repository.database.Query(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("list latest quote records: %w", MapError(err))
	}
	defer rows.Close()

	records := make([]application.LatestQuoteRecord, 0)
	for rows.Next() {
		record, scanErr := scanLatestQuoteRecord(rows)
		if scanErr != nil {
			return nil, fmt.Errorf("scan latest quote record: %w", scanErr)
		}
		records = append(records, record)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate latest quote records: %w", MapError(err))
	}
	return records, nil
}

func validateLatestQuoteFilter(filter application.LatestQuoteFilter) error {
	if filter.AssetID.IsZero() || filter.EffectiveAt.IsZero() {
		return domain.ErrInvalidData
	}
	if filter.InstrumentID != nil && filter.InstrumentID.IsZero() {
		return domain.ErrInvalidData
	}
	if filter.ProviderID != nil && filter.ProviderID.IsZero() {
		return domain.ErrInvalidData
	}
	return nil
}

func scanLatestQuoteRecord(row scanner) (application.LatestQuoteRecord, error) {
	var record application.LatestQuoteRecord
	var instrumentID, providerID, providerInstrumentID uuid.UUID
	var instrumentCode, providerCode, providerInstrumentCode string
	var marketTime, collectedAt time.Time
	var lastPrice decimal.Decimal
	var bidPrice, bidSize, askPrice, askSize *decimal.Decimal
	var open24H, high24H, low24H, baseVolume24H, quoteVolume24H *decimal.Decimal
	var qualityStatus string
	var metadata []byte
	if err := row.Scan(
		&instrumentID, &instrumentCode, &record.QuoteCurrency,
		&providerID, &providerCode, &providerInstrumentID, &providerInstrumentCode, &record.ProviderSymbol,
		&marketTime, &lastPrice, &bidPrice, &bidSize, &askPrice, &askSize,
		&open24H, &high24H, &low24H, &baseVolume24H, &quoteVolume24H,
		&qualityStatus, &collectedAt, &metadata,
	); err != nil {
		return application.LatestQuoteRecord{}, err
	}
	parsedInstrumentCode, err := domain.ParseCode(instrumentCode)
	if err != nil {
		return application.LatestQuoteRecord{}, err
	}
	parsedProviderCode, err := domain.ParseCode(providerCode)
	if err != nil {
		return application.LatestQuoteRecord{}, err
	}
	parsedProviderInstrumentCode, err := domain.ParseCode(providerInstrumentCode)
	if err != nil {
		return application.LatestQuoteRecord{}, err
	}
	parsedMarketTime, err := domain.NewUTCInstant(marketTime)
	if err != nil {
		return application.LatestQuoteRecord{}, err
	}
	parsedCollectedAt, err := domain.NewUTCInstant(collectedAt)
	if err != nil {
		return application.LatestQuoteRecord{}, err
	}
	parsedQuality, err := domain.ParseQualityStatus(qualityStatus)
	if err != nil {
		return application.LatestQuoteRecord{}, err
	}
	quote, err := domain.NewQuote(domain.Quote{
		InstrumentID: domain.IDFromUUID(instrumentID), ProviderInstrumentID: domain.IDFromUUID(providerInstrumentID),
		MarketTime: parsedMarketTime, LastPrice: domain.DecimalFromExact(lastPrice),
		BidPrice: optionalDecimalFromDatabase(bidPrice), BidSize: optionalDecimalFromDatabase(bidSize),
		AskPrice: optionalDecimalFromDatabase(askPrice), AskSize: optionalDecimalFromDatabase(askSize),
		Open24H: optionalDecimalFromDatabase(open24H), High24H: optionalDecimalFromDatabase(high24H),
		Low24H: optionalDecimalFromDatabase(low24H), BaseVolume24H: optionalDecimalFromDatabase(baseVolume24H),
		QuoteVolume24H: optionalDecimalFromDatabase(quoteVolume24H), QualityStatus: parsedQuality,
		CollectedAt: parsedCollectedAt, Metadata: copyJSON(metadata),
	})
	if err != nil {
		return application.LatestQuoteRecord{}, err
	}
	record.InstrumentCode = parsedInstrumentCode
	record.ProviderID = domain.IDFromUUID(providerID)
	record.ProviderCode = parsedProviderCode
	record.ProviderInstrumentCode = parsedProviderInstrumentCode
	record.Quote = quote
	return record, nil
}

var latestQuoteProjectionColumns = []string{
	"quote.instrument_id", "instrument.code", "instrument.quote_currency",
	"provider.id", "provider.code", "quote.provider_instrument_id", "mapping.code", "mapping.external_symbol",
	"quote.market_time", "quote.last_price", "quote.bid_price", "quote.bid_size", "quote.ask_price", "quote.ask_size",
	"quote.open_24h", "quote.high_24h", "quote.low_24h", "quote.base_volume_24h", "quote.quote_volume_24h",
	"quote.quality_status", "quote.collected_at", "quote.metadata",
}

// Compile-time contract keeps the read projection aligned with its port.
var _ application.LatestQuoteReader = (*LatestQuoteQueryRepository)(nil)
