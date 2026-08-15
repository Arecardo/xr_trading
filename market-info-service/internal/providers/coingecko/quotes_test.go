package coingecko

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"xr-trading/market-info-service/internal/ingestion/ports"
)

func TestFetchLatestQuoteMapsSimplePrice(t *testing.T) {
	t.Parallel()

	reference := usdtUSDReference(t)
	adapter, _ := newHTTPTestAdapter(t, func(writer http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodGet || request.URL.Path != simplePricePath {
			t.Errorf("request = %s %s", request.Method, request.URL.Path)
		}
		query := request.URL.Query()
		if query.Get("ids") != "tether" || query.Get("vs_currencies") != "usd" || query.Get("include_last_updated_at") != "true" {
			t.Errorf("query = %s", request.URL.RawQuery)
		}
		if request.Header.Get("Accept") != "application/json" || request.Header.Get("User-Agent") != "adapter-test/1" {
			t.Errorf("headers = %#v", request.Header)
		}
		writer.Header().Set("Content-Type", "application/json")
		_, _ = writer.Write(fixture(t, "simple_price_tether_usd.json"))
	})

	quotes, err := adapter.FetchLatestQuotes(context.Background(), []ports.ProviderInstrumentRef{reference})
	if err != nil {
		t.Fatalf("FetchLatestQuotes() error = %v", err)
	}
	if len(quotes) != 1 {
		t.Fatalf("quotes = %#v", quotes)
	}
	quote := quotes[0]
	if quote.LastPrice.String() != "0.9998" {
		t.Fatalf("LastPrice = %s", quote.LastPrice)
	}
	if !quote.MarketTime.Time().Equal(time.Unix(1784080800, 0).UTC()) {
		t.Fatalf("MarketTime = %s", quote.MarketTime)
	}
	if !quote.ReceivedAt.Time().Equal(fixedCoinGeckoNow) || len(quote.RawPayload) == 0 {
		t.Fatalf("ReceivedAt/RawPayload = (%s, %s)", quote.ReceivedAt, quote.RawPayload)
	}
	if quote.BidPrice != nil || quote.AskPrice != nil || quote.BaseVolume24H != nil {
		t.Fatalf("unexpected optional fields populated: %#v", quote)
	}
}

func TestFetchLatestQuotesAllowsMissingSnapshot(t *testing.T) {
	t.Parallel()

	reference := usdtUSDReference(t)
	adapter, _ := newHTTPTestAdapter(t, func(writer http.ResponseWriter, _ *http.Request) {
		_, _ = writer.Write(fixture(t, "simple_price_empty.json"))
	})
	quotes, err := adapter.FetchLatestQuotes(context.Background(), []ports.ProviderInstrumentRef{reference})
	if err != nil || len(quotes) != 0 {
		t.Fatalf("FetchLatestQuotes() = (%#v, %v)", quotes, err)
	}
}

func TestFetchLatestQuotesRejectsIncompleteKnownCoin(t *testing.T) {
	t.Parallel()

	reference := usdtUSDReference(t)
	adapter, _ := newHTTPTestAdapter(t, func(writer http.ResponseWriter, _ *http.Request) {
		_, _ = writer.Write(fixture(t, "simple_price_missing_currency.json"))
	})
	_, err := adapter.FetchLatestQuotes(context.Background(), []ports.ProviderInstrumentRef{reference})
	if !isCode(t, err, ports.ProviderErrorInvalidResponse) {
		t.Fatalf("FetchLatestQuotes() error = %v", err)
	}
}

func TestFetchLatestQuotesRejectsInvalidInputBeforeHTTP(t *testing.T) {
	t.Parallel()

	reference := usdtUSDReference(t)
	calls := 0
	adapter, _ := newHTTPTestAdapter(t, func(http.ResponseWriter, *http.Request) { calls++ })

	otherProvider := reference
	otherProvider.ProviderCode = mustCoinGeckoCode(t, "other")
	uppercaseID := reference
	uppercaseID.ExternalSymbol = "TETHER"
	wrongMarket := reference
	wrongMarket.ProviderMarket = "spot"
	lowercaseCurrency := reference
	lowercaseCurrency.QuoteCurrency = "usd"

	tests := []struct {
		name       string
		references []ports.ProviderInstrumentRef
		code       ports.ProviderErrorCode
	}{
		{name: "empty", code: ports.ProviderErrorBadRequest},
		{name: "other provider", references: []ports.ProviderInstrumentRef{otherProvider}, code: ports.ProviderErrorBadRequest},
		{name: "uppercase coin id", references: []ports.ProviderInstrumentRef{uppercaseID}, code: ports.ProviderErrorInvalidInstrument},
		{name: "wrong market", references: []ports.ProviderInstrumentRef{wrongMarket}, code: ports.ProviderErrorInvalidInstrument},
		{name: "lowercase currency", references: []ports.ProviderInstrumentRef{lowercaseCurrency}, code: ports.ProviderErrorInvalidInstrument},
		{name: "duplicate pair", references: []ports.ProviderInstrumentRef{reference, usdtUSDReference(t)}, code: ports.ProviderErrorInvalidInstrument},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := adapter.FetchLatestQuotes(context.Background(), test.references)
			if !isCode(t, err, test.code) {
				t.Fatalf("error = %v, want %s", err, test.code)
			}
		})
	}
	if calls != 0 {
		t.Fatalf("HTTP calls = %d, want 0", calls)
	}
	if _, err := adapter.FetchLatestQuotes(nil, []ports.ProviderInstrumentRef{reference}); err == nil {
		t.Fatal("FetchLatestQuotes(nil context) error = nil")
	}
}

func TestFetchLatestQuotesRejectsMalformedProviderData(t *testing.T) {
	t.Parallel()

	reference := usdtUSDReference(t)
	tests := []struct {
		name string
		body string
	}{
		{name: "not an object", body: `[]`},
		{name: "invalid price", body: `{"tether":{"usd":"not-number","usd_last_updated_at":1784080800}}`},
		{name: "invalid timestamp", body: `{"tether":{"usd":0.99,"usd_last_updated_at":"soon"}}`},
		{name: "zero timestamp", body: `{"tether":{"usd":0.99,"usd_last_updated_at":0}}`},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			adapter, _ := newHTTPTestAdapter(t, func(writer http.ResponseWriter, _ *http.Request) { _, _ = writer.Write([]byte(test.body)) })
			_, err := adapter.FetchLatestQuotes(context.Background(), []ports.ProviderInstrumentRef{reference})
			if !isCode(t, err, ports.ProviderErrorInvalidResponse) {
				t.Fatalf("error = %v", err)
			}
		})
	}
}

func TestParseDecimalFromNumber(t *testing.T) {
	t.Parallel()

	if value, err := parseDecimalFromNumber(json.Number("1.2345")); err != nil || value.String() != "1.2345" {
		t.Fatalf("parseDecimalFromNumber() = (%s, %v)", value, err)
	}
	if _, err := parseDecimalFromNumber(json.Number("")); err == nil {
		t.Fatal("parseDecimalFromNumber(empty) error = nil")
	}
}

func TestInstantFromSecondsAndMilliseconds(t *testing.T) {
	t.Parallel()

	if _, err := instantFromSeconds(0); err == nil {
		t.Fatal("instantFromSeconds(0) error = nil")
	}
	if _, err := instantFromMilliseconds(-1); err == nil {
		t.Fatal("instantFromMilliseconds(-1) error = nil")
	}
	if value, err := instantFromSeconds(1784080800); err != nil || value.Time().Unix() != 1784080800 {
		t.Fatalf("instantFromSeconds() = (%s, %v)", value, err)
	}
}

func TestSortedKeysIsDeterministic(t *testing.T) {
	t.Parallel()

	values := map[string]struct{}{"b": {}, "a": {}, "c": {}}
	if got := sortedKeys(values); got[0] != "a" || got[1] != "b" || got[2] != "c" {
		t.Fatalf("sortedKeys() = %v", got)
	}
}
