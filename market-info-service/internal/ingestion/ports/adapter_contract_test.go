package ports

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"xr-trading/market-info-service/internal/domain"
)

func TestFakeAdapterSatisfiesContract(t *testing.T) {
	t.Parallel()

	reference := validProviderInstrumentRef(t)
	quote := validProviderQuote(t, reference)
	request := validFetchBarsRequest(t, reference)
	bar := validProviderBar(t, reference, request.StartTime.Time())
	adapter := fakeMarketDataAdapter{
		code:         reference.ProviderCode,
		capabilities: validProviderCapabilities(t),
		quotes:       []ProviderQuote{quote},
		bars:         FetchBarsResult{Bars: []ProviderBar{bar}},
	}

	var port MarketDataAdapter = adapter
	capabilities, err := port.Capabilities(context.Background())
	if err != nil || capabilities.ProviderCode != port.ProviderCode() || capabilities.Validate() != nil {
		t.Fatalf("Capabilities() = (%#v, %v)", capabilities, err)
	}
	quotes, err := port.FetchLatestQuotes(context.Background(), []ProviderInstrumentRef{reference})
	if err != nil || ValidateLatestQuoteBatch(port.ProviderCode(), []ProviderInstrumentRef{reference}, quotes) != nil {
		t.Fatalf("FetchLatestQuotes() = (%#v, %v)", quotes, err)
	}
	bars, err := port.FetchBars(context.Background(), request)
	if err != nil || bars.Validate(request) != nil {
		t.Fatalf("FetchBars() = (%#v, %v)", bars, err)
	}
}

func TestProviderInstrumentRefValidate(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		mutate func(*ProviderInstrumentRef)
	}{
		{name: "valid"},
		{name: "provider instrument id", mutate: func(value *ProviderInstrumentRef) { value.ProviderInstrumentID = domain.ID{} }},
		{name: "instrument id", mutate: func(value *ProviderInstrumentRef) { value.InstrumentID = domain.ID{} }},
		{name: "asset id", mutate: func(value *ProviderInstrumentRef) { value.AssetID = domain.ID{} }},
		{name: "provider instrument code", mutate: func(value *ProviderInstrumentRef) { value.ProviderInstrumentCode = mustAdapterCode(t, "mapping.bad") }},
		{name: "instrument code", mutate: func(value *ProviderInstrumentRef) { value.InstrumentCode = mustAdapterCode(t, "asset.crypto.btc") }},
		{name: "provider code", mutate: func(value *ProviderInstrumentRef) { value.ProviderCode = domain.Code{} }},
		{name: "provider market", mutate: func(value *ProviderInstrumentRef) { value.ProviderMarket = "bad market" }},
		{name: "asset type", mutate: func(value *ProviderInstrumentRef) { value.AssetType = "COMMODITY" }},
		{name: "instrument type", mutate: func(value *ProviderInstrumentRef) { value.InstrumentType = "PERPETUAL" }},
		{name: "external symbol", mutate: func(value *ProviderInstrumentRef) { value.ExternalSymbol = " BTCUSDT" }},
		{name: "instrument symbol", mutate: func(value *ProviderInstrumentRef) { value.InstrumentSymbol = "" }},
		{name: "quote currency", mutate: func(value *ProviderInstrumentRef) { value.QuoteCurrency = "" }},
		{name: "metadata", mutate: func(value *ProviderInstrumentRef) { value.Metadata = json.RawMessage(`[]`) }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			value := validProviderInstrumentRef(t)
			if test.mutate != nil {
				test.mutate(&value)
			}
			err := value.Validate()
			if test.mutate == nil && err != nil {
				t.Fatalf("Validate() error = %v", err)
			}
			if test.mutate != nil && !errors.Is(err, domain.ErrInvalidData) {
				t.Fatalf("Validate() error = %v, want invalid data", err)
			}
		})
	}
}

func TestProviderQuoteAndBatchValidation(t *testing.T) {
	t.Parallel()

	reference := validProviderInstrumentRef(t)
	quote := validProviderQuote(t, reference)
	if err := quote.ValidateFor(reference); err != nil {
		t.Fatalf("ValidateFor() error = %v", err)
	}
	if err := ValidateLatestQuoteBatch(reference.ProviderCode, []ProviderInstrumentRef{reference}, []ProviderQuote{quote}); err != nil {
		t.Fatalf("ValidateLatestQuoteBatch() error = %v", err)
	}
	if err := ValidateLatestQuoteBatch(reference.ProviderCode, []ProviderInstrumentRef{reference}, nil); err != nil {
		t.Fatalf("missing provider snapshot should be allowed: %v", err)
	}

	tests := []struct {
		name   string
		invoke func() error
	}{
		{name: "missing provider", invoke: func() error { changed := quote; changed.ProviderCode = domain.Code{}; return changed.Validate() }},
		{name: "missing asset", invoke: func() error { changed := quote; changed.AssetID = domain.ID{}; return changed.Validate() }},
		{name: "invalid market data", invoke: func() error {
			changed := quote
			changed.LastPrice = mustAdapterDecimal(t, "-1")
			return changed.Validate()
		}},
		{name: "raw payload", invoke: func() error { changed := quote; changed.RawPayload = json.RawMessage(`{`); return changed.Validate() }},
		{name: "reference mismatch", invoke: func() error {
			changed := quote
			changed.AssetID = mustAdapterID(t)
			return changed.ValidateFor(reference)
		}},
		{name: "missing references", invoke: func() error { return ValidateLatestQuoteBatch(reference.ProviderCode, nil, nil) }},
		{name: "provider mismatch", invoke: func() error {
			return ValidateLatestQuoteBatch(mustAdapterCode(t, "other"), []ProviderInstrumentRef{reference}, nil)
		}},
		{name: "duplicate reference", invoke: func() error {
			return ValidateLatestQuoteBatch(reference.ProviderCode, []ProviderInstrumentRef{reference, reference}, nil)
		}},
		{name: "unrequested quote", invoke: func() error {
			changed := quote
			changed.ProviderInstrumentID = mustAdapterID(t)
			return ValidateLatestQuoteBatch(reference.ProviderCode, []ProviderInstrumentRef{reference}, []ProviderQuote{changed})
		}},
		{name: "duplicate quote", invoke: func() error {
			return ValidateLatestQuoteBatch(reference.ProviderCode, []ProviderInstrumentRef{reference}, []ProviderQuote{quote, quote})
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if err := test.invoke(); !errors.Is(err, domain.ErrInvalidData) {
				t.Fatalf("error = %v, want invalid data", err)
			}
		})
	}
}

func TestFetchBarsRequestAndResultValidation(t *testing.T) {
	t.Parallel()

	reference := validProviderInstrumentRef(t)
	request := validFetchBarsRequest(t, reference)
	first := validProviderBar(t, reference, request.StartTime.Time())
	second := validProviderBar(t, reference, request.StartTime.Time().Add(time.Hour))
	result := FetchBarsResult{Bars: []ProviderBar{first, second}, HasMore: true, NextCursor: "next-page"}
	if err := result.Validate(request); err != nil {
		t.Fatalf("Validate() error = %v", err)
	}

	tests := []struct {
		name   string
		invoke func() error
	}{
		{name: "bad reference", invoke: func() error { changed := request; changed.Instrument.AssetID = domain.ID{}; return changed.Validate() }},
		{name: "interval", invoke: func() error { changed := request; changed.Interval = "5m"; return changed.Validate() }},
		{name: "range", invoke: func() error { changed := request; changed.EndTime = changed.StartTime; return changed.Validate() }},
		{name: "limit", invoke: func() error { changed := request; changed.Limit = 0; return changed.Validate() }},
		{name: "cursor too long", invoke: func() error {
			changed := request
			changed.Cursor = string(make([]byte, maximumAdapterCursorLength+1))
			return changed.Validate()
		}},
		{name: "too many bars", invoke: func() error { changed := request; changed.Limit = 1; return result.Validate(changed) }},
		{name: "missing next cursor", invoke: func() error { changed := result; changed.NextCursor = ""; return changed.Validate(request) }},
		{name: "same cursor", invoke: func() error {
			changedRequest := request
			changedRequest.Cursor = "same"
			changed := result
			changed.NextCursor = "same"
			return changed.Validate(changedRequest)
		}},
		{name: "empty progressing page", invoke: func() error { changed := result; changed.Bars = nil; return changed.Validate(request) }},
		{name: "terminal cursor", invoke: func() error { changed := result; changed.HasMore = false; return changed.Validate(request) }},
		{name: "source mismatch", invoke: func() error {
			changed := cloneFetchBarsResult(result)
			changed.Bars[0].AssetID = mustAdapterID(t)
			return changed.Validate(request)
		}},
		{name: "interval mismatch", invoke: func() error {
			changed := cloneFetchBarsResult(result)
			changed.Bars[0].Interval = domain.BarInterval1Day
			return changed.Validate(request)
		}},
		{name: "outside range", invoke: func() error {
			changed := cloneFetchBarsResult(result)
			changed.Bars[0].OpenTime = request.EndTime
			return changed.Validate(request)
		}},
		{name: "not ascending", invoke: func() error {
			changed := result
			changed.Bars = []ProviderBar{second, first}
			return changed.Validate(request)
		}},
		{name: "invalid raw payload", invoke: func() error { changed := first; changed.RawPayload = json.RawMessage(`{`); return changed.Validate() }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if err := test.invoke(); !errors.Is(err, domain.ErrInvalidData) {
				t.Fatalf("error = %v, want invalid data", err)
			}
		})
	}
}

func cloneFetchBarsResult(result FetchBarsResult) FetchBarsResult {
	result.Bars = append([]ProviderBar(nil), result.Bars...)
	return result
}

type fakeMarketDataAdapter struct {
	code         domain.Code
	capabilities ProviderCapabilities
	quotes       []ProviderQuote
	bars         FetchBarsResult
}

func (adapter fakeMarketDataAdapter) ProviderCode() domain.Code { return adapter.code }
func (adapter fakeMarketDataAdapter) Capabilities(context.Context) (ProviderCapabilities, error) {
	return adapter.capabilities, nil
}
func (adapter fakeMarketDataAdapter) FetchLatestQuotes(context.Context, []ProviderInstrumentRef) ([]ProviderQuote, error) {
	return adapter.quotes, nil
}
func (adapter fakeMarketDataAdapter) FetchBars(context.Context, FetchBarsRequest) (FetchBarsResult, error) {
	return adapter.bars, nil
}

func validProviderInstrumentRef(t *testing.T) ProviderInstrumentRef {
	t.Helper()
	return ProviderInstrumentRef{
		ProviderInstrumentID: mustAdapterID(t), ProviderInstrumentCode: mustAdapterCode(t, "provider.bybit.spot.btcusdt"),
		InstrumentID: mustAdapterID(t), AssetID: mustAdapterID(t), ProviderCode: mustAdapterCode(t, "bybit"),
		ProviderMarket: "spot", AssetType: domain.AssetTypeCrypto, InstrumentType: domain.InstrumentTypeSpot,
		ExternalSymbol: "BTCUSDT", InstrumentCode: mustAdapterCode(t, "instrument.bybit.spot.btc-usdt"),
		InstrumentSymbol: "BTC/USDT", QuoteCurrency: "USDT", Metadata: json.RawMessage(`{"category":"spot"}`),
	}
}

func validProviderQuote(t *testing.T, reference ProviderInstrumentRef) ProviderQuote {
	t.Helper()
	marketTime := mustAdapterInstant(t, time.Date(2026, 7, 15, 1, 0, 0, 0, time.UTC))
	return ProviderQuote{
		ProviderInstrumentID: reference.ProviderInstrumentID, InstrumentID: reference.InstrumentID,
		AssetID: reference.AssetID, ProviderCode: reference.ProviderCode,
		LastPrice: mustAdapterDecimal(t, "100"), MarketTime: marketTime,
		ReceivedAt: mustAdapterInstant(t, marketTime.Time().Add(time.Second)), RawPayload: json.RawMessage(`{"price":"100"}`),
	}
}

func validFetchBarsRequest(t *testing.T, reference ProviderInstrumentRef) FetchBarsRequest {
	t.Helper()
	start := mustAdapterInstant(t, time.Date(2026, 7, 15, 0, 0, 0, 0, time.UTC))
	return FetchBarsRequest{Instrument: reference, Interval: domain.BarInterval1Hour, StartTime: start, EndTime: mustAdapterInstant(t, start.Time().Add(3*time.Hour)), Limit: 2}
}

func validProviderBar(t *testing.T, reference ProviderInstrumentRef, openTime time.Time) ProviderBar {
	t.Helper()
	open := mustAdapterInstant(t, openTime)
	closeTime := mustAdapterInstant(t, openTime.Add(time.Hour))
	return ProviderBar{
		ProviderInstrumentID: reference.ProviderInstrumentID, InstrumentID: reference.InstrumentID,
		AssetID: reference.AssetID, ProviderCode: reference.ProviderCode, Interval: domain.BarInterval1Hour,
		OpenTime: open, CloseTime: closeTime, Open: mustAdapterDecimal(t, "100"), High: mustAdapterDecimal(t, "110"),
		Low: mustAdapterDecimal(t, "90"), Close: mustAdapterDecimal(t, "105"), IsClosed: true,
		ReceivedAt: mustAdapterInstant(t, closeTime.Time().Add(time.Second)), RawPayload: json.RawMessage(`{"open":"100"}`),
	}
}

func mustAdapterID(t *testing.T) domain.ID {
	t.Helper()
	value, err := domain.NewID()
	if err != nil {
		t.Fatalf("NewID() error = %v", err)
	}
	return value
}

func mustAdapterCode(t *testing.T, value string) domain.Code {
	t.Helper()
	parsed, err := domain.ParseCode(value)
	if err != nil {
		t.Fatalf("ParseCode(%q) error = %v", value, err)
	}
	return parsed
}

func mustAdapterDecimal(t *testing.T, value string) domain.Decimal {
	t.Helper()
	parsed, err := domain.ParseDecimal(value)
	if err != nil {
		t.Fatalf("ParseDecimal(%q) error = %v", value, err)
	}
	return parsed
}

func mustAdapterInstant(t *testing.T, value time.Time) domain.UTCInstant {
	t.Helper()
	parsed, err := domain.NewUTCInstant(value)
	if err != nil {
		t.Fatalf("NewUTCInstant() error = %v", err)
	}
	return parsed
}
