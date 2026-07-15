package bybit

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"

	"xr-trading/market-info-service/internal/domain"
	"xr-trading/market-info-service/internal/ingestion/ports"
)

type tickerResult struct {
	Category string            `json:"category"`
	List     []json.RawMessage `json:"list"`
}

type spotTicker struct {
	Symbol        string `json:"symbol"`
	BidPrice      string `json:"bid1Price"`
	BidSize       string `json:"bid1Size"`
	AskPrice      string `json:"ask1Price"`
	AskSize       string `json:"ask1Size"`
	LastPrice     string `json:"lastPrice"`
	Previous24H   string `json:"prevPrice24h"`
	High24H       string `json:"highPrice24h"`
	Low24H        string `json:"lowPrice24h"`
	Turnover24H   string `json:"turnover24h"`
	BaseVolume24H string `json:"volume24h"`
}

// FetchLatestQuotes fetches one symbol directly or requests the Spot ticker
// snapshot once and filters it for a multi-symbol batch.
func (adapter *Adapter) FetchLatestQuotes(ctx context.Context, references []ports.ProviderInstrumentRef) ([]ports.ProviderQuote, error) {
	if adapter == nil {
		return nil, fmt.Errorf("fetch bybit quotes: nil adapter: %w", domain.ErrInvalidState)
	}
	requested, err := adapter.validateSpotReferences(references, maxQuoteBatchSize)
	if err != nil {
		return nil, err
	}
	query := url.Values{"category": []string{spotMarket}}
	if len(references) == 1 {
		query.Set("symbol", references[0].ExternalSymbol)
	}
	envelope, err := getJSON[tickerResult](ctx, adapter, tickersPath, query)
	if err != nil {
		return nil, err
	}
	if envelope.Result.Category != spotMarket {
		return nil, invalidResponseError(adapter.providerCode, "provider returned an unexpected market category", nil)
	}
	marketTime, err := instantFromMilliseconds(envelope.Time)
	if err != nil {
		return nil, invalidResponseError(adapter.providerCode, "provider returned an invalid market time", err)
	}
	receivedAt, err := domain.NewUTCInstant(adapter.now())
	if err != nil {
		return nil, invalidResponseError(adapter.providerCode, "collection clock returned an invalid time", err)
	}

	quotes := make([]ports.ProviderQuote, 0, len(references))
	seen := make(map[string]struct{}, len(envelope.Result.List))
	for _, raw := range envelope.Result.List {
		var ticker spotTicker
		if err := json.Unmarshal(raw, &ticker); err != nil {
			return nil, invalidResponseError(adapter.providerCode, "provider returned a malformed ticker", err)
		}
		if ticker.Symbol == "" {
			return nil, invalidResponseError(adapter.providerCode, "provider ticker symbol is missing", nil)
		}
		if _, duplicate := seen[ticker.Symbol]; duplicate {
			return nil, invalidResponseError(adapter.providerCode, "provider returned a duplicate ticker", nil)
		}
		seen[ticker.Symbol] = struct{}{}
		reference, wanted := requested[ticker.Symbol]
		if !wanted {
			if len(references) == 1 {
				return nil, invalidResponseError(adapter.providerCode, "provider returned an unrequested ticker", nil)
			}
			continue
		}
		quote, err := mapTicker(reference, ticker, marketTime, receivedAt, raw)
		if err != nil {
			return nil, invalidResponseError(adapter.providerCode, "provider ticker fields are invalid", err)
		}
		quotes = append(quotes, quote)
	}
	if err := ports.ValidateLatestQuoteBatch(adapter.providerCode, references, quotes); err != nil {
		return nil, invalidResponseError(adapter.providerCode, "provider ticker batch violated adapter contract", err)
	}
	return quotes, nil
}

func (adapter *Adapter) validateSpotReferences(references []ports.ProviderInstrumentRef, maximum int) (map[string]ports.ProviderInstrumentRef, error) {
	if err := ports.ValidateLatestQuoteBatch(adapter.providerCode, references, nil); err != nil {
		return nil, badRequestError(adapter.providerCode, "provider instrument references are invalid", err)
	}
	if len(references) > maximum {
		return nil, badRequestError(adapter.providerCode, "provider quote batch exceeds adapter limit", nil)
	}
	requested := make(map[string]ports.ProviderInstrumentRef, len(references))
	for _, reference := range references {
		if err := adapter.validateSpotReference(reference); err != nil {
			return nil, err
		}
		if _, duplicate := requested[reference.ExternalSymbol]; duplicate {
			return nil, invalidInstrumentError(adapter.providerCode, "provider symbol is mapped more than once", nil)
		}
		requested[reference.ExternalSymbol] = reference
	}
	return requested, nil
}

func mapTicker(reference ports.ProviderInstrumentRef, ticker spotTicker, marketTime, receivedAt domain.UTCInstant, raw json.RawMessage) (ports.ProviderQuote, error) {
	lastPrice, err := parseRequiredDecimal(ticker.LastPrice)
	if err != nil {
		return ports.ProviderQuote{}, err
	}
	values := []*string{&ticker.BidPrice, &ticker.BidSize, &ticker.AskPrice, &ticker.AskSize, &ticker.Previous24H, &ticker.High24H, &ticker.Low24H, &ticker.BaseVolume24H, &ticker.Turnover24H}
	parsed := make([]*domain.Decimal, len(values))
	for index, value := range values {
		parsed[index], err = parseOptionalDecimal(*value)
		if err != nil {
			return ports.ProviderQuote{}, err
		}
	}
	quote := ports.ProviderQuote{
		ProviderInstrumentID: reference.ProviderInstrumentID, InstrumentID: reference.InstrumentID,
		AssetID: reference.AssetID, ProviderCode: reference.ProviderCode, LastPrice: lastPrice,
		BidPrice: parsed[0], BidSize: parsed[1], AskPrice: parsed[2], AskSize: parsed[3],
		Open24H: parsed[4], High24H: parsed[5], Low24H: parsed[6],
		BaseVolume24H: parsed[7], QuoteVolume24H: parsed[8],
		MarketTime: marketTime, ReceivedAt: receivedAt, RawPayload: append(json.RawMessage(nil), raw...),
	}
	if err := quote.ValidateFor(reference); err != nil {
		return ports.ProviderQuote{}, err
	}
	return quote, nil
}
