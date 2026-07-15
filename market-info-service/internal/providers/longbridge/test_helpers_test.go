package longbridge

import (
	"context"
	"encoding/json"
	"os"
	"testing"
	"time"

	lbquote "github.com/longbridge/openapi-go/quote"
	"github.com/shopspring/decimal"

	"xr-trading/market-info-service/internal/domain"
	"xr-trading/market-info-service/internal/ingestion/ports"
)

var fixedLongbridgeNow = time.Date(2026, 7, 15, 21, 0, 0, 0, time.UTC)

type fakeClient struct {
	quoteFn func(context.Context, []string) ([]*lbquote.SecurityQuote, error)
	barsFn  func(context.Context, string, lbquote.Period, lbquote.AdjustType, bool, *time.Time, int32, ...lbquote.CandlestickRequestOption) ([]*lbquote.Candlestick, error)
	closed  int
}

func (client *fakeClient) Quote(ctx context.Context, symbols []string) ([]*lbquote.SecurityQuote, error) {
	if client.quoteFn == nil {
		return nil, nil
	}
	return client.quoteFn(ctx, symbols)
}

func (client *fakeClient) HistoryCandlesticksByOffset(ctx context.Context, symbol string, period lbquote.Period, adjust lbquote.AdjustType, forward bool, offset *time.Time, count int32, options ...lbquote.CandlestickRequestOption) ([]*lbquote.Candlestick, error) {
	if client.barsFn == nil {
		return nil, nil
	}
	return client.barsFn(ctx, symbol, period, adjust, forward, offset, count, options...)
}

func (client *fakeClient) Close() error {
	client.closed++
	return nil
}

func newTestAdapter(t *testing.T, client *fakeClient) *Adapter {
	t.Helper()
	adapter, err := New(Config{Client: client, Now: func() time.Time { return fixedLongbridgeNow }})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	return adapter
}

func longbridgeReference(t *testing.T, symbol string, assetType domain.AssetType, instrumentType domain.InstrumentType) ports.ProviderInstrumentRef {
	t.Helper()
	return ports.ProviderInstrumentRef{
		ProviderInstrumentID: mustLongbridgeID(t), ProviderInstrumentCode: mustLongbridgeCode(t, "provider.longbridge.us."+lowerLongbridgeASCII(symbol)),
		InstrumentID: mustLongbridgeID(t), AssetID: mustLongbridgeID(t), ProviderCode: mustLongbridgeCode(t, providerName),
		ProviderMarket: usMarket, AssetType: assetType, InstrumentType: instrumentType, ExternalSymbol: symbol,
		InstrumentCode: mustLongbridgeCode(t, "instrument.us."+lowerLongbridgeASCII(symbol)), InstrumentSymbol: symbol,
		QuoteCurrency: "USD", Metadata: json.RawMessage(`{"trade_session":"regular"}`),
	}
}

func longbridgeBarsRequest(t *testing.T, reference ports.ProviderInstrumentRef) ports.FetchBarsRequest {
	t.Helper()
	return ports.FetchBarsRequest{
		Instrument: reference, Interval: domain.BarInterval1Hour,
		StartTime: mustLongbridgeInstant(t, time.Date(2026, 7, 15, 17, 30, 0, 0, time.UTC)),
		EndTime:   mustLongbridgeInstant(t, time.Date(2026, 7, 15, 20, 0, 0, 0, time.UTC)), Limit: 2,
	}
}

type quoteFixture struct {
	Quotes []quoteFixtureItem `json:"quotes"`
}

type quoteFixtureItem struct {
	Symbol      string              `json:"symbol"`
	LastDone    *string             `json:"last_done"`
	PrevClose   *string             `json:"prev_close"`
	Open        *string             `json:"open"`
	High        *string             `json:"high"`
	Low         *string             `json:"low"`
	Timestamp   int64               `json:"timestamp"`
	Volume      int64               `json:"volume"`
	Turnover    *string             `json:"turnover"`
	TradeStatus int32               `json:"trade_status"`
	PreMarket   *sessionFixtureItem `json:"pre_market"`
}

type sessionFixtureItem struct {
	LastDone  *string `json:"last_done"`
	Timestamp int64   `json:"timestamp"`
	Volume    int64   `json:"volume"`
	Turnover  *string `json:"turnover"`
	High      *string `json:"high"`
	Low       *string `json:"low"`
	PrevClose *string `json:"prev_close"`
}

type barsFixture struct {
	Bars []barFixtureItem `json:"bars"`
}

type barFixtureItem struct {
	Open      *string `json:"open"`
	High      *string `json:"high"`
	Low       *string `json:"low"`
	Close     *string `json:"close"`
	Volume    int64   `json:"volume"`
	Turnover  *string `json:"turnover"`
	Timestamp string  `json:"timestamp"`
}

func loadQuoteFixture(t *testing.T, name string) []*lbquote.SecurityQuote {
	t.Helper()
	var document quoteFixture
	readFixture(t, name, &document)
	result := make([]*lbquote.SecurityQuote, 0, len(document.Quotes))
	for _, item := range document.Quotes {
		result = append(result, &lbquote.SecurityQuote{
			Symbol: item.Symbol, LastDone: fixtureDecimal(t, item.LastDone), PrevClose: fixtureDecimal(t, item.PrevClose),
			Open: fixtureDecimal(t, item.Open), High: fixtureDecimal(t, item.High), Low: fixtureDecimal(t, item.Low),
			Timestamp: item.Timestamp, Volume: item.Volume, Turnover: fixtureDecimal(t, item.Turnover),
			TradeStatus: lbquote.TradeStatus(item.TradeStatus), PreMarketQuote: fixtureSession(t, item.PreMarket),
		})
	}
	return result
}

func loadBarsFixture(t *testing.T, name string) []*lbquote.Candlestick {
	t.Helper()
	var document barsFixture
	readFixture(t, name, &document)
	result := make([]*lbquote.Candlestick, 0, len(document.Bars))
	for _, item := range document.Bars {
		instant, err := time.Parse(time.RFC3339, item.Timestamp)
		if err != nil {
			t.Fatalf("parse fixture timestamp: %v", err)
		}
		result = append(result, &lbquote.Candlestick{
			Open: fixtureDecimal(t, item.Open), High: fixtureDecimal(t, item.High), Low: fixtureDecimal(t, item.Low),
			Close: fixtureDecimal(t, item.Close), Volume: item.Volume, Turnover: fixtureDecimal(t, item.Turnover), Timestamp: instant.Unix(),
		})
	}
	return result
}

func readFixture(t *testing.T, name string, target any) {
	t.Helper()
	content, err := os.ReadFile("testdata/" + name)
	if err != nil {
		t.Fatalf("read fixture %s: %v", name, err)
	}
	if err := json.Unmarshal(content, target); err != nil {
		t.Fatalf("decode fixture %s: %v", name, err)
	}
}

func fixtureDecimal(t *testing.T, value *string) *decimal.Decimal {
	t.Helper()
	if value == nil {
		return nil
	}
	parsed, err := decimal.NewFromString(*value)
	if err != nil {
		t.Fatalf("parse fixture decimal: %v", err)
	}
	return &parsed
}

func fixtureSession(t *testing.T, item *sessionFixtureItem) *lbquote.PrePostQuote {
	t.Helper()
	if item == nil {
		return nil
	}
	return &lbquote.PrePostQuote{LastDone: fixtureDecimal(t, item.LastDone), Timestamp: item.Timestamp, Volume: item.Volume, Turnover: fixtureDecimal(t, item.Turnover), High: fixtureDecimal(t, item.High), Low: fixtureDecimal(t, item.Low), PrevClose: fixtureDecimal(t, item.PrevClose)}
}

func mustLongbridgeID(t *testing.T) domain.ID {
	t.Helper()
	value, err := domain.NewID()
	if err != nil {
		t.Fatalf("NewID() error = %v", err)
	}
	return value
}

func mustLongbridgeCode(t *testing.T, value string) domain.Code {
	t.Helper()
	parsed, err := domain.ParseCode(value)
	if err != nil {
		t.Fatalf("ParseCode(%q) error = %v", value, err)
	}
	return parsed
}

func mustLongbridgeInstant(t *testing.T, value time.Time) domain.UTCInstant {
	t.Helper()
	parsed, err := domain.NewUTCInstant(value)
	if err != nil {
		t.Fatalf("NewUTCInstant() error = %v", err)
	}
	return parsed
}

func lowerLongbridgeASCII(value string) string {
	buffer := []byte(value)
	for index := range buffer {
		if buffer[index] >= 'A' && buffer[index] <= 'Z' {
			buffer[index] += 'a' - 'A'
		}
	}
	return string(buffer)
}
