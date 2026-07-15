package bybit

import (
	"context"
	"fmt"
	"net/http"
	"sync/atomic"
	"testing"

	"xr-trading/market-info-service/internal/ingestion/ports"
)

func TestFetchLatestQuoteMapsSpotTicker(t *testing.T) {
	t.Parallel()

	reference := bybitReference(t, "BTCUSDT")
	adapter, _ := newHTTPTestAdapter(t, func(writer http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodGet || request.URL.Path != tickersPath {
			t.Errorf("request = %s %s", request.Method, request.URL.Path)
		}
		if request.URL.Query().Get("category") != spotMarket || request.URL.Query().Get("symbol") != reference.ExternalSymbol {
			t.Errorf("query = %s", request.URL.RawQuery)
		}
		if request.Header.Get("Accept") != "application/json" || request.Header.Get("User-Agent") != "adapter-test/1" {
			t.Errorf("headers = %#v", request.Header)
		}
		writer.Header().Set("Content-Type", "application/json")
		_, _ = writer.Write(fixture(t, "ticker_spot_single.json"))
	})

	quotes, err := adapter.FetchLatestQuotes(context.Background(), []ports.ProviderInstrumentRef{reference})
	if err != nil {
		t.Fatalf("FetchLatestQuotes() error = %v", err)
	}
	if len(quotes) != 1 {
		t.Fatalf("quotes = %#v", quotes)
	}
	quote := quotes[0]
	if quote.LastPrice.String() != "120000.15" || quote.BidPrice.String() != "120000.1" || quote.AskPrice.String() != "120000.2" ||
		quote.Open24H.String() != "118000" || quote.BaseVolume24H.String() != "3000.25" || quote.QuoteVolume24H.String() != "360000000.5" {
		t.Fatalf("mapped quote = %#v", quote)
	}
	if !quote.MarketTime.Time().Equal(fixedBybitNow) || !quote.ReceivedAt.Time().Equal(fixedBybitNow) || len(quote.RawPayload) == 0 {
		t.Fatalf("quote times/raw = (%s, %s, %s)", quote.MarketTime, quote.ReceivedAt, quote.RawPayload)
	}
}

func TestFetchLatestQuotesUsesOneAllTickerRequestForBatch(t *testing.T) {
	t.Parallel()

	btc := bybitReference(t, "BTCUSDT")
	eth := bybitReference(t, "ETHUSDT")
	var calls atomic.Int32
	adapter, _ := newHTTPTestAdapter(t, func(writer http.ResponseWriter, request *http.Request) {
		calls.Add(1)
		if request.URL.Query().Get("symbol") != "" || request.URL.Query().Get("category") != spotMarket {
			t.Errorf("batch query = %s", request.URL.RawQuery)
		}
		_, _ = writer.Write(fixture(t, "ticker_spot_batch.json"))
	})
	quotes, err := adapter.FetchLatestQuotes(context.Background(), []ports.ProviderInstrumentRef{btc, eth})
	if err != nil || len(quotes) != 2 || calls.Load() != 1 {
		t.Fatalf("FetchLatestQuotes() = (%#v, %v), calls=%d", quotes, err, calls.Load())
	}
	if quotes[0].ProviderInstrumentID != btc.ProviderInstrumentID || quotes[1].ProviderInstrumentID != eth.ProviderInstrumentID {
		t.Fatalf("quote sources = %#v", quotes)
	}
}

func TestFetchLatestQuotesAllowsMissingSnapshot(t *testing.T) {
	t.Parallel()

	btc := bybitReference(t, "BTCUSDT")
	eth := bybitReference(t, "ETHUSDT")
	adapter, _ := newHTTPTestAdapter(t, func(writer http.ResponseWriter, _ *http.Request) {
		_, _ = writer.Write(fixture(t, "ticker_spot_single.json"))
	})
	quotes, err := adapter.FetchLatestQuotes(context.Background(), []ports.ProviderInstrumentRef{btc, eth})
	if err != nil || len(quotes) != 1 || quotes[0].ProviderInstrumentID != btc.ProviderInstrumentID {
		t.Fatalf("FetchLatestQuotes() = (%#v, %v)", quotes, err)
	}
}

func TestFetchLatestQuotesRejectsInvalidInputBeforeHTTP(t *testing.T) {
	t.Parallel()

	reference := bybitReference(t, "BTCUSDT")
	var calls atomic.Int32
	adapter, _ := newHTTPTestAdapter(t, func(http.ResponseWriter, *http.Request) { calls.Add(1) })
	otherProvider := reference
	otherProvider.ProviderCode = mustBybitCode(t, "other")
	lowercaseSymbol := reference
	lowercaseSymbol.ExternalSymbol = "btcusdt"
	wrongMarket := reference
	wrongMarket.ProviderMarket = "linear"
	duplicateSymbol := bybitReference(t, reference.ExternalSymbol)
	tooMany := make([]ports.ProviderInstrumentRef, maxQuoteBatchSize+1)
	for index := range tooMany {
		tooMany[index] = bybitReference(t, fmt.Sprintf("TOKEN%dUSDT", index))
	}

	tests := []struct {
		name       string
		references []ports.ProviderInstrumentRef
		code       ports.ProviderErrorCode
	}{
		{name: "empty", code: ports.ProviderErrorBadRequest},
		{name: "other provider", references: []ports.ProviderInstrumentRef{otherProvider}, code: ports.ProviderErrorBadRequest},
		{name: "lowercase symbol", references: []ports.ProviderInstrumentRef{lowercaseSymbol}, code: ports.ProviderErrorInvalidInstrument},
		{name: "wrong market", references: []ports.ProviderInstrumentRef{wrongMarket}, code: ports.ProviderErrorInvalidInstrument},
		{name: "duplicate symbol", references: []ports.ProviderInstrumentRef{reference, duplicateSymbol}, code: ports.ProviderErrorInvalidInstrument},
		{name: "batch limit", references: tooMany, code: ports.ProviderErrorBadRequest},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := adapter.FetchLatestQuotes(context.Background(), test.references)
			providerError, ok := ports.AsProviderError(err)
			if !ok || providerError.Code != test.code {
				t.Fatalf("error = %#v, want %s", err, test.code)
			}
		})
	}
	if calls.Load() != 0 {
		t.Fatalf("HTTP calls = %d, want 0", calls.Load())
	}
	if _, err := adapter.FetchLatestQuotes(nil, []ports.ProviderInstrumentRef{reference}); err == nil {
		t.Fatal("FetchLatestQuotes(nil context) error = nil")
	}
}

func TestFetchLatestQuotesRejectsMalformedProviderData(t *testing.T) {
	t.Parallel()

	reference := bybitReference(t, "BTCUSDT")
	tests := []struct {
		name string
		body string
	}{
		{name: "category", body: `{"retCode":0,"retMsg":"OK","result":{"category":"linear","list":[]},"time":1784086200000}`},
		{name: "malformed ticker", body: `{"retCode":0,"retMsg":"OK","result":{"category":"spot","list":[[]]},"time":1784086200000}`},
		{name: "missing symbol", body: `{"retCode":0,"retMsg":"OK","result":{"category":"spot","list":[{"lastPrice":"1"}]},"time":1784086200000}`},
		{name: "unrequested", body: `{"retCode":0,"retMsg":"OK","result":{"category":"spot","list":[{"symbol":"ETHUSDT","lastPrice":"1"}]},"time":1784086200000}`},
		{name: "duplicate", body: `{"retCode":0,"retMsg":"OK","result":{"category":"spot","list":[{"symbol":"BTCUSDT","lastPrice":"1"},{"symbol":"BTCUSDT","lastPrice":"1"}]},"time":1784086200000}`},
		{name: "invalid decimal", body: `{"retCode":0,"retMsg":"OK","result":{"category":"spot","list":[{"symbol":"BTCUSDT","lastPrice":"not-number"}]},"time":1784086200000}`},
		{name: "invalid quote", body: `{"retCode":0,"retMsg":"OK","result":{"category":"spot","list":[{"symbol":"BTCUSDT","lastPrice":"2","bid1Price":"3","ask1Price":"1"}]},"time":1784086200000}`},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			adapter, _ := newHTTPTestAdapter(t, func(writer http.ResponseWriter, _ *http.Request) { _, _ = writer.Write([]byte(test.body)) })
			_, err := adapter.FetchLatestQuotes(context.Background(), []ports.ProviderInstrumentRef{reference})
			providerError, ok := ports.AsProviderError(err)
			if !ok || providerError.Code != ports.ProviderErrorInvalidResponse {
				t.Fatalf("error = %#v", err)
			}
		})
	}
}

func TestOptionalDecimalMapping(t *testing.T) {
	t.Parallel()

	if value, err := parseOptionalDecimal(""); err != nil || value != nil {
		t.Fatalf("parseOptionalDecimal(empty) = (%#v, %v)", value, err)
	}
	if _, err := parseOptionalDecimal("bad"); err == nil {
		t.Fatal("parseOptionalDecimal(bad) error = nil")
	}
	if _, err := parseRequiredDecimal(""); err == nil {
		t.Fatal("parseRequiredDecimal(empty) error = nil")
	}
}
