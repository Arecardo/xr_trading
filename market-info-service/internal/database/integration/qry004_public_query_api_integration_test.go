//go:build integration

package integration_test

import (
	"context"
	"encoding/json"
	"mime"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/shopspring/decimal"

	"xr-trading/market-info-service/internal/api/httpapi"
	"xr-trading/market-info-service/internal/application"
	poolpostgres "xr-trading/market-info-service/internal/database/postgres"
	"xr-trading/market-info-service/internal/domain"
	repositorypostgres "xr-trading/market-info-service/internal/repository/postgres"
)

func TestQRY004PublicQueryHTTPContractIsReadOnly(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	admin := openAdminConnection(t, ctx)
	t.Cleanup(func() { _ = admin.Close(context.Background()) })
	assetID, instrumentID, assetCode, instrumentCode := createCoreFixture(t, ctx, admin)
	providerIDs := []domain.ID{newIntegrationID(t), newIntegrationID(t)}
	t.Cleanup(func() {
		deleteQRY004Fixture(t, context.Background(), admin, providerIDs, instrumentID, assetID)
	})

	pool, err := poolpostgres.OpenPool(ctx, poolpostgres.Config{
		DatabaseURL: integrationDatabaseURL(t), MaxConns: 4, MinConns: 0,
		MaxConnLifetime: time.Minute, HealthCheckPeriod: time.Second,
	})
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
	providers := []domain.Provider{
		qry004Provider(t, providerIDs[0], "bybit-qry004-", "QRY004 Bybit", domain.ProviderTypeExchange, now),
		qry004Provider(t, providerIDs[1], "aggregate-qry004-", "QRY004 Aggregate", domain.ProviderTypeAggregator, now),
	}
	mappings := []domain.ProviderInstrument{
		qry004Mapping(t, providers[0].ID, instrumentID, "bybit", "BTCUSDT", true, 10, now),
		qry004Mapping(t, providers[1].ID, instrumentID, "aggregate", "BTC-USDT", false, 20, now),
	}
	for index := range providers {
		if err := catalog.CreateProvider(ctx, providers[index]); err != nil {
			t.Fatalf("CreateProvider(%d) error = %v", index, err)
		}
		if err := catalog.CreateProviderInstrument(ctx, mappings[index]); err != nil {
			t.Fatalf("CreateProviderInstrument(%d) error = %v", index, err)
		}
	}

	marketTime := now.Add(-time.Minute)
	firstQuote := qry004Quote(t, instrumentID, mappings[0].ID, marketTime, "62350.12")
	firstQuote.BidPrice = qry004Decimal("62349.80")
	firstQuote.BidSize = qry004Decimal("0.42")
	firstQuote.AskPrice = qry004Decimal("62350.20")
	firstQuote.AskSize = qry004Decimal("0.35")
	firstQuote.Open24H = qry004Decimal("61000.00")
	firstQuote.High24H = qry004Decimal("63000.00")
	firstQuote.Low24H = qry004Decimal("60500.00")
	firstQuote.BaseVolume24H = qry004Decimal("15234.8")
	firstQuote.QuoteVolume24H = qry004Decimal("941234567.8")
	secondQuote := qry004Quote(t, instrumentID, mappings[1].ID, marketTime, "62348.9")
	for index, quote := range []domain.LatestQuote{firstQuote, secondQuote} {
		if applied, writeErr := marketData.UpsertLatestQuote(ctx, quote); writeErr != nil || !applied {
			t.Fatalf("UpsertLatestQuote(%d) = (%t, %v)", index, applied, writeErr)
		}
	}

	openTime := now.Add(-2 * time.Hour).Truncate(time.Hour)
	bar := db012Bar(t, instrumentID, mappings[0].ID, openTime, "qry004-bar")
	bar.OpenPrice = domain.DecimalFromExact(decimal.RequireFromString("62180.10"))
	bar.HighPrice = domain.DecimalFromExact(decimal.RequireFromString("62420.00"))
	bar.LowPrice = domain.DecimalFromExact(decimal.RequireFromString("62120.50"))
	bar.ClosePrice = domain.DecimalFromExact(decimal.RequireFromString("62350.12"))
	bar.BaseVolume = qry004Decimal("152.834")
	bar.QuoteVolume = qry004Decimal("9512345.67")
	tradeCount := int64(12450)
	bar.TradeCount = &tradeCount
	providerUpdatedAt := integrationInstant(t, bar.CloseTime.Time().Add(time.Second))
	collectedAt := integrationInstant(t, bar.CloseTime.Time().Add(2*time.Second))
	bar.ProviderUpdatedAt = &providerUpdatedAt
	bar.CollectedAt = collectedAt
	if result, writeErr := marketData.WriteMarketBar(ctx, bar); writeErr != nil || !result.Applied {
		t.Fatalf("WriteMarketBar() = (%#v, %v)", result, writeErr)
	}

	handler := qry004PublicHandler(t, catalog, pool, now)
	before := qry004MutationSnapshot(t, ctx, admin)

	var options qry004OptionsResponse
	qry004ServeJSON(t, handler, "/api/market-info/v1/instruments?"+url.Values{"asset_code": {assetCode}}.Encode(), &options)
	if len(options.Items) != 1 || options.Items[0].InstrumentID != instrumentID.String() || options.Items[0].InstrumentCode != instrumentCode || len(options.Items[0].Providers) != 2 {
		t.Fatalf("instrument options response = %#v", options)
	}
	if options.Items[0].Providers[0].ProviderCode != providers[0].Code.String() || !options.Items[0].Providers[0].IsDefault || !reflect.DeepEqual(options.Items[0].Providers[0].SupportedIntervals, []string{"1h", "1d"}) {
		t.Fatalf("default provider response = %#v", options.Items[0].Providers)
	}

	var quotes qry004QuotesResponse
	qry004ServeJSON(t, handler, "/api/market-info/v1/quotes/latest?"+url.Values{"asset_code": {assetCode}}.Encode(), &quotes)
	if quotes.Asset.AssetID != assetID.String() || quotes.Asset.AssetCode != assetCode || quotes.Asset.AssetType != "crypto" || len(quotes.Quotes) != 2 {
		t.Fatalf("latest quotes response = %#v", quotes)
	}
	quoteByProvider := make(map[string]qry004QuoteResponse, len(quotes.Quotes))
	for _, quote := range quotes.Quotes {
		quoteByProvider[quote.Provider] = quote
	}
	if quoteByProvider[providers[0].Code.String()].Price != "62350.12" || quoteByProvider[providers[1].Code.String()].Price != "62348.9" || quoteByProvider[providers[0].Code.String()].ProviderInstrumentID == quoteByProvider[providers[1].Code.String()].ProviderInstrumentID {
		t.Fatalf("source-specific quote response = %#v", quoteByProvider)
	}
	if quoteByProvider[providers[0].Code.String()].BidPrice == nil || *quoteByProvider[providers[0].Code.String()].BidPrice != "62349.8" {
		t.Fatalf("exact decimal quote response = %#v", quoteByProvider[providers[0].Code.String()])
	}

	var bars qry004BarsResponse
	barQuery := url.Values{
		"instrument_code": {instrumentCode}, "provider": {providers[0].Code.String()},
		"interval": {"1h"}, "start_time": {bar.OpenTime.String()}, "end_time": {bar.CloseTime.String()},
	}
	qry004ServeJSON(t, handler, "/api/market-info/v1/bars?"+barQuery.Encode(), &bars)
	if bars.Instrument.InstrumentID != instrumentID.String() || bars.Instrument.InstrumentCode != instrumentCode || bars.Provider.ProviderInstrumentID != mappings[0].ID.String() || bars.Provider.ProviderInstrumentCode != mappings[0].Code.String() || bars.Interval != "1h" || bars.Order != "desc" || len(bars.Bars) != 1 || bars.NextCursor != nil {
		t.Fatalf("bars response = %#v", bars)
	}
	if bars.Bars[0].Open != "62180.1" || bars.Bars[0].Close != "62350.12" || bars.Bars[0].TradeCount == nil || *bars.Bars[0].TradeCount != 12450 || bars.Bars[0].Revision != 1 || bars.Bars[0].QualityStatus != "valid" {
		t.Fatalf("bar contract response = %#v", bars.Bars[0])
	}

	var precision qry004PrecisionResponse
	unknownID := newIntegrationID(t)
	precisionBody := `{"instrument_ids":["` + instrumentID.String() + `","` + unknownID.String() + `"]}`
	qry004ServeJSONPost(t, handler, "/api/market-info/v1/instruments/precision:batch", precisionBody, &precision)
	// The QRY004 core fixture (createCoreFixture) never sets price_scale,
	// quantity_scale, lot_size or min_quantity, so both the known-but-
	// incomplete instrument and the wholly unknown one must fail closed into
	// missing_instrument_ids rather than being silently omitted.
	if len(precision.Items) != 0 {
		t.Fatalf("precision items = %#v, want none (fixture instrument has no precision data)", precision.Items)
	}
	if len(precision.MissingInstrumentIDs) != 2 || precision.MissingInstrumentIDs[0] != instrumentID.String() || precision.MissingInstrumentIDs[1] != unknownID.String() {
		t.Fatalf("precision missing_instrument_ids = %#v", precision.MissingInstrumentIDs)
	}

	after := qry004MutationSnapshot(t, ctx, admin)
	if before != after {
		t.Fatalf("public queries changed persistent state: before=%+v after=%+v", before, after)
	}
}

func qry004PublicHandler(t *testing.T, catalog *repositorypostgres.CatalogRepository, pool *pgxpool.Pool, now time.Time) http.Handler {
	t.Helper()
	options, err := application.NewInstrumentOptionsService(catalog, catalog, func() time.Time { return now })
	if err != nil {
		t.Fatalf("NewInstrumentOptionsService() error = %v", err)
	}
	quoteReader, err := repositorypostgres.NewLatestQuoteQueryRepository(pool)
	if err != nil {
		t.Fatalf("NewLatestQuoteQueryRepository() error = %v", err)
	}
	quotes, err := application.NewLatestQuotesService(catalog, quoteReader, func() time.Time { return now })
	if err != nil {
		t.Fatalf("NewLatestQuotesService() error = %v", err)
	}
	barReader, err := repositorypostgres.NewMarketBarQueryRepository(pool)
	if err != nil {
		t.Fatalf("NewMarketBarQueryRepository() error = %v", err)
	}
	bars, err := application.NewBarsService(catalog, barReader, func() time.Time { return now })
	if err != nil {
		t.Fatalf("NewBarsService() error = %v", err)
	}
	precision, err := application.NewInstrumentPrecisionService(catalog, func() time.Time { return now })
	if err != nil {
		t.Fatalf("NewInstrumentPrecisionService() error = %v", err)
	}
	mux := http.NewServeMux()
	if err := httpapi.RegisterPublicQueryRoutes(mux, httpapi.PublicQueryRoutes{
		InstrumentOptions: options, InstrumentPrecision: precision, LatestQuotes: quotes, Bars: bars,
	}); err != nil {
		t.Fatalf("RegisterPublicQueryRoutes() error = %v", err)
	}
	return httpapi.WithRequestID(mux)
}

func qry004Provider(t *testing.T, id domain.ID, prefix, name string, providerType domain.ProviderType, now time.Time) domain.Provider {
	t.Helper()
	return domain.Provider{ID: id, Code: integrationCode(t, prefix+id.String()), Name: name, ProviderType: providerType, Status: "active", CreatedAt: now, UpdatedAt: now}
}

func qry004Mapping(t *testing.T, providerID, instrumentID domain.ID, label, symbol string, isDefault bool, priority int16, now time.Time) domain.ProviderInstrument {
	t.Helper()
	id := newIntegrationID(t)
	return domain.ProviderInstrument{
		ID: id, Code: integrationCode(t, "provider."+label+".qry004."+id.String()), ProviderID: providerID,
		InstrumentID: instrumentID, ExternalSymbol: symbol, ProviderMarket: "spot",
		Capabilities: domain.ProviderCapabilities{Quote: true, Historical: true, Intervals: []domain.BarInterval{domain.BarInterval1Hour, domain.BarInterval1Day}},
		Priority:     priority, IsDefault: isDefault, Enabled: true, CreatedAt: now, UpdatedAt: now,
	}
}

func qry004Quote(t *testing.T, instrumentID, mappingID domain.ID, marketTime time.Time, price string) domain.LatestQuote {
	t.Helper()
	instant := integrationInstant(t, marketTime)
	collected := integrationInstant(t, marketTime.Add(time.Second))
	return domain.LatestQuote{
		InstrumentID: instrumentID, ProviderInstrumentID: mappingID, MarketTime: instant,
		LastPrice: domain.DecimalFromExact(decimal.RequireFromString(price)), QualityStatus: "valid", CollectedAt: collected,
		Metadata: json.RawMessage(`{}`),
	}
}

func qry004Decimal(value string) *domain.Decimal {
	parsed := domain.DecimalFromExact(decimal.RequireFromString(value))
	return &parsed
}

func qry004ServeJSON(t *testing.T, handler http.Handler, path string, target any) {
	t.Helper()
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, path, nil))
	if response.Code != http.StatusOK {
		t.Fatalf("GET %s status = %d, body = %s", path, response.Code, response.Body.String())
	}
	mediaType, _, mediaTypeErr := mime.ParseMediaType(response.Header().Get("Content-Type"))
	if mediaTypeErr != nil || mediaType != "application/json" || !httpapi.ValidRequestID(response.Header().Get(httpapi.RequestIDHeader)) {
		t.Fatalf("GET %s headers = %#v", path, response.Header())
	}
	decoder := json.NewDecoder(response.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		t.Fatalf("decode GET %s response: %v; body=%s", path, err, response.Body.String())
	}
}

func qry004ServeJSONPost(t *testing.T, handler http.Handler, path, body string, target any) {
	t.Helper()
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodPost, path, strings.NewReader(body)))
	if response.Code != http.StatusOK {
		t.Fatalf("POST %s status = %d, body = %s", path, response.Code, response.Body.String())
	}
	mediaType, _, mediaTypeErr := mime.ParseMediaType(response.Header().Get("Content-Type"))
	if mediaTypeErr != nil || mediaType != "application/json" || !httpapi.ValidRequestID(response.Header().Get(httpapi.RequestIDHeader)) {
		t.Fatalf("POST %s headers = %#v", path, response.Header())
	}
	decoder := json.NewDecoder(response.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		t.Fatalf("decode POST %s response: %v; body=%s", path, err, response.Body.String())
	}
}

type qry004MutationCounts struct {
	Subscriptions int64
	Runs          int64
	Tasks         int64
	Checkpoints   int64
	Quotes        int64
	Bars          int64
	QualityIssues int64
}

func qry004MutationSnapshot(t *testing.T, ctx context.Context, admin *pgx.Conn) qry004MutationCounts {
	t.Helper()
	var result qry004MutationCounts
	err := admin.QueryRow(ctx, `
SELECT
    (SELECT count(*) FROM market_data.collection_subscriptions),
    (SELECT count(*) FROM market_data.ingestion_runs),
    (SELECT count(*) FROM market_data.ingestion_tasks),
    (SELECT count(*) FROM market_data.ingestion_checkpoints),
    (SELECT count(*) FROM market_data.latest_quotes),
    (SELECT count(*) FROM market_data.market_bars),
    (SELECT count(*) FROM market_data.data_quality_issues)`).Scan(
		&result.Subscriptions, &result.Runs, &result.Tasks, &result.Checkpoints,
		&result.Quotes, &result.Bars, &result.QualityIssues,
	)
	if err != nil {
		t.Fatalf("snapshot public-query tables: %v", err)
	}
	return result
}

func deleteQRY004Fixture(t *testing.T, ctx context.Context, admin *pgx.Conn, providerIDs []domain.ID, instrumentID, assetID domain.ID) {
	t.Helper()
	statements := []string{
		"DELETE FROM market_data.data_quality_issues WHERE instrument_id = $1",
		"DELETE FROM market_data.latest_quotes WHERE instrument_id = $1",
		"DELETE FROM market_data.market_bars WHERE instrument_id = $1",
		"DELETE FROM market_data.collection_subscriptions WHERE provider_instrument_id IN (SELECT id FROM market_data.provider_instruments WHERE instrument_id = $1)",
		"DELETE FROM market_data.provider_instruments WHERE instrument_id = $1",
	}
	for _, statement := range statements {
		if _, err := admin.Exec(ctx, statement, instrumentID.UUID()); err != nil {
			t.Errorf("delete QRY004 fixture using %q: %v", statement, err)
		}
	}
	for _, providerID := range providerIDs {
		if _, err := admin.Exec(ctx, "DELETE FROM market_data.providers WHERE id = $1", providerID.UUID()); err != nil {
			t.Errorf("delete QRY004 provider fixture: %v", err)
		}
	}
	if _, err := admin.Exec(ctx, "DELETE FROM core.instruments WHERE id = $1", instrumentID.UUID()); err != nil {
		t.Errorf("delete QRY004 instrument fixture: %v", err)
	}
	if _, err := admin.Exec(ctx, "DELETE FROM core.assets WHERE id = $1", assetID.UUID()); err != nil {
		t.Errorf("delete QRY004 asset fixture: %v", err)
	}
}

type qry004OptionsResponse struct {
	Items []struct {
		InstrumentID   string `json:"instrument_id"`
		InstrumentCode string `json:"instrument_code"`
		DisplayName    string `json:"display_name"`
		Providers      []struct {
			ProviderCode       string   `json:"provider_code"`
			DisplayName        string   `json:"display_name"`
			IsDefault          bool     `json:"is_default"`
			Priority           int16    `json:"priority"`
			SupportedIntervals []string `json:"supported_intervals"`
		} `json:"providers"`
	} `json:"items"`
	NextCursor *string `json:"next_cursor"`
}

type qry004QuotesResponse struct {
	Asset struct {
		AssetID   string `json:"asset_id"`
		AssetCode string `json:"asset_code"`
		AssetType string `json:"asset_type"`
	} `json:"asset"`
	Quotes []qry004QuoteResponse `json:"quotes"`
}

type qry004QuoteResponse struct {
	InstrumentID           string  `json:"instrument_id"`
	InstrumentCode         string  `json:"instrument_code"`
	Provider               string  `json:"provider"`
	ProviderInstrumentID   string  `json:"provider_instrument_id"`
	ProviderInstrumentCode string  `json:"provider_instrument_code"`
	ProviderSymbol         string  `json:"provider_symbol"`
	Price                  string  `json:"price"`
	BidPrice               *string `json:"bid_price"`
	BidSize                *string `json:"bid_size"`
	AskPrice               *string `json:"ask_price"`
	AskSize                *string `json:"ask_size"`
	Open24H                *string `json:"open_24h"`
	High24H                *string `json:"high_24h"`
	Low24H                 *string `json:"low_24h"`
	BaseVolume24H          *string `json:"base_volume_24h"`
	QuoteVolume24H         *string `json:"quote_volume_24h"`
	QuoteCurrency          string  `json:"quote_currency"`
	MarketTime             string  `json:"market_time"`
	ReceivedAt             string  `json:"received_at"`
	QualityStatus          string  `json:"quality_status"`
}

type qry004PrecisionResponse struct {
	Items []struct {
		InstrumentID   string `json:"instrument_id"`
		InstrumentCode string `json:"instrument_code"`
		PriceScale     int16  `json:"price_scale"`
		QuantityScale  int16  `json:"quantity_scale"`
		LotSize        string `json:"lot_size"`
		MinQuantity    string `json:"min_quantity"`
		AsOf           string `json:"as_of"`
	} `json:"items"`
	MissingInstrumentIDs []string `json:"missing_instrument_ids"`
}

type qry004BarsResponse struct {
	Instrument struct {
		InstrumentID   string  `json:"instrument_id"`
		InstrumentCode string  `json:"instrument_code"`
		BaseAssetCode  string  `json:"base_asset_code"`
		QuoteAssetCode *string `json:"quote_asset_code"`
		QuoteCurrency  string  `json:"quote_currency"`
	} `json:"instrument"`
	Provider struct {
		ProviderCode           string `json:"provider_code"`
		ProviderInstrumentID   string `json:"provider_instrument_id"`
		ProviderInstrumentCode string `json:"provider_instrument_code"`
		ProviderSymbol         string `json:"provider_symbol"`
	} `json:"provider"`
	Interval string `json:"interval"`
	Order    string `json:"order"`
	Bars     []struct {
		OpenTime          string  `json:"open_time"`
		CloseTime         string  `json:"close_time"`
		Open              string  `json:"open"`
		High              string  `json:"high"`
		Low               string  `json:"low"`
		Close             string  `json:"close"`
		Volume            *string `json:"volume"`
		QuoteVolume       *string `json:"quote_volume"`
		TradeCount        *int64  `json:"trade_count"`
		Revision          int     `json:"revision"`
		IsClosed          bool    `json:"is_closed"`
		QualityStatus     string  `json:"quality_status"`
		ProviderUpdatedAt *string `json:"provider_updated_at"`
		CollectedAt       string  `json:"collected_at"`
	} `json:"bars"`
	NextCursor *string `json:"next_cursor"`
}
