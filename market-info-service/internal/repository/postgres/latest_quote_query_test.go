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

	"xr-trading/market-info-service/internal/application"
	"xr-trading/market-info-service/internal/domain"
)

func TestLatestQuoteQueryRepositoryBuildsFilteredJoin(t *testing.T) {
	now := time.Date(2026, time.July, 15, 0, 0, 0, 0, time.UTC)
	assetID := domain.IDFromUUID(uuid.MustParse("019f1452-90f7-7992-a87a-ca272789160f"))
	instrumentID := domain.IDFromUUID(uuid.MustParse("019f1452-90f7-7992-a87a-ca2727891601"))
	providerID := domain.IDFromUUID(uuid.MustParse("019f1452-90f7-7992-a87a-ca2727891602"))
	rows := &fakeRows{rows: []scanFunc{latestQuoteProjectionRow(now, instrumentID, providerID)}}
	database := fakeCatalogDatabase{query: func(_ context.Context, query string, args ...any) (pgx.Rows, error) {
		for _, required := range []string{
			"mapping.instrument_id = quote.instrument_id", "instrument.status = 'active'",
			"mapping.capabilities @> '{\"quote\": true}'::jsonb", "provider.status IN ('active', 'degraded')",
			"quote.instrument_id =", "provider.id =", "ORDER BY instrument.code ASC, provider.code ASC, mapping.code ASC",
		} {
			if !strings.Contains(query, required) {
				t.Fatalf("query missing %q: %s", required, query)
			}
		}
		if len(args) != 7 || args[0] != assetID.UUID() || args[1] != now || args[4] != now || args[5] != instrumentID.UUID() || args[6] != providerID.UUID() {
			t.Fatalf("query args = %#v", args)
		}
		return rows, nil
	}}
	repository, err := NewLatestQuoteQueryRepository(database)
	if err != nil {
		t.Fatalf("NewLatestQuoteQueryRepository() error = %v", err)
	}
	records, err := repository.ListLatestQuoteRecords(context.Background(), application.LatestQuoteFilter{AssetID: assetID, InstrumentID: &instrumentID, ProviderID: &providerID, EffectiveAt: now})
	if err != nil {
		t.Fatalf("ListLatestQuoteRecords() error = %v", err)
	}
	if len(records) != 1 || records[0].Quote.InstrumentID != instrumentID || records[0].ProviderID != providerID || records[0].ProviderCode.String() != "bybit" || records[0].ProviderInstrumentCode.String() != "provider.bybit.btc-usdt" || records[0].Quote.LastPrice.String() != "100.25" || records[0].Quote.BidPrice == nil || records[0].Quote.BidPrice.String() != "100.2" || !rows.closed {
		t.Fatalf("ListLatestQuoteRecords() = %#v, closed=%t", records, rows.closed)
	}
}

func TestLatestQuoteQueryRepositoryFailures(t *testing.T) {
	now := time.Now().UTC()
	assetID := domain.IDFromUUID(uuid.New())
	zero := domain.ID{}
	for _, filter := range []application.LatestQuoteFilter{
		{},
		{AssetID: assetID},
		{AssetID: assetID, EffectiveAt: now, InstrumentID: &zero},
		{AssetID: assetID, EffectiveAt: now, ProviderID: &zero},
	} {
		repository, _ := NewLatestQuoteQueryRepository(fakeCatalogDatabase{})
		if _, err := repository.ListLatestQuoteRecords(context.Background(), filter); !errors.Is(err, domain.ErrInvalidData) {
			t.Fatalf("ListLatestQuoteRecords(%#v) error = %v", filter, err)
		}
	}
	if _, err := NewLatestQuoteQueryRepository(nil); err == nil {
		t.Fatal("NewLatestQuoteQueryRepository(nil) error = nil")
	}

	databaseError := &pgconn.PgError{Code: "08006"}
	repository, _ := NewLatestQuoteQueryRepository(fakeCatalogDatabase{query: func(context.Context, string, ...any) (pgx.Rows, error) { return nil, databaseError }})
	if _, err := repository.ListLatestQuoteRecords(context.Background(), application.LatestQuoteFilter{AssetID: assetID, EffectiveAt: now}); !errors.Is(err, domain.ErrDatabaseUnavailable) {
		t.Fatalf("ListLatestQuoteRecords(query error) = %v", err)
	}

	badRows := &fakeRows{rows: []scanFunc{func(...any) error { return errors.New("scan") }}}
	repository, _ = NewLatestQuoteQueryRepository(fakeCatalogDatabase{query: func(context.Context, string, ...any) (pgx.Rows, error) { return badRows, nil }})
	if _, err := repository.ListLatestQuoteRecords(context.Background(), application.LatestQuoteFilter{AssetID: assetID, EffectiveAt: now}); err == nil || !strings.Contains(err.Error(), "scan latest quote record") {
		t.Fatalf("ListLatestQuoteRecords(scan error) = %v", err)
	}

	iterationRows := &fakeRows{err: databaseError}
	repository, _ = NewLatestQuoteQueryRepository(fakeCatalogDatabase{query: func(context.Context, string, ...any) (pgx.Rows, error) { return iterationRows, nil }})
	if _, err := repository.ListLatestQuoteRecords(context.Background(), application.LatestQuoteFilter{AssetID: assetID, EffectiveAt: now}); !errors.Is(err, domain.ErrDatabaseUnavailable) {
		t.Fatalf("ListLatestQuoteRecords(iteration error) = %v", err)
	}
}

func latestQuoteProjectionRow(now time.Time, instrumentID, providerID domain.ID) scanFunc {
	return func(destinations ...any) error {
		*destinations[0].(*uuid.UUID) = instrumentID.UUID()
		*destinations[1].(*string) = "instrument.crypto.btc-usdt"
		*destinations[2].(*string) = "USDT"
		*destinations[3].(*uuid.UUID) = providerID.UUID()
		*destinations[4].(*string) = "bybit"
		*destinations[5].(*uuid.UUID) = uuid.MustParse("019f1452-90f7-7992-a87a-ca2727891603")
		*destinations[6].(*string) = "provider.bybit.btc-usdt"
		*destinations[7].(*string) = "BTCUSDT"
		*destinations[8].(*time.Time) = now
		*destinations[9].(*decimal.Decimal) = decimal.RequireFromString("100.25")
		bid := decimal.RequireFromString("100.20")
		*destinations[10].(**decimal.Decimal) = &bid
		*destinations[11].(**decimal.Decimal) = nil
		*destinations[12].(**decimal.Decimal) = nil
		*destinations[13].(**decimal.Decimal) = nil
		*destinations[14].(**decimal.Decimal) = nil
		*destinations[15].(**decimal.Decimal) = nil
		*destinations[16].(**decimal.Decimal) = nil
		*destinations[17].(**decimal.Decimal) = nil
		*destinations[18].(**decimal.Decimal) = nil
		*destinations[19].(*string) = "warning"
		*destinations[20].(*time.Time) = now.Add(time.Second)
		*destinations[21].(*[]byte) = []byte(`{}`)
		return nil
	}
}
