package coingecko

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"xr-trading/market-info-service/internal/domain"
	"xr-trading/market-info-service/internal/ingestion/ports"
)

var fixedCoinGeckoNow = time.Date(2026, 7, 15, 3, 30, 0, 0, time.UTC)

func newHTTPTestAdapter(t *testing.T, handler http.HandlerFunc, options ...func(*Config)) (*Adapter, *httptest.Server) {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	config := Config{BaseURL: server.URL, HTTPClient: server.Client(), Now: func() time.Time { return fixedCoinGeckoNow }, UserAgent: "adapter-test/1"}
	for _, option := range options {
		option(&config)
	}
	adapter, err := New(config)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	return adapter, server
}

func usdtUSDReference(t *testing.T) ports.ProviderInstrumentRef {
	t.Helper()
	return ports.ProviderInstrumentRef{
		ProviderInstrumentID: mustCoinGeckoID(t), ProviderInstrumentCode: mustCoinGeckoCode(t, "provider.coingecko.fx.usdt-usd"),
		InstrumentID: mustCoinGeckoID(t), AssetID: mustCoinGeckoID(t), ProviderCode: mustCoinGeckoCode(t, "coingecko"),
		ProviderMarket: fxMarket, AssetType: domain.AssetTypeFX, InstrumentType: domain.InstrumentTypeFX,
		ExternalSymbol: "tether", InstrumentCode: mustCoinGeckoCode(t, "instrument.coingecko.fx.usdt-usd"),
		InstrumentSymbol: "USDT-USD", QuoteCurrency: "USD", Metadata: json.RawMessage(`{"reference_rate":true}`),
	}
}

func coinGeckoBarsRequest(t *testing.T, reference ports.ProviderInstrumentRef, start, end time.Time, limit int) ports.FetchBarsRequest {
	t.Helper()
	return ports.FetchBarsRequest{
		Instrument: reference, Interval: domain.BarInterval1Day,
		StartTime: mustCoinGeckoInstant(t, start), EndTime: mustCoinGeckoInstant(t, end), Limit: limit,
	}
}

func fixture(t *testing.T, name string) []byte {
	t.Helper()
	content, err := os.ReadFile("testdata/" + name)
	if err != nil {
		t.Fatalf("read fixture %s: %v", name, err)
	}
	return content
}

func mustCoinGeckoID(t *testing.T) domain.ID {
	t.Helper()
	value, err := domain.NewID()
	if err != nil {
		t.Fatalf("NewID() error = %v", err)
	}
	return value
}

func mustCoinGeckoCode(t *testing.T, value string) domain.Code {
	t.Helper()
	parsed, err := domain.ParseCode(value)
	if err != nil {
		t.Fatalf("ParseCode(%q) error = %v", value, err)
	}
	return parsed
}

func mustCoinGeckoInstant(t *testing.T, value time.Time) domain.UTCInstant {
	t.Helper()
	parsed, err := domain.NewUTCInstant(value)
	if err != nil {
		t.Fatalf("NewUTCInstant() error = %v", err)
	}
	return parsed
}
