// Package httpapi contains protocol-level components shared by market-info
// HTTP handlers.
package httpapi

import (
	"encoding/json"
	"errors"
	"net/http"

	"xr-trading/market-info-service/internal/application"
	"xr-trading/market-info-service/internal/domain"
)

// ErrorEnvelope is the only public HTTP error shape.
type ErrorEnvelope struct {
	Error ErrorBody `json:"error"`
}

// ErrorBody contains safe client-facing failure information.
type ErrorBody struct {
	Code      application.ErrorCode `json:"code"`
	Message   string                `json:"message"`
	Retryable bool                  `json:"retryable"`
	Details   map[string]any        `json:"details,omitempty"`
	RequestID string                `json:"request_id"`
}

// WriteError maps an application or domain error to a safe public envelope.
func WriteError(writer http.ResponseWriter, request *http.Request, err error) {
	requestID := requestIDForResponse(request)
	status, body := classifyError(err)
	body.RequestID = requestID
	writer.Header().Set(RequestIDHeader, requestID)
	if writeErr := WriteJSON(writer, status, ErrorEnvelope{Error: body}); writeErr == nil {
		return
	}

	// Unsafe or unsupported details must not make error handling fail open.
	fallback := ErrorEnvelope{Error: ErrorBody{
		Code: application.ErrorCodeInternal, Message: "internal server error",
		Retryable: false, RequestID: requestID,
	}}
	_ = WriteJSON(writer, http.StatusInternalServerError, fallback)
}

// WriteJSON encodes a response before writing headers, preventing a marshal
// failure from committing a misleading success status.
func WriteJSON(writer http.ResponseWriter, status int, payload any) error {
	encoded, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	encoded = append(encoded, '\n')
	writer.Header().Set("Content-Type", "application/json; charset=utf-8")
	writer.Header().Set("X-Content-Type-Options", "nosniff")
	writer.WriteHeader(status)
	_, err = writer.Write(encoded)
	return err
}

func classifyError(err error) (int, ErrorBody) {
	var appError *application.Error
	if errors.As(err, &appError) {
		if appError.Validate() != nil {
			return http.StatusInternalServerError, ErrorBody{Code: application.ErrorCodeInternal, Message: "internal server error", Retryable: false}
		}
		return statusForCode(appError.Code), ErrorBody{Code: appError.Code, Message: appError.Message, Retryable: appError.Retryable, Details: appError.Details}
	}

	switch {
	case errors.Is(err, domain.ErrInvalidData):
		return http.StatusBadRequest, ErrorBody{Code: application.ErrorCodeInvalidArgument, Message: "invalid request", Retryable: false}
	case errors.Is(err, domain.ErrNotFound):
		return http.StatusNotFound, ErrorBody{Code: application.ErrorCodeNotFound, Message: "resource not found", Retryable: false}
	case errors.Is(err, domain.ErrConflict), errors.Is(err, domain.ErrReferenceViolation), errors.Is(err, domain.ErrInvalidState):
		return http.StatusConflict, ErrorBody{Code: application.ErrorCodeConflict, Message: "request conflicts with current state", Retryable: false}
	case errors.Is(err, domain.ErrDatabaseUnavailable):
		return http.StatusServiceUnavailable, ErrorBody{Code: application.ErrorCodeDatabaseUnavailable, Message: "database unavailable", Retryable: true}
	case errors.Is(err, domain.ErrRetryable):
		return http.StatusServiceUnavailable, ErrorBody{Code: application.ErrorCodeServiceUnavailable, Message: "service temporarily unavailable", Retryable: true}
	default:
		return http.StatusInternalServerError, ErrorBody{Code: application.ErrorCodeInternal, Message: "internal server error", Retryable: false}
	}
}

func statusForCode(code application.ErrorCode) int {
	switch code {
	case application.ErrorCodeInvalidArgument, application.ErrorCodeInvalidTimeRange, application.ErrorCodeUnsupportedInterval:
		return http.StatusBadRequest
	case application.ErrorCodeUnauthenticated:
		return http.StatusUnauthorized
	case application.ErrorCodePermissionDenied:
		return http.StatusForbidden
	case application.ErrorCodeNotFound, application.ErrorCodeAssetNotFound, application.ErrorCodeInstrumentNotFound,
		application.ErrorCodeSubscriptionNotFound, application.ErrorCodeTaskNotFound:
		return http.StatusNotFound
	case application.ErrorCodeConflict, application.ErrorCodeSubscriptionAlreadyExists, application.ErrorCodeTaskStateConflict,
		application.ErrorCodeBackfillAlreadyRunning, application.ErrorCodeManualRetryAlreadyRunning:
		return http.StatusConflict
	case application.ErrorCodeRateLimited:
		return http.StatusTooManyRequests
	case application.ErrorCodeServiceUnavailable, application.ErrorCodeDatabaseUnavailable:
		return http.StatusServiceUnavailable
	default:
		return http.StatusInternalServerError
	}
}
