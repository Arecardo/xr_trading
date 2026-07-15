package longbridge

import (
	"context"
	"errors"
	"testing"
	"time"

	"xr-trading/market-info-service/internal/domain"
	"xr-trading/market-info-service/internal/ingestion/ports"
)

func TestNewAdapterAndCapabilities(t *testing.T) {
	t.Parallel()
	client := &fakeClient{}
	adapter := newTestAdapter(t, client)
	if adapter.ProviderCode().String() != providerName || adapter.marketLocation.String() != "America/New_York" {
		t.Fatalf("adapter identity = (%s, %s)", adapter.ProviderCode().String(), adapter.marketLocation)
	}
	capabilities, err := adapter.Capabilities(context.Background())
	if err != nil || capabilities.Validate() != nil || len(capabilities.Markets) != 1 {
		t.Fatalf("Capabilities() = (%#v, %v)", capabilities, err)
	}
	market := capabilities.Markets[0]
	if market.ProviderMarket != usMarket || market.MaxBatchSize != 500 || market.MaxBarsPerRequest != 1000 ||
		len(market.AssetTypes) != 2 || len(market.InstrumentTypes) != 2 || len(market.Intervals) != 2 {
		t.Fatalf("US capabilities = %#v", market)
	}
	capabilities.Markets[0].Intervals[0] = domain.BarInterval1Day
	isolated, _ := adapter.Capabilities(context.Background())
	if isolated.Markets[0].Intervals[0] != domain.BarInterval1Hour {
		t.Fatal("Capabilities() did not return an isolated declaration")
	}
	if err := adapter.Close(); err != nil || client.closed != 1 {
		t.Fatalf("Close() = (%v, %d)", err, client.closed)
	}
}

func TestNewAdapterRejectsMissingClients(t *testing.T) {
	t.Parallel()
	if _, err := New(Config{}); !errors.Is(err, domain.ErrInvalidData) {
		t.Fatalf("New(nil client) error = %v", err)
	}
	var typedNil *fakeClient
	if _, err := New(Config{Client: typedNil}); !errors.Is(err, domain.ErrInvalidData) {
		t.Fatalf("New(typed nil client) error = %v", err)
	}
	if _, err := NewFromSDKConfig(nil, Config{}); !errors.Is(err, domain.ErrInvalidData) {
		t.Fatalf("NewFromSDKConfig(nil) error = %v", err)
	}
	custom := time.FixedZone("market", -5*60*60)
	adapter, err := New(Config{Client: &fakeClient{}, MarketLocation: custom})
	if err != nil || adapter.marketLocation != custom {
		t.Fatalf("New(custom location) = (%#v, %v)", adapter, err)
	}
}

func TestAdapterNilReceiverAndContexts(t *testing.T) {
	t.Parallel()
	adapter := newTestAdapter(t, &fakeClient{})
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := adapter.Capabilities(canceled); !errors.Is(err, context.Canceled) {
		t.Fatalf("Capabilities(canceled) error = %v", err)
	}
	if _, err := adapter.Capabilities(nil); !errors.Is(err, domain.ErrInvalidData) {
		t.Fatalf("Capabilities(nil) error = %v", err)
	}
	if _, err := adapter.FetchLatestQuotes(nil, []ports.ProviderInstrumentRef{}); codeOf(err) != ports.ProviderErrorBadRequest {
		t.Fatalf("FetchLatestQuotes(nil) error = %v", err)
	}
	if _, err := adapter.FetchBars(nil, ports.FetchBarsRequest{}); codeOf(err) != ports.ProviderErrorBadRequest {
		t.Fatalf("FetchBars(nil) error = %v", err)
	}
	var nilAdapter *Adapter
	if !nilAdapter.ProviderCode().IsZero() || nilAdapter.Close() != nil {
		t.Fatal("nil adapter identity or close is invalid")
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

func TestLongbridgePeriodAndSessionClose(t *testing.T) {
	t.Parallel()
	adapter := newTestAdapter(t, &fakeClient{})
	if period, err := longbridgePeriod(domain.BarInterval1Hour); err != nil || int32(period) != 60 {
		t.Fatalf("longbridgePeriod(1h) = (%d, %v)", period, err)
	}
	if period, err := longbridgePeriod(domain.BarInterval1Day); err != nil || int32(period) != 1000 {
		t.Fatalf("longbridgePeriod(1d) = (%d, %v)", period, err)
	}
	if _, err := longbridgePeriod("5m"); err == nil {
		t.Fatal("longbridgePeriod(5m) error = nil")
	}
	hourlyOpen := time.Date(2026, 7, 15, 19, 30, 0, 0, time.UTC)
	hourlyClose, err := adapter.regularSessionClose(hourlyOpen, domain.BarInterval1Hour)
	if err != nil || hourlyClose.Time() != time.Date(2026, 7, 15, 20, 0, 0, 0, time.UTC) {
		t.Fatalf("hourly close = (%s, %v)", hourlyClose, err)
	}
	dailyClose, err := adapter.regularSessionClose(time.Date(2026, 12, 1, 0, 0, 0, 0, time.UTC), domain.BarInterval1Day)
	if err != nil || dailyClose.Time() != time.Date(2026, 12, 1, 21, 0, 0, 0, time.UTC) {
		t.Fatalf("daily close = (%s, %v)", dailyClose, err)
	}
	if _, err := adapter.regularSessionClose(time.Date(2026, 7, 15, 21, 0, 0, 0, time.UTC), domain.BarInterval1Hour); err == nil {
		t.Fatal("after-session close error = nil")
	}
}

func codeOf(err error) ports.ProviderErrorCode {
	classified, ok := ports.AsProviderError(err)
	if !ok {
		return ""
	}
	return classified.Code
}
