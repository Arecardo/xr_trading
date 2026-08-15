package domain

import (
	"encoding/json"
	"fmt"
)

// BarInterval is a supported stored K-line interval.
type BarInterval string

const (
	BarInterval1Hour BarInterval = "1h"
	BarInterval1Day  BarInterval = "1d"
)

// AssetType classifies an economic asset.
type AssetType string

const (
	AssetTypeStock  AssetType = "STOCK"
	AssetTypeETF    AssetType = "ETF"
	AssetTypeCrypto AssetType = "CRYPTO"
	AssetTypeCash   AssetType = "CASH"
	// AssetTypeFX represents an FX reference rate asset (2026-08-05, RM0
	// DEC-006). It is never a tradable/holdable PortfolioMember candidate; it
	// exists only so a non-base-currency holding's quote currency can be
	// converted through market-info-service's normal catalog/collection
	// machinery. See doc/technical/03_universe_data.md §7 for the collection
	// scope exception this asset type carries.
	AssetTypeFX AssetType = "FX"
)

// InstrumentType classifies a venue-specific instrument.
type InstrumentType string

const (
	InstrumentTypeEquity InstrumentType = "EQUITY"
	InstrumentTypeETF    InstrumentType = "ETF"
	InstrumentTypeSpot   InstrumentType = "SPOT"
	// InstrumentTypeFX mirrors AssetTypeFX at the Instrument level: a
	// non-tradable FX reference rate quoted by a specific provider (currently
	// only CoinGecko). See AssetTypeFX.
	InstrumentTypeFX InstrumentType = "FX"
)

// ParseBarInterval validates a stored K-line interval.
func ParseBarInterval(value string) (BarInterval, error) {
	parsed := BarInterval(value)
	if parsed != BarInterval1Hour && parsed != BarInterval1Day {
		return "", fmt.Errorf("parse bar interval: %w", ErrInvalidData)
	}
	return parsed, nil
}

// ParseAssetType validates an AssetType.
func ParseAssetType(value string) (AssetType, error) {
	parsed := AssetType(value)
	switch parsed {
	case AssetTypeStock, AssetTypeETF, AssetTypeCrypto, AssetTypeCash, AssetTypeFX:
		return parsed, nil
	default:
		return "", fmt.Errorf("parse asset type: %w", ErrInvalidData)
	}
}

// ParseInstrumentType validates an InstrumentType.
func ParseInstrumentType(value string) (InstrumentType, error) {
	parsed := InstrumentType(value)
	switch parsed {
	case InstrumentTypeEquity, InstrumentTypeETF, InstrumentTypeSpot, InstrumentTypeFX:
		return parsed, nil
	default:
		return "", fmt.Errorf("parse instrument type: %w", ErrInvalidData)
	}
}

func marshalStringEnum(value string, kind string) ([]byte, error) {
	if value == "" {
		return nil, fmt.Errorf("marshal %s: zero value", kind)
	}
	return json.Marshal(value)
}

func (value BarInterval) MarshalJSON() ([]byte, error) {
	return marshalStringEnum(string(value), "bar interval")
}
func (value *BarInterval) UnmarshalJSON(data []byte) error {
	var text string
	if err := json.Unmarshal(data, &text); err != nil {
		return fmt.Errorf("unmarshal bar interval JSON: %w", err)
	}
	parsed, err := ParseBarInterval(text)
	if err != nil {
		return err
	}
	*value = parsed
	return nil
}
func (value AssetType) MarshalJSON() ([]byte, error) {
	return marshalStringEnum(string(value), "asset type")
}
func (value *AssetType) UnmarshalJSON(data []byte) error {
	var text string
	if err := json.Unmarshal(data, &text); err != nil {
		return fmt.Errorf("unmarshal asset type JSON: %w", err)
	}
	parsed, err := ParseAssetType(text)
	if err != nil {
		return err
	}
	*value = parsed
	return nil
}
func (value InstrumentType) MarshalJSON() ([]byte, error) {
	return marshalStringEnum(string(value), "instrument type")
}
func (value *InstrumentType) UnmarshalJSON(data []byte) error {
	var text string
	if err := json.Unmarshal(data, &text); err != nil {
		return fmt.Errorf("unmarshal instrument type JSON: %w", err)
	}
	parsed, err := ParseInstrumentType(text)
	if err != nil {
		return err
	}
	*value = parsed
	return nil
}
