package domain

import (
	"encoding/json"
	"fmt"
	"time"
)

// UTCInstant is a non-zero instant normalized to UTC.
type UTCInstant struct {
	value time.Time
}

// NewUTCInstant normalizes a non-zero time to UTC.
func NewUTCInstant(value time.Time) (UTCInstant, error) {
	if value.IsZero() {
		return UTCInstant{}, fmt.Errorf("construct UTC instant: %w", ErrInvalidData)
	}
	return UTCInstant{value: value.UTC()}, nil
}

// ParseUTCInstant parses RFC3339 text and normalizes it to UTC.
func ParseUTCInstant(value string) (UTCInstant, error) {
	parsed, err := time.Parse(time.RFC3339Nano, value)
	if err != nil {
		return UTCInstant{}, fmt.Errorf("parse UTC instant: %w", err)
	}
	return NewUTCInstant(parsed)
}

// Time returns the normalized standard-library time.
func (instant UTCInstant) Time() time.Time {
	return instant.value
}

// IsZero reports whether instant has not been initialized.
func (instant UTCInstant) IsZero() bool {
	return instant.value.IsZero()
}

// String returns RFC3339Nano text in UTC using Z.
func (instant UTCInstant) String() string {
	if instant.IsZero() {
		return ""
	}
	return instant.value.Format(time.RFC3339Nano)
}

// MarshalJSON encodes a non-zero instant as an RFC3339 string.
func (instant UTCInstant) MarshalJSON() ([]byte, error) {
	if instant.IsZero() {
		return nil, fmt.Errorf("marshal UTC instant: zero instant")
	}
	return json.Marshal(instant.String())
}

// UnmarshalJSON decodes and normalizes an RFC3339 string.
func (instant *UTCInstant) UnmarshalJSON(data []byte) error {
	if instant == nil {
		return fmt.Errorf("unmarshal UTC instant: nil receiver")
	}
	var text string
	if err := json.Unmarshal(data, &text); err != nil {
		return fmt.Errorf("unmarshal UTC instant JSON: %w", err)
	}
	parsed, err := ParseUTCInstant(text)
	if err != nil {
		return err
	}
	*instant = parsed
	return nil
}
