package httpapi

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"sort"

	"xr-trading/market-info-service/internal/application"
	"xr-trading/market-info-service/internal/domain"
)

const instrumentOptionsPath = "/api/market-info/v1/instruments"
const instrumentOptionsCursorScope = "instruments.enabled"

var instrumentOptionsPageLimits = PageLimits{Default: 50, Maximum: application.MaximumInstrumentOptionsPageSize}

// InstrumentOptionsQuery is the HTTP-facing subset of the application service.
type InstrumentOptionsQuery interface {
	List(context.Context, application.InstrumentOptionsInput) (application.InstrumentOptionsPage, error)
}

// InstrumentOptionsHandler translates the public option-query protocol.
type InstrumentOptionsHandler struct {
	query InstrumentOptionsQuery
}

// NewInstrumentOptionsHandler constructs the public read-only handler.
func NewInstrumentOptionsHandler(query InstrumentOptionsQuery) (*InstrumentOptionsHandler, error) {
	if query == nil {
		return nil, errors.New("instrument options query is required")
	}
	return &InstrumentOptionsHandler{query: query}, nil
}

// Register attaches the endpoint with a method-aware ServeMux pattern.
func (handler *InstrumentOptionsHandler) Register(mux *http.ServeMux) error {
	if handler == nil || handler.query == nil || mux == nil {
		return errors.New("instrument options handler and mux are required")
	}
	mux.Handle("GET "+instrumentOptionsPath, handler)
	return nil
}

// ServeHTTP handles GET /api/market-info/v1/instruments.
func (handler *InstrumentOptionsHandler) ServeHTTP(writer http.ResponseWriter, request *http.Request) {
	input, err := parseInstrumentOptionsRequest(request)
	if err != nil {
		WriteError(writer, request, err)
		return
	}
	page, err := handler.query.List(request.Context(), input)
	if err != nil {
		WriteError(writer, request, err)
		return
	}
	response, err := instrumentOptionsResponseFromPage(input.AssetCode, page)
	if err != nil {
		WriteError(writer, request, err)
		return
	}
	if err := WriteJSON(writer, http.StatusOK, response); err != nil {
		WriteError(writer, request, err)
	}
}

type instrumentOptionsResponse struct {
	Items      []instrumentOptionResponse `json:"items"`
	NextCursor *string                    `json:"next_cursor"`
}

type instrumentOptionResponse struct {
	InstrumentID   domain.ID                `json:"instrument_id"`
	InstrumentCode domain.Code              `json:"instrument_code"`
	DisplayName    string                   `json:"display_name"`
	Providers      []providerOptionResponse `json:"providers"`
}

type providerOptionResponse struct {
	ProviderCode       domain.Code          `json:"provider_code"`
	DisplayName        string               `json:"display_name"`
	IsDefault          bool                 `json:"is_default"`
	Priority           int16                `json:"priority"`
	SupportedIntervals []domain.BarInterval `json:"supported_intervals"`
}

func parseInstrumentOptionsRequest(request *http.Request) (application.InstrumentOptionsInput, error) {
	if request == nil || request.URL == nil {
		return application.InstrumentOptionsInput{}, errors.New("HTTP request is required")
	}
	values, err := url.ParseQuery(request.URL.RawQuery)
	if err != nil {
		return application.InstrumentOptionsInput{}, application.ValidationError([]application.FieldViolation{{Field: "query", Reason: "is malformed"}})
	}
	if err := validateInstrumentOptionsQueryKeys(values); err != nil {
		return application.InstrumentOptionsInput{}, err
	}
	assetCode, err := singleQueryValue(values, "asset_code", true)
	if err != nil {
		return application.InstrumentOptionsInput{}, err
	}
	enabled, err := singleQueryValue(values, "enabled", false)
	if err != nil {
		return application.InstrumentOptionsInput{}, err
	}
	if enabled != "" && enabled != "true" {
		return application.InstrumentOptionsInput{}, application.ValidationError([]application.FieldViolation{{Field: "enabled", Reason: "only true is supported"}})
	}
	limitValue, err := singleQueryValue(values, "limit", false)
	if err != nil {
		return application.InstrumentOptionsInput{}, err
	}
	limit, err := ParsePageSize(limitValue, instrumentOptionsPageLimits)
	if err != nil {
		return application.InstrumentOptionsInput{}, err
	}

	input := application.InstrumentOptionsInput{AssetCode: assetCode, Limit: limit}
	cursor, err := singleQueryValue(values, "cursor", false)
	if err != nil {
		return application.InstrumentOptionsInput{}, err
	}
	if cursor != "" {
		positions, decodeErr := DecodeCursor(cursor, instrumentOptionsCursorScope, 2)
		if decodeErr != nil {
			return application.InstrumentOptionsInput{}, decodeErr
		}
		if positions[0] != assetCode {
			return application.InstrumentOptionsInput{}, invalidCursor()
		}
		input.AfterInstrumentCode = positions[1]
	}
	return input, nil
}

func validateInstrumentOptionsQueryKeys(values url.Values) error {
	allowed := map[string]struct{}{"asset_code": {}, "enabled": {}, "limit": {}, "cursor": {}}
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

func singleQueryValue(values url.Values, field string, required bool) (string, error) {
	items, exists := values[field]
	if !exists {
		if required {
			return "", application.ValidationError([]application.FieldViolation{{Field: field, Reason: "is required"}})
		}
		return "", nil
	}
	if len(items) != 1 || items[0] == "" {
		return "", application.ValidationError([]application.FieldViolation{{Field: field, Reason: "must be provided exactly once with a non-empty value"}})
	}
	return items[0], nil
}

func instrumentOptionsResponseFromPage(assetCode string, page application.InstrumentOptionsPage) (instrumentOptionsResponse, error) {
	response := instrumentOptionsResponse{Items: make([]instrumentOptionResponse, 0, len(page.Items))}
	for _, item := range page.Items {
		providers := make([]providerOptionResponse, 0, len(item.Providers))
		for _, provider := range item.Providers {
			providers = append(providers, providerOptionResponse{
				ProviderCode: provider.Code, DisplayName: provider.DisplayName, IsDefault: provider.IsDefault,
				Priority: provider.Priority, SupportedIntervals: append([]domain.BarInterval(nil), provider.SupportedIntervals...),
			})
		}
		response.Items = append(response.Items, instrumentOptionResponse{
			InstrumentID: item.ID, InstrumentCode: item.Code, DisplayName: item.DisplayName, Providers: providers,
		})
	}
	if page.NextAfterInstrumentCode != nil {
		cursor, err := EncodeCursor(instrumentOptionsCursorScope, assetCode, page.NextAfterInstrumentCode.String())
		if err != nil {
			return instrumentOptionsResponse{}, err
		}
		response.NextCursor = &cursor
	}
	return response, nil
}
