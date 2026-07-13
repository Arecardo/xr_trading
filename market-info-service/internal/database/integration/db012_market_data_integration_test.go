//go:build integration

package integration_test

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	poolpostgres "xr-trading/market-info-service/internal/database/postgres"
	"xr-trading/market-info-service/internal/domain"
	repositorypostgres "xr-trading/market-info-service/internal/repository/postgres"

	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"
)

func TestDB012MarketDataRepositoryAgainstPostgres(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	admin := openAdminConnection(t, ctx)
	t.Cleanup(func() { _ = admin.Close(context.Background()) })
	assetID, instrumentID, _, _ := createCoreFixture(t, ctx, admin)
	providerID := newIntegrationID(t)
	t.Cleanup(func() { deleteDB012Fixture(t, context.Background(), admin, providerID, instrumentID, assetID) })

	pool, err := poolpostgres.OpenPool(ctx, poolpostgres.Config{DatabaseURL: integrationDatabaseURL(t), MaxConns: 3, MinConns: 0, MaxConnLifetime: time.Minute, HealthCheckPeriod: time.Second})
	if err != nil {
		t.Fatalf("OpenPool() error = %v", err)
	}
	defer pool.Close()
	catalog, err := repositorypostgres.NewCatalogRepository(pool)
	if err != nil {
		t.Fatalf("NewCatalogRepository() error = %v", err)
	}
	marketData, err := repositorypostgres.NewMarketDataRepository(pool)
	if err != nil {
		t.Fatalf("NewMarketDataRepository() error = %v", err)
	}

	now := time.Now().UTC().Truncate(time.Second)
	provider := domain.Provider{ID: providerID, Code: integrationCode(t, "bybit-db012-"+providerID.String()), Name: "DB012 Bybit", ProviderType: "EXCHANGE", Status: "active", CreatedAt: now, UpdatedAt: now}
	if err := catalog.CreateProvider(ctx, provider); err != nil {
		t.Fatalf("CreateProvider() error = %v", err)
	}
	firstMapping := db012Mapping(t, providerID, instrumentID, now, "primary")
	secondMapping := db012Mapping(t, providerID, instrumentID, now, "secondary")
	if err := catalog.CreateProviderInstrument(ctx, firstMapping); err != nil {
		t.Fatalf("CreateProviderInstrument(first) error = %v", err)
	}
	if err := catalog.CreateProviderInstrument(ctx, secondMapping); err != nil {
		t.Fatalf("CreateProviderInstrument(second) error = %v", err)
	}

	newQuote := db012Quote(instrumentID, firstMapping.ID, now, "100")
	if applied, err := marketData.UpsertLatestQuote(ctx, newQuote); err != nil || !applied {
		t.Fatalf("UpsertLatestQuote(new) = (%t, %v)", applied, err)
	}
	oldQuote := db012Quote(instrumentID, firstMapping.ID, now.Add(-time.Minute), "90")
	if applied, err := marketData.UpsertLatestQuote(ctx, oldQuote); err != nil || applied {
		t.Fatalf("UpsertLatestQuote(old) = (%t, %v)", applied, err)
	}
	otherSourceQuote := db012Quote(instrumentID, secondMapping.ID, now, "200")
	if applied, err := marketData.UpsertLatestQuote(ctx, otherSourceQuote); err != nil || !applied {
		t.Fatalf("UpsertLatestQuote(other source) = (%t, %v)", applied, err)
	}
	quotes, err := marketData.ListLatestQuotes(ctx, instrumentID)
	if err != nil || len(quotes) != 2 {
		t.Fatalf("ListLatestQuotes() = (%#v, %v)", quotes, err)
	}
	quoteBySource := map[domain.ID]decimal.Decimal{}
	for _, quote := range quotes {
		quoteBySource[quote.ProviderInstrumentID] = quote.LastPrice
	}
	if quoteBySource[firstMapping.ID].String() != "100" || quoteBySource[secondMapping.ID].String() != "200" {
		t.Fatalf("source-specific quotes = %#v", quoteBySource)
	}

	firstBar := db012Bar(instrumentID, firstMapping.ID, now.Truncate(time.Hour), "raw-a")
	if result, err := marketData.WriteMarketBar(ctx, firstBar); err != nil || !result.Applied || result.Revision != 1 {
		t.Fatalf("WriteMarketBar(first) = (%#v, %v)", result, err)
	}
	if result, err := marketData.WriteMarketBar(ctx, firstBar); err != nil || result.Applied || result.Revision != 1 {
		t.Fatalf("WriteMarketBar(unchanged) = (%#v, %v)", result, err)
	}
	revised := firstBar
	revised.ClosePrice = decimal.NewFromInt(106)
	revised.RawHash = "raw-b"
	if result, err := marketData.WriteMarketBar(ctx, revised); err != nil || !result.Applied || result.Revision != 2 {
		t.Fatalf("WriteMarketBar(revision) = (%#v, %v)", result, err)
	}
	secondBar := db012Bar(instrumentID, firstMapping.ID, firstBar.OpenTime.Add(time.Hour), "raw-c")
	if _, err := marketData.WriteMarketBar(ctx, secondBar); err != nil {
		t.Fatalf("WriteMarketBar(second) error = %v", err)
	}
	bars, err := marketData.ListCurrentMarketBars(ctx, domain.MarketBarFilter{InstrumentID: instrumentID, ProviderInstrumentID: firstMapping.ID, Interval: "1h", Limit: 1})
	if err != nil || len(bars) != 1 || bars[0].OpenTime != secondBar.OpenTime {
		t.Fatalf("ListCurrentMarketBars(latest) = (%#v, %v)", bars, err)
	}
	previous, err := marketData.ListCurrentMarketBars(ctx, domain.MarketBarFilter{InstrumentID: instrumentID, ProviderInstrumentID: firstMapping.ID, Interval: "1h", BeforeOpenTime: &bars[0].OpenTime, Limit: 1})
	if err != nil || len(previous) != 1 || previous[0].Revision != 2 || previous[0].ClosePrice.String() != "106" {
		t.Fatalf("ListCurrentMarketBars(previous) = (%#v, %v)", previous, err)
	}
}

func db012Mapping(t *testing.T, providerID, instrumentID domain.ID, now time.Time, suffix string) domain.ProviderInstrument {
	t.Helper()
	id := newIntegrationID(t)
	return domain.ProviderInstrument{ID: id, Code: integrationCode(t, "provider.bybit.db012-"+suffix+"-"+id.String()), ProviderID: providerID, InstrumentID: instrumentID, ExternalSymbol: "DB012" + suffix, ProviderMarket: "spot", Capabilities: domain.ProviderCapabilities{Quote: true, Intervals: []domain.BarInterval{domain.BarInterval1Hour}}, Enabled: true, CreatedAt: now, UpdatedAt: now}
}

func db012Quote(instrumentID, providerInstrumentID domain.ID, marketTime time.Time, price string) domain.LatestQuote {
	return domain.LatestQuote{InstrumentID: instrumentID, ProviderInstrumentID: providerInstrumentID, MarketTime: marketTime, LastPrice: decimal.RequireFromString(price), QualityStatus: "valid", CollectedAt: marketTime, Metadata: json.RawMessage(`{}`)}
}

func db012Bar(instrumentID, providerInstrumentID domain.ID, openTime time.Time, rawHash string) domain.MarketBar {
	return domain.MarketBar{InstrumentID: instrumentID, ProviderInstrumentID: providerInstrumentID, Interval: "1h", OpenTime: openTime, CloseTime: openTime.Add(time.Hour), OpenPrice: decimal.NewFromInt(100), HighPrice: decimal.NewFromInt(110), LowPrice: decimal.NewFromInt(90), ClosePrice: decimal.NewFromInt(105), IsClosed: true, QualityStatus: "valid", CollectedAt: openTime.Add(time.Hour), RawHash: rawHash, Metadata: json.RawMessage(`{}`)}
}

func deleteDB012Fixture(t *testing.T, ctx context.Context, admin *pgx.Conn, providerID, instrumentID, assetID domain.ID) {
	t.Helper()
	if _, err := admin.Exec(ctx, "DELETE FROM market_data.latest_quotes WHERE provider_instrument_id IN (SELECT id FROM market_data.provider_instruments WHERE instrument_id = $1)", instrumentID.UUID()); err != nil {
		t.Errorf("delete quote fixtures: %v", err)
	}
	if _, err := admin.Exec(ctx, "DELETE FROM market_data.market_bars WHERE provider_instrument_id IN (SELECT id FROM market_data.provider_instruments WHERE instrument_id = $1)", instrumentID.UUID()); err != nil {
		t.Errorf("delete market bar fixtures: %v", err)
	}
	deleteDB011Fixture(t, ctx, admin, providerID, instrumentID, assetID)
}
