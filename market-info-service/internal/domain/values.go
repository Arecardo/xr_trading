package domain

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/shopspring/decimal"
)

// UTC normalizes a non-zero instant before it crosses a service boundary.
// It preserves zero time so optional database fields remain optional.
func UTC(value time.Time) time.Time {
	if value.IsZero() {
		return value
	}
	return value.UTC()
}

// ParseDecimal parses a decimal value without allowing binary floating point.
// Decimal is an exact base-10 value. Its JSON representation is always a
// string so clients cannot accidentally round it through binary floating point.
type Decimal struct {
	value decimal.Decimal
}

// ParseDecimal constructs an exact Decimal from its base-10 text.
func ParseDecimal(value string) (Decimal, error) {
	parsed, err := decimal.NewFromString(value)
	if err != nil {
		return Decimal{}, fmt.Errorf("parse decimal: %w", err)
	}
	return Decimal{value: parsed}, nil
}

// DecimalFromExact adapts a shopspring Decimal at an infrastructure boundary.
func DecimalFromExact(value decimal.Decimal) Decimal {
	return Decimal{value: value}
}

// Exact returns the underlying exact decimal for storage and arithmetic.
func (value Decimal) Exact() decimal.Decimal {
	return value.value
}

// String returns the non-exponent base-10 representation.
func (value Decimal) String() string {
	return value.value.String()
}

// MarshalJSON encodes Decimal as a JSON string.
func (value Decimal) MarshalJSON() ([]byte, error) {
	return json.Marshal(value.String())
}

// UnmarshalJSON accepts only a JSON string containing an exact decimal.
func (value *Decimal) UnmarshalJSON(data []byte) error {
	if value == nil {
		return fmt.Errorf("unmarshal decimal: nil receiver")
	}
	var text string
	if err := json.Unmarshal(data, &text); err != nil {
		return fmt.Errorf("unmarshal decimal JSON string: %w", err)
	}
	parsed, err := ParseDecimal(text)
	if err != nil {
		return err
	}
	*value = parsed
	return nil
}
