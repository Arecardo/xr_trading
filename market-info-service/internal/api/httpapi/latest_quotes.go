package httpapi

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"sort"
	"strings"

	"xr-trading/market-info-service/internal/application"
	"xr-trading/market-info-service/internal/domain"
)

const latestQuotesPath = "/api/market-info/v1/quotes/latest"

// LatestQuotesQuery is the HTTP-facing subset of the application service.
type LatestQuotesQuery interface {
	List(context.Context, application.LatestQuotesInput) (application.LatestQuotesResult, error)
}

// LatestQuotesHandler translates latest-quote query parameters and responses.
type LatestQuotesHandler struct {
	query LatestQuotesQuery
}

// NewLatestQuotesHandler constructs the public read-only quote handler.
func NewLatestQuotesHandler(query LatestQuotesQuery) (*LatestQuotesHandler, error) {
	if query == nil {
		return nil, errors.New("latest quotes query is required")
	}
	return &LatestQuotesHandler{query: query}, nil
}

// Register attaches the endpoint with a method-aware ServeMux pattern.
func (handler *LatestQuotesHandler) Register(mux *http.ServeMux) error {
	if handler == nil || handler.query == nil || mux == nil {
		return errors.New("latest quotes handler and mux are required")
	}
	mux.Handle("GET "+latestQuotesPath, handler)
	return nil
}

// ServeHTTP handles GET /api/market-info/v1/quotes/latest.
func (handler *LatestQuotesHandler) ServeHTTP(writer http.ResponseWriter, request *http.Request) {
	input, err := parseLatestQuotesRequest(request)
	if err != nil {
		WriteError(writer, request, err)
		return
	}
	result, err := handler.query.List(request.Context(), input)
	if err != nil {
		WriteError(writer, request, err)
		return
	}
	if err := WriteJSON(writer, http.StatusOK, latestQuotesResponseFromResult(result)); err != nil {
		WriteError(writer, request, err)
	}
}

type latestQuotesResponse struct {
	Asset  latestQuoteAssetResponse `json:"asset"`
	Quotes []latestQuoteResponse    `json:"quotes"`
}

type latestQuoteAssetResponse struct {
	AssetID   domain.ID   `json:"asset_id"`
	AssetCode domain.Code `json:"asset_code"`
	AssetType string      `json:"asset_type"`
}

type latestQuoteResponse struct {
	InstrumentID           domain.ID            `json:"instrument_id"`
	InstrumentCode         domain.Code          `json:"instrument_code"`
	Provider               domain.Code          `json:"provider"`
	ProviderInstrumentID   domain.ID            `json:"provider_instrument_id"`
	ProviderInstrumentCode domain.Code          `json:"provider_instrument_code"`
	ProviderSymbol         string               `json:"provider_symbol"`
	Price                  domain.Decimal       `json:"price"`
	BidPrice               *domain.Decimal      `json:"bid_price"`
	BidSize                *domain.Decimal      `json:"bid_size"`
	AskPrice               *domain.Decimal      `json:"ask_price"`
	AskSize                *domain.Decimal      `json:"ask_size"`
	Open24H                *domain.Decimal      `json:"open_24h"`
	High24H                *domain.Decimal      `json:"high_24h"`
	Low24H                 *domain.Decimal      `json:"low_24h"`
	BaseVolume24H          *domain.Decimal      `json:"base_volume_24h"`
	QuoteVolume24H         *domain.Decimal      `json:"quote_volume_24h"`
	QuoteCurrency          string               `json:"quote_currency"`
	MarketTime             domain.UTCInstant    `json:"market_time"`
	ReceivedAt             domain.UTCInstant    `json:"received_at"`
	QualityStatus          domain.QualityStatus `json:"quality_status"`
}

func parseLatestQuotesRequest(request *http.Request) (application.LatestQuotesInput, error) {
	if request == nil || request.URL == nil {
		return application.LatestQuotesInput{}, errors.New("HTTP request is required")
	}
	values, err := url.ParseQuery(request.URL.RawQuery)
	if err != nil {
		return application.LatestQuotesInput{}, application.ValidationError([]application.FieldViolation{{Field: "query", Reason: "is malformed"}})
	}
	if err := validateLatestQuotesQueryKeys(values); err != nil {
		return application.LatestQuotesInput{}, err
	}
	assetCode, err := singleQueryValue(values, "asset_code", false)
	if err != nil {
		return application.LatestQuotesInput{}, err
	}
	instrumentCode, err := singleQueryValue(values, "instrument_code", false)
	if err != nil {
		return application.LatestQuotesInput{}, err
	}
	providerCode, err := singleQueryValue(values, "provider", false)
	if err != nil {
		return application.LatestQuotesInput{}, err
	}
	return application.LatestQuotesInput{AssetCode: assetCode, InstrumentCode: instrumentCode, ProviderCode: providerCode}, nil
}

func validateLatestQuotesQueryKeys(values url.Values) error {
	allowed := map[string]struct{}{"asset_code": {}, "instrument_code": {}, "provider": {}}
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

func latestQuotesResponseFromResult(result application.LatestQuotesResult) latestQuotesResponse {
	response := latestQuotesResponse{
		Asset:  latestQuoteAssetResponse{AssetID: result.Asset.ID, AssetCode: result.Asset.Code, AssetType: strings.ToLower(string(result.Asset.AssetType))},
		Quotes: make([]latestQuoteResponse, 0, len(result.Quotes)),
	}
	for _, record := range result.Quotes {
		quote := record.Quote
		response.Quotes = append(response.Quotes, latestQuoteResponse{
			InstrumentID: quote.InstrumentID, InstrumentCode: record.InstrumentCode, Provider: record.ProviderCode,
			ProviderInstrumentID: quote.ProviderInstrumentID, ProviderInstrumentCode: record.ProviderInstrumentCode,
			ProviderSymbol: record.ProviderSymbol, Price: quote.LastPrice,
			BidPrice: quote.BidPrice, BidSize: quote.BidSize, AskPrice: quote.AskPrice, AskSize: quote.AskSize,
			Open24H: quote.Open24H, High24H: quote.High24H, Low24H: quote.Low24H,
			BaseVolume24H: quote.BaseVolume24H, QuoteVolume24H: quote.QuoteVolume24H,
			QuoteCurrency: record.QuoteCurrency, MarketTime: quote.MarketTime, ReceivedAt: quote.CollectedAt,
			QualityStatus: quote.QualityStatus,
		})
	}
	return response
}
