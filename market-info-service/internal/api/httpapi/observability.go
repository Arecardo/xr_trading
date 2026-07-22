package httpapi

import (
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"xr-trading/market-info-service/internal/application"
	"xr-trading/market-info-service/internal/domain"
	"xr-trading/market-info-service/internal/observability"
)

const unmatchedRoute = "unmatched"

// NewObservabilityMiddleware adds one structured access event and recovers
// panics without exposing panic values. It must run inside WithRequestID.
func NewObservabilityMiddleware(logger *slog.Logger, now func() time.Time, observers ...observability.HTTPObserver) (func(http.Handler) http.Handler, error) {
	if logger == nil || now == nil {
		return nil, errors.New("HTTP observability logger and clock are required")
	}
	for _, observer := range observers {
		if observer == nil {
			return nil, errors.New("HTTP observer is required")
		}
	}
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
			startedAt := now().UTC()
			response := &observedResponseWriter{ResponseWriter: writer}
			requestID := RequestIDFromContext(request.Context())
			ctx := observability.WithLogger(request.Context(), logger)
			ctx = observability.WithCorrelation(ctx, observability.CorrelationFields{RequestID: requestID})
			request = request.WithContext(ctx)

			defer func() {
				recovered := recover()
				if recovered != nil {
					response.committedBeforeRecovery = response.committed
					if !response.committed {
						WriteError(response, request, application.NewError(application.ErrorCodeInternal, "internal server error", false, nil))
					}
				}
				status := response.statusCode()
				finishedAt := now().UTC()
				duration := finishedAt.Sub(startedAt)
				if duration < 0 {
					duration = 0
				}
				route := routeForLog(request)
				for _, observer := range observers {
					observer.ObserveHTTPRequest(request.Method, route, status, duration)
				}
				fields := requestCorrelation(request)
				logContext := observability.WithCorrelation(request.Context(), fields)
				attributes := []any{
					slog.String("http_method", request.Method),
					slog.String("http_route", route),
					slog.Int("status_code", status),
					slog.Int64("duration_ms", duration.Milliseconds()),
					slog.Int64("response_bytes", response.bytesWritten),
				}
				if recovered != nil {
					attributes = append(attributes, slog.Bool("panic_recovered", true), slog.Bool("response_committed", response.committedBeforeRecovery))
				}
				observability.LoggerFromContext(logContext).Log(logContext, accessLogLevel(status, recovered != nil), "http request completed", attributes...)
			}()

			if next == nil {
				WriteError(response, request, errors.New("HTTP handler is required"))
				return
			}
			next.ServeHTTP(response, request)
		})
	}, nil
}

type observedResponseWriter struct {
	http.ResponseWriter
	status                  int
	bytesWritten            int64
	committed               bool
	committedBeforeRecovery bool
}

func (writer *observedResponseWriter) WriteHeader(status int) {
	if writer.committed {
		return
	}
	writer.status = status
	writer.committed = true
	writer.ResponseWriter.WriteHeader(status)
}

func (writer *observedResponseWriter) Write(body []byte) (int, error) {
	if !writer.committed {
		writer.WriteHeader(http.StatusOK)
	}
	written, err := writer.ResponseWriter.Write(body)
	writer.bytesWritten += int64(written)
	return written, err
}

// Unwrap lets http.ResponseController retain optional response capabilities.
func (writer *observedResponseWriter) Unwrap() http.ResponseWriter {
	return writer.ResponseWriter
}

func (writer *observedResponseWriter) statusCode() int {
	if writer.status == 0 {
		return http.StatusOK
	}
	return writer.status
}

func accessLogLevel(status int, recovered bool) slog.Level {
	if recovered || status >= http.StatusInternalServerError {
		return slog.LevelError
	}
	if status == http.StatusNotFound || status < http.StatusBadRequest {
		return slog.LevelInfo
	}
	return slog.LevelWarn
}

func routeForLog(request *http.Request) string {
	if request == nil || request.Pattern == "" {
		return unmatchedRoute
	}
	if _, route, found := strings.Cut(request.Pattern, " "); found {
		return route
	}
	return request.Pattern
}

func requestCorrelation(request *http.Request) observability.CorrelationFields {
	fields := observability.CorrelationFields{}
	if request == nil {
		return fields
	}
	if request.URL == nil {
		return fields
	}
	if runID, err := domain.ParseID(firstNonEmpty(request.PathValue("run_id"), request.URL.Query().Get("run_id"))); err == nil {
		fields.RunID = runID
	}
	if taskID, err := domain.ParseID(request.PathValue("task_id")); err == nil {
		fields.TaskID = taskID
	}
	if provider, err := domain.ParseCode(request.URL.Query().Get("provider")); err == nil {
		fields.Provider = provider
	}
	if instrumentCode, err := domain.ParseCode(request.URL.Query().Get("instrument_code")); err == nil {
		fields.InstrumentCode = instrumentCode
	}
	return fields
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}
