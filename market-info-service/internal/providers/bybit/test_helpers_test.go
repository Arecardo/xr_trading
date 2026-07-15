package bybit

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

var fixedBybitNow = time.Date(2026, 7, 15, 3, 30, 0, 0, time.UTC)

func newHTTPTestAdapter(t *testing.T, handler http.HandlerFunc, options ...func(*Config)) (*Adapter, *httptest.Server) {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	config := Config{BaseURL: server.URL, HTTPClient: server.Client(), Now: func() time.Time { return fixedBybitNow }, UserAgent: "adapter-test/1"}
	for _, option := range options {
		option(&config)
	}
	adapter, err := New(config)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	return adapter, server
}

func bybitReference(t *testing.T, symbol string) ports.ProviderInstrumentRef {
	t.Helper()
	return ports.ProviderInstrumentRef{
		ProviderInstrumentID: mustBybitID(t), ProviderInstrumentCode: mustBybitCode(t, "provider.bybit.spot."+lowerASCII(symbol)),
		InstrumentID: mustBybitID(t), AssetID: mustBybitID(t), ProviderCode: mustBybitCode(t, "bybit"),
		ProviderMarket: spotMarket, AssetType: domain.AssetTypeCrypto, InstrumentType: domain.InstrumentTypeSpot,
		ExternalSymbol: symbol, InstrumentCode: mustBybitCode(t, "instrument.crypto.spot."+lowerASCII(symbol)),
		InstrumentSymbol: symbol, QuoteCurrency: "USDT", Metadata: json.RawMessage(`{"category":"spot"}`),
	}
}

func bybitBarsRequest(t *testing.T, reference ports.ProviderInstrumentRef) ports.FetchBarsRequest {
	t.Helper()
	start := mustBybitInstant(t, time.Date(2026, 7, 15, 0, 0, 0, 0, time.UTC))
	end := mustBybitInstant(t, time.Date(2026, 7, 15, 4, 0, 0, 0, time.UTC))
	return ports.FetchBarsRequest{Instrument: reference, Interval: domain.BarInterval1Hour, StartTime: start, EndTime: end, Limit: 2}
}

func fixture(t *testing.T, name string) []byte {
	t.Helper()
	content, err := os.ReadFile("testdata/" + name)
	if err != nil {
		t.Fatalf("read fixture %s: %v", name, err)
	}
	return content
}

func mustBybitID(t *testing.T) domain.ID {
	t.Helper()
	value, err := domain.NewID()
	if err != nil {
		t.Fatalf("NewID() error = %v", err)
	}
	return value
}

func mustBybitCode(t *testing.T, value string) domain.Code {
	t.Helper()
	parsed, err := domain.ParseCode(value)
	if err != nil {
		t.Fatalf("ParseCode(%q) error = %v", value, err)
	}
	return parsed
}

func mustBybitInstant(t *testing.T, value time.Time) domain.UTCInstant {
	t.Helper()
	parsed, err := domain.NewUTCInstant(value)
	if err != nil {
		t.Fatalf("NewUTCInstant() error = %v", err)
	}
	return parsed
}

func lowerASCII(value string) string {
	buffer := []byte(value)
	for index := range buffer {
		if buffer[index] >= 'A' && buffer[index] <= 'Z' {
			buffer[index] += 'a' - 'A'
		}
	}
	return string(buffer)
}
