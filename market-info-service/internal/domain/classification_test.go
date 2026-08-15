package domain

import (
	"encoding/json"
	"errors"
	"testing"
)

func TestClassificationParsers(t *testing.T) {
	for _, value := range []string{"1h", "1d"} {
		if _, err := ParseBarInterval(value); err != nil {
			t.Fatalf("ParseBarInterval(%q) error = %v", value, err)
		}
	}
	if _, err := ParseBarInterval("5m"); !errors.Is(err, ErrInvalidData) {
		t.Fatalf("ParseBarInterval(5m) error = %v", err)
	}
	for _, value := range []string{"STOCK", "ETF", "CRYPTO", "CASH", "FX"} {
		if _, err := ParseAssetType(value); err != nil {
			t.Fatalf("ParseAssetType(%q) error = %v", value, err)
		}
	}
	if _, err := ParseAssetType("crypto"); !errors.Is(err, ErrInvalidData) {
		t.Fatalf("ParseAssetType(crypto) error = %v", err)
	}
	for _, value := range []string{"EQUITY", "ETF", "SPOT", "FX"} {
		if _, err := ParseInstrumentType(value); err != nil {
			t.Fatalf("ParseInstrumentType(%q) error = %v", value, err)
		}
	}
	if _, err := ParseInstrumentType("FUTURE"); !errors.Is(err, ErrInvalidData) {
		t.Fatalf("ParseInstrumentType(FUTURE) error = %v", err)
	}
}

func TestClassificationJSONRoundTrip(t *testing.T) {
	tests := []struct {
		name   string
		input  any
		output any
	}{
		{name: "interval", input: BarInterval1Hour, output: new(BarInterval)},
		{name: "asset", input: AssetTypeCrypto, output: new(AssetType)},
		{name: "instrument", input: InstrumentTypeSpot, output: new(InstrumentType)},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			data, err := json.Marshal(test.input)
			if err != nil {
				t.Fatalf("json.Marshal() error = %v", err)
			}
			if err := json.Unmarshal(data, test.output); err != nil {
				t.Fatalf("json.Unmarshal() error = %v", err)
			}
		})
	}
	if _, err := json.Marshal(BarInterval("")); err == nil {
		t.Fatal("json.Marshal(zero interval) error = nil")
	}
}
