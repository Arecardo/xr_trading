package longbridge

import (
	"context"
	"strconv"
	"testing"
	"time"

	lbquote "github.com/longbridge/openapi-go/quote"
	"github.com/shopspring/decimal"

	"xr-trading/market-info-service/internal/domain"
	"xr-trading/market-info-service/internal/ingestion/ports"
)

func TestFetchBarsMapsAscendingPagesAndRegularSessionClose(t *testing.T) {
	t.Parallel()
	reference := longbridgeReference(t, "AAPL.US", domain.AssetTypeStock, domain.InstrumentTypeEquity)
	request := longbridgeBarsRequest(t, reference)
	pageOne := loadBarsFixture(t, "bars_us_page_1.json")
	pageTwo := loadBarsFixture(t, "bars_us_page_2.json")
	calls := 0
	client := &fakeClient{barsFn: func(_ context.Context, symbol string, period lbquote.Period, adjust lbquote.AdjustType, forward bool, offset *time.Time, count int32, options ...lbquote.CandlestickRequestOption) ([]*lbquote.Candlestick, error) {
		calls++
		if symbol != "AAPL.US" || period != lbquote.PeriodSixtyMinute || adjust != lbquote.AdjustTypeNo || forward || count != 2 || len(options) != 1 {
			t.Errorf("history request = (%s, %d, %d, %t, %d, %d)", symbol, period, adjust, forward, count, len(options))
		}
		if offset == nil || offset.Location().String() != "America/New_York" {
			t.Errorf("history offset = %#v", offset)
		}
		if calls == 1 {
			if offset.Hour() != 15 || offset.Minute() != 59 {
				t.Errorf("first offset = %s", offset)
			}
			return pageOne, nil
		}
		if offset.Hour() != 14 || offset.Minute() != 29 {
			t.Errorf("second offset = %s", offset)
		}
		return pageTwo, nil
	}}
	adapter := newTestAdapter(t, client)
	first, err := adapter.FetchBars(context.Background(), request)
	if err != nil || len(first.Bars) != 2 || !first.HasMore {
		t.Fatalf("FetchBars(first) = (%#v, %v)", first, err)
	}
	wantCursor := cursorPrefix + strconv.FormatInt(time.Date(2026, 7, 15, 18, 30, 0, 0, time.UTC).Unix(), 10)
	if first.NextCursor != wantCursor || !first.Bars[0].OpenTime.Time().Before(first.Bars[1].OpenTime.Time()) {
		t.Fatalf("first page cursor/order = %#v", first)
	}
	last := first.Bars[1]
	if last.CloseTime.Time() != time.Date(2026, 7, 15, 20, 0, 0, 0, time.UTC) || !last.IsClosed ||
		last.Open.String() != "190" || last.Close.String() != "192" || last.BaseVolume.String() != "1300" || last.QuoteVolume.String() != "249600" {
		t.Fatalf("mapped final bar = %#v", last)
	}
	request.Cursor = first.NextCursor
	second, err := adapter.FetchBars(context.Background(), request)
	if err != nil || len(second.Bars) != 1 || second.HasMore || second.NextCursor != "" || calls != 2 {
		t.Fatalf("FetchBars(second) = (%#v, %v), calls=%d", second, err, calls)
	}
}

func TestFetchBarsMapsDailyCloseAcrossDST(t *testing.T) {
	t.Parallel()
	reference := longbridgeReference(t, "SPY.US", domain.AssetTypeETF, domain.InstrumentTypeETF)
	request := ports.FetchBarsRequest{
		Instrument: reference, Interval: domain.BarInterval1Day,
		StartTime: mustLongbridgeInstant(t, time.Date(2026, 12, 1, 0, 0, 0, 0, time.UTC)),
		EndTime:   mustLongbridgeInstant(t, time.Date(2026, 12, 2, 0, 0, 0, 0, time.UTC)), Limit: 1,
	}
	value := decimal.RequireFromString("620")
	client := &fakeClient{barsFn: func(_ context.Context, _ string, period lbquote.Period, _ lbquote.AdjustType, _ bool, offset *time.Time, _ int32, _ ...lbquote.CandlestickRequestOption) ([]*lbquote.Candlestick, error) {
		if period != lbquote.PeriodDay || offset.Location() != time.UTC || offset.Day() != 1 {
			t.Errorf("daily request = (%d, %s)", period, offset)
		}
		return []*lbquote.Candlestick{{Open: &value, High: &value, Low: &value, Close: &value, Timestamp: request.StartTime.Time().Unix()}}, nil
	}}
	adapter := newTestAdapter(t, client)
	result, err := adapter.FetchBars(context.Background(), request)
	if err != nil || len(result.Bars) != 1 {
		t.Fatalf("FetchBars() = (%#v, %v)", result, err)
	}
	bar := result.Bars[0]
	if bar.CloseTime.Time() != time.Date(2026, 12, 1, 21, 0, 0, 0, time.UTC) || bar.IsClosed {
		t.Fatalf("daily bar = %#v", bar)
	}
}

func TestFetchBarsFiltersProviderBoundaryRows(t *testing.T) {
	t.Parallel()
	reference := longbridgeReference(t, "AAPL.US", domain.AssetTypeStock, domain.InstrumentTypeEquity)
	request := longbridgeBarsRequest(t, reference)
	value := decimal.RequireFromString("100")
	rows := []*lbquote.Candlestick{
		{Open: &value, High: &value, Low: &value, Close: &value, Timestamp: request.StartTime.Time().Add(-time.Hour).Unix()},
		{Open: &value, High: &value, Low: &value, Close: &value, Timestamp: request.StartTime.Time().Unix()},
	}
	adapter := newTestAdapter(t, &fakeClient{barsFn: func(context.Context, string, lbquote.Period, lbquote.AdjustType, bool, *time.Time, int32, ...lbquote.CandlestickRequestOption) ([]*lbquote.Candlestick, error) {
		return rows, nil
	}})
	result, err := adapter.FetchBars(context.Background(), request)
	if err != nil || len(result.Bars) != 1 || result.HasMore {
		t.Fatalf("FetchBars() = (%#v, %v)", result, err)
	}
}

func TestFetchBarsRejectsInvalidInputBeforeClient(t *testing.T) {
	t.Parallel()
	reference := longbridgeReference(t, "AAPL.US", domain.AssetTypeStock, domain.InstrumentTypeEquity)
	base := longbridgeBarsRequest(t, reference)
	calls := 0
	adapter := newTestAdapter(t, &fakeClient{barsFn: func(context.Context, string, lbquote.Period, lbquote.AdjustType, bool, *time.Time, int32, ...lbquote.CandlestickRequestOption) ([]*lbquote.Candlestick, error) {
		calls++
		return nil, nil
	}})
	tests := []struct {
		name   string
		mutate func(*ports.FetchBarsRequest)
		code   ports.ProviderErrorCode
	}{
		{name: "invalid request", mutate: func(value *ports.FetchBarsRequest) { value.Limit = 0 }, code: ports.ProviderErrorBadRequest},
		{name: "wrong provider", mutate: func(value *ports.FetchBarsRequest) { value.Instrument.ProviderCode = mustLongbridgeCode(t, "other") }, code: ports.ProviderErrorInvalidInstrument},
		{name: "unsupported interval", mutate: func(value *ports.FetchBarsRequest) { value.Interval = "5m" }, code: ports.ProviderErrorBadRequest},
		{name: "limit", mutate: func(value *ports.FetchBarsRequest) { value.Limit = maxBarsPerRequest + 1 }, code: ports.ProviderErrorBadRequest},
		{name: "cursor version", mutate: func(value *ports.FetchBarsRequest) { value.Cursor = "v2:end:1" }, code: ports.ProviderErrorBadRequest},
		{name: "cursor start", mutate: func(value *ports.FetchBarsRequest) {
			value.Cursor = cursorPrefix + strconv.FormatInt(value.StartTime.Time().Unix(), 10)
		}, code: ports.ProviderErrorBadRequest},
		{name: "cursor end", mutate: func(value *ports.FetchBarsRequest) {
			value.Cursor = cursorPrefix + strconv.FormatInt(value.EndTime.Time().Unix(), 10)
		}, code: ports.ProviderErrorBadRequest},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			request := base
			test.mutate(&request)
			_, err := adapter.FetchBars(context.Background(), request)
			if codeOf(err) != test.code {
				t.Fatalf("error = %#v, want %s", err, test.code)
			}
		})
	}
	if calls != 0 {
		t.Fatalf("client calls = %d", calls)
	}
}

func TestFetchBarsRejectsMalformedProviderData(t *testing.T) {
	t.Parallel()
	request := longbridgeBarsRequest(t, longbridgeReference(t, "AAPL.US", domain.AssetTypeStock, domain.InstrumentTypeEquity))
	valid := loadBarsFixture(t, "bars_us_page_2.json")[0]
	badPrice := *valid
	badPrice.Open = nil
	negativeVolume := *valid
	negativeVolume.Volume = -1
	invalidTime := *valid
	invalidTime.Timestamp = 0
	tests := []struct {
		name   string
		values []*lbquote.Candlestick
	}{
		{name: "nil", values: []*lbquote.Candlestick{nil}},
		{name: "over limit", values: []*lbquote.Candlestick{valid, valid, valid}},
		{name: "duplicate", values: []*lbquote.Candlestick{valid, valid}},
		{name: "price", values: []*lbquote.Candlestick{&badPrice}},
		{name: "volume", values: []*lbquote.Candlestick{&negativeVolume}},
		{name: "timestamp", values: []*lbquote.Candlestick{&invalidTime}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			adapter := newTestAdapter(t, &fakeClient{barsFn: func(context.Context, string, lbquote.Period, lbquote.AdjustType, bool, *time.Time, int32, ...lbquote.CandlestickRequestOption) ([]*lbquote.Candlestick, error) {
				return test.values, nil
			}})
			_, err := adapter.FetchBars(context.Background(), request)
			if codeOf(err) != ports.ProviderErrorInvalidResponse {
				t.Fatalf("error = %#v", err)
			}
		})
	}
}

func TestMapCandlestickRejectsAfterSessionBar(t *testing.T) {
	t.Parallel()
	reference := longbridgeReference(t, "AAPL.US", domain.AssetTypeStock, domain.InstrumentTypeEquity)
	stick := loadBarsFixture(t, "bars_us_page_2.json")[0]
	stick.Timestamp = time.Date(2026, 7, 15, 21, 0, 0, 0, time.UTC).Unix()
	adapter := newTestAdapter(t, &fakeClient{})
	if _, err := adapter.mapCandlestick(reference, domain.BarInterval1Hour, stick, mustLongbridgeInstant(t, fixedLongbridgeNow)); err == nil {
		t.Fatal("mapCandlestick(after session) error = nil")
	}
}

func TestEffectiveEnd(t *testing.T) {
	t.Parallel()
	request := longbridgeBarsRequest(t, longbridgeReference(t, "AAPL.US", domain.AssetTypeStock, domain.InstrumentTypeEquity))
	if end, err := effectiveEnd(request); err != nil || end != request.EndTime.Time() {
		t.Fatalf("effectiveEnd(first) = (%s, %v)", end, err)
	}
	request.Cursor = cursorPrefix + strconv.FormatInt(time.Date(2026, 7, 15, 18, 30, 0, 0, time.UTC).Unix(), 10)
	if end, err := effectiveEnd(request); err != nil || end.Unix() == 0 {
		t.Fatalf("effectiveEnd(next) = (%s, %v)", end, err)
	}
	request.Cursor = cursorPrefix + "bad"
	if _, err := effectiveEnd(request); err == nil {
		t.Fatal("effectiveEnd(bad) error = nil")
	}
}
