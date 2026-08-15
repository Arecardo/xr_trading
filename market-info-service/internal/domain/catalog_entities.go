package domain

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"time"
	"unicode/utf8"
)

const (
	assetCodeMaximumLength              = 128
	instrumentCodeMaximumLength         = 160
	providerCodeMaximumLength           = 64
	providerInstrumentCodeMaximumLength = 192
)

// Asset is an economic asset owned by the core catalog.
type Asset struct {
	ID              ID
	Code            Code
	AssetType       AssetType
	CanonicalSymbol string
	Name            string
	Status          AssetStatus
	Metadata        json.RawMessage
	CreatedAt       time.Time
	UpdatedAt       time.Time
}

// NewAsset normalizes and validates an Asset loaded from a trusted catalog or
// assembled by an application use case.
func NewAsset(asset Asset) (Asset, error) {
	asset.Metadata = normalizedJSON(asset.Metadata)
	asset.CreatedAt = UTC(asset.CreatedAt)
	asset.UpdatedAt = UTC(asset.UpdatedAt)
	if err := asset.Validate(); err != nil {
		return Asset{}, err
	}
	return asset, nil
}

// Validate checks the invariants that belong to Asset itself.
func (asset Asset) Validate() error {
	if err := validateIdentity(asset.ID, asset.Code, "asset.", assetCodeMaximumLength, "asset"); err != nil {
		return err
	}
	if _, err := ParseAssetType(string(asset.AssetType)); err != nil {
		return fmt.Errorf("validate asset type: %w", err)
	}
	if _, err := ParseAssetStatus(string(asset.Status)); err != nil {
		return fmt.Errorf("validate asset status: %w", err)
	}
	if err := validateText(asset.CanonicalSymbol, 64, "asset canonical symbol"); err != nil {
		return err
	}
	if err := validateText(asset.Name, 255, "asset name"); err != nil {
		return err
	}
	return validateRecord(asset.Metadata, asset.CreatedAt, asset.UpdatedAt, "asset")
}

// Instrument is a venue-specific, tradeable representation of an Asset.
// AssetID is the base economic asset for a spot pair; QuoteAssetID is optional
// while QuoteCurrency always preserves the provider-independent quote symbol.
type Instrument struct {
	ID             ID
	Code           Code
	AssetID        ID
	Venue          string
	InstrumentType InstrumentType
	Symbol         string
	QuoteAssetID   *ID
	QuoteCurrency  string
	MarketTimezone string
	PriceScale     *int16
	QuantityScale  *int16
	LotSize        *Decimal
	MinQuantity    *Decimal
	Status         InstrumentStatus
	ValidFrom      *time.Time
	ValidTo        *time.Time
	Metadata       json.RawMessage
	CreatedAt      time.Time
	UpdatedAt      time.Time
}

// NewInstrument normalizes and validates an Instrument.
func NewInstrument(instrument Instrument) (Instrument, error) {
	instrument.Metadata = normalizedJSON(instrument.Metadata)
	instrument.ValidFrom = normalizedOptionalTime(instrument.ValidFrom)
	instrument.ValidTo = normalizedOptionalTime(instrument.ValidTo)
	instrument.CreatedAt = UTC(instrument.CreatedAt)
	instrument.UpdatedAt = UTC(instrument.UpdatedAt)
	if err := instrument.Validate(); err != nil {
		return Instrument{}, err
	}
	return instrument, nil
}

// Validate checks the invariants that belong to Instrument itself.
func (instrument Instrument) Validate() error {
	if err := validateIdentity(instrument.ID, instrument.Code, "instrument.", instrumentCodeMaximumLength, "instrument"); err != nil {
		return err
	}
	if instrument.AssetID.IsZero() {
		return invalidData("instrument asset ID is required")
	}
	if _, err := ParseInstrumentType(string(instrument.InstrumentType)); err != nil {
		return fmt.Errorf("validate instrument type: %w", err)
	}
	if _, err := ParseInstrumentStatus(string(instrument.Status)); err != nil {
		return fmt.Errorf("validate instrument status: %w", err)
	}
	if err := validateText(instrument.Venue, 32, "instrument venue"); err != nil {
		return err
	}
	if err := validateText(instrument.Symbol, 64, "instrument symbol"); err != nil {
		return err
	}
	if err := validateText(instrument.QuoteCurrency, 16, "instrument quote currency"); err != nil {
		return err
	}
	if err := validateText(instrument.MarketTimezone, 64, "instrument market timezone"); err != nil {
		return err
	}
	if _, err := time.LoadLocation(instrument.MarketTimezone); err != nil {
		return invalidData("instrument market timezone is not an IANA timezone")
	}
	if instrument.QuoteAssetID != nil {
		if instrument.QuoteAssetID.IsZero() || *instrument.QuoteAssetID == instrument.AssetID {
			return invalidData("instrument quote asset must be non-zero and differ from its base asset")
		}
	}
	if err := validateScale(instrument.PriceScale, "instrument price scale"); err != nil {
		return err
	}
	if err := validateScale(instrument.QuantityScale, "instrument quantity scale"); err != nil {
		return err
	}
	if err := validatePositiveDecimal(instrument.LotSize, "instrument lot size"); err != nil {
		return err
	}
	if err := validatePositiveDecimal(instrument.MinQuantity, "instrument minimum quantity"); err != nil {
		return err
	}
	if err := validateValidRange(instrument.ValidFrom, instrument.ValidTo, "instrument"); err != nil {
		return err
	}
	return validateRecord(instrument.Metadata, instrument.CreatedAt, instrument.UpdatedAt, "instrument")
}

// Provider describes a market-data source. Credentials never belong here.
type Provider struct {
	ID           ID
	Code         Code
	Name         string
	ProviderType ProviderType
	Status       ProviderStatus
	Metadata     json.RawMessage
	CreatedAt    time.Time
	UpdatedAt    time.Time
}

// NewProvider normalizes and validates a Provider.
func NewProvider(provider Provider) (Provider, error) {
	provider.Metadata = normalizedJSON(provider.Metadata)
	provider.CreatedAt = UTC(provider.CreatedAt)
	provider.UpdatedAt = UTC(provider.UpdatedAt)
	if err := provider.Validate(); err != nil {
		return Provider{}, err
	}
	return provider, nil
}

// Validate checks the invariants that belong to Provider itself.
func (provider Provider) Validate() error {
	if err := validateIdentity(provider.ID, provider.Code, "", providerCodeMaximumLength, "provider"); err != nil {
		return err
	}
	if err := validateText(provider.Name, 128, "provider name"); err != nil {
		return err
	}
	if _, err := ParseProviderType(string(provider.ProviderType)); err != nil {
		return fmt.Errorf("validate provider type: %w", err)
	}
	if _, err := ParseProviderStatus(string(provider.Status)); err != nil {
		return fmt.Errorf("validate provider status: %w", err)
	}
	return validateRecord(provider.Metadata, provider.CreatedAt, provider.UpdatedAt, "provider")
}

// ProviderCapabilities describes which market-data operations a provider
// mapping supports. Intervals are explicit so unsupported intervals cannot be
// inferred from a provider-wide setting.
type ProviderCapabilities struct {
	Quote      bool          `json:"quote,omitempty"`
	Historical bool          `json:"historical,omitempty"`
	Intervals  []BarInterval `json:"intervals,omitempty"`
}

// ParseProviderCapabilities strictly parses the stable capabilities document.
func ParseProviderCapabilities(raw json.RawMessage) (ProviderCapabilities, error) {
	if len(raw) == 0 {
		return ProviderCapabilities{}, nil
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	var capabilities ProviderCapabilities
	if err := decoder.Decode(&capabilities); err != nil {
		return ProviderCapabilities{}, fmt.Errorf("parse provider capabilities: %w", ErrInvalidData)
	}
	if err := ensureJSONEnd(decoder); err != nil {
		return ProviderCapabilities{}, err
	}
	if err := capabilities.Validate(); err != nil {
		return ProviderCapabilities{}, err
	}
	return capabilities, nil
}

// Validate checks capability intervals and rejects duplicate declarations.
func (capabilities ProviderCapabilities) Validate() error {
	seen := make(map[BarInterval]struct{}, len(capabilities.Intervals))
	for _, interval := range capabilities.Intervals {
		parsed, err := ParseBarInterval(string(interval))
		if err != nil || parsed != interval {
			return invalidData("provider capabilities contain an unsupported interval")
		}
		if _, exists := seen[interval]; exists {
			return invalidData("provider capabilities contain a duplicate interval")
		}
		seen[interval] = struct{}{}
	}
	return nil
}

// ProviderInstrument maps an internal Instrument to a provider-specific code.
type ProviderInstrument struct {
	ID             ID
	Code           Code
	ProviderID     ID
	InstrumentID   ID
	ExternalSymbol string
	ProviderMarket string
	Capabilities   ProviderCapabilities
	Priority       int16
	IsDefault      bool
	Enabled        bool
	ValidFrom      *time.Time
	ValidTo        *time.Time
	Metadata       json.RawMessage
	CreatedAt      time.Time
	UpdatedAt      time.Time
}

// NewProviderInstrument normalizes and validates a provider mapping.
func NewProviderInstrument(mapping ProviderInstrument) (ProviderInstrument, error) {
	mapping.Metadata = normalizedJSON(mapping.Metadata)
	mapping.ValidFrom = normalizedOptionalTime(mapping.ValidFrom)
	mapping.ValidTo = normalizedOptionalTime(mapping.ValidTo)
	mapping.CreatedAt = UTC(mapping.CreatedAt)
	mapping.UpdatedAt = UTC(mapping.UpdatedAt)
	if err := mapping.Validate(); err != nil {
		return ProviderInstrument{}, err
	}
	return mapping, nil
}

// Validate checks provider mapping identity, capability and default-source
// invariants. Cross-row uniqueness remains enforced by PostgreSQL.
func (mapping ProviderInstrument) Validate() error {
	if err := validateIdentity(mapping.ID, mapping.Code, "provider.", providerInstrumentCodeMaximumLength, "provider instrument"); err != nil {
		return err
	}
	if mapping.ProviderID.IsZero() || mapping.InstrumentID.IsZero() {
		return invalidData("provider and instrument IDs are required")
	}
	if err := validateText(mapping.ExternalSymbol, 128, "provider external symbol"); err != nil {
		return err
	}
	if err := validateText(mapping.ProviderMarket, 32, "provider market"); err != nil {
		return err
	}
	if mapping.Priority < 0 {
		return invalidData("provider instrument priority cannot be negative")
	}
	if err := mapping.Capabilities.Validate(); err != nil {
		return err
	}
	if err := validateValidRange(mapping.ValidFrom, mapping.ValidTo, "provider instrument"); err != nil {
		return err
	}
	if mapping.IsDefault && (!mapping.Enabled || mapping.ValidTo != nil) {
		return invalidData("default provider instrument must be enabled and have no valid_to")
	}
	return validateRecord(mapping.Metadata, mapping.CreatedAt, mapping.UpdatedAt, "provider instrument")
}

// ValidateAssetInstrument enforces the Asset + Instrument relationship and
// the supported first-phase identity combinations.
func ValidateAssetInstrument(asset Asset, instrument Instrument) error {
	if err := asset.Validate(); err != nil {
		return err
	}
	if err := instrument.Validate(); err != nil {
		return err
	}
	if asset.ID != instrument.AssetID {
		return invalidData("instrument does not belong to asset")
	}
	compatible := (asset.AssetType == AssetTypeStock && instrument.InstrumentType == InstrumentTypeEquity) ||
		(asset.AssetType == AssetTypeETF && instrument.InstrumentType == InstrumentTypeETF) ||
		(asset.AssetType == AssetTypeCrypto && instrument.InstrumentType == InstrumentTypeSpot) ||
		(asset.AssetType == AssetTypeFX && instrument.InstrumentType == InstrumentTypeFX)
	if !compatible {
		return invalidData("asset and instrument types are incompatible")
	}
	return nil
}

// ValidateProviderMapping enforces the Provider + Instrument +
// ProviderInstrument relationship without requiring infrastructure access.
func ValidateProviderMapping(provider Provider, instrument Instrument, mapping ProviderInstrument) error {
	if err := provider.Validate(); err != nil {
		return err
	}
	if err := instrument.Validate(); err != nil {
		return err
	}
	if err := mapping.Validate(); err != nil {
		return err
	}
	if mapping.ProviderID != provider.ID || mapping.InstrumentID != instrument.ID {
		return invalidData("provider instrument relationship IDs do not match")
	}
	return nil
}

func validateIdentity(id ID, code Code, prefix string, maximumLength int, kind string) error {
	if id.IsZero() {
		return invalidData(kind + " ID is required")
	}
	parsed, err := ParseCode(code.String())
	if err != nil || parsed != code || len(code.String()) > maximumLength {
		return invalidData(kind + " code is invalid")
	}
	if prefix != "" && !strings.HasPrefix(code.String(), prefix) {
		return invalidData(kind + " code has an invalid prefix")
	}
	return nil
}

func validateText(value string, maximumLength int, field string) error {
	if strings.TrimSpace(value) == "" || utf8.RuneCountInString(value) > maximumLength {
		return invalidData(field + " is required or exceeds its maximum length")
	}
	return nil
}

func validateScale(value *int16, field string) error {
	if value != nil && (*value < 0 || *value > 18) {
		return invalidData(field + " must be between 0 and 18")
	}
	return nil
}

func validatePositiveDecimal(value *Decimal, field string) error {
	if value != nil && !value.Exact().IsPositive() {
		return invalidData(field + " must be positive")
	}
	return nil
}

func validateValidRange(from, to *time.Time, kind string) error {
	if from != nil && to != nil && !to.After(*from) {
		return invalidData(kind + " valid_to must be after valid_from")
	}
	return nil
}

func validateRecord(metadata json.RawMessage, createdAt, updatedAt time.Time, kind string) error {
	if !isJSONObject(metadata) {
		return invalidData(kind + " metadata must be a JSON object")
	}
	if createdAt.IsZero() || updatedAt.IsZero() || updatedAt.Before(createdAt) {
		return invalidData(kind + " timestamps are invalid")
	}
	return nil
}

func normalizedJSON(value json.RawMessage) json.RawMessage {
	if len(value) == 0 {
		return json.RawMessage("{}")
	}
	return append(json.RawMessage(nil), value...)
}

func normalizedOptionalTime(value *time.Time) *time.Time {
	if value == nil {
		return nil
	}
	normalized := UTC(*value)
	return &normalized
}

func isJSONObject(value json.RawMessage) bool {
	if !json.Valid(value) {
		return false
	}
	var object map[string]json.RawMessage
	return json.Unmarshal(value, &object) == nil && object != nil
}

func ensureJSONEnd(decoder *json.Decoder) error {
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		return invalidData("provider capabilities contain trailing JSON")
	}
	return nil
}

func invalidData(message string) error {
	return fmt.Errorf("%s: %w", message, ErrInvalidData)
}
