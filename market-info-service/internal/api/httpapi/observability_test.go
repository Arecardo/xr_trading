package httpapi

import (
	"bufio"
	"bytes"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"xr-trading/market-info-service/internal/application"
	"xr-trading/market-info-service/internal/observability"
)

func TestObservabilityMiddlewareLogsLevelsRoutesAndCorrelation(t *testing.T) {
	t.Parallel()

	var output bytes.Buffer
	logger := observability.NewJSONLogger(&output, slog.LevelDebug)
	var ticks atomic.Int64
	start := time.Date(2026, 7, 23, 0, 0, 0, 0, time.UTC)
	now := func() time.Time { return start.Add(time.Duration(ticks.Add(1)) * 25 * time.Millisecond) }
	middleware, err := NewObservabilityMiddleware(logger, now)
	if err != nil {
		t.Fatalf("NewObservabilityMiddleware() error = %v", err)
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /ok", func(writer http.ResponseWriter, _ *http.Request) { writer.WriteHeader(http.StatusNoContent) })
	mux.HandleFunc("GET /bad", func(writer http.ResponseWriter, _ *http.Request) { WriteError(writer, nil, errInvalidTestRequest) })
	mux.HandleFunc("GET /tasks/{task_id}", func(writer http.ResponseWriter, _ *http.Request) { writer.WriteHeader(http.StatusNoContent) })
	handler := fixedRequestIDMiddleware(t, middleware(mux))

	requests := []struct {
		path       string
		wantStatus int
	}{
		{"/ok?access_token=must-not-appear", http.StatusNoContent},
		{"/missing?secret=must-not-appear", http.StatusNotFound},
		{"/bad", http.StatusBadRequest},
		{"/tasks/019f1452-90f7-7992-a87a-ca2727899301?run_id=019f1452-90f7-7992-a87a-ca2727899302&provider=bybit&instrument_code=instrument.bybit.spot.btc-usdt", http.StatusNoContent},
	}
	for _, item := range requests {
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, item.path, nil))
		if response.Code != item.wantStatus {
			t.Fatalf("GET %s status = %d", item.path, response.Code)
		}
	}

	entries := decodeLogEntries(t, output.String())
	if len(entries) != 4 {
		t.Fatalf("log entries = %d, output=%s", len(entries), output.String())
	}
	assertLogFields(t, entries[0], map[string]any{"level": "INFO", "http_route": "/ok", "status_code": float64(204), "duration_ms": float64(25), "request_id": testRequestID})
	assertLogFields(t, entries[1], map[string]any{"level": "INFO", "http_route": unmatchedRoute, "status_code": float64(404)})
	assertLogFields(t, entries[2], map[string]any{"level": "WARN", "http_route": "/bad", "status_code": float64(400)})
	assertLogFields(t, entries[3], map[string]any{
		"level": "INFO", "http_route": "/tasks/{task_id}", "task_id": "019f1452-90f7-7992-a87a-ca2727899301",
		"run_id": "019f1452-90f7-7992-a87a-ca2727899302", "provider": "bybit", "instrument_code": "instrument.bybit.spot.btc-usdt",
	})
	if strings.Contains(output.String(), "must-not-appear") || strings.Contains(output.String(), "access_token") {
		t.Fatalf("HTTP log leaked query data: %s", output.String())
	}
}

func TestObservabilityMiddlewareRecoversPanicsWithoutLeakingValue(t *testing.T) {
	t.Parallel()

	var output bytes.Buffer
	logger := observability.NewJSONLogger(&output, slog.LevelInfo)
	now := steppedHTTPClock()
	middleware, _ := NewObservabilityMiddleware(logger, now)
	handler := fixedRequestIDMiddleware(t, middleware(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		panic("provider-token-must-not-leak")
	})))

	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/panic", nil))
	if response.Code != http.StatusInternalServerError || !strings.Contains(response.Body.String(), `"code":"INTERNAL_ERROR"`) || !strings.Contains(response.Body.String(), `"request_id":"`+testRequestID+`"`) {
		t.Fatalf("panic response = status %d body %s", response.Code, response.Body.String())
	}
	if strings.Contains(output.String(), "provider-token-must-not-leak") {
		t.Fatalf("panic value leaked: %s", output.String())
	}
	entries := decodeLogEntries(t, output.String())
	assertLogFields(t, entries[0], map[string]any{"level": "ERROR", "status_code": float64(500), "panic_recovered": true, "response_committed": false})
}

func TestBackfillBusinessEventSharesRequestRunTaskAndSourceFields(t *testing.T) {
	t.Parallel()

	var output bytes.Buffer
	middleware, _ := NewObservabilityMiddleware(observability.NewJSONLogger(&output, slog.LevelInfo), steppedHTTPClock())
	principal, err := application.NewPrincipal("admin@example.com", application.ActorTypeUser, application.PermissionIngestionManage)
	if err != nil {
		t.Fatal(err)
	}
	service := &backfillHTTPServiceStub{result: backfillHTTPResult()}
	mux := http.NewServeMux()
	if err := RegisterBackfillRoutes(mux, service, &stubAuthenticator{principal: principal}); err != nil {
		t.Fatal(err)
	}
	handler := fixedRequestIDMiddleware(t, middleware(mux))
	body := `{"provider":"bybit","instrument_code":"instrument.bybit.spot.btc-usdt","interval":"1h","start_time":"2026-07-01T00:00:00Z","end_time":"2026-07-02T00:00:00Z","reason":"operator-reason-must-not-log"}`
	request := httptest.NewRequest(http.MethodPost, ingestionBackfillPath, strings.NewReader(body))
	request.Header.Set(authorizationHeader, "Bearer admin-token")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusAccepted {
		t.Fatalf("backfill status = %d body=%s", response.Code, response.Body.String())
	}
	entries := decodeLogEntries(t, output.String())
	if len(entries) != 2 {
		t.Fatalf("backfill log entries = %d output=%s", len(entries), output.String())
	}
	assertLogFields(t, entries[0], map[string]any{
		"msg": "ingestion backfill accepted", "request_id": testRequestID,
		"run_id": service.result.RunID.String(), "task_id": service.result.TaskID.String(),
		"provider": "bybit", "instrument_code": "instrument.bybit.spot.btc-usdt", "interval": "1h",
	})
	assertLogFields(t, entries[1], map[string]any{"msg": "http request completed", "request_id": testRequestID, "status_code": float64(202)})
	if strings.Contains(output.String(), "operator-reason-must-not-log") || strings.Contains(output.String(), "admin-token") {
		t.Fatalf("business event leaked request data: %s", output.String())
	}
}

func TestObservabilityMiddlewareHandlesCommittedPanicAndNilDependencies(t *testing.T) {
	t.Parallel()

	if _, err := NewObservabilityMiddleware(nil, time.Now); err == nil {
		t.Fatal("NewObservabilityMiddleware(nil logger) error = nil")
	}
	if _, err := NewObservabilityMiddleware(slog.Default(), nil); err == nil {
		t.Fatal("NewObservabilityMiddleware(nil clock) error = nil")
	}

	var output bytes.Buffer
	middleware, _ := NewObservabilityMiddleware(observability.NewJSONLogger(&output, slog.LevelInfo), steppedHTTPClock())
	handler := fixedRequestIDMiddleware(t, middleware(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.WriteHeader(http.StatusAccepted)
		panic("after commit")
	})))
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/committed", nil))
	if response.Code != http.StatusAccepted {
		t.Fatalf("committed panic status = %d", response.Code)
	}
	entries := decodeLogEntries(t, output.String())
	assertLogFields(t, entries[0], map[string]any{"level": "ERROR", "status_code": float64(202), "panic_recovered": true, "response_committed": true})

	output.Reset()
	handler = fixedRequestIDMiddleware(t, middleware(nil))
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/nil", nil))
	if response.Code != http.StatusInternalServerError {
		t.Fatalf("nil next status = %d", response.Code)
	}
}

func fixedRequestIDMiddleware(t *testing.T, next http.Handler) http.Handler {
	t.Helper()
	middleware, err := NewRequestIDMiddleware(func() (string, error) { return testRequestID, nil })
	if err != nil {
		t.Fatalf("NewRequestIDMiddleware() error = %v", err)
	}
	return middleware(next)
}

func steppedHTTPClock() func() time.Time {
	var ticks atomic.Int64
	base := time.Date(2026, 7, 23, 0, 0, 0, 0, time.UTC)
	return func() time.Time { return base.Add(time.Duration(ticks.Add(1)) * time.Millisecond) }
}

func decodeLogEntries(t *testing.T, output string) []map[string]any {
	t.Helper()
	entries := make([]map[string]any, 0)
	scanner := bufio.NewScanner(strings.NewReader(output))
	for scanner.Scan() {
		var entry map[string]any
		if err := json.Unmarshal(scanner.Bytes(), &entry); err != nil {
			t.Fatalf("decode log %q: %v", scanner.Text(), err)
		}
		entries = append(entries, entry)
	}
	if err := scanner.Err(); err != nil {
		t.Fatalf("scan logs: %v", err)
	}
	return entries
}

func assertLogFields(t *testing.T, actual map[string]any, expected map[string]any) {
	t.Helper()
	for key, value := range expected {
		if actual[key] != value {
			t.Fatalf("log[%s] = %#v, want %#v; entry=%#v", key, actual[key], value, actual)
		}
	}
}

var errInvalidTestRequest = applicationValidationError()

func applicationValidationError() error {
	return application.ValidationError([]application.FieldViolation{{Field: "test", Reason: "invalid"}})
}
