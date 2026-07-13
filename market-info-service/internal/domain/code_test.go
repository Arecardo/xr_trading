package domain

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

func TestParseCode(t *testing.T) {
	valid := []string{"bybit", "asset.crypto.btc", "instrument.bybit.spot.btc-usdt", "a1.b2-c3"}
	for _, input := range valid {
		got, err := ParseCode(input)
		if err != nil || got.String() != input || got.IsZero() {
			t.Fatalf("ParseCode(%q) = (%q, %v)", input, got.String(), err)
		}
	}
	invalid := []string{"", "BTC", "asset_crypto_btc", ".asset", "asset.", "asset..btc", "asset.-btc", "asset btc", strings.Repeat("a", maximumCodeLength+1)}
	for _, input := range invalid {
		if _, err := ParseCode(input); !errors.Is(err, ErrInvalidData) {
			t.Fatalf("ParseCode(%q) error = %v, want invalid data", input, err)
		}
	}
}

func TestCodeJSONRoundTrip(t *testing.T) {
	want, _ := ParseCode("asset.crypto.btc")
	data, err := json.Marshal(want)
	if err != nil || string(data) != `"asset.crypto.btc"` {
		t.Fatalf("json.Marshal() = (%s, %v)", data, err)
	}
	var got Code
	if err := json.Unmarshal(data, &got); err != nil || got != want {
		t.Fatalf("json.Unmarshal() = (%v, %v)", got, err)
	}
	if _, err := json.Marshal(Code{}); err == nil {
		t.Fatal("json.Marshal(zero Code) error = nil, want error")
	}
}
