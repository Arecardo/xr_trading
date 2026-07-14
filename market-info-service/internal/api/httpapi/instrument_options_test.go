package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/uuid"

	"xr-trading/market-info-service/internal/application"
	"xr-trading/market-info-service/internal/domain"
)

type stubInstrumentOptionsQuery struct {
	page  application.InstrumentOptionsPage
	err   error
	input application.InstrumentOptionsInput
	calls int
}

func (stub *stubInstrumentOptionsQuery) List(_ context.Context, input application.InstrumentOptionsInput) (application.InstrumentOptionsPage, error) {
	stub.calls++
	stub.input = input
	return stub.page, stub.err
}

func TestInstrumentOptionsHandlerReturnsPageAndScopedCursor(t *testing.T) {
	instrumentCode := testHTTPCode(t, "instrument.crypto.btc-usdt")
	providerCode := testHTTPCode(t, "bybit")
	query := &stubInstrumentOptionsQuery{page: application.InstrumentOptionsPage{
		Items: []application.InstrumentOption{{
			ID: domain.IDFromUUID(uuid.MustParse("019f1452-90f7-7992-a87a-ca272789160f")), Code: instrumentCode, DisplayName: "BTC/USDT",
			Providers: []application.ProviderOption{{Code: providerCode, DisplayName: "Bybit", IsDefault: true, Priority: 10, SupportedIntervals: []domain.BarInterval{domain.BarInterval1Hour, domain.BarInterval1Day}}},
		}},
		NextAfterInstrumentCode: &instrumentCode,
	}}
	handler, err := NewInstrumentOptionsHandler(query)
	if err != nil {
		t.Fatalf("NewInstrumentOptionsHandler() error = %v", err)
	}
	mux := http.NewServeMux()
	if err := handler.Register(mux); err != nil {
		t.Fatalf("Register() error = %v", err)
	}

	response := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, instrumentOptionsPath+"?asset_code=asset.crypto.btc&enabled=true&limit=1", nil)
	WithRequestID(mux).ServeHTTP(response, request)
	if response.Code != http.StatusOK || response.Header().Get("Content-Type") != "application/json; charset=utf-8" || !ValidRequestID(response.Header().Get(RequestIDHeader)) {
		t.Fatalf("response status=%d headers=%v body=%s", response.Code, response.Header(), response.Body.String())
	}
	var body struct {
		Items []struct {
			InstrumentID string `json:"instrument_id"`
			Providers    []struct {
				ProviderCode       string   `json:"provider_code"`
				SupportedIntervals []string `json:"supported_intervals"`
			} `json:"providers"`
		} `json:"items"`
		NextCursor *string `json:"next_cursor"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if len(body.Items) != 1 || body.Items[0].InstrumentID == "" || len(body.Items[0].Providers) != 1 || body.Items[0].Providers[0].ProviderCode != "bybit" || len(body.Items[0].Providers[0].SupportedIntervals) != 2 || body.NextCursor == nil {
		t.Fatalf("response body = %#v", body)
	}
	positions, err := DecodeCursor(*body.NextCursor, instrumentOptionsCursorScope, 2)
	if err != nil || positions[0] != "asset.crypto.btc" || positions[1] != instrumentCode.String() {
		t.Fatalf("cursor = (%#v, %v)", positions, err)
	}
	if query.input.AssetCode != "asset.crypto.btc" || query.input.Limit != 1 || query.input.AfterInstrumentCode != "" {
		t.Fatalf("query input = %#v", query.input)
	}
}

func TestInstrumentOptionsHandlerParsesMatchingCursor(t *testing.T) {
	cursor, _ := EncodeCursor(instrumentOptionsCursorScope, "asset.crypto.btc", "instrument.crypto.btc-usdc")
	query := &stubInstrumentOptionsQuery{page: application.InstrumentOptionsPage{Items: []application.InstrumentOption{}}}
	handler, _ := NewInstrumentOptionsHandler(query)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, instrumentOptionsPath+"?asset_code=asset.crypto.btc&cursor="+cursor, nil))
	if response.Code != http.StatusOK || query.input.Limit != instrumentOptionsPageLimits.Default || query.input.AfterInstrumentCode != "instrument.crypto.btc-usdc" || !strings.Contains(response.Body.String(), `"next_cursor":null`) {
		t.Fatalf("response=%d %s input=%#v", response.Code, response.Body.String(), query.input)
	}
}

func TestInstrumentOptionsHandlerRejectsInvalidQuery(t *testing.T) {
	otherAssetCursor, _ := EncodeCursor(instrumentOptionsCursorScope, "asset.crypto.eth", "instrument.crypto.eth-usdt")
	tests := []string{
		"",
		"?asset_code=asset.crypto.btc&enabled=false",
		"?asset_code=asset.crypto.btc&enabled=TRUE",
		"?asset_code=asset.crypto.btc&asset_code=asset.crypto.eth",
		"?asset_code=asset.crypto.btc&unknown=value",
		"?asset_code=asset.crypto.btc&limit=101",
		"?asset_code=asset.crypto.btc&cursor=bad",
		"?asset_code=asset.crypto.btc&cursor=" + otherAssetCursor,
		"?asset_code=asset.crypto.btc&enabled=",
		"?asset_code=asset.crypto.btc&bad=%zz",
	}
	for _, rawQuery := range tests {
		t.Run(rawQuery, func(t *testing.T) {
			query := &stubInstrumentOptionsQuery{}
			handler, _ := NewInstrumentOptionsHandler(query)
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, instrumentOptionsPath+rawQuery, nil))
			if response.Code != http.StatusBadRequest || query.calls != 0 {
				t.Fatalf("response=%d body=%s calls=%d", response.Code, response.Body.String(), query.calls)
			}
			var envelope ErrorEnvelope
			if err := json.Unmarshal(response.Body.Bytes(), &envelope); err != nil || envelope.Error.Code != application.ErrorCodeInvalidArgument {
				t.Fatalf("error envelope = %#v, decode err=%v", envelope, err)
			}
		})
	}
}

func TestInstrumentOptionsHandlerMapsServiceAndResponseFailures(t *testing.T) {
	tests := []struct {
		name       string
		query      *stubInstrumentOptionsQuery
		wantStatus int
		wantCode   application.ErrorCode
	}{
		{"asset missing", &stubInstrumentOptionsQuery{err: application.NewError(application.ErrorCodeAssetNotFound, "asset not found", false, nil)}, http.StatusNotFound, application.ErrorCodeAssetNotFound},
		{"bad cursor position", &stubInstrumentOptionsQuery{page: application.InstrumentOptionsPage{NextAfterInstrumentCode: &domain.Code{}}}, http.StatusInternalServerError, application.ErrorCodeInternal},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			handler, _ := NewInstrumentOptionsHandler(test.query)
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, instrumentOptionsPath+"?asset_code=asset.crypto.btc", nil))
			var envelope ErrorEnvelope
			if err := json.Unmarshal(response.Body.Bytes(), &envelope); err != nil || response.Code != test.wantStatus || envelope.Error.Code != test.wantCode {
				t.Fatalf("response=%d body=%s decode=%v", response.Code, response.Body.String(), err)
			}
		})
	}
}

func TestInstrumentOptionsHandlerRequiresDependencies(t *testing.T) {
	if _, err := NewInstrumentOptionsHandler(nil); err == nil {
		t.Fatal("NewInstrumentOptionsHandler(nil) error = nil")
	}
	handler, _ := NewInstrumentOptionsHandler(&stubInstrumentOptionsQuery{})
	if err := handler.Register(nil); err == nil {
		t.Fatal("Register(nil) error = nil")
	}
	var nilHandler *InstrumentOptionsHandler
	if err := nilHandler.Register(http.NewServeMux()); err == nil {
		t.Fatal("nil handler Register() error = nil")
	}
}

func TestParseInstrumentOptionsRequestRejectsNil(t *testing.T) {
	if _, err := parseInstrumentOptionsRequest(nil); err == nil {
		t.Fatal("parseInstrumentOptionsRequest(nil) error = nil")
	}
	if _, err := singleQueryValue(map[string][]string{"limit": {"1", "2"}}, "limit", false); err == nil {
		t.Fatal("singleQueryValue(repeated) error = nil")
	}
	if _, err := instrumentOptionsResponseFromPage("asset.crypto.btc", application.InstrumentOptionsPage{NextAfterInstrumentCode: &domain.Code{}}); err == nil {
		t.Fatal("instrumentOptionsResponseFromPage(invalid) error = nil")
	}
}

func testHTTPCode(t *testing.T, value string) domain.Code {
	t.Helper()
	code, err := domain.ParseCode(value)
	if err != nil {
		t.Fatalf("ParseCode(%q) error = %v", value, err)
	}
	return code
}
