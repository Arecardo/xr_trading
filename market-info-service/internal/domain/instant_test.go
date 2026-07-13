package domain

import (
	"encoding/json"
	"errors"
	"testing"
	"time"
)

func TestUTCInstantNormalizesAndRoundTrips(t *testing.T) {
	instant, err := ParseUTCInstant("2026-07-13T08:30:00.123456+08:00")
	if err != nil {
		t.Fatalf("ParseUTCInstant() error = %v", err)
	}
	if got := instant.String(); got != "2026-07-13T00:30:00.123456Z" {
		t.Fatalf("UTCInstant.String() = %q", got)
	}
	if instant.Time().Location() != time.UTC {
		t.Fatalf("UTCInstant.Time().Location() = %v", instant.Time().Location())
	}
	data, err := json.Marshal(instant)
	if err != nil {
		t.Fatalf("json.Marshal() error = %v", err)
	}
	var decoded UTCInstant
	if err := json.Unmarshal(data, &decoded); err != nil || !decoded.Time().Equal(instant.Time()) {
		t.Fatalf("json.Unmarshal() = (%v, %v)", decoded.Time(), err)
	}
}

func TestUTCInstantRejectsInvalidAndZero(t *testing.T) {
	if _, err := NewUTCInstant(time.Time{}); !errors.Is(err, ErrInvalidData) {
		t.Fatalf("NewUTCInstant(zero) error = %v", err)
	}
	if _, err := ParseUTCInstant("not-a-time"); err == nil {
		t.Fatal("ParseUTCInstant(invalid) error = nil")
	}
	if _, err := json.Marshal(UTCInstant{}); err == nil {
		t.Fatal("json.Marshal(zero instant) error = nil")
	}
}
