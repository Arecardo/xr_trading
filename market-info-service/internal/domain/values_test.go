package domain

import (
	"encoding/json"
	"testing"
	"time"
)

func TestUTC(t *testing.T) {
	local := time.Date(2026, time.July, 12, 8, 30, 0, 0, time.FixedZone("UTC+8", 8*60*60))
	got := UTC(local)
	if got.Location() != time.UTC {
		t.Fatalf("UTC() location = %v, want UTC", got.Location())
	}
	if got.Hour() != 0 || got.Minute() != 30 {
		t.Fatalf("UTC() = %v, want 00:30 UTC", got)
	}
	if !UTC(time.Time{}).IsZero() {
		t.Fatal("UTC(zero) must preserve zero time")
	}
}

func TestParseDecimal(t *testing.T) {
	got, err := ParseDecimal("123.450000000000000001")
	if err != nil {
		t.Fatalf("ParseDecimal() error = %v", err)
	}
	if got.String() != "123.450000000000000001" {
		t.Fatalf("ParseDecimal() = %s", got)
	}
	if _, err := ParseDecimal("not-a-number"); err == nil {
		t.Fatal("ParseDecimal() error = nil, want error")
	}
}

func TestDecimalJSONRequiresString(t *testing.T) {
	want, err := ParseDecimal("123.450000000000000001")
	if err != nil {
		t.Fatalf("ParseDecimal() error = %v", err)
	}
	data, err := json.Marshal(want)
	if err != nil || string(data) != `"123.450000000000000001"` {
		t.Fatalf("json.Marshal() = (%s, %v)", data, err)
	}
	var got Decimal
	if err := json.Unmarshal(data, &got); err != nil || got.String() != want.String() {
		t.Fatalf("json.Unmarshal() = (%s, %v)", got.String(), err)
	}
	if err := json.Unmarshal([]byte(`123.45`), &got); err == nil {
		t.Fatal("json.Unmarshal(number) error = nil, want error")
	}
}
