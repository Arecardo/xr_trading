// Package domain contains the market information service's business types and
// contracts. It must not depend on infrastructure implementations.
package domain

import (
	"encoding/json"
	"fmt"

	"github.com/google/uuid"
)

// ID is an immutable UUID identity used for entities and relationships.
// New IDs are generated as UUIDv7; readable codes remain separate fields.
type ID uuid.UUID

// NewID creates a new UUIDv7 identifier.
func NewID() (ID, error) {
	generated, err := uuid.NewV7()
	if err != nil {
		return ID{}, fmt.Errorf("generate UUIDv7: %w", err)
	}
	return IDFromUUID(generated), nil
}

// ParseID parses a canonical UUID string into an ID.
func ParseID(value string) (ID, error) {
	parsed, err := uuid.Parse(value)
	if err != nil {
		return ID{}, fmt.Errorf("parse ID: %w", err)
	}
	if parsed.String() != value {
		return ID{}, fmt.Errorf("parse ID: non-canonical UUID")
	}
	return IDFromUUID(parsed), nil
}

// IDFromUUID converts a UUID from an infrastructure boundary into a domain ID.
func IDFromUUID(value uuid.UUID) ID {
	return ID(value)
}

// UUID returns the standard UUID representation for a storage boundary.
func (id ID) UUID() uuid.UUID {
	return uuid.UUID(id)
}

// String returns the canonical UUID string.
func (id ID) String() string {
	return id.UUID().String()
}

// IsZero reports whether id has not been set.
func (id ID) IsZero() bool {
	return id.UUID() == uuid.Nil
}

// MarshalText encodes ID as a canonical UUID string.
func (id ID) MarshalText() ([]byte, error) {
	if id.IsZero() {
		return nil, fmt.Errorf("marshal ID: zero ID")
	}
	return []byte(id.String()), nil
}

// UnmarshalText decodes a canonical UUID string.
func (id *ID) UnmarshalText(value []byte) error {
	if id == nil {
		return fmt.Errorf("unmarshal ID: nil receiver")
	}
	parsed, err := ParseID(string(value))
	if err != nil {
		return err
	}
	*id = parsed
	return nil
}

// MarshalJSON encodes ID as a JSON string.
func (id ID) MarshalJSON() ([]byte, error) {
	text, err := id.MarshalText()
	if err != nil {
		return nil, err
	}
	return json.Marshal(string(text))
}

// UnmarshalJSON decodes ID from a JSON string.
func (id *ID) UnmarshalJSON(data []byte) error {
	var value string
	if err := json.Unmarshal(data, &value); err != nil {
		return fmt.Errorf("unmarshal ID JSON: %w", err)
	}
	return id.UnmarshalText([]byte(value))
}
