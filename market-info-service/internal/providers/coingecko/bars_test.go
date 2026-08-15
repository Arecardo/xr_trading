package coingecko

import (
	"context"
	"net/http"
	"strconv"
	"testing"
	"time"

	"xr-trading/market-info-service/internal/domain"
	"xr-trading/market-info-service/internal/ingestion/ports"
)

func TestFetchBarsCollapsesDailyPricesWithLastPointPerDayWinning(t *testing.T) {
	t.Parallel()

	reference := usdtUSDReference(t)
	adapter, _ := newHTTPTestAdapter(t, func(writer http.ResponseWriter, request *http.Request) {
		wantPath := "/api/v3/coins/tether/market_chart/range"
		if request.Method != http.MethodGet || request.URL.Path != wantPath {
			t.Errorf("request = %s %s", request.Method, request.URL.Path)
		}
		if request.URL.Query().Get("vs_currency") != "usd" {
			t.Errorf("vs_currency = %s", request.URL.Query().Get("vs_currency"))
		}
		writer.Header().Set("Content-Type", "application/json")
		_, _ = writer.Write(fixture(t, "market_chart_range.json"))
	})

	request := coinGeckoBarsRequest(t, reference, time.Date(2026, 7, 10, 0, 0, 0, 0, time.UTC), time.Date(2026, 7, 14, 0, 0, 0, 0, time.UTC), 10)
	result, err := adapter.FetchBars(context.Background(), request)
	if err != nil {
		t.Fatalf("FetchBars() error = %v", err)
	}
	if result.HasMore || result.NextCursor != "" {
		t.Fatalf("result = %#v", result)
	}
	if len(result.Bars) != 4 {
		t.Fatalf("bars = %d, want 4", len(result.Bars))
	}
	wantOpens := []string{"2026-07-10T00:00:00Z", "2026-07-11T00:00:00Z", "2026-07-12T00:00:00Z", "2026-07-13T00:00:00Z"}
	wantPrices := []string{"0.999", "0.9993", "0.9994", "0.9995"}
	for index, bar := range result.Bars {
		if bar.OpenTime.Time().Format(time.RFC3339) != wantOpens[index] {
			t.Fatalf("bar[%d].OpenTime = %s, want %s", index, bar.OpenTime, wantOpens[index])
		}
		if !bar.CloseTime.Time().Equal(bar.OpenTime.Time().Add(24 * time.Hour)) {
			t.Fatalf("bar[%d].CloseTime = %s", index, bar.CloseTime)
		}
		// The 2026-07-11 day has two raw points (00:10 and 12:00 UTC); the
		// later one (0.9993) must win, proving day-collapse keeps the LAST
		// observed price rather than the first.
		if bar.Open.String() != wantPrices[index] || bar.High.String() != wantPrices[index] ||
			bar.Low.String() != wantPrices[index] || bar.Close.String() != wantPrices[index] {
			t.Fatalf("bar[%d] OHLC = (%s,%s,%s,%s), want all %s", index, bar.Open, bar.High, bar.Low, bar.Close, wantPrices[index])
		}
		if bar.BaseVolume == nil || bar.BaseVolume.String() != "0" || bar.QuoteVolume == nil || bar.QuoteVolume.String() != "0" {
			t.Fatalf("bar[%d] volume = (%v, %v), want explicit zero", index, bar.BaseVolume, bar.QuoteVolume)
		}
		if !bar.IsClosed {
			t.Fatalf("bar[%d].IsClosed = false, want true (fully in the past)", index)
		}
		if len(bar.RawPayload) == 0 {
			t.Fatalf("bar[%d].RawPayload is empty", index)
		}
	}
}

func TestFetchBarsPaginatesByLimitAndAdvancesCursor(t *testing.T) {
	t.Parallel()

	reference := usdtUSDReference(t)
	adapter, _ := newHTTPTestAdapter(t, func(writer http.ResponseWriter, _ *http.Request) {
		_, _ = writer.Write(fixture(t, "market_chart_range.json"))
	})

	request := coinGeckoBarsRequest(t, reference, time.Date(2026, 7, 10, 0, 0, 0, 0, time.UTC), time.Date(2026, 7, 14, 0, 0, 0, 0, time.UTC), 2)
	first, err := adapter.FetchBars(context.Background(), request)
	if err != nil {
		t.Fatalf("FetchBars(page 1) error = %v", err)
	}
	if len(first.Bars) != 2 || !first.HasMore || first.NextCursor == "" {
		t.Fatalf("page 1 = %#v", first)
	}
	if first.Bars[0].OpenTime.Time().Format(time.RFC3339) != "2026-07-10T00:00:00Z" || first.Bars[1].OpenTime.Time().Format(time.RFC3339) != "2026-07-11T00:00:00Z" {
		t.Fatalf("page 1 opens = %v", first.Bars)
	}

	request.Cursor = first.NextCursor
	second, err := adapter.FetchBars(context.Background(), request)
	if err != nil {
		t.Fatalf("FetchBars(page 2) error = %v", err)
	}
	if len(second.Bars) != 2 || second.HasMore {
		t.Fatalf("page 2 = %#v", second)
	}
	if second.Bars[0].OpenTime.Time().Format(time.RFC3339) != "2026-07-12T00:00:00Z" || second.Bars[1].OpenTime.Time().Format(time.RFC3339) != "2026-07-13T00:00:00Z" {
		t.Fatalf("page 2 opens = %v", second.Bars)
	}
}

func TestFetchBarsRejectsUnsupportedIntervalAndInvalidCursor(t *testing.T) {
	t.Parallel()

	reference := usdtUSDReference(t)
	calls := 0
	adapter, _ := newHTTPTestAdapter(t, func(http.ResponseWriter, *http.Request) { calls++ })

	hourly := coinGeckoBarsRequest(t, reference, time.Date(2026, 7, 10, 0, 0, 0, 0, time.UTC), time.Date(2026, 7, 11, 0, 0, 0, 0, time.UTC), 10)
	hourly.Interval = domain.BarInterval1Hour
	if _, err := adapter.FetchBars(context.Background(), hourly); !isCode(t, err, ports.ProviderErrorUnsupportedInterval) {
		t.Fatalf("FetchBars(1h) error = %v", err)
	}

	badCursor := coinGeckoBarsRequest(t, reference, time.Date(2026, 7, 10, 0, 0, 0, 0, time.UTC), time.Date(2026, 7, 14, 0, 0, 0, 0, time.UTC), 10)
	badCursor.Cursor = "not-a-cursor"
	if _, err := adapter.FetchBars(context.Background(), badCursor); !isCode(t, err, ports.ProviderErrorBadRequest) {
		t.Fatalf("FetchBars(bad cursor) error = %v", err)
	}

	tooLarge := coinGeckoBarsRequest(t, reference, time.Date(2026, 7, 10, 0, 0, 0, 0, time.UTC), time.Date(2026, 7, 11, 0, 0, 0, 0, time.UTC), maxBarsPerRequest+1)
	if _, err := adapter.FetchBars(context.Background(), tooLarge); !isCode(t, err, ports.ProviderErrorBadRequest) {
		t.Fatalf("FetchBars(limit too large) error = %v", err)
	}

	wrongMarket := reference
	wrongMarket.ProviderMarket = "spot"
	invalidInstrument := coinGeckoBarsRequest(t, wrongMarket, time.Date(2026, 7, 10, 0, 0, 0, 0, time.UTC), time.Date(2026, 7, 11, 0, 0, 0, 0, time.UTC), 10)
	if _, err := adapter.FetchBars(context.Background(), invalidInstrument); !isCode(t, err, ports.ProviderErrorInvalidInstrument) {
		t.Fatalf("FetchBars(wrong market) error = %v", err)
	}

	if calls != 0 {
		t.Fatalf("HTTP calls = %d, want 0", calls)
	}
	if _, err := adapter.FetchBars(nil, coinGeckoBarsRequest(t, reference, time.Date(2026, 7, 10, 0, 0, 0, 0, time.UTC), time.Date(2026, 7, 11, 0, 0, 0, 0, time.UTC), 1)); err == nil {
		t.Fatal("FetchBars(nil context) error = nil")
	}
}

func TestFetchBarsRejectsMalformedProviderData(t *testing.T) {
	t.Parallel()

	reference := usdtUSDReference(t)
	tests := []struct {
		name string
		body string
	}{
		{name: "not an object", body: `[]`},
		{name: "invalid timestamp", body: `{"prices":[["soon",0.99]]}`},
		{name: "invalid price", body: `{"prices":[[1784080800000,"nope"]]}`},
		{name: "zero timestamp", body: `{"prices":[[0,0.99]]}`},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			adapter, _ := newHTTPTestAdapter(t, func(writer http.ResponseWriter, _ *http.Request) { _, _ = writer.Write([]byte(test.body)) })
			request := coinGeckoBarsRequest(t, reference, time.Date(2026, 7, 10, 0, 0, 0, 0, time.UTC), time.Date(2026, 7, 14, 0, 0, 0, 0, time.UTC), 10)
			_, err := adapter.FetchBars(context.Background(), request)
			if !isCode(t, err, ports.ProviderErrorInvalidResponse) {
				t.Fatalf("error = %v", err)
			}
		})
	}
}

func TestFetchBarsMarksOpenTodayBarAsNotClosed(t *testing.T) {
	t.Parallel()

	reference := usdtUSDReference(t)
	// fixedCoinGeckoNow is 2026-07-15T03:30:00Z; a bar opening 2026-07-15
	// closes at 2026-07-16T00:00:00Z, which is still in the future relative
	// to the collection clock, so it must be reported as not-yet-closed.
	todayMilliseconds := time.Date(2026, 7, 15, 1, 0, 0, 0, time.UTC).UnixMilli()
	body := []byte(`{"prices":[[` + strconv.FormatInt(todayMilliseconds, 10) + `,0.9997]]}`)
	adapter, _ := newHTTPTestAdapter(t, func(writer http.ResponseWriter, _ *http.Request) { _, _ = writer.Write(body) })

	request := coinGeckoBarsRequest(t, reference, time.Date(2026, 7, 15, 0, 0, 0, 0, time.UTC), time.Date(2026, 7, 16, 0, 0, 0, 0, time.UTC), 10)
	result, err := adapter.FetchBars(context.Background(), request)
	if err != nil || len(result.Bars) != 1 {
		t.Fatalf("FetchBars() = (%#v, %v)", result, err)
	}
	if result.Bars[0].IsClosed {
		t.Fatal("bar for the current in-progress day must not be reported as closed")
	}
}

func TestParseBarsCursor(t *testing.T) {
	t.Parallel()

	value := time.Date(2026, 7, 12, 0, 0, 0, 0, time.UTC)
	cursor := barsCursorPrefix + strconv.FormatInt(value.UnixMilli(), 10)
	parsed, err := parseBarsCursor(cursor)
	if err != nil || !parsed.Equal(value) {
		t.Fatalf("parseBarsCursor() = (%s, %v)", parsed, err)
	}
	if _, err := parseBarsCursor("v2:wrong-prefix:1"); err == nil {
		t.Fatal("parseBarsCursor(wrong prefix) error = nil")
	}
	if _, err := parseBarsCursor(barsCursorPrefix + "not-a-number"); err == nil {
		t.Fatal("parseBarsCursor(non-numeric) error = nil")
	}
}

func TestCivilDayHelpers(t *testing.T) {
	t.Parallel()

	early := civilDayOf(time.Date(2026, 7, 10, 23, 59, 0, 0, time.UTC))
	late := civilDayOf(time.Date(2026, 7, 11, 0, 0, 1, 0, time.UTC))
	if !early.before(late) || late.before(early) {
		t.Fatalf("civilDay ordering broken: early=%v late=%v", early, late)
	}
	if !early.midnightUTC().Equal(time.Date(2026, 7, 10, 0, 0, 0, 0, time.UTC)) {
		t.Fatalf("midnightUTC() = %s", early.midnightUTC())
	}
}
