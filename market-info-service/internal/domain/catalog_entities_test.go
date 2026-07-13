package domain

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
)

func TestNewCatalogEntitiesNormalizeAndValidate(t *testing.T) {
	location := time.FixedZone("UTC+8", 8*60*60)
	now := time.Date(2026, time.July, 13, 16, 0, 0, 0, location)
	asset := validAsset(t, now)
	asset.Metadata = nil
	loadedAsset, err := NewAsset(asset)
	if err != nil {
		t.Fatalf("NewAsset() error = %v", err)
	}
	if loadedAsset.CreatedAt.Location() != time.UTC || string(loadedAsset.Metadata) != "{}" {
		t.Fatalf("NewAsset() = %#v", loadedAsset)
	}

	instrument := validInstrument(t, loadedAsset.ID, now)
	loadedInstrument, err := NewInstrument(instrument)
	if err != nil {
		t.Fatalf("NewInstrument() error = %v", err)
	}
	if loadedInstrument.ValidFrom == nil || loadedInstrument.ValidFrom.Location() != time.UTC {
		t.Fatalf("NewInstrument() valid_from = %v", loadedInstrument.ValidFrom)
	}

	provider := validProvider(t, now)
	if _, err := NewProvider(provider); err != nil {
		t.Fatalf("NewProvider() error = %v", err)
	}
	mapping := validProviderInstrument(t, provider.ID, loadedInstrument.ID, now)
	if _, err := NewProviderInstrument(mapping); err != nil {
		t.Fatalf("NewProviderInstrument() error = %v", err)
	}
}

func TestAssetValidationRejectsInvalidFields(t *testing.T) {
	now := time.Now().UTC()
	tests := []struct {
		name   string
		mutate func(*Asset)
	}{
		{"zero ID", func(value *Asset) { value.ID = ID{} }},
		{"wrong code prefix", func(value *Asset) { value.Code = testDomainCode(t, "crypto.btc") }},
		{"long code", func(value *Asset) { value.Code = testDomainCode(t, "asset."+strings.Repeat("a", 123)) }},
		{"invalid type", func(value *Asset) { value.AssetType = "OPTION" }},
		{"blank symbol", func(value *Asset) { value.CanonicalSymbol = " " }},
		{"blank name", func(value *Asset) { value.Name = "" }},
		{"invalid status", func(value *Asset) { value.Status = "unknown" }},
		{"non-object metadata", func(value *Asset) { value.Metadata = json.RawMessage(`[]`) }},
		{"updated before created", func(value *Asset) { value.UpdatedAt = value.CreatedAt.Add(-time.Second) }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			value := validAsset(t, now)
			test.mutate(&value)
			if _, err := NewAsset(value); !errors.Is(err, ErrInvalidData) {
				t.Fatalf("NewAsset() error = %v, want ErrInvalidData", err)
			}
		})
	}
}

func TestInstrumentValidationCoversSpotAndPrecisionRules(t *testing.T) {
	now := time.Now().UTC()
	assetID := testDomainID("019f1452-90f7-7992-a87a-ca2727891601")
	valid := validInstrument(t, assetID, now)
	if _, err := NewInstrument(valid); err != nil {
		t.Fatalf("NewInstrument(valid) error = %v", err)
	}

	negativeScale := int16(-1)
	tooLargeScale := int16(19)
	zeroDecimal, _ := ParseDecimal("0")
	tests := []struct {
		name   string
		mutate func(*Instrument)
	}{
		{"zero asset ID", func(value *Instrument) { value.AssetID = ID{} }},
		{"invalid type", func(value *Instrument) { value.InstrumentType = "FUTURE" }},
		{"invalid status", func(value *Instrument) { value.Status = "unknown" }},
		{"blank venue", func(value *Instrument) { value.Venue = "" }},
		{"blank symbol", func(value *Instrument) { value.Symbol = "" }},
		{"blank quote", func(value *Instrument) { value.QuoteCurrency = "" }},
		{"invalid timezone", func(value *Instrument) { value.MarketTimezone = "Mars/Olympus" }},
		{"same base and quote", func(value *Instrument) { value.QuoteAssetID = &value.AssetID }},
		{"negative price scale", func(value *Instrument) { value.PriceScale = &negativeScale }},
		{"large quantity scale", func(value *Instrument) { value.QuantityScale = &tooLargeScale }},
		{"zero lot size", func(value *Instrument) { value.LotSize = &zeroDecimal }},
		{"zero minimum quantity", func(value *Instrument) { value.MinQuantity = &zeroDecimal }},
		{"invalid range", func(value *Instrument) { value.ValidTo = value.ValidFrom }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			value := valid
			test.mutate(&value)
			if _, err := NewInstrument(value); !errors.Is(err, ErrInvalidData) {
				t.Fatalf("NewInstrument() error = %v, want ErrInvalidData", err)
			}
		})
	}
}

func TestProviderAndMappingValidation(t *testing.T) {
	now := time.Now().UTC()
	provider := validProvider(t, now)
	provider.ProviderType = "FEED"
	if _, err := NewProvider(provider); !errors.Is(err, ErrInvalidData) {
		t.Fatalf("NewProvider(invalid type) error = %v", err)
	}
	provider = validProvider(t, now)
	provider.Status = "offline"
	if _, err := NewProvider(provider); !errors.Is(err, ErrInvalidData) {
		t.Fatalf("NewProvider(invalid status) error = %v", err)
	}

	instrument := validInstrument(t, testDomainID("019f1452-90f7-7992-a87a-ca2727891601"), now)
	mapping := validProviderInstrument(t, provider.ID, instrument.ID, now)
	mapping.Enabled = false
	if _, err := NewProviderInstrument(mapping); !errors.Is(err, ErrInvalidData) {
		t.Fatalf("NewProviderInstrument(disabled default) error = %v", err)
	}
	mapping = validProviderInstrument(t, provider.ID, instrument.ID, now)
	mapping.Priority = -1
	if _, err := NewProviderInstrument(mapping); !errors.Is(err, ErrInvalidData) {
		t.Fatalf("NewProviderInstrument(negative priority) error = %v", err)
	}
	mapping = validProviderInstrument(t, provider.ID, instrument.ID, now)
	validTo := now.Add(time.Hour)
	mapping.ValidTo = &validTo
	if _, err := NewProviderInstrument(mapping); !errors.Is(err, ErrInvalidData) {
		t.Fatalf("NewProviderInstrument(expiring default) error = %v", err)
	}
}

func TestProviderCapabilitiesStrictParsing(t *testing.T) {
	capabilities, err := ParseProviderCapabilities(json.RawMessage(`{"quote":true,"historical":true,"intervals":["1h","1d"]}`))
	if err != nil || !capabilities.Quote || len(capabilities.Intervals) != 2 {
		t.Fatalf("ParseProviderCapabilities() = (%#v, %v)", capabilities, err)
	}
	if _, err := ParseProviderCapabilities(nil); err != nil {
		t.Fatalf("ParseProviderCapabilities(nil) error = %v", err)
	}
	invalid := []json.RawMessage{
		json.RawMessage(`{"unknown":true}`),
		json.RawMessage(`{"intervals":["5m"]}`),
		json.RawMessage(`{"intervals":["1h","1h"]}`),
		json.RawMessage(`{} {}`),
	}
	for _, raw := range invalid {
		if _, err := ParseProviderCapabilities(raw); !errors.Is(err, ErrInvalidData) {
			t.Fatalf("ParseProviderCapabilities(%s) error = %v, want ErrInvalidData", raw, err)
		}
	}
}

func TestCatalogRelationshipValidation(t *testing.T) {
	now := time.Now().UTC()
	asset := validAsset(t, now)
	instrument := validInstrument(t, asset.ID, now)
	provider := validProvider(t, now)
	mapping := validProviderInstrument(t, provider.ID, instrument.ID, now)
	if err := ValidateAssetInstrument(asset, instrument); err != nil {
		t.Fatalf("ValidateAssetInstrument() error = %v", err)
	}
	if err := ValidateProviderMapping(provider, instrument, mapping); err != nil {
		t.Fatalf("ValidateProviderMapping() error = %v", err)
	}

	instrument.InstrumentType = InstrumentTypeEquity
	if err := ValidateAssetInstrument(asset, instrument); !errors.Is(err, ErrInvalidData) {
		t.Fatalf("ValidateAssetInstrument(type mismatch) error = %v", err)
	}
	instrument.InstrumentType = InstrumentTypeSpot
	mapping.ProviderID = testDomainID("019f1452-90f7-7992-a87a-ca2727891609")
	if err := ValidateProviderMapping(provider, instrument, mapping); !errors.Is(err, ErrInvalidData) {
		t.Fatalf("ValidateProviderMapping(ID mismatch) error = %v", err)
	}
}

func TestCatalogEnumsJSON(t *testing.T) {
	values := []struct {
		value any
		into  any
		want  string
	}{
		{AssetStatusActive, new(AssetStatus), "active"},
		{InstrumentStatusSuspended, new(InstrumentStatus), "suspended"},
		{ProviderTypeExchange, new(ProviderType), "EXCHANGE"},
		{ProviderStatusDegraded, new(ProviderStatus), "degraded"},
	}
	for _, value := range values {
		encoded, err := json.Marshal(value.value)
		if err != nil {
			t.Fatalf("json.Marshal(%v) error = %v", value.value, err)
		}
		if err := json.Unmarshal(encoded, value.into); err != nil {
			t.Fatalf("json.Unmarshal(%s) error = %v", encoded, err)
		}
		if string(encoded) != `"`+value.want+`"` {
			t.Fatalf("json.Marshal(%v) = %s", value.value, encoded)
		}
	}
}

func validAsset(t *testing.T, now time.Time) Asset {
	t.Helper()
	return Asset{
		ID:              testDomainID("019f1452-90f7-7992-a87a-ca2727891601"),
		Code:            testDomainCode(t, "asset.crypto.btc"),
		AssetType:       AssetTypeCrypto,
		CanonicalSymbol: "BTC",
		Name:            "Bitcoin",
		Status:          AssetStatusActive,
		Metadata:        json.RawMessage(`{"network":"bitcoin"}`),
		CreatedAt:       now,
		UpdatedAt:       now,
	}
}

func validInstrument(t *testing.T, assetID ID, now time.Time) Instrument {
	t.Helper()
	validFrom := now.Add(-time.Hour)
	priceScale := int16(8)
	quantityScale := int16(6)
	lotSize, _ := ParseDecimal("0.000001")
	minimum, _ := ParseDecimal("0.00001")
	return Instrument{
		ID:             testDomainID("019f1452-90f7-7992-a87a-ca2727891602"),
		Code:           testDomainCode(t, "instrument.bybit.spot.btc-usdt"),
		AssetID:        assetID,
		Venue:          "BYBIT",
		InstrumentType: InstrumentTypeSpot,
		Symbol:         "BTC-USDT",
		QuoteCurrency:  "USDT",
		MarketTimezone: "UTC",
		PriceScale:     &priceScale,
		QuantityScale:  &quantityScale,
		LotSize:        &lotSize,
		MinQuantity:    &minimum,
		Status:         InstrumentStatusActive,
		ValidFrom:      &validFrom,
		Metadata:       json.RawMessage(`{}`),
		CreatedAt:      now,
		UpdatedAt:      now,
	}
}

func validProvider(t *testing.T, now time.Time) Provider {
	t.Helper()
	return Provider{
		ID:           testDomainID("019f1452-90f7-7992-a87a-ca2727891603"),
		Code:         testDomainCode(t, "bybit"),
		Name:         "Bybit",
		ProviderType: ProviderTypeExchange,
		Status:       ProviderStatusActive,
		Metadata:     json.RawMessage(`{}`),
		CreatedAt:    now,
		UpdatedAt:    now,
	}
}

func validProviderInstrument(t *testing.T, providerID, instrumentID ID, now time.Time) ProviderInstrument {
	t.Helper()
	return ProviderInstrument{
		ID:             testDomainID("019f1452-90f7-7992-a87a-ca2727891604"),
		Code:           testDomainCode(t, "provider.bybit.spot.btcusdt"),
		ProviderID:     providerID,
		InstrumentID:   instrumentID,
		ExternalSymbol: "BTCUSDT",
		ProviderMarket: "spot",
		Capabilities: ProviderCapabilities{
			Quote:      true,
			Historical: true,
			Intervals:  []BarInterval{BarInterval1Hour, BarInterval1Day},
		},
		Priority:  10,
		IsDefault: true,
		Enabled:   true,
		Metadata:  json.RawMessage(`{}`),
		CreatedAt: now,
		UpdatedAt: now,
	}
}

func testDomainID(value string) ID {
	return IDFromUUID(uuid.MustParse(value))
}

func testDomainCode(t *testing.T, value string) Code {
	t.Helper()
	code, err := ParseCode(value)
	if err != nil {
		t.Fatalf("ParseCode(%q) error = %v", value, err)
	}
	return code
}
