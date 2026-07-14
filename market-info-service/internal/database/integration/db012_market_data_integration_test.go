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

	"xr-trading/market-info-service/internal/application"
)

func TestDB012MarketDataRepositoryAgainstPostgres(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	admin := openAdminConnection(t, ctx)
	t.Cleanup(func() { _ = admin.Close(context.Background()) })
	assetID, instrumentID, assetCode, instrumentCode := createCoreFixture(t, ctx, admin)
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

	newQuote := db012Quote(t, instrumentID, firstMapping.ID, now, "100")
	if applied, err := marketData.UpsertLatestQuote(ctx, newQuote); err != nil || !applied {
		t.Fatalf("UpsertLatestQuote(new) = (%t, %v)", applied, err)
	}
	oldQuote := db012Quote(t, instrumentID, firstMapping.ID, now.Add(-time.Minute), "90")
	if applied, err := marketData.UpsertLatestQuote(ctx, oldQuote); err != nil || applied {
		t.Fatalf("UpsertLatestQuote(old) = (%t, %v)", applied, err)
	}
	otherSourceQuote := db012Quote(t, instrumentID, secondMapping.ID, now, "200")
	if applied, err := marketData.UpsertLatestQuote(ctx, otherSourceQuote); err != nil || !applied {
		t.Fatalf("UpsertLatestQuote(other source) = (%t, %v)", applied, err)
	}
	quotes, err := marketData.ListLatestQuotes(ctx, instrumentID)
	if err != nil || len(quotes) != 2 {
		t.Fatalf("ListLatestQuotes() = (%#v, %v)", quotes, err)
	}
	quoteBySource := map[domain.ID]domain.Decimal{}
	for _, quote := range quotes {
		quoteBySource[quote.ProviderInstrumentID] = quote.LastPrice
	}
	if quoteBySource[firstMapping.ID].String() != "100" || quoteBySource[secondMapping.ID].String() != "200" {
		t.Fatalf("source-specific quotes = %#v", quoteBySource)
	}
	latestQuoteReader, err := repositorypostgres.NewLatestQuoteQueryRepository(pool)
	if err != nil {
		t.Fatalf("NewLatestQuoteQueryRepository() error = %v", err)
	}
	latestQuoteService, err := application.NewLatestQuotesService(catalog, latestQuoteReader, func() time.Time { return now })
	if err != nil {
		t.Fatalf("NewLatestQuotesService() error = %v", err)
	}
	latest, err := latestQuoteService.List(ctx, application.LatestQuotesInput{AssetCode: assetCode, InstrumentCode: instrumentCode, ProviderCode: provider.Code.String()})
	if err != nil {
		t.Fatalf("LatestQuotesService.List() error = %v", err)
	}
	if latest.Asset.ID != assetID || len(latest.Quotes) != 2 || latest.Quotes[0].Quote.ProviderInstrumentID == latest.Quotes[1].Quote.ProviderInstrumentID || latest.Quotes[0].ProviderCode != provider.Code || latest.Quotes[1].ProviderCode != provider.Code {
		t.Fatalf("LatestQuotesService.List() = %#v", latest)
	}

	firstBar := db012Bar(t, instrumentID, firstMapping.ID, now.Truncate(time.Hour), "raw-a")
	if result, err := marketData.WriteMarketBar(ctx, firstBar); err != nil || !result.Applied || result.Revision != 1 {
		t.Fatalf("WriteMarketBar(first) = (%#v, %v)", result, err)
	}
	if result, err := marketData.WriteMarketBar(ctx, firstBar); err != nil || result.Applied || result.Revision != 1 {
		t.Fatalf("WriteMarketBar(unchanged) = (%#v, %v)", result, err)
	}
	revised := firstBar
	revised.ClosePrice = domain.DecimalFromExact(decimal.NewFromInt(106))
	revised.RawHash = "raw-b"
	if result, err := marketData.WriteMarketBar(ctx, revised); err != nil || !result.Applied || result.Revision != 2 {
		t.Fatalf("WriteMarketBar(revision) = (%#v, %v)", result, err)
	}
	secondBar := db012Bar(t, instrumentID, firstMapping.ID, firstBar.OpenTime.Time().Add(time.Hour), "raw-c")
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
	barReader, err := repositorypostgres.NewMarketBarQueryRepository(pool)
	if err != nil {
		t.Fatalf("NewMarketBarQueryRepository() error = %v", err)
	}
	barService, err := application.NewBarsService(catalog, barReader, func() time.Time { return now })
	if err != nil {
		t.Fatalf("NewBarsService() error = %v", err)
	}
	start := firstBar.OpenTime.Time()
	end := secondBar.CloseTime.Time()
	barPage, err := barService.List(ctx, application.BarsInput{InstrumentCode: instrumentCode, ProviderCode: provider.Code.String(), Interval: "1h", StartTime: &start, EndTime: &end, Order: application.BarOrderDescending, Limit: 1})
	if err != nil || len(barPage.Bars) != 1 || barPage.Bars[0].OpenTime != secondBar.OpenTime || barPage.NextCursorOpenTime == nil || barPage.Source.ProviderInstrumentID != firstMapping.ID {
		t.Fatalf("BarsService.List(first page) = (%#v, %v)", barPage, err)
	}
	cursorTime := barPage.NextCursorOpenTime.Time()
	barPage, err = barService.List(ctx, application.BarsInput{InstrumentCode: instrumentCode, ProviderCode: provider.Code.String(), Interval: "1h", StartTime: &start, EndTime: &end, Order: application.BarOrderDescending, CursorOpenTime: &cursorTime, Limit: 1})
	if err != nil || len(barPage.Bars) != 1 || barPage.Bars[0].Revision != 2 || barPage.Bars[0].ClosePrice.String() != "106" || barPage.NextCursorOpenTime != nil {
		t.Fatalf("BarsService.List(second page) = (%#v, %v)", barPage, err)
	}
}

func db012Mapping(t *testing.T, providerID, instrumentID domain.ID, now time.Time, suffix string) domain.ProviderInstrument {
	t.Helper()
	id := newIntegrationID(t)
	return domain.ProviderInstrument{ID: id, Code: integrationCode(t, "provider.bybit.db012-"+suffix+"-"+id.String()), ProviderID: providerID, InstrumentID: instrumentID, ExternalSymbol: "DB012" + suffix, ProviderMarket: "spot", Capabilities: domain.ProviderCapabilities{Quote: true, Historical: true, Intervals: []domain.BarInterval{domain.BarInterval1Hour}}, Enabled: true, CreatedAt: now, UpdatedAt: now}
}

func db012Quote(t *testing.T, instrumentID, providerInstrumentID domain.ID, marketTime time.Time, price string) domain.LatestQuote {
	t.Helper()
	instant := integrationInstant(t, marketTime)
	return domain.LatestQuote{InstrumentID: instrumentID, ProviderInstrumentID: providerInstrumentID, MarketTime: instant, LastPrice: domain.DecimalFromExact(decimal.RequireFromString(price)), QualityStatus: "valid", CollectedAt: instant, Metadata: json.RawMessage(`{}`)}
}

func db012Bar(t *testing.T, instrumentID, providerInstrumentID domain.ID, openTime time.Time, rawHash string) domain.MarketBar {
	t.Helper()
	closeTime := openTime.Add(time.Hour)
	return domain.MarketBar{InstrumentID: instrumentID, ProviderInstrumentID: providerInstrumentID, Interval: "1h", OpenTime: integrationInstant(t, openTime), CloseTime: integrationInstant(t, closeTime), OpenPrice: domain.DecimalFromExact(decimal.NewFromInt(100)), HighPrice: domain.DecimalFromExact(decimal.NewFromInt(110)), LowPrice: domain.DecimalFromExact(decimal.NewFromInt(90)), ClosePrice: domain.DecimalFromExact(decimal.NewFromInt(105)), IsClosed: true, QualityStatus: "valid", CollectedAt: integrationInstant(t, closeTime), RawHash: rawHash, Metadata: json.RawMessage(`{}`)}
}

func integrationInstant(t *testing.T, value time.Time) domain.UTCInstant {
	t.Helper()
	instant, err := domain.NewUTCInstant(value)
	if err != nil {
		t.Fatalf("NewUTCInstant() error = %v", err)
	}
	return instant
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
