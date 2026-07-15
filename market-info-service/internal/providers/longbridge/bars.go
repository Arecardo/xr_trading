package longbridge

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	lbquote "github.com/longbridge/openapi-go/quote"

	"xr-trading/market-info-service/internal/domain"
	"xr-trading/market-info-service/internal/ingestion/ports"
)

const cursorPrefix = "v1:end-sec:"

type rawCandlestick struct {
	Open      *string `json:"open,omitempty"`
	High      *string `json:"high,omitempty"`
	Low       *string `json:"low,omitempty"`
	Close     *string `json:"close,omitempty"`
	Volume    int64   `json:"volume"`
	Turnover  *string `json:"turnover,omitempty"`
	Timestamp int64   `json:"timestamp"`
}

// FetchBars requests one backward page from the official history endpoint and
// returns only bars inside the domain's original [start,end) range.
func (adapter *Adapter) FetchBars(ctx context.Context, request ports.FetchBarsRequest) (ports.FetchBarsResult, error) {
	if adapter == nil {
		return ports.FetchBarsResult{}, fmt.Errorf("fetch Longbridge bars: nil adapter: %w", domain.ErrInvalidState)
	}
	if ctx == nil {
		return ports.FetchBarsResult{}, badRequestError(adapter.providerCode, "provider bar context is required", nil)
	}
	if err := request.Validate(); err != nil {
		return ports.FetchBarsResult{}, badRequestError(adapter.providerCode, "provider bar request is invalid", err)
	}
	if err := adapter.validateUSReference(request.Instrument); err != nil {
		return ports.FetchBarsResult{}, err
	}
	period, err := longbridgePeriod(request.Interval)
	if err != nil {
		return ports.FetchBarsResult{}, unsupportedIntervalError(adapter.providerCode, err)
	}
	if request.Limit > maxBarsPerRequest {
		return ports.FetchBarsResult{}, badRequestError(adapter.providerCode, "provider bar request exceeds adapter limit", nil)
	}
	effectiveEnd, err := effectiveEnd(request)
	if err != nil {
		return ports.FetchBarsResult{}, badRequestError(adapter.providerCode, "provider bar cursor is invalid", err)
	}
	offset := adapter.sdkOffset(effectiveEnd, request.Interval)
	sticks, err := adapter.client.HistoryCandlesticksByOffset(
		ctx, request.Instrument.ExternalSymbol, period, lbquote.AdjustTypeNo, false, &offset, int32(request.Limit),
		lbquote.CandlestickRequestTradeSession(lbquote.CandlestickTradeSessionNormal),
	)
	if err != nil {
		return ports.FetchBarsResult{}, classifyClientError(adapter.providerCode, barOperation, ctx, err)
	}
	if len(sticks) > request.Limit {
		return ports.FetchBarsResult{}, invalidResponseError(adapter.providerCode, "provider bar page exceeded requested limit", nil)
	}
	receivedAt, err := domain.NewUTCInstant(adapter.now())
	if err != nil {
		return ports.FetchBarsResult{}, invalidResponseError(adapter.providerCode, "collection clock returned an invalid time", err)
	}

	bars := make([]ports.ProviderBar, 0, len(sticks))
	seen := make(map[int64]struct{}, len(sticks))
	var oldest time.Time
	for _, stick := range sticks {
		if stick == nil {
			return ports.FetchBarsResult{}, invalidResponseError(adapter.providerCode, "provider returned a nil candlestick", nil)
		}
		if stick.Timestamp <= 0 {
			return ports.FetchBarsResult{}, invalidResponseError(adapter.providerCode, "provider returned an invalid bar timestamp", nil)
		}
		if _, duplicate := seen[stick.Timestamp]; duplicate {
			return ports.FetchBarsResult{}, invalidResponseError(adapter.providerCode, "provider returned a duplicate bar", nil)
		}
		seen[stick.Timestamp] = struct{}{}
		open := time.Unix(stick.Timestamp, 0).UTC()
		if oldest.IsZero() || open.Before(oldest) {
			oldest = open
		}
		if open.Before(request.StartTime.Time()) || !open.Before(request.EndTime.Time()) || !open.Before(effectiveEnd) {
			continue
		}
		bar, err := adapter.mapCandlestick(request.Instrument, request.Interval, stick, receivedAt)
		if err != nil {
			return ports.FetchBarsResult{}, invalidResponseError(adapter.providerCode, "provider bar fields are invalid", err)
		}
		bars = append(bars, bar)
	}
	sort.Slice(bars, func(left, right int) bool { return bars[left].OpenTime.Time().Before(bars[right].OpenTime.Time()) })

	result := ports.FetchBarsResult{Bars: bars}
	if len(sticks) == request.Limit && len(bars) > 0 && oldest.After(request.StartTime.Time()) {
		result.HasMore = true
		result.NextCursor = cursorPrefix + strconv.FormatInt(oldest.Unix(), 10)
	}
	if err := result.Validate(request); err != nil {
		return ports.FetchBarsResult{}, invalidResponseError(adapter.providerCode, "provider bar page violated adapter contract", err)
	}
	return result, nil
}

func (adapter *Adapter) mapCandlestick(reference ports.ProviderInstrumentRef, interval domain.BarInterval, stick *lbquote.Candlestick, receivedAt domain.UTCInstant) (ports.ProviderBar, error) {
	openPrice, err := requiredDecimal(stick.Open)
	if err != nil {
		return ports.ProviderBar{}, err
	}
	highPrice, err := requiredDecimal(stick.High)
	if err != nil {
		return ports.ProviderBar{}, err
	}
	lowPrice, err := requiredDecimal(stick.Low)
	if err != nil {
		return ports.ProviderBar{}, err
	}
	closePrice, err := requiredDecimal(stick.Close)
	if err != nil {
		return ports.ProviderBar{}, err
	}
	openTime, err := instantFromSeconds(stick.Timestamp)
	if err != nil {
		return ports.ProviderBar{}, err
	}
	closeTime, err := adapter.regularSessionClose(openTime.Time(), interval)
	if err != nil {
		return ports.ProviderBar{}, err
	}
	volume, err := volumeDecimal(stick.Volume)
	if err != nil {
		return ports.ProviderBar{}, err
	}
	raw, err := json.Marshal(rawCandlestick{
		Open: decimalText(stick.Open), High: decimalText(stick.High), Low: decimalText(stick.Low),
		Close: decimalText(stick.Close), Volume: stick.Volume, Turnover: decimalText(stick.Turnover), Timestamp: stick.Timestamp,
	})
	if err != nil {
		return ports.ProviderBar{}, err
	}
	bar := ports.ProviderBar{
		ProviderInstrumentID: reference.ProviderInstrumentID, InstrumentID: reference.InstrumentID,
		AssetID: reference.AssetID, ProviderCode: reference.ProviderCode, Interval: interval,
		OpenTime: openTime, CloseTime: closeTime, Open: openPrice, High: highPrice, Low: lowPrice, Close: closePrice,
		BaseVolume: volume, QuoteVolume: optionalDecimal(stick.Turnover),
		IsClosed: !receivedAt.Time().Before(closeTime.Time()), ReceivedAt: receivedAt, RawPayload: raw,
	}
	if err := bar.ValidateFor(reference); err != nil {
		return ports.ProviderBar{}, err
	}
	return bar, nil
}

func effectiveEnd(request ports.FetchBarsRequest) (time.Time, error) {
	end := request.EndTime.Time()
	if request.Cursor == "" {
		return end, nil
	}
	if !strings.HasPrefix(request.Cursor, cursorPrefix) {
		return time.Time{}, fmt.Errorf("unexpected cursor version")
	}
	seconds, err := strconv.ParseInt(strings.TrimPrefix(request.Cursor, cursorPrefix), 10, 64)
	if err != nil || seconds <= 0 {
		return time.Time{}, fmt.Errorf("invalid cursor timestamp")
	}
	parsed := time.Unix(seconds, 0).UTC()
	if !parsed.After(request.StartTime.Time()) || !parsed.Before(end) {
		return time.Time{}, fmt.Errorf("cursor is outside request range")
	}
	return parsed, nil
}
