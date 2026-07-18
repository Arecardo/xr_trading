package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"xr-trading/market-info-service/internal/application"
	"xr-trading/market-info-service/internal/domain"
	"xr-trading/market-info-service/internal/ingestion"
)

func TestIngestionQueryRoutesListRunsWithScopedCursor(t *testing.T) {
	service, run, _ := ingestionHTTPFixture(t)
	service.runPage = application.RunPage{Items: []application.RunRecord{run}, NextAfterID: &run.Run.ID}
	handler := registeredIngestionQueryHandler(t, service, application.PermissionOperationsRead)
	values := url.Values{"run_type": {"backfill"}, "trigger_type": {"manual"}, "status": {"running"}, "requested_by": {"admin@example.com"}, "created_from": {"2026-07-18T20:00:00-04:00"}, "created_to": {"2026-07-20T00:00:00Z"}, "limit": {"1"}}
	request := httptest.NewRequest(http.MethodGet, ingestionRunsPath+"?"+values.Encode(), nil)
	request.Header.Set(authorizationHeader, "Bearer read-token")
	request.Header.Set(RequestIDHeader, testRequestID)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK || service.runInput.Limit != 1 || service.runInput.CreatedFrom.Location() != time.UTC {
		t.Fatalf("response=%d body=%s input=%#v", response.Code, response.Body.String(), service.runInput)
	}
	var body struct {
		Items []struct {
			RunID        string            `json:"run_id"`
			Status       string            `json:"status"`
			RunningCount int               `json:"running_count"`
			Context      map[string]string `json:"context"`
		} `json:"items"`
		NextCursor *string `json:"next_cursor"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil || len(body.Items) != 1 || body.Items[0].Status != "running" || body.Items[0].RunningCount != 1 || body.NextCursor == nil {
		t.Fatalf("body=%#v err=%v payload=%s", body, err, response.Body.String())
	}
	values.Set("cursor", *body.NextCursor)
	second := httptest.NewRequest(http.MethodGet, ingestionRunsPath+"?"+values.Encode(), nil)
	second.Header.Set(authorizationHeader, "Bearer read-token")
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, second)
	if response.Code != http.StatusOK || service.runInput.AfterID == nil || *service.runInput.AfterID != run.Run.ID {
		t.Fatalf("cursor response=%d body=%s input=%#v", response.Code, response.Body.String(), service.runInput)
	}
}

func TestIngestionQueryRoutesReturnRunAndTaskDetails(t *testing.T) {
	service, run, task := ingestionHTTPFixture(t)
	service.run = run
	service.task = task
	handler := registeredIngestionQueryHandler(t, service, application.PermissionOperationsRead)
	for _, test := range []struct{ path, field string }{{ingestionRunsPath + "/" + run.Run.ID.String(), `"run"`}, {ingestionTasksPath + "/" + task.Task.ID.String(), `"task"`}} {
		request := httptest.NewRequest(http.MethodGet, test.path, nil)
		request.Header.Set(authorizationHeader, "Bearer read-token")
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), test.field) {
			t.Fatalf("GET %s response=%d body=%s", test.path, response.Code, response.Body.String())
		}
		if strings.Contains(response.Body.String(), "raw secret") || strings.Contains(response.Body.String(), "token") {
			t.Fatalf("detail leaked unsafe fields: %s", response.Body.String())
		}
	}
}

func TestIngestionQueryRoutesListTasksWithFiltersAndCursor(t *testing.T) {
	service, run, task := ingestionHTTPFixture(t)
	service.taskPage = application.TaskPage{Items: []application.TaskRecord{task}, NextAfterID: &task.Task.ID}
	handler := registeredIngestionQueryHandler(t, service, application.PermissionOperationsRead)
	values := url.Values{"run_id": {run.Run.ID.String()}, "status": {"retry_wait"}, "provider": {"bybit"}, "instrument_code": {"instrument.bybit.spot.btc-usdt"}, "interval": {"1h"}, "created_from": {"2026-07-18T00:00:00Z"}, "created_to": {"2026-07-20T00:00:00Z"}, "limit": {"1"}}
	request := httptest.NewRequest(http.MethodGet, ingestionTasksPath+"?"+values.Encode(), nil)
	request.Header.Set(authorizationHeader, "Bearer read-token")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK || service.taskInput.RunID == nil || *service.taskInput.RunID != run.Run.ID || service.taskInput.ProviderCode != "bybit" {
		t.Fatalf("response=%d body=%s input=%#v", response.Code, response.Body.String(), service.taskInput)
	}
	var body struct {
		NextCursor *string `json:"next_cursor"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil || body.NextCursor == nil || !strings.Contains(response.Body.String(), `"error_summary":"provider network request failed"`) {
		t.Fatalf("body=%s err=%v", response.Body.String(), err)
	}
	values.Set("cursor", *body.NextCursor)
	second := httptest.NewRequest(http.MethodGet, ingestionTasksPath+"?"+values.Encode(), nil)
	second.Header.Set(authorizationHeader, "Bearer read-token")
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, second)
	if response.Code != http.StatusOK || service.taskInput.AfterID == nil || *service.taskInput.AfterID != task.Task.ID {
		t.Fatalf("cursor response=%d body=%s input=%#v", response.Code, response.Body.String(), service.taskInput)
	}
}

func TestIngestionQueryRoutesEnforceAuthAndRejectInvalidProtocol(t *testing.T) {
	service, run, _ := ingestionHTTPFixture(t)
	handler := registeredIngestionQueryHandler(t, service, application.PermissionSubscriptionsManage)
	request := httptest.NewRequest(http.MethodGet, ingestionRunsPath, nil)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated response=%d", response.Code)
	}
	request = httptest.NewRequest(http.MethodGet, ingestionRunsPath, nil)
	request.Header.Set(authorizationHeader, "Bearer token")
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusForbidden {
		t.Fatalf("forbidden response=%d", response.Code)
	}
	handler = registeredIngestionQueryHandler(t, service, application.PermissionOperationsRead)
	badCursor, _ := EncodeCursor(ingestionRunsCursorScope, "different", "", "", "", "", "", run.Run.ID.String())
	paths := []string{
		ingestionRunsPath + "?unknown=x", ingestionRunsPath + "?created_from=bad",
		ingestionRunsPath + "?run_type=backfill&cursor=" + badCursor, ingestionRunsPath + "/not-uuid",
		ingestionTasksPath + "?run_id=bad", ingestionTasksPath + "/not-uuid",
		ingestionRunsPath + "/" + run.Run.ID.String() + "?unexpected=x",
	}
	for _, path := range paths {
		request := httptest.NewRequest(http.MethodGet, path, nil)
		request.Header.Set(authorizationHeader, "Bearer read-token")
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		if response.Code != http.StatusBadRequest {
			t.Fatalf("GET %s response=%d body=%s", path, response.Code, response.Body.String())
		}
	}
	if err := RegisterIngestionQueryRoutes(nil, nil, nil); err == nil {
		t.Fatal("RegisterIngestionQueryRoutes(nil) error = nil")
	}
}

type ingestionHTTPServiceStub struct {
	runPage   application.RunPage
	taskPage  application.TaskPage
	run       application.RunRecord
	task      application.TaskRecord
	runInput  application.RunListInput
	taskInput application.TaskListInput
	err       error
}

func (stub *ingestionHTTPServiceStub) ListRuns(_ context.Context, input application.RunListInput) (application.RunPage, error) {
	stub.runInput = input
	return stub.runPage, stub.err
}
func (stub *ingestionHTTPServiceStub) GetRun(context.Context, domain.ID) (application.RunRecord, error) {
	return stub.run, stub.err
}
func (stub *ingestionHTTPServiceStub) ListTasks(_ context.Context, input application.TaskListInput) (application.TaskPage, error) {
	stub.taskInput = input
	return stub.taskPage, stub.err
}
func (stub *ingestionHTTPServiceStub) GetTask(context.Context, domain.ID) (application.TaskRecord, error) {
	return stub.task, stub.err
}

func registeredIngestionQueryHandler(t *testing.T, service *ingestionHTTPServiceStub, permissions ...application.Permission) http.Handler {
	t.Helper()
	principal, err := application.NewPrincipal("admin@example.com", application.ActorTypeUser, permissions...)
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	if err := RegisterIngestionQueryRoutes(mux, service, &stubAuthenticator{principal: principal}); err != nil {
		t.Fatal(err)
	}
	return WithRequestID(mux)
}

func ingestionHTTPFixture(t *testing.T) (*ingestionHTTPServiceStub, application.RunRecord, application.TaskRecord) {
	t.Helper()
	now := time.Date(2026, 7, 19, 12, 0, 0, 0, time.UTC)
	runID := domain.IDFromUUID(uuid.MustParse("019f1452-90f7-7992-a87a-ca2727897801"))
	taskID := domain.IDFromUUID(uuid.MustParse("019f1452-90f7-7992-a87a-ca2727897802"))
	subscriptionID := domain.IDFromUUID(uuid.MustParse("019f1452-90f7-7992-a87a-ca2727897803"))
	requestedBy := "admin@example.com"
	run := application.RunRecord{Run: domain.IngestionRun{ID: runID, RunKey: "backfill.manual.test", RunType: "backfill", TriggerType: "manual", RequestedBy: &requestedBy, CreatedAt: now}, Summary: ingestion.RunSummary{RunID: runID, Status: "running", TaskCount: 1, RunningCount: 1}, Context: map[string]string{"reason": "history"}}
	providerCode, _ := domain.ParseCode("bybit")
	instrumentCode, _ := domain.ParseCode("instrument.bybit.spot.btc-usdt")
	mappingCode, _ := domain.ParseCode("provider.bybit.spot.btcusdt")
	errorCode, rawMessage, safeSummary := "network", "raw secret", "provider network request failed"
	task := application.TaskRecord{Task: domain.IngestionTask{ID: taskID, RunID: runID, SubscriptionID: subscriptionID, RangeStart: now.Add(-2 * time.Hour), RangeEnd: now.Add(-time.Hour), Status: "retry_wait", AttemptCount: 1, MaxAttempts: 5, ErrorCode: &errorCode, ErrorMessage: &rawMessage, CreatedAt: now, UpdatedAt: now}, RunType: "backfill", TriggerType: "manual", SubscriptionInterval: "1h", ProviderID: domain.IDFromUUID(uuid.MustParse("019f1452-90f7-7992-a87a-ca2727897810")), ProviderCode: providerCode, InstrumentID: domain.IDFromUUID(uuid.MustParse("019f1452-90f7-7992-a87a-ca2727897811")), InstrumentCode: instrumentCode, ProviderInstrumentID: domain.IDFromUUID(uuid.MustParse("019f1452-90f7-7992-a87a-ca2727897812")), ProviderInstrumentCode: mappingCode, ProviderSymbol: "BTCUSDT", ErrorSummary: &safeSummary, SafeErrorDetails: map[string]string{"provider_code": "bybit"}}
	return &ingestionHTTPServiceStub{run: run, task: task}, run, task
}
