package domain

import (
	"encoding/json"
	"fmt"
	"regexp"
)

const maximumCodeLength = 192

var codePattern = regexp.MustCompile(`^[a-z0-9]+(?:[.-][a-z0-9]+)*$`)

// Code is a stable human-readable identity used by configuration and APIs.
// Database relations continue to use ID.
type Code struct {
	value string
}

// ParseCode validates and constructs a Code.
func ParseCode(value string) (Code, error) {
	if len(value) == 0 || len(value) > maximumCodeLength || !codePattern.MatchString(value) {
		return Code{}, fmt.Errorf("parse code: %w", ErrInvalidData)
	}
	return Code{value: value}, nil
}

// String returns the canonical lowercase code.
func (code Code) String() string {
	return code.value
}

// IsZero reports whether code has not been initialized.
func (code Code) IsZero() bool {
	return code.value == ""
}

// MarshalText encodes Code as text.
func (code Code) MarshalText() ([]byte, error) {
	if code.IsZero() {
		return nil, fmt.Errorf("marshal code: zero code")
	}
	return []byte(code.value), nil
}

// UnmarshalText validates a text code.
func (code *Code) UnmarshalText(data []byte) error {
	if code == nil {
		return fmt.Errorf("unmarshal code: nil receiver")
	}
	parsed, err := ParseCode(string(data))
	if err != nil {
		return err
	}
	*code = parsed
	return nil
}

// MarshalJSON encodes Code as a JSON string.
func (code Code) MarshalJSON() ([]byte, error) {
	text, err := code.MarshalText()
	if err != nil {
		return nil, err
	}
	return json.Marshal(string(text))
}

// UnmarshalJSON decodes Code from a JSON string.
func (code *Code) UnmarshalJSON(data []byte) error {
	var text string
	if err := json.Unmarshal(data, &text); err != nil {
		return fmt.Errorf("unmarshal code JSON: %w", err)
	}
	return code.UnmarshalText([]byte(text))
}
