package providers

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"xr-trading/market-info-service/internal/domain"
	"xr-trading/market-info-service/internal/ingestion/ports"
)

func TestNewRegistryAndLookup(t *testing.T) {
	t.Parallel()

	bybit := newRegistryAdapter(t, "bybit")
	longbridge := newRegistryAdapter(t, "longbridge")
	registry, err := NewRegistry(context.Background(), longbridge, bybit)
	if err != nil {
		t.Fatalf("NewRegistry() error = %v", err)
	}

	adapter, ok := registry.Get(bybit.code)
	if !ok || adapter != bybit {
		t.Fatalf("Get(bybit) = (%#v, %t)", adapter, ok)
	}
	if _, ok := registry.Get(mustRegistryCode(t, "missing")); ok {
		t.Fatal("Get(missing) unexpectedly succeeded")
	}
	listed := registry.List()
	if len(listed) != 2 || listed[0] != bybit || listed[1] != longbridge {
		t.Fatalf("List() = %#v, want provider-code order", listed)
	}
	listed[0] = nil
	if current, _ := registry.Get(bybit.code); current != bybit {
		t.Fatal("mutating List result changed registry")
	}
	if bybit.capabilityCalls.Load() != 1 || longbridge.capabilityCalls.Load() != 1 {
		t.Fatal("capabilities must be loaded exactly once during construction")
	}

	capabilities, ok := registry.Capabilities(bybit.code)
	if !ok || len(capabilities.Markets) != 1 {
		t.Fatalf("Capabilities(bybit) = (%#v, %t)", capabilities, ok)
	}
	capabilities.Markets[0].Intervals[0] = domain.BarInterval1Day
	isolated, _ := registry.Capabilities(bybit.code)
	if isolated.Markets[0].Intervals[0] != domain.BarInterval1Hour {
		t.Fatal("mutating capability result changed cached snapshot")
	}
	if _, ok := registry.Capabilities(mustRegistryCode(t, "missing")); ok {
		t.Fatal("Capabilities(missing) unexpectedly succeeded")
	}
}

func TestNewRegistryRejectsInvalidRegistration(t *testing.T) {
	t.Parallel()

	valid := newRegistryAdapter(t, "bybit")
	typedNil := (*registryAdapter)(nil)
	capabilityFailure := newRegistryAdapter(t, "failed")
	capabilityFailure.capabilityErr = errors.New("configuration unavailable")
	mismatch := newRegistryAdapter(t, "mismatch")
	mismatch.capabilities.ProviderCode = mustRegistryCode(t, "other")
	invalidCapability := newRegistryAdapter(t, "invalid")
	invalidCapability.capabilities.Markets[0].Intervals = []domain.BarInterval{"5m"}
	zeroCode := newRegistryAdapter(t, "zero")
	zeroCode.code = domain.Code{}

	tests := []struct {
		name     string
		ctx      context.Context
		adapters []ports.MarketDataAdapter
		want     error
	}{
		{name: "nil context", adapters: []ports.MarketDataAdapter{valid}, want: domain.ErrInvalidData},
		{name: "nil adapter", ctx: context.Background(), adapters: []ports.MarketDataAdapter{nil}, want: domain.ErrInvalidData},
		{name: "typed nil adapter", ctx: context.Background(), adapters: []ports.MarketDataAdapter{typedNil}, want: domain.ErrInvalidData},
		{name: "zero code", ctx: context.Background(), adapters: []ports.MarketDataAdapter{zeroCode}, want: domain.ErrInvalidData},
		{name: "duplicate", ctx: context.Background(), adapters: []ports.MarketDataAdapter{valid, valid}, want: domain.ErrConflict},
		{name: "capability error", ctx: context.Background(), adapters: []ports.MarketDataAdapter{capabilityFailure}, want: capabilityFailure.capabilityErr},
		{name: "provider mismatch", ctx: context.Background(), adapters: []ports.MarketDataAdapter{mismatch}, want: domain.ErrInvalidData},
		{name: "invalid capability", ctx: context.Background(), adapters: []ports.MarketDataAdapter{invalidCapability}, want: domain.ErrInvalidData},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := NewRegistry(test.ctx, test.adapters...)
			if !errors.Is(err, test.want) {
				t.Fatalf("NewRegistry() error = %v, want %v", err, test.want)
			}
		})
	}
}

func TestRegistryValidatesLatestQuoteCapability(t *testing.T) {
	t.Parallel()

	adapter := newRegistryAdapter(t, "bybit")
	registry := mustRegistry(t, adapter)
	reference := registryReference(t, adapter.code)
	if err := registry.ValidateLatestQuoteRequest(adapter.code, []ports.ProviderInstrumentRef{reference}); err != nil {
		t.Fatalf("ValidateLatestQuoteRequest() error = %v", err)
	}

	otherProvider := reference
	otherProvider.ProviderCode = mustRegistryCode(t, "other")
	unsupportedType := reference
	unsupportedType.AssetType = domain.AssetTypeStock
	adapter.capabilities.Markets[0].MaxBatchSize = 1 // cached snapshot must remain unchanged

	tests := []struct {
		name       string
		code       domain.Code
		references []ports.ProviderInstrumentRef
		want       error
	}{
		{name: "adapter missing", code: mustRegistryCode(t, "missing"), references: []ports.ProviderInstrumentRef{reference}, want: ports.ErrAdapterNotRegistered},
		{name: "empty", code: adapter.code, want: domain.ErrInvalidData},
		{name: "provider mismatch", code: adapter.code, references: []ports.ProviderInstrumentRef{otherProvider}, want: domain.ErrInvalidData},
		{name: "duplicate", code: adapter.code, references: []ports.ProviderInstrumentRef{reference, reference}, want: domain.ErrInvalidData},
		{name: "unsupported type", code: adapter.code, references: []ports.ProviderInstrumentRef{unsupportedType}, want: ports.ErrCapabilityUnsupported},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if err := registry.ValidateLatestQuoteRequest(test.code, test.references); !errors.Is(err, test.want) {
				t.Fatalf("ValidateLatestQuoteRequest() error = %v, want %v", err, test.want)
			}
		})
	}

	second := registryReference(t, adapter.code)
	adapter.capabilities.Markets[0].MaxBatchSize = 2
	limitedAdapter := newRegistryAdapter(t, "limited")
	limitedAdapter.capabilities.Markets[0].MaxBatchSize = 1
	limitedRegistry := mustRegistry(t, limitedAdapter)
	reference.ProviderCode = limitedAdapter.code
	second.ProviderCode = limitedAdapter.code
	if err := limitedRegistry.ValidateLatestQuoteRequest(limitedAdapter.code, []ports.ProviderInstrumentRef{reference, second}); !errors.Is(err, ports.ErrProviderLimitExceeded) {
		t.Fatalf("batch limit error = %v", err)
	}
}

func TestRegistryValidatesBarsCapability(t *testing.T) {
	t.Parallel()

	adapter := newRegistryAdapter(t, "bybit")
	registry := mustRegistry(t, adapter)
	request := registryBarsRequest(t, registryReference(t, adapter.code))
	if err := registry.ValidateBarsRequest(request); err != nil {
		t.Fatalf("ValidateBarsRequest() error = %v", err)
	}

	tests := []struct {
		name   string
		mutate func(*ports.FetchBarsRequest)
		want   error
	}{
		{name: "unsupported interval", mutate: func(value *ports.FetchBarsRequest) { value.Interval = domain.BarInterval1Day }, want: ports.ErrCapabilityUnsupported},
		{name: "unsupported market", mutate: func(value *ports.FetchBarsRequest) { value.Instrument.ProviderMarket = "linear" }, want: ports.ErrCapabilityUnsupported},
		{name: "limit", mutate: func(value *ports.FetchBarsRequest) { value.Limit = 101 }, want: ports.ErrProviderLimitExceeded},
		{name: "invalid request", mutate: func(value *ports.FetchBarsRequest) { value.Limit = 0 }, want: domain.ErrInvalidData},
		{name: "missing adapter", mutate: func(value *ports.FetchBarsRequest) { value.Instrument.ProviderCode = mustRegistryCode(t, "missing") }, want: ports.ErrAdapterNotRegistered},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			changed := request
			test.mutate(&changed)
			if err := registry.ValidateBarsRequest(changed); !errors.Is(err, test.want) {
				t.Fatalf("ValidateBarsRequest() error = %v, want %v", err, test.want)
			}
		})
	}
}

func TestRegistryConcurrentReads(t *testing.T) {
	t.Parallel()

	adapter := newRegistryAdapter(t, "bybit")
	registry := mustRegistry(t, adapter)
	request := registryBarsRequest(t, registryReference(t, adapter.code))

	var wait sync.WaitGroup
	for range 32 {
		wait.Add(1)
		go func() {
			defer wait.Done()
			for range 100 {
				if current, ok := registry.Get(adapter.code); !ok || current != adapter {
					t.Errorf("Get() = (%#v, %t)", current, ok)
					return
				}
				if len(registry.List()) != 1 {
					t.Error("List() length changed")
					return
				}
				if capabilities, ok := registry.Capabilities(adapter.code); !ok || len(capabilities.Markets) != 1 {
					t.Error("Capabilities() failed")
					return
				}
				if err := registry.ValidateBarsRequest(request); err != nil {
					t.Errorf("ValidateBarsRequest() error = %v", err)
					return
				}
			}
		}()
	}
	wait.Wait()
	if adapter.capabilityCalls.Load() != 1 {
		t.Fatalf("Capabilities() calls = %d, want 1", adapter.capabilityCalls.Load())
	}
}

func TestNilRegistry(t *testing.T) {
	t.Parallel()

	var registry *Registry
	if _, ok := registry.Get(mustRegistryCode(t, "bybit")); ok || registry.List() != nil {
		t.Fatal("nil registry lookup unexpectedly succeeded")
	}
	if _, ok := registry.Capabilities(mustRegistryCode(t, "bybit")); ok {
		t.Fatal("nil registry capability lookup unexpectedly succeeded")
	}
	if err := registry.ValidateLatestQuoteRequest(mustRegistryCode(t, "bybit"), []ports.ProviderInstrumentRef{{}}); !errors.Is(err, ports.ErrAdapterNotRegistered) {
		t.Fatalf("nil registry validation error = %v", err)
	}
}

type registryAdapter struct {
	code            domain.Code
	capabilities    ports.ProviderCapabilities
	capabilityErr   error
	capabilityCalls atomic.Int32
}

func newRegistryAdapter(t *testing.T, code string) *registryAdapter {
	t.Helper()
	parsed := mustRegistryCode(t, code)
	return &registryAdapter{
		code: parsed,
		capabilities: ports.ProviderCapabilities{ProviderCode: parsed, Markets: []ports.ProviderMarketCapability{{
			ProviderMarket: "spot", AssetTypes: []domain.AssetType{domain.AssetTypeCrypto},
			InstrumentTypes: []domain.InstrumentType{domain.InstrumentTypeSpot}, SupportsQuote: true,
			SupportsBars: true, Intervals: []domain.BarInterval{domain.BarInterval1Hour},
			MaxBatchSize: 2, MaxBarsPerRequest: 100,
		}}},
	}
}

func (adapter *registryAdapter) ProviderCode() domain.Code { return adapter.code }
func (adapter *registryAdapter) Capabilities(context.Context) (ports.ProviderCapabilities, error) {
	adapter.capabilityCalls.Add(1)
	return adapter.capabilities, adapter.capabilityErr
}
func (*registryAdapter) FetchLatestQuotes(context.Context, []ports.ProviderInstrumentRef) ([]ports.ProviderQuote, error) {
	return nil, nil
}
func (*registryAdapter) FetchBars(context.Context, ports.FetchBarsRequest) (ports.FetchBarsResult, error) {
	return ports.FetchBarsResult{}, nil
}

func mustRegistry(t *testing.T, adapters ...ports.MarketDataAdapter) *Registry {
	t.Helper()
	registry, err := NewRegistry(context.Background(), adapters...)
	if err != nil {
		t.Fatalf("NewRegistry() error = %v", err)
	}
	return registry
}

func registryReference(t *testing.T, providerCode domain.Code) ports.ProviderInstrumentRef {
	t.Helper()
	return ports.ProviderInstrumentRef{
		ProviderInstrumentID: mustRegistryID(t), ProviderInstrumentCode: mustRegistryCode(t, "provider.bybit.spot.btcusdt"),
		InstrumentID: mustRegistryID(t), AssetID: mustRegistryID(t), ProviderCode: providerCode,
		ProviderMarket: "spot", AssetType: domain.AssetTypeCrypto, InstrumentType: domain.InstrumentTypeSpot,
		ExternalSymbol: "BTCUSDT", InstrumentCode: mustRegistryCode(t, "instrument.crypto.spot.btc-usdt"),
		InstrumentSymbol: "BTC/USDT", QuoteCurrency: "USDT",
	}
}

func registryBarsRequest(t *testing.T, reference ports.ProviderInstrumentRef) ports.FetchBarsRequest {
	t.Helper()
	start, err := domain.NewUTCInstant(time.Date(2026, 7, 15, 0, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatalf("NewUTCInstant() error = %v", err)
	}
	end, err := domain.NewUTCInstant(start.Time().Add(time.Hour))
	if err != nil {
		t.Fatalf("NewUTCInstant() error = %v", err)
	}
	return ports.FetchBarsRequest{Instrument: reference, Interval: domain.BarInterval1Hour, StartTime: start, EndTime: end, Limit: 100}
}

func mustRegistryID(t *testing.T) domain.ID {
	t.Helper()
	value, err := domain.NewID()
	if err != nil {
		t.Fatalf("NewID() error = %v", err)
	}
	return value
}

func mustRegistryCode(t *testing.T, value string) domain.Code {
	t.Helper()
	parsed, err := domain.ParseCode(value)
	if err != nil {
		t.Fatalf("ParseCode(%q) error = %v", value, err)
	}
	return parsed
}
