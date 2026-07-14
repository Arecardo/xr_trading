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
	"github.com/shopspring/decimal"

	"xr-trading/market-info-service/internal/application"
	"xr-trading/market-info-service/internal/domain"
)

type stubLatestQuotesQuery struct {
	result application.LatestQuotesResult
	err    error
	input  application.LatestQuotesInput
	calls  int
}

func (stub *stubLatestQuotesQuery) List(_ context.Context, input application.LatestQuotesInput) (application.LatestQuotesResult, error) {
	stub.calls++
	stub.input = input
	return stub.result, stub.err
}

func TestLatestQuotesHandlerReturnsExactMultiSourceData(t *testing.T) {
	asset := httpLatestQuoteAsset(t)
	record := httpLatestQuoteRecord(t)
	query := &stubLatestQuotesQuery{result: application.LatestQuotesResult{Asset: asset, Quotes: []application.LatestQuoteRecord{record}}}
	handler, _ := NewLatestQuotesHandler(query)
	mux := http.NewServeMux()
	if err := handler.Register(mux); err != nil {
		t.Fatalf("Register() error = %v", err)
	}
	response := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, latestQuotesPath+"?asset_code=asset.crypto.btc&instrument_code=instrument.crypto.btc-usdt&provider=bybit", nil)
	WithRequestID(mux).ServeHTTP(response, request)
	if response.Code != http.StatusOK || !ValidRequestID(response.Header().Get(RequestIDHeader)) {
		t.Fatalf("response=%d headers=%v body=%s", response.Code, response.Header(), response.Body.String())
	}
	var body map[string]any
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	assetBody := body["asset"].(map[string]any)
	quotes := body["quotes"].([]any)
	quote := quotes[0].(map[string]any)
	if assetBody["asset_type"] != "crypto" || quote["provider"] != "bybit" || quote["provider_instrument_code"] != "provider.bybit.btc-usdt" || quote["price"] != "100.25" || quote["bid_price"] != "100.2" || quote["ask_price"] != nil || quote["market_time"] != "2026-07-15T00:00:00.123Z" || quote["quality_status"] != "warning" {
		t.Fatalf("response body = %#v", body)
	}
	if query.input.AssetCode != "asset.crypto.btc" || query.input.InstrumentCode != "instrument.crypto.btc-usdt" || query.input.ProviderCode != "bybit" {
		t.Fatalf("query input = %#v", query.input)
	}
}

func TestLatestQuotesHandlerReturnsEmptyArray(t *testing.T) {
	query := &stubLatestQuotesQuery{result: application.LatestQuotesResult{Asset: httpLatestQuoteAsset(t), Quotes: []application.LatestQuoteRecord{}}}
	handler, _ := NewLatestQuotesHandler(query)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, latestQuotesPath+"?asset_code=asset.crypto.btc", nil))
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"quotes":[]`) {
		t.Fatalf("response=%d body=%s", response.Code, response.Body.String())
	}
}

func TestLatestQuotesHandlerRejectsProtocolQueryErrors(t *testing.T) {
	tests := []string{
		"?asset_code=asset.crypto.btc&unknown=value",
		"?asset_code=asset.crypto.btc&provider=bybit&provider=coingecko",
		"?asset_code=asset.crypto.btc&provider=",
		"?asset_code=asset.crypto.btc&bad=%zz",
	}
	for _, rawQuery := range tests {
		t.Run(rawQuery, func(t *testing.T) {
			query := &stubLatestQuotesQuery{}
			handler, _ := NewLatestQuotesHandler(query)
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, latestQuotesPath+rawQuery, nil))
			if response.Code != http.StatusBadRequest || query.calls != 0 {
				t.Fatalf("response=%d body=%s calls=%d", response.Code, response.Body.String(), query.calls)
			}
		})
	}
}

func TestLatestQuotesHandlerMapsApplicationErrors(t *testing.T) {
	tests := []struct {
		name       string
		err        error
		wantStatus int
		wantCode   application.ErrorCode
	}{
		{"missing filter", application.ValidationError(nil), http.StatusBadRequest, application.ErrorCodeInvalidArgument},
		{"instrument missing", application.NewError(application.ErrorCodeInstrumentNotFound, "instrument not found", false, nil), http.StatusNotFound, application.ErrorCodeInstrumentNotFound},
		{"database unavailable", domain.ErrDatabaseUnavailable, http.StatusServiceUnavailable, application.ErrorCodeDatabaseUnavailable},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			query := &stubLatestQuotesQuery{err: test.err}
			handler, _ := NewLatestQuotesHandler(query)
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, latestQuotesPath, nil))
			var envelope ErrorEnvelope
			if err := json.Unmarshal(response.Body.Bytes(), &envelope); err != nil || response.Code != test.wantStatus || envelope.Error.Code != test.wantCode {
				t.Fatalf("response=%d body=%s decode=%v", response.Code, response.Body.String(), err)
			}
		})
	}
}

func TestLatestQuotesHandlerRequiresDependencies(t *testing.T) {
	if _, err := NewLatestQuotesHandler(nil); err == nil {
		t.Fatal("NewLatestQuotesHandler(nil) error = nil")
	}
	handler, _ := NewLatestQuotesHandler(&stubLatestQuotesQuery{})
	if err := handler.Register(nil); err == nil {
		t.Fatal("Register(nil) error = nil")
	}
	var nilHandler *LatestQuotesHandler
	if err := nilHandler.Register(http.NewServeMux()); err == nil {
		t.Fatal("nil handler Register() error = nil")
	}
	if _, err := parseLatestQuotesRequest(nil); err == nil {
		t.Fatal("parseLatestQuotesRequest(nil) error = nil")
	}
}

func TestLatestQuotesHandlerHandlesMarshalFailure(t *testing.T) {
	query := &stubLatestQuotesQuery{result: application.LatestQuotesResult{Quotes: []application.LatestQuoteRecord{}}}
	handler, _ := NewLatestQuotesHandler(query)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, latestQuotesPath+"?asset_code=asset.crypto.btc", nil))
	if response.Code != http.StatusInternalServerError || !strings.Contains(response.Body.String(), `"code":"INTERNAL_ERROR"`) {
		t.Fatalf("response=%d body=%s", response.Code, response.Body.String())
	}
}

func httpLatestQuoteAsset(t *testing.T) domain.Asset {
	t.Helper()
	now := time.Date(2026, time.July, 15, 0, 0, 0, 0, time.UTC)
	asset, err := domain.NewAsset(domain.Asset{ID: domain.IDFromUUID(uuid.MustParse("019f1452-90f7-7992-a87a-ca272789160f")), Code: testHTTPCode(t, "asset.crypto.btc"), AssetType: domain.AssetTypeCrypto, CanonicalSymbol: "BTC", Name: "Bitcoin", Status: domain.AssetStatusActive, CreatedAt: now, UpdatedAt: now})
	if err != nil {
		t.Fatalf("NewAsset() error = %v", err)
	}
	return asset
}

func httpLatestQuoteRecord(t *testing.T) application.LatestQuoteRecord {
	t.Helper()
	instant, _ := domain.NewUTCInstant(time.Date(2026, time.July, 15, 0, 0, 0, 123000000, time.UTC))
	bid := domain.DecimalFromExact(decimal.RequireFromString("100.20"))
	quote, err := domain.NewQuote(domain.Quote{
		InstrumentID:         domain.IDFromUUID(uuid.MustParse("019f1452-90f7-7992-a87a-ca2727891601")),
		ProviderInstrumentID: domain.IDFromUUID(uuid.MustParse("019f1452-90f7-7992-a87a-ca2727891602")),
		MarketTime:           instant, LastPrice: domain.DecimalFromExact(decimal.RequireFromString("100.25")), BidPrice: &bid,
		QualityStatus: domain.QualityStatusWarning, CollectedAt: instant,
	})
	if err != nil {
		t.Fatalf("NewQuote() error = %v", err)
	}
	return application.LatestQuoteRecord{
		InstrumentCode: testHTTPCode(t, "instrument.crypto.btc-usdt"), QuoteCurrency: "USDT",
		ProviderID: domain.IDFromUUID(uuid.MustParse("019f1452-90f7-7992-a87a-ca2727891603")), ProviderCode: testHTTPCode(t, "bybit"),
		ProviderInstrumentCode: testHTTPCode(t, "provider.bybit.btc-usdt"), ProviderSymbol: "BTCUSDT", Quote: quote,
	}
}
