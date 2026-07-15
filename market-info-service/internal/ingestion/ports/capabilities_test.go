package ports

import (
	"errors"
	"testing"

	"xr-trading/market-info-service/internal/domain"
)

func TestProviderCapabilitiesValidate(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		mutate func(*ProviderCapabilities)
	}{
		{name: "valid"},
		{name: "provider code", mutate: func(value *ProviderCapabilities) { value.ProviderCode = domain.Code{} }},
		{name: "markets", mutate: func(value *ProviderCapabilities) { value.Markets = nil }},
		{name: "duplicate market", mutate: func(value *ProviderCapabilities) { value.Markets = append(value.Markets, value.Markets[0]) }},
		{name: "invalid market", mutate: func(value *ProviderCapabilities) { value.Markets[0].ProviderMarket = "SPOT" }},
		{name: "asset types", mutate: func(value *ProviderCapabilities) { value.Markets[0].AssetTypes = nil }},
		{name: "duplicate asset type", mutate: func(value *ProviderCapabilities) {
			value.Markets[0].AssetTypes = []domain.AssetType{domain.AssetTypeCrypto, domain.AssetTypeCrypto}
		}},
		{name: "invalid asset type", mutate: func(value *ProviderCapabilities) { value.Markets[0].AssetTypes = []domain.AssetType{"OPTION"} }},
		{name: "instrument types", mutate: func(value *ProviderCapabilities) { value.Markets[0].InstrumentTypes = nil }},
		{name: "duplicate instrument type", mutate: func(value *ProviderCapabilities) {
			value.Markets[0].InstrumentTypes = []domain.InstrumentType{domain.InstrumentTypeSpot, domain.InstrumentTypeSpot}
		}},
		{name: "invalid instrument type", mutate: func(value *ProviderCapabilities) {
			value.Markets[0].InstrumentTypes = []domain.InstrumentType{"PERPETUAL"}
		}},
		{name: "no operations", mutate: func(value *ProviderCapabilities) {
			value.Markets[0].SupportsQuote = false
			value.Markets[0].SupportsBars = false
			value.Markets[0].MaxBatchSize = 0
			value.Markets[0].MaxBarsPerRequest = 0
			value.Markets[0].Intervals = nil
		}},
		{name: "quote limit", mutate: func(value *ProviderCapabilities) { value.Markets[0].MaxBatchSize = 0 }},
		{name: "limit without quote", mutate: func(value *ProviderCapabilities) { value.Markets[0].SupportsQuote = false }},
		{name: "bar limit", mutate: func(value *ProviderCapabilities) { value.Markets[0].MaxBarsPerRequest = 0 }},
		{name: "bar intervals", mutate: func(value *ProviderCapabilities) { value.Markets[0].Intervals = nil }},
		{name: "bars disabled with settings", mutate: func(value *ProviderCapabilities) { value.Markets[0].SupportsBars = false }},
		{name: "invalid interval", mutate: func(value *ProviderCapabilities) { value.Markets[0].Intervals = []domain.BarInterval{"5m"} }},
		{name: "duplicate interval", mutate: func(value *ProviderCapabilities) {
			value.Markets[0].Intervals = []domain.BarInterval{domain.BarInterval1Hour, domain.BarInterval1Hour}
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			value := validProviderCapabilities(t)
			if test.mutate != nil {
				test.mutate(&value)
			}
			err := value.Validate()
			if test.mutate == nil && err != nil {
				t.Fatalf("Validate() error = %v", err)
			}
			if test.mutate != nil && !errors.Is(err, domain.ErrInvalidData) {
				t.Fatalf("Validate() error = %v, want invalid data", err)
			}
		})
	}
}

func validProviderCapabilities(t *testing.T) ProviderCapabilities {
	t.Helper()
	return ProviderCapabilities{
		ProviderCode: mustAdapterCode(t, "bybit"),
		Markets: []ProviderMarketCapability{{
			ProviderMarket: "spot", AssetTypes: []domain.AssetType{domain.AssetTypeCrypto},
			InstrumentTypes: []domain.InstrumentType{domain.InstrumentTypeSpot},
			SupportsQuote:   true, SupportsBars: true,
			Intervals:    []domain.BarInterval{domain.BarInterval1Hour, domain.BarInterval1Day},
			MaxBatchSize: 50, MaxBarsPerRequest: 1000,
		}},
	}
}
