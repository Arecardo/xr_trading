package httpapi

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"sort"
	"time"

	"xr-trading/market-info-service/internal/application"
	"xr-trading/market-info-service/internal/domain"
)

const barsPath = "/api/market-info/v1/bars"
const barsCursorScope = "bars"

var barsPageLimits = PageLimits{Default: 200, Maximum: application.MaximumBarsPageSize}

// BarsQuery is the HTTP-facing subset of the application service.
type BarsQuery interface {
	List(context.Context, application.BarsInput) (application.BarsResult, error)
}

// BarsHandler translates K-line query parameters and cursor state.
type BarsHandler struct {
	query BarsQuery
}

// NewBarsHandler constructs the public read-only bar handler.
func NewBarsHandler(query BarsQuery) (*BarsHandler, error) {
	if query == nil {
		return nil, errors.New("bars query is required")
	}
	return &BarsHandler{query: query}, nil
}

// Register attaches the endpoint with a method-aware ServeMux pattern.
func (handler *BarsHandler) Register(mux *http.ServeMux) error {
	if handler == nil || handler.query == nil || mux == nil {
		return errors.New("bars handler and mux are required")
	}
	mux.Handle("GET "+barsPath, handler)
	return nil
}

// ServeHTTP handles GET /api/market-info/v1/bars.
func (handler *BarsHandler) ServeHTTP(writer http.ResponseWriter, request *http.Request) {
	input, err := parseBarsRequest(request)
	if err != nil {
		WriteError(writer, request, err)
		return
	}
	result, err := handler.query.List(request.Context(), input)
	if err != nil {
		WriteError(writer, request, err)
		return
	}
	response, err := barsResponseFromResult(input, result)
	if err != nil {
		WriteError(writer, request, err)
		return
	}
	if err := WriteJSON(writer, http.StatusOK, response); err != nil {
		WriteError(writer, request, err)
	}
}

type barsResponse struct {
	Instrument barsInstrumentResponse `json:"instrument"`
	Provider   barsProviderResponse   `json:"provider"`
	Interval   domain.BarInterval     `json:"interval"`
	Order      application.BarOrder   `json:"order"`
	Bars       []barResponse          `json:"bars"`
	NextCursor *string                `json:"next_cursor"`
}

type barsInstrumentResponse struct {
	InstrumentID   domain.ID    `json:"instrument_id"`
	InstrumentCode domain.Code  `json:"instrument_code"`
	BaseAssetCode  domain.Code  `json:"base_asset_code"`
	QuoteAssetCode *domain.Code `json:"quote_asset_code"`
	QuoteCurrency  string       `json:"quote_currency"`
}

type barsProviderResponse struct {
	ProviderCode           domain.Code `json:"provider_code"`
	ProviderInstrumentID   domain.ID   `json:"provider_instrument_id"`
	ProviderInstrumentCode domain.Code `json:"provider_instrument_code"`
	ProviderSymbol         string      `json:"provider_symbol"`
}

type barResponse struct {
	OpenTime          domain.UTCInstant    `json:"open_time"`
	CloseTime         domain.UTCInstant    `json:"close_time"`
	Open              domain.Decimal       `json:"open"`
	High              domain.Decimal       `json:"high"`
	Low               domain.Decimal       `json:"low"`
	Close             domain.Decimal       `json:"close"`
	Volume            *domain.Decimal      `json:"volume"`
	QuoteVolume       *domain.Decimal      `json:"quote_volume"`
	TradeCount        *int64               `json:"trade_count"`
	Revision          int                  `json:"revision"`
	IsClosed          bool                 `json:"is_closed"`
	QualityStatus     domain.QualityStatus `json:"quality_status"`
	ProviderUpdatedAt *domain.UTCInstant   `json:"provider_updated_at"`
	CollectedAt       domain.UTCInstant    `json:"collected_at"`
}

func parseBarsRequest(request *http.Request) (application.BarsInput, error) {
	if request == nil || request.URL == nil {
		return application.BarsInput{}, errors.New("HTTP request is required")
	}
	values, err := url.ParseQuery(request.URL.RawQuery)
	if err != nil {
		return application.BarsInput{}, application.ValidationError([]application.FieldViolation{{Field: "query", Reason: "is malformed"}})
	}
	if err := validateBarsQueryKeys(values); err != nil {
		return application.BarsInput{}, err
	}
	instrumentCode, err := singleQueryValue(values, "instrument_code", true)
	if err != nil {
		return application.BarsInput{}, err
	}
	providerCode, err := singleQueryValue(values, "provider", true)
	if err != nil {
		return application.BarsInput{}, err
	}
	interval, err := singleQueryValue(values, "interval", true)
	if err != nil {
		return application.BarsInput{}, err
	}
	startTime, err := parseOptionalQueryTime(values, "start_time")
	if err != nil {
		return application.BarsInput{}, err
	}
	endTime, err := parseOptionalQueryTime(values, "end_time")
	if err != nil {
		return application.BarsInput{}, err
	}
	orderValue, err := singleQueryValue(values, "order", false)
	if err != nil {
		return application.BarsInput{}, err
	}
	order := application.BarOrder(orderValue)
	if order == "" {
		order = application.BarOrderDescending
	}
	limitValue, err := singleQueryValue(values, "limit", false)
	if err != nil {
		return application.BarsInput{}, err
	}
	limit, err := ParsePageSize(limitValue, barsPageLimits)
	if err != nil {
		return application.BarsInput{}, err
	}
	input := application.BarsInput{
		InstrumentCode: instrumentCode, ProviderCode: providerCode, Interval: interval,
		StartTime: startTime, EndTime: endTime, Order: order, Limit: limit,
	}
	cursor, err := singleQueryValue(values, "cursor", false)
	if err != nil {
		return application.BarsInput{}, err
	}
	if cursor != "" {
		positions, decodeErr := DecodeCursor(cursor, barsCursorScope, 7)
		if decodeErr != nil {
			return application.BarsInput{}, decodeErr
		}
		bindings := barsCursorBindings(input)
		for index := range bindings {
			if positions[index] != bindings[index] {
				return application.BarsInput{}, invalidCursor()
			}
		}
		instant, parseErr := domain.ParseUTCInstant(positions[6])
		if parseErr != nil {
			return application.BarsInput{}, invalidCursor()
		}
		position := instant.Time()
		input.CursorOpenTime = &position
	}
	return input, nil
}

func validateBarsQueryKeys(values url.Values) error {
	allowed := map[string]struct{}{
		"instrument_code": {}, "provider": {}, "interval": {}, "start_time": {},
		"end_time": {}, "limit": {}, "order": {}, "cursor": {},
	}
	unknown := make([]string, 0)
	for key := range values {
		if _, exists := allowed[key]; !exists {
			unknown = append(unknown, key)
		}
	}
	if len(unknown) == 0 {
		return nil
	}
	sort.Strings(unknown)
	violations := make([]application.FieldViolation, 0, len(unknown))
	for _, field := range unknown {
		violations = append(violations, application.FieldViolation{Field: field, Reason: "is not supported"})
	}
	return application.ValidationError(violations)
}

func parseOptionalQueryTime(values url.Values, field string) (*time.Time, error) {
	raw, err := singleQueryValue(values, field, false)
	if err != nil || raw == "" {
		return nil, err
	}
	instant, err := domain.ParseUTCInstant(raw)
	if err != nil {
		return nil, application.ValidationError([]application.FieldViolation{{Field: field, Reason: "must be an RFC3339 timestamp"}})
	}
	parsed := instant.Time()
	return &parsed, nil
}

func barsCursorBindings(input application.BarsInput) []string {
	return []string{
		input.InstrumentCode, input.ProviderCode, input.Interval, string(input.Order),
		optionalCursorTime(input.StartTime), optionalCursorTime(input.EndTime),
	}
}

func optionalCursorTime(value *time.Time) string {
	if value == nil {
		return "-"
	}
	return domain.UTC(*value).Format(time.RFC3339Nano)
}

func barsResponseFromResult(input application.BarsInput, result application.BarsResult) (barsResponse, error) {
	response := barsResponse{
		Instrument: barsInstrumentResponse{
			InstrumentID: result.Instrument.ID, InstrumentCode: result.Instrument.Code,
			BaseAssetCode: result.Source.BaseAssetCode, QuoteAssetCode: result.Source.QuoteAssetCode,
			QuoteCurrency: result.Source.QuoteCurrency,
		},
		Provider: barsProviderResponse{
			ProviderCode: result.Provider.Code, ProviderInstrumentID: result.Source.ProviderInstrumentID,
			ProviderInstrumentCode: result.Source.ProviderInstrumentCode, ProviderSymbol: result.Source.ProviderSymbol,
		},
		Interval: result.Interval, Order: result.Order, Bars: make([]barResponse, 0, len(result.Bars)),
	}
	for _, bar := range result.Bars {
		response.Bars = append(response.Bars, barResponse{
			OpenTime: bar.OpenTime, CloseTime: bar.CloseTime, Open: bar.OpenPrice, High: bar.HighPrice,
			Low: bar.LowPrice, Close: bar.ClosePrice, Volume: bar.BaseVolume, QuoteVolume: bar.QuoteVolume,
			TradeCount: bar.TradeCount, Revision: bar.Revision, IsClosed: bar.IsClosed,
			QualityStatus: bar.QualityStatus, ProviderUpdatedAt: bar.ProviderUpdatedAt, CollectedAt: bar.CollectedAt,
		})
	}
	if result.NextCursorOpenTime != nil {
		values := append(barsCursorBindings(input), result.NextCursorOpenTime.String())
		cursor, err := EncodeCursor(barsCursorScope, values...)
		if err != nil {
			return barsResponse{}, err
		}
		response.NextCursor = &cursor
	}
	return response, nil
}
