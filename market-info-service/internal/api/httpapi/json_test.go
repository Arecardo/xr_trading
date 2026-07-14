package httpapi

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"xr-trading/market-info-service/internal/application"
	"xr-trading/market-info-service/internal/domain"
)

func TestDecodeJSONAcceptsOneKnownObject(t *testing.T) {
	request := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(`{"name":"btc","enabled":true}`))
	var destination struct {
		Name    string `json:"name"`
		Enabled bool   `json:"enabled"`
	}
	if err := DecodeJSON(httptest.NewRecorder(), request, &destination, DefaultMaximumRequestBodyBytes); err != nil {
		t.Fatalf("DecodeJSON() error = %v", err)
	}
	if destination.Name != "btc" || !destination.Enabled {
		t.Fatalf("destination = %#v", destination)
	}
}

func TestDecodeJSONRejectsInvalidBodies(t *testing.T) {
	tests := []struct {
		name string
		body string
		max  int64
	}{
		{"empty", "", 100},
		{"unknown field", `{"name":"btc","extra":true}`, 100},
		{"wrong type", `{"name":42}`, 100},
		{"multiple values", `{"name":"btc"} {"name":"eth"}`, 100},
		{"too large", `{"name":"bitcoin"}`, 5},
		{"malformed", `{"name":`, 100},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(test.body))
			var destination struct {
				Name string `json:"name"`
			}
			err := DecodeJSON(httptest.NewRecorder(), request, &destination, test.max)
			var appError *application.Error
			if !errors.As(err, &appError) || appError.Code != application.ErrorCodeInvalidArgument {
				t.Fatalf("DecodeJSON() error = %v", err)
			}
		})
	}
}

func TestDecodeJSONRejectsInvalidConfiguration(t *testing.T) {
	request := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(`{}`))
	if err := DecodeJSON(nil, request, &struct{}{}, 10); err == nil {
		t.Fatal("DecodeJSON(nil writer) error = nil")
	}
	if err := DecodeJSON(httptest.NewRecorder(), request, &struct{}{}, 0); err == nil {
		t.Fatal("DecodeJSON(zero maximum) error = nil")
	}
}

func TestWriteJSONReusesUTCAndExactDecimalEncoding(t *testing.T) {
	instant, _ := domain.NewUTCInstant(time.Date(2026, time.July, 14, 8, 0, 0, 123, time.FixedZone("UTC+8", 8*60*60)))
	price, _ := domain.ParseDecimal("62350.120000000000000001")
	payload := struct {
		MarketTime domain.UTCInstant `json:"market_time"`
		Price      domain.Decimal    `json:"price"`
	}{MarketTime: instant, Price: price}
	response := httptest.NewRecorder()
	if err := WriteJSON(response, http.StatusOK, payload); err != nil {
		t.Fatalf("WriteJSON() error = %v", err)
	}
	want := `{"market_time":"2026-07-14T00:00:00.000000123Z","price":"62350.120000000000000001"}`
	if strings.TrimSpace(response.Body.String()) != want {
		t.Fatalf("WriteJSON() body = %s", response.Body.String())
	}
}
