package domain

import (
	"encoding/json"
	"testing"

	"github.com/google/uuid"
)

func TestNewIDCreatesUUIDv7(t *testing.T) {
	id, err := NewID()
	if err != nil {
		t.Fatalf("NewID() error = %v", err)
	}
	if id.IsZero() {
		t.Fatal("NewID() returned zero ID")
	}
	if got := id.UUID().Version(); got != uuid.Version(7) {
		t.Fatalf("NewID() version = %d, want 7", got)
	}
}

func TestIDRoundTrip(t *testing.T) {
	const value = "019f1452-90f7-7992-a87a-ca272789160f"

	id, err := ParseID(value)
	if err != nil {
		t.Fatalf("ParseID() error = %v", err)
	}
	if got := id.String(); got != value {
		t.Fatalf("ID.String() = %q, want %q", got, value)
	}
	if got := IDFromUUID(id.UUID()); got != id {
		t.Fatalf("IDFromUUID(UUID()) = %v, want %v", got, id)
	}
}

func TestParseIDRejectsInvalidValue(t *testing.T) {
	if _, err := ParseID("not-a-uuid"); err == nil {
		t.Fatal("ParseID() error = nil, want error")
	}
	if _, err := ParseID("{019f1452-90f7-7992-a87a-ca272789160f}"); err == nil {
		t.Fatal("ParseID(non-canonical) error = nil, want error")
	}
}

func TestIDJSONRoundTrip(t *testing.T) {
	want, err := ParseID("019f1452-90f7-7992-a87a-ca272789160f")
	if err != nil {
		t.Fatalf("ParseID() error = %v", err)
	}
	data, err := json.Marshal(want)
	if err != nil {
		t.Fatalf("json.Marshal() error = %v", err)
	}
	if string(data) != `"019f1452-90f7-7992-a87a-ca272789160f"` {
		t.Fatalf("json.Marshal() = %s", data)
	}
	var got ID
	if err := json.Unmarshal(data, &got); err != nil || got != want {
		t.Fatalf("json.Unmarshal() = (%v, %v), want %v", got, err, want)
	}
	if _, err := json.Marshal(ID{}); err == nil {
		t.Fatal("json.Marshal(zero ID) error = nil, want error")
	}
}
