package postgres

import (
	"context"
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

const defaultMarketBarPageSize = 200
const maximumMarketBarPageSize = 1000

// MarketDataRepository stores source-specific latest quotes and market bars.
type MarketDataRepository struct {
	database marketDataDatabase
}

type marketDataDatabase interface {
	catalogDatabase
	Begin(context.Context) (marketDataTransaction, error)
}

type marketDataTransaction interface {
	catalogDatabase
	Commit(context.Context) error
	Rollback(context.Context) error
}

type pgxMarketDataDatabase struct{ pool *pgxpool.Pool }

func (database pgxMarketDataDatabase) Exec(ctx context.Context, query string, args ...any) (pgconn.CommandTag, error) {
	return database.pool.Exec(ctx, query, args...)
}
func (database pgxMarketDataDatabase) QueryRow(ctx context.Context, query string, args ...any) pgx.Row {
	return database.pool.QueryRow(ctx, query, args...)
}
func (database pgxMarketDataDatabase) Query(ctx context.Context, query string, args ...any) (pgx.Rows, error) {
	return database.pool.Query(ctx, query, args...)
}
func (database pgxMarketDataDatabase) Begin(ctx context.Context) (marketDataTransaction, error) {
	tx, err := database.pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	return pgxMarketDataTransaction{tx: tx}, nil
}

type pgxMarketDataTransaction struct{ tx pgx.Tx }

func (transaction pgxMarketDataTransaction) Exec(ctx context.Context, query string, args ...any) (pgconn.CommandTag, error) {
	return transaction.tx.Exec(ctx, query, args...)
}
func (transaction pgxMarketDataTransaction) QueryRow(ctx context.Context, query string, args ...any) pgx.Row {
	return transaction.tx.QueryRow(ctx, query, args...)
}
func (transaction pgxMarketDataTransaction) Query(ctx context.Context, query string, args ...any) (pgx.Rows, error) {
	return transaction.tx.Query(ctx, query, args...)
}
func (transaction pgxMarketDataTransaction) Commit(ctx context.Context) error {
	return transaction.tx.Commit(ctx)
}
func (transaction pgxMarketDataTransaction) Rollback(ctx context.Context) error {
	return transaction.tx.Rollback(ctx)
}

// NewMarketDataRepository constructs a market data repository over pool.
func NewMarketDataRepository(pool *pgxpool.Pool) (*MarketDataRepository, error) {
	if pool == nil {
		return nil, errors.New("PostgreSQL pool is required")
	}
	return newMarketDataRepository(pgxMarketDataDatabase{pool: pool})
}

func newMarketDataRepository(database marketDataDatabase) (*MarketDataRepository, error) {
	if database == nil {
		return nil, errors.New("PostgreSQL database is required")
	}
	return &MarketDataRepository{database: database}, nil
}

// UpsertLatestQuote inserts a new source-specific quote or replaces it only
// when marketTime is newer. It returns false for an older or equal snapshot.
func (repository *MarketDataRepository) UpsertLatestQuote(ctx context.Context, quote domain.LatestQuote) (bool, error) {
	if quote.InstrumentID.IsZero() || quote.ProviderInstrumentID.IsZero() || quote.MarketTime.IsZero() {
		return false, fmt.Errorf("upsert latest quote: %w", domain.ErrInvalidData)
	}
	var storedTime time.Time
	err := repository.database.QueryRow(ctx, `
INSERT INTO market_data.latest_quotes (
    instrument_id, provider_instrument_id, market_time, last_price, bid_price, bid_size,
    ask_price, ask_size, open_24h, high_24h, low_24h, base_volume_24h,
    quote_volume_24h, quality_status, collected_at, metadata
) VALUES ($1, $2, $3, $4::numeric, $5::numeric, $6::numeric, $7::numeric, $8::numeric,
          $9::numeric, $10::numeric, $11::numeric, $12::numeric, $13::numeric, $14, $15, $16::jsonb)
ON CONFLICT (instrument_id, provider_instrument_id) DO UPDATE SET
    market_time = EXCLUDED.market_time, last_price = EXCLUDED.last_price,
    bid_price = EXCLUDED.bid_price, bid_size = EXCLUDED.bid_size, ask_price = EXCLUDED.ask_price,
    ask_size = EXCLUDED.ask_size, open_24h = EXCLUDED.open_24h, high_24h = EXCLUDED.high_24h,
    low_24h = EXCLUDED.low_24h, base_volume_24h = EXCLUDED.base_volume_24h,
    quote_volume_24h = EXCLUDED.quote_volume_24h, quality_status = EXCLUDED.quality_status,
    collected_at = EXCLUDED.collected_at, metadata = EXCLUDED.metadata
WHERE EXCLUDED.market_time > market_data.latest_quotes.market_time
RETURNING market_time`, quoteArguments(quote)...).Scan(&storedTime)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("upsert latest quote: %w", MapError(err))
	}
	return true, nil
}

// ListLatestQuotes returns all source-specific snapshots for an Instrument.
func (repository *MarketDataRepository) ListLatestQuotes(ctx context.Context, instrumentID domain.ID) ([]domain.LatestQuote, error) {
	if instrumentID.IsZero() {
		return nil, fmt.Errorf("list latest quotes: %w", domain.ErrInvalidData)
	}
	query, args, err := sqlbuilder.Select(latestQuoteColumns...).From("market_data.latest_quotes").
		Where(sqlbuilder.Eq("instrument_id", IDToDatabase(instrumentID))).
		OrderBy("market_time DESC", "provider_instrument_id ASC").Build()
	if err != nil {
		return nil, fmt.Errorf("build latest quote query: %w", err)
	}
	rows, err := repository.database.Query(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("list latest quotes: %w", MapError(err))
	}
	defer rows.Close()
	quotes := make([]domain.LatestQuote, 0)
	for rows.Next() {
		quote, err := scanLatestQuote(rows)
		if err != nil {
			return nil, fmt.Errorf("scan latest quote: %w", MapError(err))
		}
		quotes = append(quotes, quote)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate latest quotes: %w", MapError(err))
	}
	return quotes, nil
}

// WriteMarketBar atomically preserves an unchanged current bar or writes a
// new revision after closing the prior current revision.
func (repository *MarketDataRepository) WriteMarketBar(ctx context.Context, bar domain.MarketBar) (domain.MarketBarWriteResult, error) {
	if err := validateMarketBar(bar); err != nil {
		return domain.MarketBarWriteResult{}, err
	}
	tx, err := repository.database.Begin(ctx)
	if err != nil {
		return domain.MarketBarWriteResult{}, fmt.Errorf("begin market bar write: %w", MapError(err))
	}
	defer func() { _ = tx.Rollback(ctx) }()

	current, err := loadCurrentMarketBar(ctx, tx, bar)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return domain.MarketBarWriteResult{}, fmt.Errorf("load current market bar: %w", MapError(err))
	}
	if err == nil && bar.RawHash != "" && current.RawHash == bar.RawHash {
		return domain.MarketBarWriteResult{Applied: false, Revision: current.Revision}, nil
	}
	revision := 1
	if err == nil {
		revision = current.Revision + 1
		if _, err := tx.Exec(ctx, `UPDATE market_data.market_bars SET is_current = false
WHERE instrument_id = $1 AND provider_instrument_id = $2 AND interval = $3 AND open_time = $4 AND revision = $5 AND is_current = true`,
			IDToDatabase(bar.InstrumentID), IDToDatabase(bar.ProviderInstrumentID), bar.Interval, TimeToDatabase(bar.OpenTime), current.Revision); err != nil {
			return domain.MarketBarWriteResult{}, fmt.Errorf("close current market bar: %w", MapError(err))
		}
	}
	bar.Revision = revision
	bar.IsCurrent = true
	if _, err := tx.Exec(ctx, insertMarketBarSQL, marketBarArguments(bar)...); err != nil {
		return domain.MarketBarWriteResult{}, fmt.Errorf("insert market bar revision: %w", MapError(err))
	}
	if err := tx.Commit(ctx); err != nil {
		return domain.MarketBarWriteResult{}, fmt.Errorf("commit market bar revision: %w", MapError(err))
	}
	return domain.MarketBarWriteResult{Applied: true, Revision: revision}, nil
}

// ListCurrentMarketBars returns one ProviderInstrument's current revisions in
// reverse chronological order. Both source identity fields are mandatory.
func (repository *MarketDataRepository) ListCurrentMarketBars(ctx context.Context, filter domain.MarketBarFilter) ([]domain.MarketBar, error) {
	if filter.InstrumentID.IsZero() || filter.ProviderInstrumentID.IsZero() || filter.Interval == "" {
		return nil, fmt.Errorf("list current market bars: %w", domain.ErrInvalidData)
	}
	limit, err := marketBarPageLimit(filter.Limit)
	if err != nil {
		return nil, err
	}
	builder := sqlbuilder.Select(marketBarColumns...).From("market_data.market_bars").
		Where(sqlbuilder.Eq("instrument_id", IDToDatabase(filter.InstrumentID))).
		And(sqlbuilder.Eq("provider_instrument_id", IDToDatabase(filter.ProviderInstrumentID))).
		And(sqlbuilder.Eq("interval", filter.Interval)).
		And(sqlbuilder.Raw("is_current = true"))
	if filter.StartAt != nil {
		builder.And(sqlbuilder.Gte("open_time", TimeToDatabase(*filter.StartAt)))
	}
	if filter.EndAt != nil {
		builder.And(sqlbuilder.Lte("open_time", TimeToDatabase(*filter.EndAt)))
	}
	if filter.BeforeOpenTime != nil {
		builder.And(sqlbuilder.Raw("open_time < ?", TimeToDatabase(*filter.BeforeOpenTime)))
	}
	query, args, err := builder.OrderBy("open_time DESC").Limit(limit).Build()
	if err != nil {
		return nil, fmt.Errorf("build current market bars query: %w", err)
	}
	rows, err := repository.database.Query(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("list current market bars: %w", MapError(err))
	}
	defer rows.Close()
	bars := make([]domain.MarketBar, 0, limit)
	for rows.Next() {
		bar, err := scanMarketBar(rows)
		if err != nil {
			return nil, fmt.Errorf("scan current market bar: %w", MapError(err))
		}
		bars = append(bars, bar)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate current market bars: %w", MapError(err))
	}
	return bars, nil
}

var latestQuoteColumns = []string{"instrument_id", "provider_instrument_id", "market_time", "last_price", "bid_price", "bid_size", "ask_price", "ask_size", "open_24h", "high_24h", "low_24h", "base_volume_24h", "quote_volume_24h", "quality_status", "collected_at", "metadata"}
var marketBarColumns = []string{"instrument_id", "provider_instrument_id", "interval", "open_time", "revision", "close_time", "open_price", "high_price", "low_price", "close_price", "base_volume", "quote_volume", "trade_count", "is_closed", "is_current", "quality_status", "provider_updated_at", "collected_at", "raw_hash", "metadata"}

const insertMarketBarSQL = `INSERT INTO market_data.market_bars (
instrument_id, provider_instrument_id, interval, open_time, revision, close_time, open_price, high_price, low_price, close_price, base_volume, quote_volume, trade_count, is_closed, is_current, quality_status, provider_updated_at, collected_at, raw_hash, metadata
) VALUES ($1, $2, $3, $4, $5, $6, $7::numeric, $8::numeric, $9::numeric, $10::numeric, $11::numeric, $12::numeric, $13, $14, $15, $16, $17, $18, $19, $20::jsonb)`

func loadCurrentMarketBar(ctx context.Context, tx marketDataTransaction, bar domain.MarketBar) (domain.MarketBar, error) {
	row := tx.QueryRow(ctx, "SELECT "+joinColumns(marketBarColumns)+" FROM market_data.market_bars WHERE instrument_id = $1 AND provider_instrument_id = $2 AND interval = $3 AND open_time = $4 AND is_current = true FOR UPDATE", IDToDatabase(bar.InstrumentID), IDToDatabase(bar.ProviderInstrumentID), bar.Interval, TimeToDatabase(bar.OpenTime))
	return scanMarketBar(row)
}

func quoteArguments(quote domain.LatestQuote) []any {
	return []any{IDToDatabase(quote.InstrumentID), IDToDatabase(quote.ProviderInstrumentID), TimeToDatabase(quote.MarketTime), DecimalToDatabase(quote.LastPrice), optionalDecimalToDatabase(quote.BidPrice), optionalDecimalToDatabase(quote.BidSize), optionalDecimalToDatabase(quote.AskPrice), optionalDecimalToDatabase(quote.AskSize), optionalDecimalToDatabase(quote.Open24H), optionalDecimalToDatabase(quote.High24H), optionalDecimalToDatabase(quote.Low24H), optionalDecimalToDatabase(quote.BaseVolume24H), optionalDecimalToDatabase(quote.QuoteVolume24H), quote.QualityStatus, TimeToDatabase(quote.CollectedAt), jsonValue(quote.Metadata)}
}

func marketBarArguments(bar domain.MarketBar) []any {
	return []any{IDToDatabase(bar.InstrumentID), IDToDatabase(bar.ProviderInstrumentID), bar.Interval, TimeToDatabase(bar.OpenTime), bar.Revision, TimeToDatabase(bar.CloseTime), DecimalToDatabase(bar.OpenPrice), DecimalToDatabase(bar.HighPrice), DecimalToDatabase(bar.LowPrice), DecimalToDatabase(bar.ClosePrice), optionalDecimalToDatabase(bar.BaseVolume), optionalDecimalToDatabase(bar.QuoteVolume), bar.TradeCount, bar.IsClosed, bar.IsCurrent, bar.QualityStatus, optionalTimeToDatabase(bar.ProviderUpdatedAt), TimeToDatabase(bar.CollectedAt), nullableString(bar.RawHash), jsonValue(bar.Metadata)}
}

func scanLatestQuote(row scanner) (domain.LatestQuote, error) {
	var quote domain.LatestQuote
	var instrumentID, providerInstrumentID uuid.UUID
	var metadata []byte
	if err := row.Scan(&instrumentID, &providerInstrumentID, &quote.MarketTime, &quote.LastPrice, &quote.BidPrice, &quote.BidSize, &quote.AskPrice, &quote.AskSize, &quote.Open24H, &quote.High24H, &quote.Low24H, &quote.BaseVolume24H, &quote.QuoteVolume24H, &quote.QualityStatus, &quote.CollectedAt, &metadata); err != nil {
		return domain.LatestQuote{}, err
	}
	quote.InstrumentID, quote.ProviderInstrumentID = IDFromDatabase(instrumentID), IDFromDatabase(providerInstrumentID)
	quote.MarketTime, quote.CollectedAt, quote.Metadata = TimeToDatabase(quote.MarketTime), TimeToDatabase(quote.CollectedAt), copyJSON(metadata)
	return quote, nil
}

func scanMarketBar(row scanner) (domain.MarketBar, error) {
	var bar domain.MarketBar
	var instrumentID, providerInstrumentID uuid.UUID
	var metadata []byte
	var rawHash *string
	if err := row.Scan(&instrumentID, &providerInstrumentID, &bar.Interval, &bar.OpenTime, &bar.Revision, &bar.CloseTime, &bar.OpenPrice, &bar.HighPrice, &bar.LowPrice, &bar.ClosePrice, &bar.BaseVolume, &bar.QuoteVolume, &bar.TradeCount, &bar.IsClosed, &bar.IsCurrent, &bar.QualityStatus, &bar.ProviderUpdatedAt, &bar.CollectedAt, &rawHash, &metadata); err != nil {
		return domain.MarketBar{}, err
	}
	bar.InstrumentID, bar.ProviderInstrumentID = IDFromDatabase(instrumentID), IDFromDatabase(providerInstrumentID)
	bar.OpenTime, bar.CloseTime, bar.CollectedAt, bar.Metadata = TimeToDatabase(bar.OpenTime), TimeToDatabase(bar.CloseTime), TimeToDatabase(bar.CollectedAt), copyJSON(metadata)
	bar.ProviderUpdatedAt = optionalTimeFromDatabase(bar.ProviderUpdatedAt)
	if rawHash != nil {
		bar.RawHash = *rawHash
	}
	return bar, nil
}

func validateMarketBar(bar domain.MarketBar) error {
	if bar.InstrumentID.IsZero() || bar.ProviderInstrumentID.IsZero() || bar.Interval == "" || bar.OpenTime.IsZero() || bar.CloseTime.IsZero() || bar.CollectedAt.IsZero() {
		return fmt.Errorf("write market bar: %w", domain.ErrInvalidData)
	}
	return nil
}
func marketBarPageLimit(value int) (int, error) {
	if value == 0 {
		return defaultMarketBarPageSize, nil
	}
	if value < 0 || value > maximumMarketBarPageSize {
		return 0, fmt.Errorf("market bar page limit: %w", domain.ErrInvalidData)
	}
	return value, nil
}
func optionalDecimalToDatabase(value *decimal.Decimal) any {
	if value == nil {
		return nil
	}
	return DecimalToDatabase(*value)
}
func nullableString(value string) any {
	if value == "" {
		return nil
	}
	return value
}
func joinColumns(columns []string) string {
	result := ""
	for index, column := range columns {
		if index > 0 {
			result += ", "
		}
		result += column
	}
	return result
}

var _ domain.MarketDataRepository = (*MarketDataRepository)(nil)
