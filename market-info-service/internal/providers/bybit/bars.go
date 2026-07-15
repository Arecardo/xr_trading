package bybit

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

const cursorPrefix = "v1:end-ms:"

type klineResult struct {
	Category string            `json:"category"`
	Symbol   string            `json:"symbol"`
	List     []json.RawMessage `json:"list"`
}

// FetchBars fetches one reverse-ordered Bybit page and normalizes it to the
// adapter contract's ascending order. The opaque cursor pages backward by an
// exclusive end timestamp because Bybit does not return a native cursor.
func (adapter *Adapter) FetchBars(ctx context.Context, request ports.FetchBarsRequest) (ports.FetchBarsResult, error) {
	if adapter == nil {
		return ports.FetchBarsResult{}, fmt.Errorf("fetch bybit bars: nil adapter: %w", domain.ErrInvalidState)
	}
	if err := request.Validate(); err != nil {
		return ports.FetchBarsResult{}, badRequestError(adapter.providerCode, "provider bar request is invalid", err)
	}
	if err := adapter.validateSpotReference(request.Instrument); err != nil {
		return ports.FetchBarsResult{}, err
	}
	interval, duration, err := bybitInterval(request.Interval)
	if err != nil {
		return ports.FetchBarsResult{}, unsupportedIntervalError(adapter.providerCode, err)
	}
	if request.Limit > maxBarsPerRequest {
		return ports.FetchBarsResult{}, badRequestError(adapter.providerCode, "provider bar request exceeds adapter limit", nil)
	}

	startMilliseconds := ceilUnixMilliseconds(request.StartTime.Time())
	effectiveEnd, err := effectiveEndMilliseconds(request)
	if err != nil || startMilliseconds <= 0 || effectiveEnd <= startMilliseconds {
		return ports.FetchBarsResult{}, badRequestError(adapter.providerCode, "provider bar cursor is invalid", err)
	}
	query := url.Values{
		"category": []string{spotMarket}, "symbol": []string{request.Instrument.ExternalSymbol},
		"interval": []string{interval}, "start": []string{strconv.FormatInt(startMilliseconds, 10)},
		"end": []string{strconv.FormatInt(effectiveEnd-1, 10)}, "limit": []string{strconv.Itoa(request.Limit)},
	}
	envelope, err := getJSON[klineResult](ctx, adapter, klinePath, query)
	if err != nil {
		return ports.FetchBarsResult{}, err
	}
	if envelope.Result.Category != spotMarket || envelope.Result.Symbol != request.Instrument.ExternalSymbol {
		return ports.FetchBarsResult{}, invalidResponseError(adapter.providerCode, "provider returned an unexpected bar source", nil)
	}
	providerTime, err := instantFromMilliseconds(envelope.Time)
	if err != nil {
		return ports.FetchBarsResult{}, invalidResponseError(adapter.providerCode, "provider returned an invalid response time", err)
	}
	receivedAt, err := domain.NewUTCInstant(adapter.now())
	if err != nil {
		return ports.FetchBarsResult{}, invalidResponseError(adapter.providerCode, "collection clock returned an invalid time", err)
	}

	bars := make([]ports.ProviderBar, 0, len(envelope.Result.List))
	seen := make(map[int64]struct{}, len(envelope.Result.List))
	for _, raw := range envelope.Result.List {
		bar, openMilliseconds, err := mapKline(request.Instrument, request.Interval, duration, providerTime, receivedAt, raw)
		if err != nil {
			return ports.FetchBarsResult{}, invalidResponseError(adapter.providerCode, "provider bar fields are invalid", err)
		}
		if _, duplicate := seen[openMilliseconds]; duplicate {
			return ports.FetchBarsResult{}, invalidResponseError(adapter.providerCode, "provider returned a duplicate bar", nil)
		}
		seen[openMilliseconds] = struct{}{}
		bars = append(bars, bar)
	}
	sort.Slice(bars, func(left, right int) bool {
		return bars[left].OpenTime.Time().Before(bars[right].OpenTime.Time())
	})

	result := ports.FetchBarsResult{Bars: bars}
	if len(bars) == request.Limit && len(bars) > 0 && bars[0].OpenTime.Time().After(request.StartTime.Time()) {
		result.HasMore = true
		result.NextCursor = cursorPrefix + strconv.FormatInt(bars[0].OpenTime.Time().UnixMilli(), 10)
	}
	if err := result.Validate(request); err != nil {
		return ports.FetchBarsResult{}, invalidResponseError(adapter.providerCode, "provider bar page violated adapter contract", err)
	}
	return result, nil
}

func mapKline(reference ports.ProviderInstrumentRef, interval domain.BarInterval, duration time.Duration, providerTime, receivedAt domain.UTCInstant, raw json.RawMessage) (ports.ProviderBar, int64, error) {
	var fields []string
	if err := json.Unmarshal(raw, &fields); err != nil || len(fields) != 7 {
		return ports.ProviderBar{}, 0, fmt.Errorf("decode Bybit kline tuple")
	}
	openMilliseconds, err := strconv.ParseInt(fields[0], 10, 64)
	if err != nil || openMilliseconds <= 0 {
		return ports.ProviderBar{}, 0, fmt.Errorf("parse Bybit kline start")
	}
	openTime, err := instantFromMilliseconds(openMilliseconds)
	if err != nil {
		return ports.ProviderBar{}, 0, err
	}
	closeTime, err := domain.NewUTCInstant(openTime.Time().Add(duration))
	if err != nil {
		return ports.ProviderBar{}, 0, err
	}
	decimals := make([]domain.Decimal, 6)
	for index := 1; index < len(fields); index++ {
		decimals[index-1], err = parseRequiredDecimal(fields[index])
		if err != nil {
			return ports.ProviderBar{}, 0, err
		}
	}
	bar := ports.ProviderBar{
		ProviderInstrumentID: reference.ProviderInstrumentID, InstrumentID: reference.InstrumentID,
		AssetID: reference.AssetID, ProviderCode: reference.ProviderCode, Interval: interval,
		OpenTime: openTime, CloseTime: closeTime, Open: decimals[0], High: decimals[1],
		Low: decimals[2], Close: decimals[3], BaseVolume: &decimals[4], QuoteVolume: &decimals[5],
		IsClosed:   !closeTime.Time().After(providerTime.Time()) && !closeTime.Time().After(receivedAt.Time()),
		ReceivedAt: receivedAt, RawPayload: append(json.RawMessage(nil), raw...),
	}
	if err := bar.ValidateFor(reference); err != nil {
		return ports.ProviderBar{}, 0, err
	}
	return bar, openMilliseconds, nil
}

func effectiveEndMilliseconds(request ports.FetchBarsRequest) (int64, error) {
	end := exclusiveEndMilliseconds(request.EndTime.Time())
	if request.Cursor == "" {
		return end, nil
	}
	if !strings.HasPrefix(request.Cursor, cursorPrefix) {
		return 0, fmt.Errorf("unexpected cursor version")
	}
	parsed, err := strconv.ParseInt(strings.TrimPrefix(request.Cursor, cursorPrefix), 10, 64)
	if err != nil || parsed <= ceilUnixMilliseconds(request.StartTime.Time()) || parsed >= end {
		return 0, fmt.Errorf("cursor is outside request range")
	}
	return parsed, nil
}

func ceilUnixMilliseconds(value time.Time) int64 {
	milliseconds := value.UnixMilli()
	if value.Nanosecond()%int(time.Millisecond) != 0 {
		milliseconds++
	}
	return milliseconds
}

func exclusiveEndMilliseconds(value time.Time) int64 {
	return ceilUnixMilliseconds(value)
}
