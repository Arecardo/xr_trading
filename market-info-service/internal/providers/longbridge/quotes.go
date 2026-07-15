package longbridge

import (
	"context"
	"encoding/json"
	"fmt"

	lbquote "github.com/longbridge/openapi-go/quote"
	"github.com/shopspring/decimal"

	"xr-trading/market-info-service/internal/domain"
	"xr-trading/market-info-service/internal/ingestion/ports"
)

type rawSessionQuote struct {
	LastDone  *string `json:"last_done,omitempty"`
	Timestamp int64   `json:"timestamp,omitempty"`
	Volume    int64   `json:"volume"`
	Turnover  *string `json:"turnover,omitempty"`
	High      *string `json:"high,omitempty"`
	Low       *string `json:"low,omitempty"`
	PrevClose *string `json:"prev_close,omitempty"`
}

type rawSecurityQuote struct {
	Symbol      string           `json:"symbol"`
	LastDone    *string          `json:"last_done,omitempty"`
	PrevClose   *string          `json:"prev_close,omitempty"`
	Open        *string          `json:"open,omitempty"`
	High        *string          `json:"high,omitempty"`
	Low         *string          `json:"low,omitempty"`
	Timestamp   int64            `json:"timestamp"`
	Volume      int64            `json:"volume"`
	Turnover    *string          `json:"turnover,omitempty"`
	TradeStatus int32            `json:"trade_status"`
	PreMarket   *rawSessionQuote `json:"pre_market,omitempty"`
	PostMarket  *rawSessionQuote `json:"post_market,omitempty"`
	Overnight   *rawSessionQuote `json:"overnight,omitempty"`
}

// FetchLatestQuotes maps the SDK's base quote, which represents the regular
// session. Extended-session snapshots remain trace data and are not selected.
func (adapter *Adapter) FetchLatestQuotes(ctx context.Context, references []ports.ProviderInstrumentRef) ([]ports.ProviderQuote, error) {
	if adapter == nil {
		return nil, fmt.Errorf("fetch Longbridge quotes: nil adapter: %w", domain.ErrInvalidState)
	}
	if ctx == nil {
		return nil, badRequestError(adapter.providerCode, "provider quote context is required", nil)
	}
	requested, err := adapter.validateUSReferences(references)
	if err != nil {
		return nil, err
	}
	symbols := make([]string, len(references))
	for index := range references {
		symbols[index] = references[index].ExternalSymbol
	}
	snapshots, err := adapter.client.Quote(ctx, symbols)
	if err != nil {
		return nil, classifyClientError(adapter.providerCode, quoteOperation, ctx, err)
	}
	receivedAt, err := domain.NewUTCInstant(adapter.now())
	if err != nil {
		return nil, invalidResponseError(adapter.providerCode, "collection clock returned an invalid time", err)
	}

	mapped := make(map[string]ports.ProviderQuote, len(snapshots))
	for _, snapshot := range snapshots {
		if snapshot == nil || snapshot.Symbol == "" {
			return nil, invalidResponseError(adapter.providerCode, "provider returned a quote without a symbol", nil)
		}
		reference, wanted := requested[snapshot.Symbol]
		if !wanted {
			return nil, invalidResponseError(adapter.providerCode, "provider returned an unrequested quote", nil)
		}
		if _, duplicate := mapped[snapshot.Symbol]; duplicate {
			return nil, invalidResponseError(adapter.providerCode, "provider returned a duplicate quote", nil)
		}
		quote, err := mapSecurityQuote(reference, snapshot, receivedAt)
		if err != nil {
			return nil, invalidResponseError(adapter.providerCode, "provider quote fields are invalid", err)
		}
		mapped[snapshot.Symbol] = quote
	}
	quotes := make([]ports.ProviderQuote, 0, len(mapped))
	for _, reference := range references {
		if quote, exists := mapped[reference.ExternalSymbol]; exists {
			quotes = append(quotes, quote)
		}
	}
	if err := ports.ValidateLatestQuoteBatch(adapter.providerCode, references, quotes); err != nil {
		return nil, invalidResponseError(adapter.providerCode, "provider quote batch violated adapter contract", err)
	}
	return quotes, nil
}

func (adapter *Adapter) validateUSReferences(references []ports.ProviderInstrumentRef) (map[string]ports.ProviderInstrumentRef, error) {
	if err := ports.ValidateLatestQuoteBatch(adapter.providerCode, references, nil); err != nil {
		return nil, badRequestError(adapter.providerCode, "provider instrument references are invalid", err)
	}
	if len(references) > maxQuoteBatchSize {
		return nil, badRequestError(adapter.providerCode, "provider quote batch exceeds adapter limit", nil)
	}
	requested := make(map[string]ports.ProviderInstrumentRef, len(references))
	for _, reference := range references {
		if err := adapter.validateUSReference(reference); err != nil {
			return nil, err
		}
		if _, duplicate := requested[reference.ExternalSymbol]; duplicate {
			return nil, invalidInstrumentError(adapter.providerCode, "provider symbol is mapped more than once", nil)
		}
		requested[reference.ExternalSymbol] = reference
	}
	return requested, nil
}

func mapSecurityQuote(reference ports.ProviderInstrumentRef, snapshot *lbquote.SecurityQuote, receivedAt domain.UTCInstant) (ports.ProviderQuote, error) {
	lastPrice, err := requiredDecimal(snapshot.LastDone)
	if err != nil {
		return ports.ProviderQuote{}, err
	}
	marketTime, err := instantFromSeconds(snapshot.Timestamp)
	if err != nil {
		return ports.ProviderQuote{}, err
	}
	raw, err := json.Marshal(detachQuote(snapshot))
	if err != nil {
		return ports.ProviderQuote{}, err
	}
	quote := ports.ProviderQuote{
		ProviderInstrumentID: reference.ProviderInstrumentID,
		InstrumentID:         reference.InstrumentID, AssetID: reference.AssetID, ProviderCode: reference.ProviderCode,
		LastPrice: lastPrice, MarketTime: marketTime, ReceivedAt: receivedAt, RawPayload: raw,
	}
	if err := quote.ValidateFor(reference); err != nil {
		return ports.ProviderQuote{}, err
	}
	return quote, nil
}

func detachQuote(snapshot *lbquote.SecurityQuote) rawSecurityQuote {
	return rawSecurityQuote{
		Symbol: snapshot.Symbol, LastDone: decimalText(snapshot.LastDone), PrevClose: decimalText(snapshot.PrevClose),
		Open: decimalText(snapshot.Open), High: decimalText(snapshot.High), Low: decimalText(snapshot.Low),
		Timestamp: snapshot.Timestamp, Volume: snapshot.Volume, Turnover: decimalText(snapshot.Turnover),
		TradeStatus: int32(snapshot.TradeStatus), PreMarket: detachSessionQuote(snapshot.PreMarketQuote),
		PostMarket: detachSessionQuote(snapshot.PostMarketQuote), Overnight: detachSessionQuote(snapshot.OverNightQuote),
	}
}

func detachSessionQuote(snapshot *lbquote.PrePostQuote) *rawSessionQuote {
	if snapshot == nil {
		return nil
	}
	return &rawSessionQuote{
		LastDone: decimalText(snapshot.LastDone), Timestamp: snapshot.Timestamp, Volume: snapshot.Volume,
		Turnover: decimalText(snapshot.Turnover), High: decimalText(snapshot.High), Low: decimalText(snapshot.Low),
		PrevClose: decimalText(snapshot.PrevClose),
	}
}

func decimalText(value *decimal.Decimal) *string {
	if value == nil {
		return nil
	}
	text := value.String()
	return &text
}
