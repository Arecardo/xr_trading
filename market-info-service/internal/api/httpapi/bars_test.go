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

type stubBarsQuery struct {
	result application.BarsResult
	err    error
	input  application.BarsInput
	calls  int
}

func (stub *stubBarsQuery) List(_ context.Context, input application.BarsInput) (application.BarsResult, error) {
	stub.calls++
	stub.input = input
	return stub.result, stub.err
}

func TestBarsHandlerReturnsExactPageAndBoundCursor(t *testing.T) {
	result := httpBarsResult(t)
	query := &stubBarsQuery{result: result}
	handler, _ := NewBarsHandler(query)
	mux := http.NewServeMux()
	if err := handler.Register(mux); err != nil {
		t.Fatalf("Register() error = %v", err)
	}
	path := barsPath + "?instrument_code=instrument.crypto.btc-usdt&provider=bybit&interval=1h&start_time=2026-07-15T00:00:00%2B08:00&end_time=2026-07-16T00:00:00%2B08:00&order=asc&limit=1"
	response := httptest.NewRecorder()
	WithRequestID(mux).ServeHTTP(response, httptest.NewRequest(http.MethodGet, path, nil))
	if response.Code != http.StatusOK || !ValidRequestID(response.Header().Get(RequestIDHeader)) {
		t.Fatalf("response=%d headers=%v body=%s", response.Code, response.Header(), response.Body.String())
	}
	var body map[string]any
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	bars := body["bars"].([]any)
	bar := bars[0].(map[string]any)
	provider := body["provider"].(map[string]any)
	if body["interval"] != "1h" || body["order"] != "asc" || bar["open"] != "100" || bar["volume"] != "12.5" || bar["quote_volume"] == nil || bar["revision"] != float64(1) || provider["provider_instrument_code"] != "provider.bybit.btc-usdt" || body["next_cursor"] == nil {
		t.Fatalf("response body = %#v", body)
	}
	cursor := body["next_cursor"].(string)
	positions, err := DecodeCursor(cursor, barsCursorScope, 7)
	if err != nil || positions[0] != "instrument.crypto.btc-usdt" || positions[3] != "asc" || positions[4] != "2026-07-14T16:00:00Z" || positions[6] != result.NextCursorOpenTime.String() {
		t.Fatalf("cursor = (%#v, %v)", positions, err)
	}
	if query.input.StartTime == nil || query.input.StartTime.Location() != time.UTC || query.input.Order != application.BarOrderAscending || query.input.Limit != 1 {
		t.Fatalf("query input = %#v", query.input)
	}

	nextQuery := &stubBarsQuery{result: result}
	nextHandler, _ := NewBarsHandler(nextQuery)
	nextResponse := httptest.NewRecorder()
	nextHandler.ServeHTTP(nextResponse, httptest.NewRequest(http.MethodGet, path+"&cursor="+cursor, nil))
	if nextResponse.Code != http.StatusOK || nextQuery.input.CursorOpenTime == nil || !nextQuery.input.CursorOpenTime.Equal(result.NextCursorOpenTime.Time()) {
		t.Fatalf("next response=%d body=%s input=%#v", nextResponse.Code, nextResponse.Body.String(), nextQuery.input)
	}
}

func TestBarsHandlerRejectsInvalidProtocolQueries(t *testing.T) {
	valid := "?instrument_code=instrument.crypto.btc-usdt&provider=bybit&interval=1h"
	cursorInput := application.BarsInput{InstrumentCode: "instrument.crypto.btc-usdt", ProviderCode: "bybit", Interval: "1h", Order: application.BarOrderDescending, Limit: 200}
	values := append(barsCursorBindings(cursorInput), "2026-07-15T00:00:00Z")
	cursor, _ := EncodeCursor(barsCursorScope, values...)
	tests := []string{
		"",
		"?instrument_code=instrument.crypto.btc-usdt&provider=bybit",
		valid + "&unknown=value",
		valid + "&provider=coingecko",
		valid + "&start_time=not-time",
		valid + "&limit=1001",
		valid + "&cursor=bad",
		valid + "&order=asc&cursor=" + cursor,
		valid + "&bad=%zz",
	}
	for _, rawQuery := range tests {
		t.Run(rawQuery, func(t *testing.T) {
			query := &stubBarsQuery{}
			handler, _ := NewBarsHandler(query)
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, barsPath+rawQuery, nil))
			if response.Code != http.StatusBadRequest || query.calls != 0 {
				t.Fatalf("response=%d body=%s calls=%d", response.Code, response.Body.String(), query.calls)
			}
		})
	}
}

func TestBarsHandlerMapsApplicationErrors(t *testing.T) {
	tests := []struct {
		err        error
		wantStatus int
		wantCode   application.ErrorCode
	}{
		{application.NewError(application.ErrorCodeUnsupportedInterval, "unsupported", false, nil), http.StatusBadRequest, application.ErrorCodeUnsupportedInterval},
		{application.NewError(application.ErrorCodeInstrumentNotFound, "missing", false, nil), http.StatusNotFound, application.ErrorCodeInstrumentNotFound},
		{domain.ErrDatabaseUnavailable, http.StatusServiceUnavailable, application.ErrorCodeDatabaseUnavailable},
	}
	for _, test := range tests {
		query := &stubBarsQuery{err: test.err}
		handler, _ := NewBarsHandler(query)
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, barsPath+"?instrument_code=instrument.crypto.btc-usdt&provider=bybit&interval=1h", nil))
		var envelope ErrorEnvelope
		if err := json.Unmarshal(response.Body.Bytes(), &envelope); err != nil || response.Code != test.wantStatus || envelope.Error.Code != test.wantCode {
			t.Fatalf("response=%d body=%s decode=%v", response.Code, response.Body.String(), err)
		}
	}
}

func TestBarsHandlerRequiresDependenciesAndValidResponse(t *testing.T) {
	if _, err := NewBarsHandler(nil); err == nil {
		t.Fatal("NewBarsHandler(nil) error = nil")
	}
	handler, _ := NewBarsHandler(&stubBarsQuery{})
	if err := handler.Register(nil); err == nil {
		t.Fatal("Register(nil) error = nil")
	}
	var nilHandler *BarsHandler
	if err := nilHandler.Register(http.NewServeMux()); err == nil {
		t.Fatal("nil handler Register() error = nil")
	}
	if _, err := parseBarsRequest(nil); err == nil {
		t.Fatal("parseBarsRequest(nil) error = nil")
	}
	invalid := httpBarsResult(t)
	invalid.NextCursorOpenTime = &domain.UTCInstant{}
	if _, err := barsResponseFromResult(application.BarsInput{InstrumentCode: "instrument.crypto.btc-usdt", ProviderCode: "bybit", Interval: "1h", Order: application.BarOrderDescending}, invalid); err == nil {
		t.Fatal("barsResponseFromResult(invalid cursor) error = nil")
	}
}

func TestBarsHandlerReturnsEmptyBars(t *testing.T) {
	result := httpBarsResult(t)
	result.Bars = []domain.MarketBar{}
	result.NextCursorOpenTime = nil
	query := &stubBarsQuery{result: result}
	handler, _ := NewBarsHandler(query)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, barsPath+"?instrument_code=instrument.crypto.btc-usdt&provider=bybit&interval=1h", nil))
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"bars":[]`) || !strings.Contains(response.Body.String(), `"next_cursor":null`) {
		t.Fatalf("response=%d body=%s", response.Code, response.Body.String())
	}
}

func httpBarsResult(t *testing.T) application.BarsResult {
	t.Helper()
	asset := httpLatestQuoteAsset(t)
	now := time.Date(2026, time.July, 15, 0, 0, 0, 0, time.UTC)
	instrument, err := domain.NewInstrument(domain.Instrument{
		ID: domain.IDFromUUID(uuid.MustParse("019f1452-90f7-7992-a87a-ca2727891601")), Code: testHTTPCode(t, "instrument.crypto.btc-usdt"),
		AssetID: asset.ID, Venue: "BYBIT", InstrumentType: domain.InstrumentTypeSpot, Symbol: "BTC-USDT", QuoteCurrency: "USDT",
		MarketTimezone: "UTC", Status: domain.InstrumentStatusActive, CreatedAt: now, UpdatedAt: now,
	})
	if err != nil {
		t.Fatalf("NewInstrument() error = %v", err)
	}
	provider, _ := domain.NewProvider(domain.Provider{ID: domain.IDFromUUID(uuid.MustParse("019f1452-90f7-7992-a87a-ca2727891602")), Code: testHTTPCode(t, "bybit"), Name: "Bybit", ProviderType: domain.ProviderTypeExchange, Status: domain.ProviderStatusActive, CreatedAt: now, UpdatedAt: now})
	source := application.BarSourceRecord{
		InstrumentID: instrument.ID, BaseAssetCode: asset.Code, QuoteCurrency: "USDT", ProviderID: provider.ID,
		ProviderInstrumentID:   domain.IDFromUUID(uuid.MustParse("019f1452-90f7-7992-a87a-ca2727891603")),
		ProviderInstrumentCode: testHTTPCode(t, "provider.bybit.btc-usdt"), ProviderSymbol: "BTCUSDT",
		Capabilities: domain.ProviderCapabilities{Historical: true, Intervals: []domain.BarInterval{domain.BarInterval1Hour}},
	}
	openTime, _ := domain.NewUTCInstant(time.Date(2026, time.July, 15, 0, 0, 0, 0, time.UTC))
	closeTime, _ := domain.NewUTCInstant(openTime.Time().Add(time.Hour))
	volume := domain.DecimalFromExact(decimal.RequireFromString("12.50"))
	quoteVolume := domain.DecimalFromExact(decimal.RequireFromString("1250.00"))
	bar, _ := domain.NewStoredBar(domain.Bar{
		InstrumentID: instrument.ID, ProviderInstrumentID: source.ProviderInstrumentID, Interval: domain.BarInterval1Hour,
		OpenTime: openTime, CloseTime: closeTime, Revision: 1,
		OpenPrice: domain.DecimalFromExact(decimal.NewFromInt(100)), HighPrice: domain.DecimalFromExact(decimal.NewFromInt(110)),
		LowPrice: domain.DecimalFromExact(decimal.NewFromInt(90)), ClosePrice: domain.DecimalFromExact(decimal.NewFromInt(105)),
		BaseVolume: &volume, QuoteVolume: &quoteVolume, IsClosed: true, IsCurrent: true,
		QualityStatus: domain.QualityStatusValid, CollectedAt: closeTime,
	})
	position := openTime
	return application.BarsResult{Instrument: instrument, Provider: provider, Source: source, Interval: domain.BarInterval1Hour, Order: application.BarOrderAscending, Bars: []domain.MarketBar{bar}, NextCursorOpenTime: &position}
}
