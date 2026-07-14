package httpapi

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"xr-trading/market-info-service/internal/application"
)

func TestParsePageSize(t *testing.T) {
	limits := PageLimits{Default: 100, Maximum: 500}
	if value, err := ParsePageSize("", limits); err != nil || value != 100 {
		t.Fatalf("ParsePageSize(empty) = (%d, %v)", value, err)
	}
	if value, err := ParsePageSize("250", limits); err != nil || value != 250 {
		t.Fatalf("ParsePageSize(250) = (%d, %v)", value, err)
	}
	for _, raw := range []string{"0", "501", "abc", "-1"} {
		_, err := ParsePageSize(raw, limits)
		assertInvalidArgument(t, err)
	}
	if _, err := ParsePageSize("", PageLimits{Default: 10, Maximum: 5}); err == nil {
		t.Fatal("ParsePageSize(invalid limits) error = nil")
	}
}

func TestCursorRoundTripAndScopeBinding(t *testing.T) {
	encoded, err := EncodeCursor("bars.bybit.1h.desc", "2026-07-14T00:00:00Z", "019f1452-90f7-7992-a87a-ca272789160f")
	if err != nil {
		t.Fatalf("EncodeCursor() error = %v", err)
	}
	if strings.ContainsAny(encoded, "+/=") {
		t.Fatalf("cursor is not raw URL-safe base64: %q", encoded)
	}
	values, err := DecodeCursor(encoded, "bars.bybit.1h.desc", 2)
	if err != nil || len(values) != 2 || values[0] != "2026-07-14T00:00:00Z" {
		t.Fatalf("DecodeCursor() = (%#v, %v)", values, err)
	}
	values[0] = "changed"
	again, _ := DecodeCursor(encoded, "bars.bybit.1h.desc", 2)
	if again[0] == "changed" {
		t.Fatal("DecodeCursor returned shared values")
	}
	_, err = DecodeCursor(encoded, "instruments", 2)
	assertInvalidArgument(t, err)
	_, err = DecodeCursor(encoded, "bars.bybit.1h.desc", 1)
	assertInvalidArgument(t, err)
}

func TestCursorRejectsMalformedInput(t *testing.T) {
	for _, encoded := range []string{"", "not-base64!", strings.Repeat("a", maximumEncodedCursorLength+1)} {
		_, err := DecodeCursor(encoded, "bars", 1)
		assertInvalidArgument(t, err)
	}
	if _, err := DecodeCursor("anything", "bars", 0); err == nil {
		t.Fatal("DecodeCursor(invalid expected count) error = nil")
	}

	badPayloads := []map[string]any{
		{"v": 2, "scope": "bars", "values": []string{"one"}},
		{"v": 1, "scope": "bars", "values": []string{}},
		{"v": 1, "scope": "bars", "values": []string{""}},
		{"v": 1, "scope": "bars", "values": []string{"one"}, "extra": true},
	}
	for _, payload := range badPayloads {
		raw, _ := json.Marshal(payload)
		_, err := DecodeCursor(base64.RawURLEncoding.EncodeToString(raw), "bars", 1)
		assertInvalidArgument(t, err)
	}
}

func TestEncodeCursorRejectsInvalidServerInput(t *testing.T) {
	tests := []struct {
		scope  string
		values []string
	}{
		{"Bad Scope", []string{"one"}},
		{"bars", nil},
		{"bars", []string{""}},
		{"bars", []string{strings.Repeat("a", maximumCursorValueLength+1)}},
	}
	for _, test := range tests {
		if _, err := EncodeCursor(test.scope, test.values...); err == nil {
			t.Fatalf("EncodeCursor(%q, %#v) error = nil", test.scope, test.values)
		}
	}
}

func assertInvalidArgument(t *testing.T, err error) {
	t.Helper()
	var appError *application.Error
	if !errors.As(err, &appError) || appError.Code != application.ErrorCodeInvalidArgument {
		t.Fatalf("error = %v, want INVALID_ARGUMENT", err)
	}
}
