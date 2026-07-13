package domain

import (
	"encoding/json"
	"fmt"
)

// AssetStatus describes whether an economic asset can be used by current
// market-data configuration.
type AssetStatus string

const (
	AssetStatusActive   AssetStatus = "active"
	AssetStatusInactive AssetStatus = "inactive"
	AssetStatusDelisted AssetStatus = "delisted"
)

// InstrumentStatus describes the lifecycle state of a venue instrument.
type InstrumentStatus string

const (
	InstrumentStatusActive    InstrumentStatus = "active"
	InstrumentStatusSuspended InstrumentStatus = "suspended"
	InstrumentStatusDelisted  InstrumentStatus = "delisted"
)

// ProviderType classifies a market-data provider without exposing credentials.
type ProviderType string

const (
	ProviderTypeBroker     ProviderType = "BROKER"
	ProviderTypeExchange   ProviderType = "EXCHANGE"
	ProviderTypeAggregator ProviderType = "AGGREGATOR"
)

// ProviderStatus describes provider availability for configuration and use.
type ProviderStatus string

const (
	ProviderStatusActive   ProviderStatus = "active"
	ProviderStatusDisabled ProviderStatus = "disabled"
	ProviderStatusDegraded ProviderStatus = "degraded"
)

func ParseAssetStatus(value string) (AssetStatus, error) {
	parsed := AssetStatus(value)
	switch parsed {
	case AssetStatusActive, AssetStatusInactive, AssetStatusDelisted:
		return parsed, nil
	default:
		return "", invalidData("unsupported asset status")
	}
}

func ParseInstrumentStatus(value string) (InstrumentStatus, error) {
	parsed := InstrumentStatus(value)
	switch parsed {
	case InstrumentStatusActive, InstrumentStatusSuspended, InstrumentStatusDelisted:
		return parsed, nil
	default:
		return "", invalidData("unsupported instrument status")
	}
}

func ParseProviderType(value string) (ProviderType, error) {
	parsed := ProviderType(value)
	switch parsed {
	case ProviderTypeBroker, ProviderTypeExchange, ProviderTypeAggregator:
		return parsed, nil
	default:
		return "", invalidData("unsupported provider type")
	}
}

func ParseProviderStatus(value string) (ProviderStatus, error) {
	parsed := ProviderStatus(value)
	switch parsed {
	case ProviderStatusActive, ProviderStatusDisabled, ProviderStatusDegraded:
		return parsed, nil
	default:
		return "", invalidData("unsupported provider status")
	}
}

func marshalCatalogEnum(value, kind string) ([]byte, error) {
	if value == "" {
		return nil, fmt.Errorf("marshal %s: zero value", kind)
	}
	return json.Marshal(value)
}

func (value AssetStatus) MarshalJSON() ([]byte, error) {
	return marshalCatalogEnum(string(value), "asset status")
}

func (value *AssetStatus) UnmarshalJSON(data []byte) error {
	return unmarshalCatalogEnum(data, func(text string) error {
		parsed, err := ParseAssetStatus(text)
		*value = parsed
		return err
	})
}

func (value InstrumentStatus) MarshalJSON() ([]byte, error) {
	return marshalCatalogEnum(string(value), "instrument status")
}

func (value *InstrumentStatus) UnmarshalJSON(data []byte) error {
	return unmarshalCatalogEnum(data, func(text string) error {
		parsed, err := ParseInstrumentStatus(text)
		*value = parsed
		return err
	})
}

func (value ProviderType) MarshalJSON() ([]byte, error) {
	return marshalCatalogEnum(string(value), "provider type")
}

func (value *ProviderType) UnmarshalJSON(data []byte) error {
	return unmarshalCatalogEnum(data, func(text string) error {
		parsed, err := ParseProviderType(text)
		*value = parsed
		return err
	})
}

func (value ProviderStatus) MarshalJSON() ([]byte, error) {
	return marshalCatalogEnum(string(value), "provider status")
}

func (value *ProviderStatus) UnmarshalJSON(data []byte) error {
	return unmarshalCatalogEnum(data, func(text string) error {
		parsed, err := ParseProviderStatus(text)
		*value = parsed
		return err
	})
}

func unmarshalCatalogEnum(data []byte, assign func(string) error) error {
	var text string
	if err := json.Unmarshal(data, &text); err != nil {
		return fmt.Errorf("unmarshal catalog enum JSON: %w", err)
	}
	return assign(text)
}
