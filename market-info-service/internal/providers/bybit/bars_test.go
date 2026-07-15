package bybit

import (
	"context"
	"fmt"
	"net/http"
	"strconv"
	"sync/atomic"
	"testing"
	"time"

	"xr-trading/market-info-service/internal/domain"
	"xr-trading/market-info-service/internal/ingestion/ports"
)

func TestFetchBarsMapsAscendingPagesAndOpaqueCursor(t *testing.T) {
	t.Parallel()

	reference := bybitReference(t, "BTCUSDT")
	request := bybitBarsRequest(t, reference)
	var calls atomic.Int32
	adapter, _ := newHTTPTestAdapter(t, func(writer http.ResponseWriter, httpRequest *http.Request) {
		call := calls.Add(1)
		query := httpRequest.URL.Query()
		if httpRequest.URL.Path != klinePath || query.Get("category") != spotMarket || query.Get("symbol") != reference.ExternalSymbol ||
			query.Get("interval") != "60" || query.Get("start") != "1784073600000" || query.Get("limit") != "2" {
			t.Errorf("bar request = %s?%s", httpRequest.URL.Path, httpRequest.URL.RawQuery)
		}
		if call == 1 {
			if query.Get("end") != "1784087999999" {
				t.Errorf("first page end = %s", query.Get("end"))
			}
			_, _ = writer.Write(fixture(t, "kline_spot_page_1.json"))
			return
		}
		if query.Get("end") != "1784077199999" {
			t.Errorf("second page end = %s", query.Get("end"))
		}
		_, _ = writer.Write(fixture(t, "kline_spot_page_2.json"))
	})

	first, err := adapter.FetchBars(context.Background(), request)
	if err != nil {
		t.Fatalf("FetchBars(first) error = %v", err)
	}
	if len(first.Bars) != 2 || !first.HasMore || first.NextCursor != cursorPrefix+"1784077200000" {
		t.Fatalf("first page = %#v", first)
	}
	if first.Bars[0].OpenTime.Time().UnixMilli() != 1784077200000 || first.Bars[1].OpenTime.Time().UnixMilli() != 1784080800000 {
		t.Fatalf("first page order = %#v", first.Bars)
	}
	bar := first.Bars[0]
	if bar.Open.String() != "101" || bar.High.String() != "111" || bar.Low.String() != "91" || bar.Close.String() != "106" ||
		bar.BaseVolume.String() != "11" || bar.QuoteVolume.String() != "1166" || !bar.IsClosed || len(bar.RawPayload) == 0 {
		t.Fatalf("mapped bar = %#v", bar)
	}

	request.Cursor = first.NextCursor
	second, err := adapter.FetchBars(context.Background(), request)
	if err != nil || len(second.Bars) != 1 || second.HasMore || second.NextCursor != "" {
		t.Fatalf("FetchBars(second) = (%#v, %v)", second, err)
	}
	if second.Bars[0].OpenTime.Time().UnixMilli() != 1784073600000 || calls.Load() != 2 {
		t.Fatalf("second page/calls = (%#v, %d)", second.Bars, calls.Load())
	}
}

func TestFetchBarsMapsDailyIntervalAndOpenBar(t *testing.T) {
	t.Parallel()

	reference := bybitReference(t, "BTCUSDT")
	request := bybitBarsRequest(t, reference)
	request.Interval = domain.BarInterval1Day
	request.StartTime = mustBybitInstant(t, time.Date(2026, 7, 15, 0, 0, 0, 0, time.UTC))
	request.EndTime = mustBybitInstant(t, time.Date(2026, 7, 16, 0, 0, 0, 0, time.UTC))
	request.Limit = 1
	adapter, _ := newHTTPTestAdapter(t, func(writer http.ResponseWriter, httpRequest *http.Request) {
		if httpRequest.URL.Query().Get("interval") != "D" {
			t.Errorf("interval = %s", httpRequest.URL.Query().Get("interval"))
		}
		_, _ = writer.Write([]byte(`{"retCode":0,"retMsg":"OK","result":{"category":"spot","symbol":"BTCUSDT","list":[["1784073600000","100","110","90","105","10","1050"]]},"time":1784086200000}`))
	})
	result, err := adapter.FetchBars(context.Background(), request)
	if err != nil || len(result.Bars) != 1 {
		t.Fatalf("FetchBars() = (%#v, %v)", result, err)
	}
	if result.Bars[0].IsClosed || result.Bars[0].CloseTime.Time() != time.Date(2026, 7, 16, 0, 0, 0, 0, time.UTC) {
		t.Fatalf("daily bar = %#v", result.Bars[0])
	}
}

func TestFetchBarsRejectsInvalidInputBeforeHTTP(t *testing.T) {
	t.Parallel()

	reference := bybitReference(t, "BTCUSDT")
	base := bybitBarsRequest(t, reference)
	var calls atomic.Int32
	adapter, _ := newHTTPTestAdapter(t, func(http.ResponseWriter, *http.Request) { calls.Add(1) })

	tests := []struct {
		name   string
		mutate func(*ports.FetchBarsRequest)
		code   ports.ProviderErrorCode
	}{
		{name: "invalid request", mutate: func(value *ports.FetchBarsRequest) { value.Limit = 0 }, code: ports.ProviderErrorBadRequest},
		{name: "wrong market", mutate: func(value *ports.FetchBarsRequest) { value.Instrument.ProviderMarket = "linear" }, code: ports.ProviderErrorInvalidInstrument},
		{name: "unsupported interval", mutate: func(value *ports.FetchBarsRequest) { value.Interval = "5m" }, code: ports.ProviderErrorBadRequest},
		{name: "adapter limit", mutate: func(value *ports.FetchBarsRequest) { value.Limit = maxBarsPerRequest + 1 }, code: ports.ProviderErrorBadRequest},
		{name: "cursor version", mutate: func(value *ports.FetchBarsRequest) { value.Cursor = "v2:123" }, code: ports.ProviderErrorBadRequest},
		{name: "cursor range", mutate: func(value *ports.FetchBarsRequest) {
			value.Cursor = cursorPrefix + strconv.FormatInt(value.StartTime.Time().UnixMilli(), 10)
		}, code: ports.ProviderErrorBadRequest},
		{name: "cursor equals end", mutate: func(value *ports.FetchBarsRequest) {
			value.Cursor = cursorPrefix + strconv.FormatInt(value.EndTime.Time().UnixMilli(), 10)
		}, code: ports.ProviderErrorBadRequest},
		{name: "pre epoch", mutate: func(value *ports.FetchBarsRequest) {
			value.StartTime = mustBybitInstant(t, time.Date(1969, 1, 1, 0, 0, 0, 0, time.UTC))
			value.EndTime = mustBybitInstant(t, time.Date(1969, 1, 2, 0, 0, 0, 0, time.UTC))
		}, code: ports.ProviderErrorBadRequest},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			request := base
			test.mutate(&request)
			_, err := adapter.FetchBars(context.Background(), request)
			classified, ok := ports.AsProviderError(err)
			if !ok || classified.Code != test.code {
				t.Fatalf("error = %#v, want %s", err, test.code)
			}
		})
	}
	if calls.Load() != 0 {
		t.Fatalf("HTTP calls = %d", calls.Load())
	}
}

func TestFetchBarsRejectsMalformedProviderData(t *testing.T) {
	t.Parallel()

	request := bybitBarsRequest(t, bybitReference(t, "BTCUSDT"))
	tests := []struct {
		name string
		body string
	}{
		{name: "source", body: `{"retCode":0,"retMsg":"OK","result":{"category":"linear","symbol":"BTCUSDT","list":[]},"time":1784086200000}`},
		{name: "tuple shape", body: `{"retCode":0,"retMsg":"OK","result":{"category":"spot","symbol":"BTCUSDT","list":[["1784073600000","1"]]},"time":1784086200000}`},
		{name: "timestamp", body: `{"retCode":0,"retMsg":"OK","result":{"category":"spot","symbol":"BTCUSDT","list":[["bad","100","110","90","105","10","1050"]]},"time":1784086200000}`},
		{name: "decimal", body: `{"retCode":0,"retMsg":"OK","result":{"category":"spot","symbol":"BTCUSDT","list":[["1784073600000","bad","110","90","105","10","1050"]]},"time":1784086200000}`},
		{name: "ohlc", body: `{"retCode":0,"retMsg":"OK","result":{"category":"spot","symbol":"BTCUSDT","list":[["1784073600000","100","90","110","105","10","1050"]]},"time":1784086200000}`},
		{name: "duplicate", body: `{"retCode":0,"retMsg":"OK","result":{"category":"spot","symbol":"BTCUSDT","list":[["1784073600000","100","110","90","105","10","1050"],["1784073600000","100","110","90","105","10","1050"]]},"time":1784086200000}`},
		{name: "outside range", body: fmt.Sprintf(`{"retCode":0,"retMsg":"OK","result":{"category":"spot","symbol":"BTCUSDT","list":[["%d","100","110","90","105","10","1050"]]},"time":1784086200000}`, request.EndTime.Time().UnixMilli())},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			adapter, _ := newHTTPTestAdapter(t, func(writer http.ResponseWriter, _ *http.Request) { _, _ = writer.Write([]byte(test.body)) })
			_, err := adapter.FetchBars(context.Background(), request)
			classified, ok := ports.AsProviderError(err)
			if !ok || classified.Code != ports.ProviderErrorInvalidResponse {
				t.Fatalf("error = %#v", err)
			}
		})
	}
}

func TestEffectiveEndMilliseconds(t *testing.T) {
	t.Parallel()

	request := bybitBarsRequest(t, bybitReference(t, "BTCUSDT"))
	if end, err := effectiveEndMilliseconds(request); err != nil || end != request.EndTime.Time().UnixMilli() {
		t.Fatalf("effectiveEndMilliseconds(first) = (%d, %v)", end, err)
	}
	request.Cursor = cursorPrefix + "1784077200000"
	if end, err := effectiveEndMilliseconds(request); err != nil || end != 1784077200000 {
		t.Fatalf("effectiveEndMilliseconds(next) = (%d, %v)", end, err)
	}
	request.Cursor = cursorPrefix + "not-number"
	if _, err := effectiveEndMilliseconds(request); err == nil {
		t.Fatal("effectiveEndMilliseconds(invalid) error = nil")
	}
}

func TestInstantFromMilliseconds(t *testing.T) {
	t.Parallel()

	instant, err := instantFromMilliseconds(1784073600000)
	if err != nil || instant.Time().UnixMilli() != 1784073600000 {
		t.Fatalf("instantFromMilliseconds() = (%s, %v)", instant, err)
	}
	if _, err := instantFromMilliseconds(0); err == nil {
		t.Fatal("instantFromMilliseconds(0) error = nil")
	}
}

func TestMapKlineRejectsBadRawJSON(t *testing.T) {
	t.Parallel()

	reference := bybitReference(t, "BTCUSDT")
	now := mustBybitInstant(t, fixedBybitNow)
	if _, _, err := mapKline(reference, domain.BarInterval1Hour, time.Hour, now, now, []byte(`{`)); err == nil {
		t.Fatal("mapKline() error = nil")
	}
}
