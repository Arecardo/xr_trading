package bybit

import (
	"context"
	"errors"
	"testing"
	"time"

	"xr-trading/market-info-service/internal/domain"
	"xr-trading/market-info-service/internal/ingestion/ports"
)

func TestNewAdapterDefaultsAndCapabilities(t *testing.T) {
	t.Parallel()

	adapter, err := New(Config{})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	if adapter.ProviderCode().String() != "bybit" || adapter.baseURL.String() != DefaultBaseURL {
		t.Fatalf("adapter identity = (%s, %s)", adapter.ProviderCode().String(), adapter.baseURL)
	}
	capabilities, err := adapter.Capabilities(context.Background())
	if err != nil || capabilities.Validate() != nil || capabilities.ProviderCode != adapter.ProviderCode() {
		t.Fatalf("Capabilities() = (%#v, %v)", capabilities, err)
	}
	market := capabilities.Markets[0]
	if market.ProviderMarket != spotMarket || market.MaxBatchSize != maxQuoteBatchSize || market.MaxBarsPerRequest != maxBarsPerRequest || len(market.Intervals) != 2 {
		t.Fatalf("Spot capabilities = %#v", market)
	}
	capabilities.Markets[0].Intervals[0] = domain.BarInterval1Day
	isolated, _ := adapter.Capabilities(context.Background())
	if isolated.Markets[0].Intervals[0] != domain.BarInterval1Hour {
		t.Fatal("Capabilities() did not return an isolated declaration")
	}
}

func TestNewAdapterRejectsInvalidConfig(t *testing.T) {
	t.Parallel()

	tests := []Config{
		{BaseURL: "://bad"},
		{BaseURL: "ftp://api.bybit.com"},
		{BaseURL: "https://user@example.com"},
		{BaseURL: "https://api.bybit.com?q=1"},
		{BaseURL: "https://api.bybit.com/#fragment"},
		{MaxResponseBytes: -1},
		{UserAgent: "bad\r\nheader"},
	}
	for _, config := range tests {
		if _, err := New(config); !errors.Is(err, domain.ErrInvalidData) {
			t.Fatalf("New(%#v) error = %v", config, err)
		}
	}
}

func TestAdapterCapabilityContextAndNilReceiver(t *testing.T) {
	t.Parallel()

	adapter, _ := New(Config{})
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := adapter.Capabilities(canceled); !errors.Is(err, context.Canceled) {
		t.Fatalf("Capabilities(canceled) error = %v", err)
	}
	if _, err := adapter.Capabilities(nil); !errors.Is(err, domain.ErrInvalidData) {
		t.Fatalf("Capabilities(nil) error = %v", err)
	}
	var nilAdapter *Adapter
	if !nilAdapter.ProviderCode().IsZero() {
		t.Fatal("nil adapter returned provider code")
	}
	if _, err := nilAdapter.Capabilities(context.Background()); !errors.Is(err, domain.ErrInvalidState) {
		t.Fatalf("nil Capabilities() error = %v", err)
	}
	if _, err := nilAdapter.FetchLatestQuotes(context.Background(), nil); !errors.Is(err, domain.ErrInvalidState) {
		t.Fatalf("nil FetchLatestQuotes() error = %v", err)
	}
	if _, err := nilAdapter.FetchBars(context.Background(), ports.FetchBarsRequest{}); !errors.Is(err, domain.ErrInvalidState) {
		t.Fatalf("nil FetchBars() error = %v", err)
	}
}

func TestIntervalAndTimeMapping(t *testing.T) {
	t.Parallel()

	if value, duration, err := bybitInterval(domain.BarInterval1Hour); err != nil || value != "60" || duration != time.Hour {
		t.Fatalf("bybitInterval(1h) = (%q, %s, %v)", value, duration, err)
	}
	if value, duration, err := bybitInterval(domain.BarInterval1Day); err != nil || value != "D" || duration != 24*time.Hour {
		t.Fatalf("bybitInterval(1d) = (%q, %s, %v)", value, duration, err)
	}
	if _, _, err := bybitInterval("5m"); err == nil {
		t.Fatal("bybitInterval(5m) error = nil")
	}
	value := time.Date(2026, 7, 15, 0, 0, 0, 500_000, time.UTC)
	if got := ceilUnixMilliseconds(value); got != value.UnixMilli()+1 || exclusiveEndMilliseconds(value) != got {
		t.Fatalf("ceil milliseconds = %d", got)
	}
}

func TestProviderErrorHelpers(t *testing.T) {
	t.Parallel()

	code := mustBybitCode(t, "bybit")
	tests := []struct {
		err  error
		code ports.ProviderErrorCode
	}{
		{providerError(code, ports.ProviderErrorNetwork, "network failed", nil, errors.New("wire detail")), ports.ProviderErrorNetwork},
		{badRequestError(code, "bad request", nil), ports.ProviderErrorBadRequest},
		{invalidInstrumentError(code, "invalid instrument", nil), ports.ProviderErrorInvalidInstrument},
		{unsupportedIntervalError(code, nil), ports.ProviderErrorUnsupportedInterval},
		{invalidResponseError(code, "invalid response", nil), ports.ProviderErrorInvalidResponse},
	}
	for _, test := range tests {
		classified, ok := ports.AsProviderError(test.err)
		if !ok || classified.Code != test.code {
			t.Fatalf("error = %#v, want %s", test.err, test.code)
		}
	}
	if err := providerError(domain.Code{}, ports.ProviderErrorNetwork, "network failed", nil, nil); !errors.Is(err, domain.ErrInvalidData) {
		t.Fatalf("invalid provider error = %v", err)
	}
}
