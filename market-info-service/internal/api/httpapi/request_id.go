package httpapi

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/google/uuid"

	"xr-trading/market-info-service/internal/application"
	"xr-trading/market-info-service/internal/domain"
)

const RequestIDHeader = "X-Request-ID"
const requestIDPrefix = "req_"
const fallbackRequestID = "req_00000000-0000-7000-8000-000000000000"

type requestIDContextKey struct{}

// RequestIDGenerator creates a canonical req_<UUIDv7> value.
type RequestIDGenerator func() (string, error)

// WithRequestID adds the production Request ID middleware.
func WithRequestID(next http.Handler) http.Handler {
	middleware, _ := NewRequestIDMiddleware(defaultRequestID)
	return middleware(next)
}

// NewRequestIDMiddleware constructs an injectable middleware for tests and
// alternate process boundaries.
func NewRequestIDMiddleware(generator RequestIDGenerator) (func(http.Handler) http.Handler, error) {
	if generator == nil {
		return nil, errors.New("request ID generator is required")
	}
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
			requestID := request.Header.Get(RequestIDHeader)
			if !ValidRequestID(requestID) {
				generated, err := generator()
				if err != nil || !ValidRequestID(generated) {
					ctx := context.WithValue(request.Context(), requestIDContextKey{}, fallbackRequestID)
					request = request.WithContext(ctx)
					cause := err
					if cause == nil {
						cause = errors.New("request ID generator returned invalid value")
					}
					WriteError(writer, request, application.WrapError(cause, application.ErrorCodeInternal, "internal server error", false, nil))
					return
				}
				requestID = generated
			}
			ctx := context.WithValue(request.Context(), requestIDContextKey{}, requestID)
			writer.Header().Set(RequestIDHeader, requestID)
			if next == nil {
				WriteError(writer, request.WithContext(ctx), errors.New("HTTP handler is required"))
				return
			}
			next.ServeHTTP(writer, request.WithContext(ctx))
		})
	}, nil
}

// RequestIDFromContext returns the current request correlation identity.
func RequestIDFromContext(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	requestID, _ := ctx.Value(requestIDContextKey{}).(string)
	return requestID
}

// ValidRequestID accepts only a canonical UUIDv7 with the public req_ prefix.
func ValidRequestID(value string) bool {
	if !strings.HasPrefix(value, requestIDPrefix) {
		return false
	}
	id, err := domain.ParseID(strings.TrimPrefix(value, requestIDPrefix))
	return err == nil && id.UUID().Version() == uuid.Version(7)
}

func defaultRequestID() (string, error) {
	id, err := domain.NewID()
	if err != nil {
		return "", fmt.Errorf("generate request ID: %w", err)
	}
	return requestIDPrefix + id.String(), nil
}

func requestIDForResponse(request *http.Request) string {
	if request != nil {
		if requestID := RequestIDFromContext(request.Context()); ValidRequestID(requestID) {
			return requestID
		}
	}
	requestID, err := defaultRequestID()
	if err != nil {
		return fallbackRequestID
	}
	return requestID
}
