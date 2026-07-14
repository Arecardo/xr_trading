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

	"xr-trading/market-info-service/internal/application"
	"xr-trading/market-info-service/internal/domain"
)

func TestMarketBarQueryRepositoryResolvesBestSource(t *testing.T) {
	now := time.Now().UTC()
	instrumentID := domain.IDFromUUID(uuid.MustParse("019f1452-90f7-7992-a87a-ca2727891601"))
	providerID := domain.IDFromUUID(uuid.MustParse("019f1452-90f7-7992-a87a-ca2727891602"))
	database := fakeCatalogDatabase{queryRow: func(_ context.Context, query string, args ...any) pgx.Row {
		if !strings.Contains(query, "ORDER BY mapping.is_default DESC, mapping.priority ASC, mapping.code ASC") || !strings.Contains(query, "mapping.valid_to > $3") || len(args) != 3 || args[0] != instrumentID.UUID() || args[1] != providerID.UUID() || args[2] != now {
			t.Fatalf("source query=%q args=%#v", query, args)
		}
		return scanFunc(func(destinations ...any) error {
			*destinations[0].(*uuid.UUID) = instrumentID.UUID()
			*destinations[1].(*string) = "asset.crypto.btc"
			*destinations[2].(**string) = nil
			*destinations[3].(*string) = "USDT"
			*destinations[4].(*uuid.UUID) = providerID.UUID()
			*destinations[5].(*uuid.UUID) = uuid.MustParse("019f1452-90f7-7992-a87a-ca2727891603")
			*destinations[6].(*string) = "provider.bybit.btc-usdt"
			*destinations[7].(*string) = "BTCUSDT"
			*destinations[8].(*[]byte) = []byte(`{"historical":true,"intervals":["1h","1d"]}`)
			return nil
		})
	}}
	repository, err := NewMarketBarQueryRepository(database)
	if err != nil {
		t.Fatalf("NewMarketBarQueryRepository() error = %v", err)
	}
	source, err := repository.ResolveBarSource(context.Background(), application.BarSourceFilter{InstrumentID: instrumentID, ProviderID: providerID, EffectiveAt: now})
	if err != nil || source.InstrumentID != instrumentID || source.ProviderID != providerID || source.ProviderSymbol != "BTCUSDT" || source.QuoteAssetCode != nil || len(source.Capabilities.Intervals) != 2 {
		t.Fatalf("ResolveBarSource() = (%#v, %v)", source, err)
	}
}

func TestMarketBarQueryRepositoryListsBothOrders(t *testing.T) {
	openTime := time.Now().UTC().Truncate(time.Hour)
	bar := testBar(t, openTime)
	bar.Revision = 1
	start := openTime.Add(-time.Hour)
	end := openTime.Add(2 * time.Hour)
	timeRange, _ := domain.NewBoundedTimeRange(start, end)
	cursor, _ := domain.NewUTCInstant(openTime.Add(-time.Minute))
	for _, order := range []application.BarOrder{application.BarOrderAscending, application.BarOrderDescending} {
		t.Run(string(order), func(t *testing.T) {
			rows := &fakeRows{rows: []scanFunc{marketBarRow(bar)}}
			database := fakeCatalogDatabase{query: func(_ context.Context, query string, args ...any) (pgx.Rows, error) {
				operator := "open_time <"
				orderSQL := "open_time DESC"
				if order == application.BarOrderAscending {
					operator = "open_time >"
					orderSQL = "open_time ASC"
				}
				if !strings.Contains(query, operator) || !strings.Contains(query, "ORDER BY "+orderSQL) || !strings.Contains(query, "LIMIT 11") || len(args) != 6 {
					t.Fatalf("bar query=%q args=%#v", query, args)
				}
				return rows, nil
			}}
			repository, _ := NewMarketBarQueryRepository(database)
			bars, err := repository.ListBars(context.Background(), application.BarReadFilter{
				InstrumentID: bar.InstrumentID, ProviderInstrumentID: bar.ProviderInstrumentID, Interval: domain.BarInterval1Hour,
				Range: timeRange, Order: order, CursorOpenTime: &cursor, Limit: 11,
			})
			if err != nil || len(bars) != 1 || bars[0].Revision != 1 || !rows.closed {
				t.Fatalf("ListBars() = (%#v, %v), closed=%t", bars, err, rows.closed)
			}
		})
	}
}

func TestMarketBarQueryRepositoryFailures(t *testing.T) {
	now := time.Now().UTC()
	id := domain.IDFromUUID(uuid.New())
	if _, err := NewMarketBarQueryRepository(nil); err == nil {
		t.Fatal("NewMarketBarQueryRepository(nil) error = nil")
	}
	repository, _ := NewMarketBarQueryRepository(fakeCatalogDatabase{})
	if _, err := repository.ResolveBarSource(context.Background(), application.BarSourceFilter{}); !errors.Is(err, domain.ErrInvalidData) {
		t.Fatalf("ResolveBarSource(invalid) error = %v", err)
	}
	for _, filter := range []application.BarReadFilter{
		{},
		{InstrumentID: id, ProviderInstrumentID: id, Interval: "5m", Order: application.BarOrderDescending, Limit: 1},
		{InstrumentID: id, ProviderInstrumentID: id, Interval: "1h", Order: "sideways", Limit: 1},
		{InstrumentID: id, ProviderInstrumentID: id, Interval: "1h", Order: application.BarOrderDescending, Limit: application.MaximumBarsPageSize + 2},
	} {
		if _, err := repository.ListBars(context.Background(), filter); !errors.Is(err, domain.ErrInvalidData) {
			t.Fatalf("ListBars(%#v) error = %v", filter, err)
		}
	}

	databaseError := &pgconn.PgError{Code: "08006"}
	repository, _ = NewMarketBarQueryRepository(fakeCatalogDatabase{queryRow: func(context.Context, string, ...any) pgx.Row {
		return scanFunc(func(...any) error { return databaseError })
	}})
	if _, err := repository.ResolveBarSource(context.Background(), application.BarSourceFilter{InstrumentID: id, ProviderID: id, EffectiveAt: now}); !errors.Is(err, domain.ErrDatabaseUnavailable) {
		t.Fatalf("ResolveBarSource(database error) = %v", err)
	}
	repository, _ = NewMarketBarQueryRepository(fakeCatalogDatabase{query: func(context.Context, string, ...any) (pgx.Rows, error) { return nil, databaseError }})
	if _, err := repository.ListBars(context.Background(), application.BarReadFilter{InstrumentID: id, ProviderInstrumentID: id, Interval: "1h", Order: application.BarOrderDescending, Limit: 1}); !errors.Is(err, domain.ErrDatabaseUnavailable) {
		t.Fatalf("ListBars(database error) = %v", err)
	}
	badRows := &fakeRows{rows: []scanFunc{func(...any) error { return errors.New("scan") }}}
	repository, _ = NewMarketBarQueryRepository(fakeCatalogDatabase{query: func(context.Context, string, ...any) (pgx.Rows, error) { return badRows, nil }})
	if _, err := repository.ListBars(context.Background(), application.BarReadFilter{InstrumentID: id, ProviderInstrumentID: id, Interval: "1h", Order: application.BarOrderDescending, Limit: 1}); err == nil || !strings.Contains(err.Error(), "scan bar") {
		t.Fatalf("ListBars(scan error) = %v", err)
	}
}
