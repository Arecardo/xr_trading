package postgres

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/shopspring/decimal"

	"xr-trading/market-info-service/internal/domain"
)

type fakeMarketDataTransaction struct {
	exec       func(context.Context, string, ...any) (pgconn.CommandTag, error)
	queryRow   func(context.Context, string, ...any) pgx.Row
	query      func(context.Context, string, ...any) (pgx.Rows, error)
	commit     func(context.Context) error
	committed  bool
	rolledBack bool
}

func (tx *fakeMarketDataTransaction) Exec(ctx context.Context, query string, args ...any) (pgconn.CommandTag, error) {
	return tx.exec(ctx, query, args...)
}
func (tx *fakeMarketDataTransaction) QueryRow(ctx context.Context, query string, args ...any) pgx.Row {
	return tx.queryRow(ctx, query, args...)
}
func (tx *fakeMarketDataTransaction) Query(ctx context.Context, query string, args ...any) (pgx.Rows, error) {
	return tx.query(ctx, query, args...)
}
func (tx *fakeMarketDataTransaction) Commit(ctx context.Context) error {
	if tx.commit != nil {
		return tx.commit(ctx)
	}
	tx.committed = true
	return nil
}
func (tx *fakeMarketDataTransaction) Rollback(context.Context) error {
	tx.rolledBack = true
	return nil
}

type fakeMarketDataDatabase struct {
	fakeCatalogDatabase
	begin func(context.Context) (marketDataTransaction, error)
}

func (database fakeMarketDataDatabase) Begin(ctx context.Context) (marketDataTransaction, error) {
	return database.begin(ctx)
}

func TestMarketDataRepositoryQuotes(t *testing.T) {
	now := time.Now().UTC()
	quote := testQuote(t, now)
	quoteRow := scanFunc(func(destinations ...any) error { *destinations[0].(*time.Time) = now; return nil })
	rows := &fakeRows{rows: []scanFunc{latestQuoteRow(quote)}}
	database := fakeMarketDataDatabase{fakeCatalogDatabase: fakeCatalogDatabase{
		queryRow: func(context.Context, string, ...any) pgx.Row { return quoteRow },
		query:    func(context.Context, string, ...any) (pgx.Rows, error) { return rows, nil },
		exec:     func(context.Context, string, ...any) (pgconn.CommandTag, error) { return pgconn.CommandTag{}, nil },
	}, begin: func(context.Context) (marketDataTransaction, error) { return nil, errors.New("not used") }}
	repository, err := newMarketDataRepository(database)
	if err != nil {
		t.Fatalf("newMarketDataRepository() error = %v", err)
	}
	applied, err := repository.UpsertLatestQuote(context.Background(), quote)
	if err != nil || !applied {
		t.Fatalf("UpsertLatestQuote() = (%t, %v)", applied, err)
	}
	loaded, err := repository.ListLatestQuotes(context.Background(), quote.InstrumentID)
	if err != nil || len(loaded) != 1 || loaded[0].ProviderInstrumentID != quote.ProviderInstrumentID || !rows.closed {
		t.Fatalf("ListLatestQuotes() = (%#v, %v)", loaded, err)
	}
}

func TestMarketDataRepositoryRejectsOldQuoteAndInvalidIdentity(t *testing.T) {
	database := fakeMarketDataDatabase{fakeCatalogDatabase: fakeCatalogDatabase{
		queryRow: func(context.Context, string, ...any) pgx.Row {
			return scanFunc(func(...any) error { return pgx.ErrNoRows })
		},
		query: func(context.Context, string, ...any) (pgx.Rows, error) { return nil, &pgconn.PgError{Code: "08006"} },
		exec:  func(context.Context, string, ...any) (pgconn.CommandTag, error) { return pgconn.CommandTag{}, nil },
	}, begin: func(context.Context) (marketDataTransaction, error) { return nil, errors.New("not used") }}
	repository, _ := newMarketDataRepository(database)
	if applied, err := repository.UpsertLatestQuote(context.Background(), testQuote(t, time.Now().UTC())); err != nil || applied {
		t.Fatalf("UpsertLatestQuote(old) = (%t, %v)", applied, err)
	}
	if _, err := repository.UpsertLatestQuote(context.Background(), domain.LatestQuote{}); !errors.Is(err, domain.ErrInvalidData) {
		t.Fatalf("UpsertLatestQuote(invalid) error = %v", err)
	}
	if _, err := repository.ListLatestQuotes(context.Background(), domain.ID{}); !errors.Is(err, domain.ErrInvalidData) {
		t.Fatalf("ListLatestQuotes(zero) error = %v", err)
	}
	if _, err := repository.ListLatestQuotes(context.Background(), domain.IDFromUUID(uuid.New())); !errors.Is(err, domain.ErrDatabaseUnavailable) {
		t.Fatalf("ListLatestQuotes(unavailable) error = %v", err)
	}
}

func TestWriteMarketBarCreatesSkipsAndRevises(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	bar := testBar(t, now)
	for _, test := range []struct {
		name         string
		current      pgx.Row
		wantApplied  bool
		wantRevision int
		wantExecs    int
		wantCommit   bool
	}{
		{name: "first revision", current: scanFunc(func(...any) error { return pgx.ErrNoRows }), wantApplied: true, wantRevision: 1, wantExecs: 1, wantCommit: true},
		{name: "unchanged", current: marketBarRow(bar), wantApplied: false, wantRevision: 1, wantExecs: 0, wantCommit: false},
		{name: "changed", current: marketBarRow(bar), wantApplied: true, wantRevision: 2, wantExecs: 2, wantCommit: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			candidate := bar
			if test.name == "changed" {
				candidate.RawHash = "new-hash"
			}
			execs := 0
			tx := &fakeMarketDataTransaction{queryRow: func(context.Context, string, ...any) pgx.Row { return test.current }, query: func(context.Context, string, ...any) (pgx.Rows, error) { return nil, nil }, exec: func(context.Context, string, ...any) (pgconn.CommandTag, error) {
				execs++
				return pgconn.NewCommandTag("INSERT 0 1"), nil
			}}
			database := fakeMarketDataDatabase{fakeCatalogDatabase: fakeCatalogDatabase{exec: func(context.Context, string, ...any) (pgconn.CommandTag, error) { return pgconn.CommandTag{}, nil }, queryRow: func(context.Context, string, ...any) pgx.Row { return nil }, query: func(context.Context, string, ...any) (pgx.Rows, error) { return nil, nil }}, begin: func(context.Context) (marketDataTransaction, error) { return tx, nil }}
			repository, _ := newMarketDataRepository(database)
			result, err := repository.WriteMarketBar(context.Background(), candidate)
			if err != nil || result.Applied != test.wantApplied || result.Revision != test.wantRevision || execs != test.wantExecs || tx.committed != test.wantCommit || !tx.rolledBack {
				t.Fatalf("WriteMarketBar() = (%#v, %v), execs=%d commit=%t rollback=%t", result, err, execs, tx.committed, tx.rolledBack)
			}
		})
	}
}

func TestMarketBarValidationAndPageLimit(t *testing.T) {
	if err := validateMarketBar(domain.MarketBar{}); !errors.Is(err, domain.ErrInvalidData) {
		t.Fatalf("validateMarketBar() error = %v", err)
	}
	if _, err := marketBarPageLimit(-1); !errors.Is(err, domain.ErrInvalidData) {
		t.Fatalf("marketBarPageLimit() error = %v", err)
	}
	if got, err := marketBarPageLimit(0); err != nil || got != defaultMarketBarPageSize {
		t.Fatalf("marketBarPageLimit(0) = (%d, %v)", got, err)
	}
}

func TestListCurrentMarketBars(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	bar := testBar(t, now)
	rangeEnd := now.Add(2 * time.Hour)
	timeRange, err := domain.NewBoundedTimeRange(now, rangeEnd)
	if err != nil {
		t.Fatalf("NewBoundedTimeRange() error = %v", err)
	}
	rows := &fakeRows{rows: []scanFunc{marketBarRow(bar)}}
	var builtQuery string
	database := fakeMarketDataDatabase{fakeCatalogDatabase: fakeCatalogDatabase{
		exec:     func(context.Context, string, ...any) (pgconn.CommandTag, error) { return pgconn.CommandTag{}, nil },
		queryRow: func(context.Context, string, ...any) pgx.Row { return nil },
		query: func(_ context.Context, query string, _ ...any) (pgx.Rows, error) {
			builtQuery = query
			return rows, nil
		},
	}, begin: func(context.Context) (marketDataTransaction, error) { return nil, errors.New("not used") }}
	repository, err := newMarketDataRepository(database)
	if err != nil {
		t.Fatalf("newMarketDataRepository() error = %v", err)
	}
	result, err := repository.ListCurrentMarketBars(context.Background(), domain.MarketBarFilter{
		InstrumentID: bar.InstrumentID, ProviderInstrumentID: bar.ProviderInstrumentID, Interval: "1h", Range: timeRange, Limit: 1,
	})
	if err != nil || len(result) != 1 || result[0].RawHash != bar.RawHash || !rows.closed || !strings.Contains(builtQuery, "open_time <") || strings.Contains(builtQuery, "open_time <=") {
		t.Fatalf("ListCurrentMarketBars() = (%#v, %v)", result, err)
	}
	if _, err := repository.ListCurrentMarketBars(context.Background(), domain.MarketBarFilter{}); !errors.Is(err, domain.ErrInvalidData) {
		t.Fatalf("ListCurrentMarketBars(invalid) error = %v", err)
	}
}

func TestNewMarketDataRepositoryRequiresDatabase(t *testing.T) {
	if _, err := NewMarketDataRepository(nil); err == nil {
		t.Fatal("NewMarketDataRepository(nil) error = nil, want error")
	}
	if _, err := newMarketDataRepository(nil); err == nil {
		t.Fatal("newMarketDataRepository(nil) error = nil, want error")
	}
}

func testQuote(t *testing.T, now time.Time) domain.LatestQuote {
	t.Helper()
	instant := testMarketInstant(t, now)
	return domain.LatestQuote{InstrumentID: domain.IDFromUUID(uuid.MustParse("019f1452-90f7-7992-a87a-ca2727891601")), ProviderInstrumentID: domain.IDFromUUID(uuid.MustParse("019f1452-90f7-7992-a87a-ca2727891602")), MarketTime: instant, LastPrice: domain.DecimalFromExact(decimal.RequireFromString("100.25")), QualityStatus: "valid", CollectedAt: instant}
}
func testBar(t *testing.T, now time.Time) domain.MarketBar {
	t.Helper()
	openTime := testMarketInstant(t, now)
	closeTime := testMarketInstant(t, now.Add(time.Hour))
	return domain.MarketBar{InstrumentID: domain.IDFromUUID(uuid.MustParse("019f1452-90f7-7992-a87a-ca2727891601")), ProviderInstrumentID: domain.IDFromUUID(uuid.MustParse("019f1452-90f7-7992-a87a-ca2727891602")), Interval: "1h", OpenTime: openTime, CloseTime: closeTime, OpenPrice: domain.DecimalFromExact(decimal.NewFromInt(100)), HighPrice: domain.DecimalFromExact(decimal.NewFromInt(110)), LowPrice: domain.DecimalFromExact(decimal.NewFromInt(90)), ClosePrice: domain.DecimalFromExact(decimal.NewFromInt(105)), IsClosed: true, IsCurrent: true, QualityStatus: "valid", CollectedAt: closeTime, RawHash: "same-hash"}
}

func testMarketInstant(t *testing.T, value time.Time) domain.UTCInstant {
	t.Helper()
	instant, err := domain.NewUTCInstant(value)
	if err != nil {
		t.Fatalf("NewUTCInstant() error = %v", err)
	}
	return instant
}

func latestQuoteRow(quote domain.LatestQuote) scanFunc {
	return func(destinations ...any) error {
		*destinations[0].(*uuid.UUID) = quote.InstrumentID.UUID()
		*destinations[1].(*uuid.UUID) = quote.ProviderInstrumentID.UUID()
		*destinations[2].(*time.Time) = quote.MarketTime.Time()
		*destinations[3].(*decimal.Decimal) = quote.LastPrice.Exact()
		*destinations[4].(**decimal.Decimal) = nil
		*destinations[5].(**decimal.Decimal) = nil
		*destinations[6].(**decimal.Decimal) = nil
		*destinations[7].(**decimal.Decimal) = nil
		*destinations[8].(**decimal.Decimal) = nil
		*destinations[9].(**decimal.Decimal) = nil
		*destinations[10].(**decimal.Decimal) = nil
		*destinations[11].(**decimal.Decimal) = nil
		*destinations[12].(**decimal.Decimal) = nil
		*destinations[13].(*string) = string(quote.QualityStatus)
		*destinations[14].(*time.Time) = quote.CollectedAt.Time()
		*destinations[15].(*[]byte) = []byte(`{}`)
		return nil
	}
}
func marketBarRow(bar domain.MarketBar) scanFunc {
	return func(destinations ...any) error {
		*destinations[0].(*uuid.UUID) = bar.InstrumentID.UUID()
		*destinations[1].(*uuid.UUID) = bar.ProviderInstrumentID.UUID()
		*destinations[2].(*string) = string(bar.Interval)
		*destinations[3].(*time.Time) = bar.OpenTime.Time()
		*destinations[4].(*int) = bar.Revision
		if bar.Revision == 0 {
			*destinations[4].(*int) = 1
		}
		*destinations[5].(*time.Time) = bar.CloseTime.Time()
		*destinations[6].(*decimal.Decimal) = bar.OpenPrice.Exact()
		*destinations[7].(*decimal.Decimal) = bar.HighPrice.Exact()
		*destinations[8].(*decimal.Decimal) = bar.LowPrice.Exact()
		*destinations[9].(*decimal.Decimal) = bar.ClosePrice.Exact()
		*destinations[10].(**decimal.Decimal) = nil
		*destinations[11].(**decimal.Decimal) = nil
		*destinations[12].(**int64) = nil
		*destinations[13].(*bool) = bar.IsClosed
		*destinations[14].(*bool) = true
		*destinations[15].(*string) = string(bar.QualityStatus)
		*destinations[16].(**time.Time) = nil
		*destinations[17].(*time.Time) = bar.CollectedAt.Time()
		raw := bar.RawHash
		*destinations[18].(**string) = &raw
		*destinations[19].(*[]byte) = []byte(`{}`)
		return nil
	}
}
