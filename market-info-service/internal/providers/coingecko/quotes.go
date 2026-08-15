package coingecko

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"sort"
	"strings"

	"xr-trading/market-info-service/internal/domain"
	"xr-trading/market-info-service/internal/ingestion/ports"
)

const lastUpdatedSuffix = "_last_updated_at"

// FetchLatestQuotes calls CoinGecko's simple/price endpoint once for every
// distinct (coin id, quote currency) pair in the batch, mirroring Bybit's
// one-request-per-batch shape even though CoinGecko's response is a nested
// map rather than a ticker list.
func (adapter *Adapter) FetchLatestQuotes(ctx context.Context, references []ports.ProviderInstrumentRef) ([]ports.ProviderQuote, error) {
	if adapter == nil {
		return nil, fmt.Errorf("fetch coingecko quotes: nil adapter: %w", domain.ErrInvalidState)
	}
	if err := adapter.validateFXReferences(references, maxQuoteBatchSize); err != nil {
		return nil, err
	}

	ids := make(map[string]struct{}, len(references))
	currencies := make(map[string]struct{}, len(references))
	for _, reference := range references {
		ids[reference.ExternalSymbol] = struct{}{}
		currencies[strings.ToLower(reference.QuoteCurrency)] = struct{}{}
	}
	query := url.Values{
		"ids":                     []string{strings.Join(sortedKeys(ids), ",")},
		"vs_currencies":           []string{strings.Join(sortedKeys(currencies), ",")},
		"include_last_updated_at": []string{"true"},
	}
	body, err := adapter.doGet(ctx, simplePricePath, query)
	if err != nil {
		return nil, err
	}

	var payload map[string]map[string]json.Number
	if err := json.Unmarshal(body, &payload); err != nil {
		return nil, invalidResponseError(adapter.providerCode, "provider returned a malformed simple/price response", err)
	}

	receivedAt, err := domain.NewUTCInstant(adapter.now())
	if err != nil {
		return nil, invalidResponseError(adapter.providerCode, "collection clock returned an invalid time", err)
	}

	quotes := make([]ports.ProviderQuote, 0, len(references))
	for _, reference := range references {
		currency := strings.ToLower(reference.QuoteCurrency)
		submap, present := payload[reference.ExternalSymbol]
		if !present {
			// CoinGecko has no current snapshot for this coin id at all; this
			// mirrors Bybit's "allows missing snapshot" behavior rather than
			// failing the whole batch for one absent id.
			continue
		}
		priceNumber, priceOK := submap[currency]
		updatedNumber, updatedOK := submap[currency+lastUpdatedSuffix]
		if !priceOK || !updatedOK {
			return nil, invalidResponseError(adapter.providerCode, "provider omitted requested currency fields for a known coin id", nil)
		}
		price, err := parseDecimalFromNumber(priceNumber)
		if err != nil {
			return nil, invalidResponseError(adapter.providerCode, "provider price is not a valid decimal", err)
		}
		updatedSeconds, err := updatedNumber.Int64()
		if err != nil {
			return nil, invalidResponseError(adapter.providerCode, "provider last_updated_at is not a valid integer", err)
		}
		marketTime, err := instantFromSeconds(updatedSeconds)
		if err != nil {
			return nil, invalidResponseError(adapter.providerCode, "provider returned an invalid last_updated_at", err)
		}
		rawSubmap, err := json.Marshal(submap)
		if err != nil {
			return nil, invalidResponseError(adapter.providerCode, "provider payload could not be preserved", err)
		}
		quote := ports.ProviderQuote{
			ProviderInstrumentID: reference.ProviderInstrumentID, InstrumentID: reference.InstrumentID,
			AssetID: reference.AssetID, ProviderCode: reference.ProviderCode, LastPrice: price,
			MarketTime: marketTime, ReceivedAt: receivedAt, RawPayload: rawSubmap,
		}
		if err := quote.ValidateFor(reference); err != nil {
			return nil, invalidResponseError(adapter.providerCode, "provider quote fields are invalid", err)
		}
		quotes = append(quotes, quote)
	}
	if err := ports.ValidateLatestQuoteBatch(adapter.providerCode, references, quotes); err != nil {
		return nil, invalidResponseError(adapter.providerCode, "provider quote batch violated adapter contract", err)
	}
	return quotes, nil
}

func (adapter *Adapter) validateFXReferences(references []ports.ProviderInstrumentRef, maximum int) error {
	if err := ports.ValidateLatestQuoteBatch(adapter.providerCode, references, nil); err != nil {
		return badRequestError(adapter.providerCode, "provider instrument references are invalid", err)
	}
	if len(references) > maximum {
		return badRequestError(adapter.providerCode, "provider quote batch exceeds adapter limit", nil)
	}
	pairs := make(map[string]struct{}, len(references))
	for _, reference := range references {
		if err := adapter.validateFXReference(reference); err != nil {
			return err
		}
		pairKey := strings.ToLower(reference.ExternalSymbol) + "|" + strings.ToLower(reference.QuoteCurrency)
		if _, duplicate := pairs[pairKey]; duplicate {
			return invalidInstrumentError(adapter.providerCode, "provider coin id and currency pair is mapped more than once", nil)
		}
		pairs[pairKey] = struct{}{}
	}
	return nil
}

func sortedKeys(values map[string]struct{}) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}
