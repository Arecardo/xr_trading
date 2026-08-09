package httpapi

import (
	"context"
	"errors"
	"net/http"

	"xr-trading/market-info-service/internal/application"
	"xr-trading/market-info-service/internal/domain"
)

// instrumentPrecisionBatchPath is CONTRACT-005's frozen route. It reuses the
// existing GET /instruments route prefix but is a distinct POST endpoint: the
// list of instrument_ids is a batch query body, not a single-resource path.
const instrumentPrecisionBatchPath = "/api/market-info/v1/instruments/precision:batch"

// InstrumentPrecisionQuery is the HTTP-facing subset of the application service.
type InstrumentPrecisionQuery interface {
	Batch(context.Context, application.InstrumentPrecisionInput) (application.InstrumentPrecisionResult, error)
}

// InstrumentPrecisionHandler translates the CONTRACT-005 batch precision
// query protocol. Like /instruments, /quotes/latest and /bars it is a public,
// read-only query and carries no authentication middleware.
type InstrumentPrecisionHandler struct {
	query InstrumentPrecisionQuery
}

// NewInstrumentPrecisionHandler constructs the public read-only handler.
func NewInstrumentPrecisionHandler(query InstrumentPrecisionQuery) (*InstrumentPrecisionHandler, error) {
	if query == nil {
		return nil, errors.New("instrument precision query is required")
	}
	return &InstrumentPrecisionHandler{query: query}, nil
}

// Register attaches the endpoint with a method-aware ServeMux pattern.
func (handler *InstrumentPrecisionHandler) Register(mux *http.ServeMux) error {
	if handler == nil || handler.query == nil || mux == nil {
		return errors.New("instrument precision handler and mux are required")
	}
	mux.Handle("POST "+instrumentPrecisionBatchPath, handler)
	return nil
}

// ServeHTTP handles POST /api/market-info/v1/instruments/precision:batch.
func (handler *InstrumentPrecisionHandler) ServeHTTP(writer http.ResponseWriter, request *http.Request) {
	var body instrumentPrecisionBatchRequest
	if err := DecodeJSON(writer, request, &body, DefaultMaximumRequestBodyBytes); err != nil {
		WriteError(writer, request, err)
		return
	}
	result, err := handler.query.Batch(request.Context(), application.InstrumentPrecisionInput{InstrumentIDs: body.InstrumentIDs})
	if err != nil {
		WriteError(writer, request, err)
		return
	}
	response, err := instrumentPrecisionBatchResponseFromResult(result)
	if err != nil {
		WriteError(writer, request, err)
		return
	}
	if err := WriteJSON(writer, http.StatusOK, response); err != nil {
		WriteError(writer, request, err)
	}
}

type instrumentPrecisionBatchRequest struct {
	InstrumentIDs []string `json:"instrument_ids"`
}

type instrumentPrecisionBatchResponse struct {
	Items                []instrumentPrecisionItemResponse `json:"items"`
	MissingInstrumentIDs []domain.ID                       `json:"missing_instrument_ids"`
}

type instrumentPrecisionItemResponse struct {
	InstrumentID   domain.ID      `json:"instrument_id"`
	InstrumentCode domain.Code    `json:"instrument_code"`
	PriceScale     int16          `json:"price_scale"`
	QuantityScale  int16          `json:"quantity_scale"`
	LotSize        domain.Decimal `json:"lot_size"`
	MinQuantity    domain.Decimal `json:"min_quantity"`
	AsOf           timeValue      `json:"as_of"`
}

func instrumentPrecisionBatchResponseFromResult(result application.InstrumentPrecisionResult) (instrumentPrecisionBatchResponse, error) {
	response := instrumentPrecisionBatchResponse{
		Items:                make([]instrumentPrecisionItemResponse, 0, len(result.Items)),
		MissingInstrumentIDs: make([]domain.ID, 0, len(result.MissingInstrumentIDs)),
	}
	for _, item := range result.Items {
		response.Items = append(response.Items, instrumentPrecisionItemResponse{
			InstrumentID: item.InstrumentID, InstrumentCode: item.InstrumentCode,
			PriceScale: item.PriceScale, QuantityScale: item.QuantityScale,
			LotSize: item.LotSize, MinQuantity: item.MinQuantity, AsOf: timeValue{item.AsOf},
		})
	}
	response.MissingInstrumentIDs = append(response.MissingInstrumentIDs, result.MissingInstrumentIDs...)
	return response, nil
}
