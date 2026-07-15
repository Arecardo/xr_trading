// Package providers contains provider adapter infrastructure shared by
// concrete implementations such as Bybit and Longbridge.
package providers

import (
	"context"
	"fmt"
	"reflect"
	"sort"

	"xr-trading/market-info-service/internal/domain"
	"xr-trading/market-info-service/internal/ingestion/ports"
)

// Registry is an immutable adapter registry. Construction performs the only
// capability calls; normal lookups and validations use the cached snapshot.
type Registry struct {
	entries map[domain.Code]registryEntry
	ordered []ports.MarketDataAdapter
}

type registryEntry struct {
	adapter      ports.MarketDataAdapter
	capabilities ports.ProviderCapabilities
}

// NewRegistry validates adapters and their capability declarations, then
// returns a registry safe for concurrent reads.
func NewRegistry(ctx context.Context, adapters ...ports.MarketDataAdapter) (*Registry, error) {
	if ctx == nil {
		return nil, fmt.Errorf("construct adapter registry: %w", domain.ErrInvalidData)
	}

	registry := &Registry{
		entries: make(map[domain.Code]registryEntry, len(adapters)),
		ordered: make([]ports.MarketDataAdapter, 0, len(adapters)),
	}
	for index, adapter := range adapters {
		if isNilAdapter(adapter) {
			return nil, fmt.Errorf("register adapter %d: %w", index, domain.ErrInvalidData)
		}
		providerCode := adapter.ProviderCode()
		if providerCode.IsZero() {
			return nil, fmt.Errorf("register adapter %d: provider code is required: %w", index, domain.ErrInvalidData)
		}
		if _, exists := registry.entries[providerCode]; exists {
			return nil, fmt.Errorf("register adapter %s: %w", providerCode.String(), domain.ErrConflict)
		}

		capabilities, err := adapter.Capabilities(ctx)
		if err != nil {
			return nil, fmt.Errorf("load adapter %s capabilities: %w", providerCode.String(), err)
		}
		if capabilities.ProviderCode != providerCode {
			return nil, fmt.Errorf("register adapter %s: capability provider code mismatch: %w", providerCode.String(), domain.ErrInvalidData)
		}
		if err := capabilities.Validate(); err != nil {
			return nil, fmt.Errorf("register adapter %s: %w", providerCode.String(), err)
		}

		registry.entries[providerCode] = registryEntry{adapter: adapter, capabilities: cloneCapabilities(capabilities)}
		registry.ordered = append(registry.ordered, adapter)
	}
	sort.Slice(registry.ordered, func(left, right int) bool {
		return registry.ordered[left].ProviderCode().String() < registry.ordered[right].ProviderCode().String()
	})
	return registry, nil
}

// Get returns the adapter registered for providerCode.
func (registry *Registry) Get(providerCode domain.Code) (ports.MarketDataAdapter, bool) {
	if registry == nil {
		return nil, false
	}
	entry, exists := registry.entries[providerCode]
	return entry.adapter, exists
}

// List returns registered adapters in provider-code order. The returned slice
// is a copy and cannot mutate registry state.
func (registry *Registry) List() []ports.MarketDataAdapter {
	if registry == nil {
		return nil
	}
	return append([]ports.MarketDataAdapter(nil), registry.ordered...)
}

// Capabilities returns an isolated copy of the startup capability snapshot.
func (registry *Registry) Capabilities(providerCode domain.Code) (ports.ProviderCapabilities, bool) {
	if registry == nil {
		return ports.ProviderCapabilities{}, false
	}
	entry, exists := registry.entries[providerCode]
	if !exists {
		return ports.ProviderCapabilities{}, false
	}
	return cloneCapabilities(entry.capabilities), true
}

// ValidateLatestQuoteRequest checks source identity, provider support and the
// adapter-wide batch limit without calling the provider.
func (registry *Registry) ValidateLatestQuoteRequest(providerCode domain.Code, references []ports.ProviderInstrumentRef) error {
	entry, err := registry.entry(providerCode)
	if err != nil {
		return err
	}
	if len(references) == 0 {
		return fmt.Errorf("validate latest quote request: references are required: %w", domain.ErrInvalidData)
	}

	counts := make(map[string]int)
	seen := make(map[domain.ID]struct{}, len(references))
	for _, reference := range references {
		if err := reference.Validate(); err != nil {
			return fmt.Errorf("validate latest quote request: %w", err)
		}
		if reference.ProviderCode != providerCode {
			return fmt.Errorf("validate latest quote request: provider mismatch: %w", domain.ErrInvalidData)
		}
		if _, exists := seen[reference.ProviderInstrumentID]; exists {
			return fmt.Errorf("validate latest quote request: duplicate source: %w", domain.ErrInvalidData)
		}
		seen[reference.ProviderInstrumentID] = struct{}{}

		capability, ok := matchingCapability(entry.capabilities, reference)
		if !ok || !capability.SupportsQuote {
			return unsupported(providerCode, "latest quote")
		}
		counts[reference.ProviderMarket]++
		if counts[reference.ProviderMarket] > capability.MaxBatchSize {
			return fmt.Errorf("validate latest quote request for provider %s: %w", providerCode.String(), ports.ErrProviderLimitExceeded)
		}
	}
	return nil
}

// ValidateBarsRequest checks the normalized request and cached provider-market
// capability, including interval and page-size limits.
func (registry *Registry) ValidateBarsRequest(request ports.FetchBarsRequest) error {
	if err := request.Validate(); err != nil {
		return fmt.Errorf("validate bars request: %w", err)
	}
	providerCode := request.Instrument.ProviderCode
	entry, err := registry.entry(providerCode)
	if err != nil {
		return err
	}
	capability, ok := matchingCapability(entry.capabilities, request.Instrument)
	if !ok || !capability.SupportsBars || !containsInterval(capability.Intervals, request.Interval) {
		return unsupported(providerCode, "bars")
	}
	if request.Limit > capability.MaxBarsPerRequest {
		return fmt.Errorf("validate bars request for provider %s: %w", providerCode.String(), ports.ErrProviderLimitExceeded)
	}
	return nil
}

func (registry *Registry) entry(providerCode domain.Code) (registryEntry, error) {
	if registry == nil || providerCode.IsZero() {
		return registryEntry{}, fmt.Errorf("lookup adapter: %w", ports.ErrAdapterNotRegistered)
	}
	entry, exists := registry.entries[providerCode]
	if !exists {
		return registryEntry{}, fmt.Errorf("lookup adapter %s: %w", providerCode.String(), ports.ErrAdapterNotRegistered)
	}
	return entry, nil
}

func matchingCapability(capabilities ports.ProviderCapabilities, reference ports.ProviderInstrumentRef) (ports.ProviderMarketCapability, bool) {
	for _, capability := range capabilities.Markets {
		if capability.ProviderMarket == reference.ProviderMarket &&
			containsAssetType(capability.AssetTypes, reference.AssetType) &&
			containsInstrumentType(capability.InstrumentTypes, reference.InstrumentType) {
			return capability, true
		}
	}
	return ports.ProviderMarketCapability{}, false
}

func containsAssetType(values []domain.AssetType, expected domain.AssetType) bool {
	for _, value := range values {
		if value == expected {
			return true
		}
	}
	return false
}

func containsInstrumentType(values []domain.InstrumentType, expected domain.InstrumentType) bool {
	for _, value := range values {
		if value == expected {
			return true
		}
	}
	return false
}

func containsInterval(values []domain.BarInterval, expected domain.BarInterval) bool {
	for _, value := range values {
		if value == expected {
			return true
		}
	}
	return false
}

func unsupported(providerCode domain.Code, operation string) error {
	return fmt.Errorf("provider %s does not support %s request: %w", providerCode.String(), operation, ports.ErrCapabilityUnsupported)
}

func cloneCapabilities(capabilities ports.ProviderCapabilities) ports.ProviderCapabilities {
	cloned := capabilities
	cloned.Markets = append([]ports.ProviderMarketCapability(nil), capabilities.Markets...)
	for index := range cloned.Markets {
		cloned.Markets[index].AssetTypes = append([]domain.AssetType(nil), capabilities.Markets[index].AssetTypes...)
		cloned.Markets[index].InstrumentTypes = append([]domain.InstrumentType(nil), capabilities.Markets[index].InstrumentTypes...)
		cloned.Markets[index].Intervals = append([]domain.BarInterval(nil), capabilities.Markets[index].Intervals...)
	}
	return cloned
}

func isNilAdapter(adapter ports.MarketDataAdapter) bool {
	if adapter == nil {
		return true
	}
	value := reflect.ValueOf(adapter)
	switch value.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return value.IsNil()
	default:
		return false
	}
}

var _ ports.AdapterRegistry = (*Registry)(nil)
