package longbridge

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"

	lbquote "github.com/longbridge/openapi-go/quote"

	"xr-trading/market-info-service/internal/domain"
	"xr-trading/market-info-service/internal/ingestion/ports"
)

func TestFetchLatestQuotesMapsRegularSessionAndPreservesTrace(t *testing.T) {
	t.Parallel()
	snapshots := loadQuoteFixture(t, "quotes_us.json")
	aapl := longbridgeReference(t, "AAPL.US", domain.AssetTypeStock, domain.InstrumentTypeEquity)
	spy := longbridgeReference(t, "SPY.US", domain.AssetTypeETF, domain.InstrumentTypeETF)
	client := &fakeClient{quoteFn: func(_ context.Context, symbols []string) ([]*lbquote.SecurityQuote, error) {
		if len(symbols) != 2 || symbols[0] != "SPY.US" || symbols[1] != "AAPL.US" {
			t.Errorf("quote symbols = %#v", symbols)
		}
		return []*lbquote.SecurityQuote{snapshots[0], snapshots[1]}, nil
	}}
	adapter := newTestAdapter(t, client)
	quotes, err := adapter.FetchLatestQuotes(context.Background(), []ports.ProviderInstrumentRef{spy, aapl})
	if err != nil || len(quotes) != 2 {
		t.Fatalf("FetchLatestQuotes() = (%#v, %v)", quotes, err)
	}
	if quotes[0].ProviderInstrumentID != spy.ProviderInstrumentID || quotes[1].ProviderInstrumentID != aapl.ProviderInstrumentID {
		t.Fatalf("quote order = %#v", quotes)
	}
	quote := quotes[1]
	if quote.LastPrice.String() != "189.45" || quote.MarketTime.Time().Unix() != 1784127600 || quote.ReceivedAt.Time() != fixedLongbridgeNow {
		t.Fatalf("mapped quote = %#v", quote)
	}
	if quote.BidPrice != nil || quote.Open24H != nil || quote.High24H != nil || quote.BaseVolume24H != nil {
		t.Fatalf("session values leaked into 24h fields = %#v", quote)
	}
	var raw rawSecurityQuote
	if err := json.Unmarshal(quote.RawPayload, &raw); err != nil || raw.Open == nil || *raw.Open != "188.1" || raw.PreMarket == nil {
		t.Fatalf("raw payload = (%s, %#v, %v)", quote.RawPayload, raw, err)
	}
}

func TestFetchLatestQuotesAllowsMissingSnapshot(t *testing.T) {
	t.Parallel()
	reference := longbridgeReference(t, "AAPL.US", domain.AssetTypeStock, domain.InstrumentTypeEquity)
	adapter := newTestAdapter(t, &fakeClient{})
	quotes, err := adapter.FetchLatestQuotes(context.Background(), []ports.ProviderInstrumentRef{reference})
	if err != nil || len(quotes) != 0 {
		t.Fatalf("FetchLatestQuotes() = (%#v, %v)", quotes, err)
	}
}

func TestFetchLatestQuotesRejectsInvalidInputBeforeClient(t *testing.T) {
	t.Parallel()
	calls := 0
	adapter := newTestAdapter(t, &fakeClient{quoteFn: func(context.Context, []string) ([]*lbquote.SecurityQuote, error) { calls++; return nil, nil }})
	stock := longbridgeReference(t, "AAPL.US", domain.AssetTypeStock, domain.InstrumentTypeEquity)
	tests := []struct {
		name string
		refs []ports.ProviderInstrumentRef
		code ports.ProviderErrorCode
	}{
		{name: "empty", refs: nil, code: ports.ProviderErrorBadRequest},
		{name: "wrong market", refs: []ports.ProviderInstrumentRef{mutateReference(stock, func(value *ports.ProviderInstrumentRef) { value.ProviderMarket = "hk" })}, code: ports.ProviderErrorInvalidInstrument},
		{name: "wrong type", refs: []ports.ProviderInstrumentRef{mutateReference(stock, func(value *ports.ProviderInstrumentRef) { value.InstrumentType = domain.InstrumentTypeETF })}, code: ports.ProviderErrorInvalidInstrument},
		{name: "lower symbol", refs: []ports.ProviderInstrumentRef{mutateReference(stock, func(value *ports.ProviderInstrumentRef) { value.ExternalSymbol = "aapl.US" })}, code: ports.ProviderErrorInvalidInstrument},
		{name: "duplicate symbol", refs: []ports.ProviderInstrumentRef{stock, mutateReference(stock, func(value *ports.ProviderInstrumentRef) { value.ProviderInstrumentID = mustLongbridgeID(t) })}, code: ports.ProviderErrorInvalidInstrument},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := adapter.FetchLatestQuotes(context.Background(), test.refs)
			if codeOf(err) != test.code {
				t.Fatalf("error = %#v, want %s", err, test.code)
			}
		})
	}
	if calls != 0 {
		t.Fatalf("client calls = %d", calls)
	}
}

func TestFetchLatestQuotesRejectsOversizedBatch(t *testing.T) {
	t.Parallel()
	references := make([]ports.ProviderInstrumentRef, 0, maxQuoteBatchSize+1)
	for index := 0; index <= maxQuoteBatchSize; index++ {
		reference := longbridgeReference(t, fmt.Sprintf("S%03d.US", index), domain.AssetTypeStock, domain.InstrumentTypeEquity)
		references = append(references, reference)
	}
	adapter := newTestAdapter(t, &fakeClient{})
	if _, err := adapter.FetchLatestQuotes(context.Background(), references); codeOf(err) != ports.ProviderErrorBadRequest {
		t.Fatalf("oversized batch error = %#v", err)
	}
}

func TestFetchLatestQuotesRejectsMalformedProviderData(t *testing.T) {
	t.Parallel()
	reference := longbridgeReference(t, "AAPL.US", domain.AssetTypeStock, domain.InstrumentTypeEquity)
	valid := loadQuoteFixture(t, "quotes_us.json")[0]
	tests := []struct {
		name   string
		values []*lbquote.SecurityQuote
	}{
		{name: "nil", values: []*lbquote.SecurityQuote{nil}},
		{name: "missing symbol", values: []*lbquote.SecurityQuote{{}}},
		{name: "unrequested", values: []*lbquote.SecurityQuote{cloneQuote(valid, func(value *lbquote.SecurityQuote) { value.Symbol = "MSFT.US" })}},
		{name: "duplicate", values: []*lbquote.SecurityQuote{valid, valid}},
		{name: "missing last", values: []*lbquote.SecurityQuote{cloneQuote(valid, func(value *lbquote.SecurityQuote) { value.LastDone = nil })}},
		{name: "invalid timestamp", values: []*lbquote.SecurityQuote{cloneQuote(valid, func(value *lbquote.SecurityQuote) { value.Timestamp = 0 })}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			adapter := newTestAdapter(t, &fakeClient{quoteFn: func(context.Context, []string) ([]*lbquote.SecurityQuote, error) { return test.values, nil }})
			_, err := adapter.FetchLatestQuotes(context.Background(), []ports.ProviderInstrumentRef{reference})
			if codeOf(err) != ports.ProviderErrorInvalidResponse {
				t.Fatalf("error = %#v", err)
			}
		})
	}
}

func mutateReference(value ports.ProviderInstrumentRef, mutate func(*ports.ProviderInstrumentRef)) ports.ProviderInstrumentRef {
	mutate(&value)
	return value
}
func cloneQuote(value *lbquote.SecurityQuote, mutate func(*lbquote.SecurityQuote)) *lbquote.SecurityQuote {
	cloned := *value
	mutate(&cloned)
	return &cloned
}
