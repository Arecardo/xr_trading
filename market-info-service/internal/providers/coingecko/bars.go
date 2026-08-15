package coingecko

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"xr-trading/market-info-service/internal/domain"
	"xr-trading/market-info-service/internal/ingestion/ports"
)

const barsCursorPrefix = "v1:next-open-ms:"

// syntheticDailyBarPayload is stored as a bar's RawPayload (and, via
// ingestion's structural quality validator, ends up under
// market_bars.metadata.provider_payload). It exists so a stored FX bar is
// self-documenting: anyone reading the row later can see it was built from
// exactly one real CoinGecko price observation, not fabricated or
// interpolated OHLC. See RM0 DEC-006 and doc/technical/roadmap/01_decisions.md.
type syntheticDailyBarPayload struct {
	OHLCSynthesizedFromSinglePrice bool            `json:"ohlc_synthesized_from_single_price"`
	Note                           string          `json:"note"`
	RawPricePoint                  json.RawMessage `json:"raw_price_point"`
}

// FetchBars calls CoinGecko's market_chart/range endpoint and collapses its
// raw price points into at most one daily bar per UTC calendar day. Only the
// daily interval is supported: see Capabilities for why.
func (adapter *Adapter) FetchBars(ctx context.Context, request ports.FetchBarsRequest) (ports.FetchBarsResult, error) {
	if adapter == nil {
		return ports.FetchBarsResult{}, fmt.Errorf("fetch coingecko bars: nil adapter: %w", domain.ErrInvalidState)
	}
	if err := request.Validate(); err != nil {
		return ports.FetchBarsResult{}, badRequestError(adapter.providerCode, "provider bar request is invalid", err)
	}
	if err := adapter.validateFXReference(request.Instrument); err != nil {
		return ports.FetchBarsResult{}, err
	}
	if request.Interval != domain.BarInterval1Day {
		return ports.FetchBarsResult{}, unsupportedIntervalError(adapter.providerCode, fmt.Errorf("unsupported CoinGecko interval %q", request.Interval))
	}
	if request.Limit > maxBarsPerRequest {
		return ports.FetchBarsResult{}, badRequestError(adapter.providerCode, "provider bar request exceeds adapter limit", nil)
	}

	startTime := request.StartTime.Time().UTC()
	endTime := request.EndTime.Time().UTC()
	effectiveStart := startTime
	if request.Cursor != "" {
		cursorStart, err := parseBarsCursor(request.Cursor)
		if err != nil || cursorStart.Before(startTime) || !cursorStart.Before(endTime) {
			return ports.FetchBarsResult{}, badRequestError(adapter.providerCode, "provider bar cursor is invalid", err)
		}
		effectiveStart = cursorStart
	}

	query := url.Values{
		"vs_currency": []string{strings.ToLower(request.Instrument.QuoteCurrency)},
		"from":        []string{strconv.FormatInt(effectiveStart.Unix(), 10)},
		"to":          []string{strconv.FormatInt(endTime.Unix(), 10)},
	}
	body, err := adapter.doGet(ctx, fmt.Sprintf(marketChartRangePathTmpl, request.Instrument.ExternalSymbol), query)
	if err != nil {
		return ports.FetchBarsResult{}, err
	}

	var payload struct {
		Prices [][2]json.Number `json:"prices"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		return ports.FetchBarsResult{}, invalidResponseError(adapter.providerCode, "provider returned a malformed market_chart response", err)
	}

	receivedAtTime := adapter.now()
	receivedAt, err := domain.NewUTCInstant(receivedAtTime)
	if err != nil {
		return ports.FetchBarsResult{}, invalidResponseError(adapter.providerCode, "collection clock returned an invalid time", err)
	}

	type dayPoint struct {
		price domain.Decimal
		at    domain.UTCInstant
		raw   json.RawMessage
	}
	days := make(map[civilDay]dayPoint, len(payload.Prices))
	order := make([]civilDay, 0, len(payload.Prices))
	for _, point := range payload.Prices {
		milliseconds, err := point[0].Int64()
		if err != nil {
			return ports.FetchBarsResult{}, invalidResponseError(adapter.providerCode, "provider price timestamp is not a valid integer", err)
		}
		pointTime, err := instantFromMilliseconds(milliseconds)
		if err != nil {
			return ports.FetchBarsResult{}, invalidResponseError(adapter.providerCode, "provider returned an invalid price timestamp", err)
		}
		price, err := parseDecimalFromNumber(point[1])
		if err != nil {
			return ports.FetchBarsResult{}, invalidResponseError(adapter.providerCode, "provider price is not a valid decimal", err)
		}
		raw, err := json.Marshal(point)
		if err != nil {
			return ports.FetchBarsResult{}, invalidResponseError(adapter.providerCode, "provider price point could not be preserved", err)
		}
		day := civilDayOf(pointTime.Time())
		if _, exists := days[day]; !exists {
			order = append(order, day)
		}
		// CoinGecko returns points in ascending chronological order, so an
		// unconditional overwrite keeps the LAST (latest) observed price for
		// each day -- the representative daily reference rate this adapter
		// reports for that day.
		days[day] = dayPoint{price: price, at: pointTime, raw: raw}
	}
	sort.Slice(order, func(left, right int) bool { return order[left].before(order[right]) })

	eligible := make([]civilDay, 0, len(order))
	for _, day := range order {
		openTime := day.midnightUTC()
		if openTime.Before(effectiveStart) || !openTime.Before(endTime) {
			continue
		}
		eligible = append(eligible, day)
	}

	selected := eligible
	result := ports.FetchBarsResult{}
	if len(eligible) > request.Limit {
		selected = eligible[:request.Limit]
		result.HasMore = true
		result.NextCursor = barsCursorPrefix + strconv.FormatInt(eligible[request.Limit].midnightUTC().UnixMilli(), 10)
	}

	zeroVolume, err := domain.ParseDecimal("0")
	if err != nil {
		return ports.FetchBarsResult{}, invalidResponseError(adapter.providerCode, "internal zero volume decimal is invalid", err)
	}

	bars := make([]ports.ProviderBar, 0, len(selected))
	for _, day := range selected {
		openTime := day.midnightUTC()
		closeTime := openTime.Add(24 * time.Hour)
		openInstant, err := domain.NewUTCInstant(openTime)
		if err != nil {
			return ports.FetchBarsResult{}, invalidResponseError(adapter.providerCode, "computed bar open time is invalid", err)
		}
		closeInstant, err := domain.NewUTCInstant(closeTime)
		if err != nil {
			return ports.FetchBarsResult{}, invalidResponseError(adapter.providerCode, "computed bar close time is invalid", err)
		}
		point := days[day]
		markerPayload, err := json.Marshal(syntheticDailyBarPayload{
			OHLCSynthesizedFromSinglePrice: true,
			Note:                           "CoinGecko's free tier gives one price per day, not real OHLC; open=high=low=close=that single observed price and volume=0 (RM0 DEC-006).",
			RawPricePoint:                  point.raw,
		})
		if err != nil {
			return ports.FetchBarsResult{}, invalidResponseError(adapter.providerCode, "bar raw payload could not be constructed", err)
		}
		bar := ports.ProviderBar{
			ProviderInstrumentID: request.Instrument.ProviderInstrumentID, InstrumentID: request.Instrument.InstrumentID,
			AssetID: request.Instrument.AssetID, ProviderCode: request.Instrument.ProviderCode,
			Interval: domain.BarInterval1Day, OpenTime: openInstant, CloseTime: closeInstant,
			// --- Deliberate single-price-as-OHLC simplification, see
			// syntheticDailyBarPayload above and RM0 DEC-006. ---
			Open: point.price, High: point.price, Low: point.price, Close: point.price,
			BaseVolume: &zeroVolume, QuoteVolume: &zeroVolume,
			IsClosed:          !closeTime.After(receivedAtTime),
			ProviderUpdatedAt: &point.at,
			ReceivedAt:        receivedAt,
			RawPayload:        markerPayload,
		}
		if err := bar.ValidateFor(request.Instrument); err != nil {
			return ports.FetchBarsResult{}, invalidResponseError(adapter.providerCode, "provider bar fields are invalid", err)
		}
		bars = append(bars, bar)
	}
	result.Bars = bars
	if err := result.Validate(request); err != nil {
		return ports.FetchBarsResult{}, invalidResponseError(adapter.providerCode, "provider bar page violated adapter contract", err)
	}
	return result, nil
}

func parseBarsCursor(cursor string) (time.Time, error) {
	if !strings.HasPrefix(cursor, barsCursorPrefix) {
		return time.Time{}, fmt.Errorf("unexpected cursor version")
	}
	milliseconds, err := strconv.ParseInt(strings.TrimPrefix(cursor, barsCursorPrefix), 10, 64)
	if err != nil {
		return time.Time{}, fmt.Errorf("parse cursor: %w", err)
	}
	return time.UnixMilli(milliseconds).UTC(), nil
}
