package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"xr-trading/market-info-service/internal/application"
	"xr-trading/market-info-service/internal/domain"
)

type stubInstrumentPrecisionQuery struct {
	result application.InstrumentPrecisionResult
	err    error
	input  application.InstrumentPrecisionInput
	calls  int
}

func (stub *stubInstrumentPrecisionQuery) Batch(_ context.Context, input application.InstrumentPrecisionInput) (application.InstrumentPrecisionResult, error) {
	stub.calls++
	stub.input = input
	return stub.result, stub.err
}

func TestInstrumentPrecisionHandlerReturnsItemsAndMissingIDs(t *testing.T) {
	instrumentID := domain.IDFromUUID(uuid.MustParse("019f1452-90f7-7992-a87a-ca272789160f"))
	missingID := domain.IDFromUUID(uuid.MustParse("019f1452-90f7-7992-a87a-ca2727891699"))
	instrumentCode := testHTTPCode(t, "instrument.bybit.spot.btc-usdt")
	lotSize, err := domain.ParseDecimal("0.000001")
	if err != nil {
		t.Fatalf("ParseDecimal() error = %v", err)
	}
	minQuantity, err := domain.ParseDecimal("0.0001")
	if err != nil {
		t.Fatalf("ParseDecimal() error = %v", err)
	}
	asOf, err := domain.NewUTCInstant(time.Date(2026, time.August, 5, 0, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatalf("NewUTCInstant() error = %v", err)
	}
	query := &stubInstrumentPrecisionQuery{result: application.InstrumentPrecisionResult{
		Items: []application.InstrumentPrecisionItem{{
			InstrumentID: instrumentID, InstrumentCode: instrumentCode, PriceScale: 2, QuantityScale: 6,
			LotSize: lotSize, MinQuantity: minQuantity, AsOf: asOf,
		}},
		MissingInstrumentIDs: []domain.ID{missingID},
	}}
	handler, err := NewInstrumentPrecisionHandler(query)
	if err != nil {
		t.Fatalf("NewInstrumentPrecisionHandler() error = %v", err)
	}
	mux := http.NewServeMux()
	if err := handler.Register(mux); err != nil {
		t.Fatalf("Register() error = %v", err)
	}

	requestBody := `{"instrument_ids":["` + instrumentID.String() + `","` + missingID.String() + `"]}`
	request := httptest.NewRequest(http.MethodPost, instrumentPrecisionBatchPath, strings.NewReader(requestBody))
	response := httptest.NewRecorder()
	WithRequestID(mux).ServeHTTP(response, request)
	if response.Code != http.StatusOK || response.Header().Get("Content-Type") != "application/json; charset=utf-8" || !ValidRequestID(response.Header().Get(RequestIDHeader)) {
		t.Fatalf("response status=%d headers=%v body=%s", response.Code, response.Header(), response.Body.String())
	}
	if query.input.InstrumentIDs[0] != instrumentID.String() || query.input.InstrumentIDs[1] != missingID.String() {
		t.Fatalf("query input = %#v", query.input)
	}

	var body struct {
		Items []struct {
			InstrumentID   string `json:"instrument_id"`
			InstrumentCode string `json:"instrument_code"`
			PriceScale     int16  `json:"price_scale"`
			QuantityScale  int16  `json:"quantity_scale"`
			LotSize        string `json:"lot_size"`
			MinQuantity    string `json:"min_quantity"`
			AsOf           string `json:"as_of"`
		} `json:"items"`
		MissingInstrumentIDs []string `json:"missing_instrument_ids"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if len(body.Items) != 1 || body.Items[0].InstrumentID != instrumentID.String() || body.Items[0].InstrumentCode != instrumentCode.String() ||
		body.Items[0].PriceScale != 2 || body.Items[0].QuantityScale != 6 || body.Items[0].LotSize != "0.000001" || body.Items[0].MinQuantity != "0.0001" ||
		!strings.HasSuffix(body.Items[0].AsOf, "Z") {
		t.Fatalf("response items = %#v", body.Items)
	}
	if len(body.MissingInstrumentIDs) != 1 || body.MissingInstrumentIDs[0] != missingID.String() {
		t.Fatalf("response missing_instrument_ids = %#v", body.MissingInstrumentIDs)
	}
}

func TestInstrumentPrecisionHandlerReturnsEmptyArraysNotNull(t *testing.T) {
	query := &stubInstrumentPrecisionQuery{result: application.InstrumentPrecisionResult{}}
	handler, _ := NewInstrumentPrecisionHandler(query)
	response := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, instrumentPrecisionBatchPath, strings.NewReader(`{"instrument_ids":["019f1452-90f7-7992-a87a-ca272789160f"]}`))
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"items":[]`) || !strings.Contains(response.Body.String(), `"missing_instrument_ids":[]`) {
		t.Fatalf("response=%d body=%s", response.Code, response.Body.String())
	}
}

func TestInstrumentPrecisionHandlerRejectsInvalidRequests(t *testing.T) {
	tests := []struct {
		name string
		body string
	}{
		{"empty body", ""},
		{"not JSON", "not json"},
		{"unknown field", `{"instrument_ids":["019f1452-90f7-7992-a87a-ca272789160f"],"unexpected":true}`},
		{"trailing data", `{"instrument_ids":["019f1452-90f7-7992-a87a-ca272789160f"]}{}`},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			query := &stubInstrumentPrecisionQuery{}
			handler, _ := NewInstrumentPrecisionHandler(query)
			response := httptest.NewRecorder()
			request := httptest.NewRequest(http.MethodPost, instrumentPrecisionBatchPath, strings.NewReader(test.body))
			handler.ServeHTTP(response, request)
			if response.Code != http.StatusBadRequest {
				t.Fatalf("response=%d body=%s", response.Code, response.Body.String())
			}
			var envelope ErrorEnvelope
			if err := json.Unmarshal(response.Body.Bytes(), &envelope); err != nil || envelope.Error.Code != application.ErrorCodeInvalidArgument {
				t.Fatalf("error envelope = %#v, decode err=%v", envelope, err)
			}
		})
	}
}

func TestInstrumentPrecisionHandlerRejectsOtherMethods(t *testing.T) {
	query := &stubInstrumentPrecisionQuery{}
	handler, _ := NewInstrumentPrecisionHandler(query)
	mux := http.NewServeMux()
	if err := handler.Register(mux); err != nil {
		t.Fatalf("Register() error = %v", err)
	}
	response := httptest.NewRecorder()
	mux.ServeHTTP(response, httptest.NewRequest(http.MethodGet, instrumentPrecisionBatchPath, nil))
	if response.Code != http.StatusMethodNotAllowed {
		t.Fatalf("GET response = %d, want 405", response.Code)
	}
}

func TestInstrumentPrecisionHandlerMapsServiceFailure(t *testing.T) {
	query := &stubInstrumentPrecisionQuery{err: application.NewError(application.ErrorCodeInternal, "internal server error", false, nil)}
	handler, _ := NewInstrumentPrecisionHandler(query)
	response := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, instrumentPrecisionBatchPath, strings.NewReader(`{"instrument_ids":["019f1452-90f7-7992-a87a-ca272789160f"]}`))
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusInternalServerError {
		t.Fatalf("response=%d body=%s", response.Code, response.Body.String())
	}
}

func TestInstrumentPrecisionHandlerRequiresDependencies(t *testing.T) {
	if _, err := NewInstrumentPrecisionHandler(nil); err == nil {
		t.Fatal("NewInstrumentPrecisionHandler(nil) error = nil")
	}
	handler, _ := NewInstrumentPrecisionHandler(&stubInstrumentPrecisionQuery{})
	if err := handler.Register(nil); err == nil {
		t.Fatal("Register(nil) error = nil")
	}
	var nilHandler *InstrumentPrecisionHandler
	if err := nilHandler.Register(http.NewServeMux()); err == nil {
		t.Fatal("nil handler Register() error = nil")
	}
}
