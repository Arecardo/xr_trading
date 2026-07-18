package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"xr-trading/market-info-service/internal/application"
	"xr-trading/market-info-service/internal/domain"
	"xr-trading/market-info-service/internal/ingestion"
)

func TestBackfillRouteCreatesOneAuditedPendingTask(t *testing.T) {
	service := &backfillHTTPServiceStub{result: backfillHTTPResult()}
	handler := registeredBackfillHandler(t, service, application.PermissionIngestionManage)
	body := `{"provider":"bybit","instrument_code":"instrument.bybit.spot.btc-usdt","interval":"1h","start_time":"2026-07-01T08:00:00+08:00","end_time":"2026-07-02T08:00:00+08:00","reason":"initialize historical data"}`
	request := httptest.NewRequest(http.MethodPost, ingestionBackfillPath, strings.NewReader(body))
	request.Header.Set(authorizationHeader, "Bearer admin-token")
	request.Header.Set(RequestIDHeader, testRequestID)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusAccepted || response.Header().Get(RequestIDHeader) != testRequestID || service.calls != 1 {
		t.Fatalf("response=%d body=%s calls=%d", response.Code, response.Body.String(), service.calls)
	}
	if service.input.RequestedBy != "admin@example.com" || service.input.ActorType != "user" || service.input.RequestID != testRequestID || service.input.Reason != "initialize historical data" || service.input.StartTime.Location() != time.UTC || service.input.StartTime.Hour() != 0 {
		t.Fatalf("input = %#v", service.input)
	}
	var result struct {
		RunID     string `json:"run_id"`
		TaskID    string `json:"task_id"`
		Status    string `json:"status"`
		CreatedAt string `json:"created_at"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil || result.RunID != service.result.RunID.String() || result.TaskID != service.result.TaskID.String() || result.Status != "pending" || result.CreatedAt != "2026-07-19T12:00:00Z" {
		t.Fatalf("result=%#v err=%v", result, err)
	}
}

func TestBackfillRouteEnforcesAuthenticationAndPermission(t *testing.T) {
	service := &backfillHTTPServiceStub{result: backfillHTTPResult()}
	readOnly := registeredBackfillHandler(t, service, application.PermissionOperationsRead)
	request := httptest.NewRequest(http.MethodPost, ingestionBackfillPath, strings.NewReader(`{}`))
	response := httptest.NewRecorder()
	readOnly.ServeHTTP(response, request)
	if response.Code != http.StatusUnauthorized || service.calls != 0 {
		t.Fatalf("unauthenticated response=%d calls=%d", response.Code, service.calls)
	}
	request = httptest.NewRequest(http.MethodPost, ingestionBackfillPath, strings.NewReader(`{}`))
	request.Header.Set(authorizationHeader, "Bearer read-token")
	response = httptest.NewRecorder()
	readOnly.ServeHTTP(response, request)
	if response.Code != http.StatusForbidden || service.calls != 0 {
		t.Fatalf("forbidden response=%d body=%s calls=%d", response.Code, response.Body.String(), service.calls)
	}
}

func TestBackfillRouteRejectsInvalidAndUnknownFields(t *testing.T) {
	service := &backfillHTTPServiceStub{result: backfillHTTPResult()}
	handler := registeredBackfillHandler(t, service, application.PermissionIngestionManage)
	tests := []string{
		`{}`,
		`{"provider":"BYBIT","instrument_code":"","interval":"5m","start_time":"bad","end_time":"","reason":""}`,
		`{"provider":"bybit","instrument_code":"instrument.bybit.spot.btc-usdt","interval":"1h","start_time":"2026-07-02T00:00:00Z","end_time":"2026-07-01T00:00:00Z","reason":"history"}`,
		`{"provider":"bybit","instrument_code":"instrument.bybit.spot.btc-usdt","interval":"1h","start_time":"2026-07-01T00:00:00Z","end_time":"2026-07-02T00:00:00Z","reason":"history","ranges":[]}`,
		`[{"provider":"bybit"}]`,
	}
	for _, body := range tests {
		request := httptest.NewRequest(http.MethodPost, ingestionBackfillPath, strings.NewReader(body))
		request.Header.Set(authorizationHeader, "Bearer admin-token")
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		if response.Code != http.StatusBadRequest {
			t.Fatalf("body=%s response=%d payload=%s", body, response.Code, response.Body.String())
		}
	}
	if service.calls != 0 {
		t.Fatalf("service calls = %d", service.calls)
	}
}

func TestBackfillRouteMapsStableFailuresAndInvalidResults(t *testing.T) {
	tests := []struct {
		name       string
		err        error
		result     ingestion.BackfillResult
		wantStatus int
		wantCode   string
	}{
		{"duplicate", ingestion.ErrBackfillAlreadyRunning, ingestion.BackfillResult{}, http.StatusConflict, "BACKFILL_ALREADY_RUNNING"},
		{"missing subscription", domain.ErrNotFound, ingestion.BackfillResult{}, http.StatusNotFound, "SUBSCRIPTION_NOT_FOUND"},
		{"invalid", domain.ErrInvalidData, ingestion.BackfillResult{}, http.StatusBadRequest, "INVALID_ARGUMENT"},
		{"database", domain.ErrDatabaseUnavailable, ingestion.BackfillResult{}, http.StatusServiceUnavailable, "DATABASE_UNAVAILABLE"},
		{"invalid result", nil, ingestion.BackfillResult{}, http.StatusInternalServerError, "INTERNAL_ERROR"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			service := &backfillHTTPServiceStub{result: test.result, err: test.err}
			handler := registeredBackfillHandler(t, service, application.PermissionIngestionManage)
			body := `{"provider":"bybit","instrument_code":"instrument.bybit.spot.btc-usdt","interval":"1h","start_time":"2026-07-01T00:00:00Z","end_time":"2026-07-02T00:00:00Z","reason":"history"}`
			request := httptest.NewRequest(http.MethodPost, ingestionBackfillPath, strings.NewReader(body))
			request.Header.Set(authorizationHeader, "Bearer admin-token")
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, request)
			if response.Code != test.wantStatus || !strings.Contains(response.Body.String(), `"code":"`+test.wantCode+`"`) {
				t.Fatalf("response=%d body=%s", response.Code, response.Body.String())
			}
		})
	}
	if err := RegisterBackfillRoutes(nil, nil, nil); err == nil {
		t.Fatal("RegisterBackfillRoutes(nil) error = nil")
	}
}

type backfillHTTPServiceStub struct {
	result ingestion.BackfillResult
	err    error
	input  ingestion.BackfillInput
	calls  int
}

func (stub *backfillHTTPServiceStub) Create(_ context.Context, input ingestion.BackfillInput) (ingestion.BackfillResult, error) {
	stub.calls++
	stub.input = input
	return stub.result, stub.err
}

func registeredBackfillHandler(t *testing.T, service *backfillHTTPServiceStub, permissions ...application.Permission) http.Handler {
	t.Helper()
	principal, err := application.NewPrincipal("admin@example.com", application.ActorTypeUser, permissions...)
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	if err := RegisterBackfillRoutes(mux, service, &stubAuthenticator{principal: principal}); err != nil {
		t.Fatal(err)
	}
	return WithRequestID(mux)
}

func backfillHTTPResult() ingestion.BackfillResult {
	return ingestion.BackfillResult{
		RunID:  domain.IDFromUUID(uuid.MustParse("019f1452-90f7-7992-a87a-ca2727897501")),
		TaskID: domain.IDFromUUID(uuid.MustParse("019f1452-90f7-7992-a87a-ca2727897502")),
		Status: "pending", CreatedAt: time.Date(2026, 7, 19, 12, 0, 0, 0, time.UTC),
	}
}
