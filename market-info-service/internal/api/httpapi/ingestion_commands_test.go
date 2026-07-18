package httpapi

import (
	"context"
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

type taskCommandHTTPStub struct {
	retryResult             ingestion.ManualRetryResult
	cancelResult            ingestion.TaskCancellationResult
	retryErr, cancelErr     error
	taskID                  domain.ID
	audit                   ingestion.TaskOperationAudit
	retryCalls, cancelCalls int
}

func (stub *taskCommandHTTPStub) Retry(_ context.Context, taskID domain.ID, audit ingestion.TaskOperationAudit) (ingestion.ManualRetryResult, error) {
	stub.taskID, stub.audit, stub.retryCalls = taskID, audit, stub.retryCalls+1
	return stub.retryResult, stub.retryErr
}
func (stub *taskCommandHTTPStub) Cancel(_ context.Context, taskID domain.ID, audit ingestion.TaskOperationAudit) (ingestion.TaskCancellationResult, error) {
	stub.taskID, stub.audit, stub.cancelCalls = taskID, audit, stub.cancelCalls+1
	return stub.cancelResult, stub.cancelErr
}

func TestIngestionTaskCommandRoutesRetryAndCancel(t *testing.T) {
	now := time.Date(2026, 7, 19, 12, 0, 0, 0, time.UTC)
	originalID := commandHTTPID("019f1452-90f7-7992-a87a-ca2727898301")
	runID := commandHTTPID("019f1452-90f7-7992-a87a-ca2727898302")
	retryID := commandHTTPID("019f1452-90f7-7992-a87a-ca2727898303")
	service := &taskCommandHTTPStub{
		retryResult:  ingestion.ManualRetryResult{RunID: runID, TaskID: retryID, Status: "pending", CreatedAt: now},
		cancelResult: ingestion.TaskCancellationResult{RunID: runID, TaskID: originalID, Status: "canceled", CanceledAt: now},
	}
	handler := registeredTaskCommandHandler(t, service, application.PermissionIngestionManage)

	retry := taskCommandRequestForTest(originalID, "retry", `{"reason":"credentials renewed"}`)
	retry.Header.Set(RequestIDHeader, testRequestID)
	retryResponse := httptest.NewRecorder()
	handler.ServeHTTP(retryResponse, retry)
	if retryResponse.Code != http.StatusAccepted || !strings.Contains(retryResponse.Body.String(), retryID.String()) || !strings.Contains(retryResponse.Body.String(), `"status":"pending"`) {
		t.Fatalf("retry response=%d body=%s", retryResponse.Code, retryResponse.Body.String())
	}
	if service.retryCalls != 1 || service.taskID != originalID || service.audit.RequestedBy != "admin@example.com" || service.audit.ActorType != "user" || service.audit.RequestID != testRequestID || service.audit.Reason != "credentials renewed" {
		t.Fatalf("retry capture=%#v calls=%d", service, service.retryCalls)
	}

	cancel := taskCommandRequestForTest(originalID, "cancel", `{"reason":"incorrect range"}`)
	cancelResponse := httptest.NewRecorder()
	handler.ServeHTTP(cancelResponse, cancel)
	if cancelResponse.Code != http.StatusOK || !strings.Contains(cancelResponse.Body.String(), originalID.String()) || !strings.Contains(cancelResponse.Body.String(), `"status":"canceled"`) || service.cancelCalls != 1 {
		t.Fatalf("cancel response=%d body=%s calls=%d", cancelResponse.Code, cancelResponse.Body.String(), service.cancelCalls)
	}
}

func TestIngestionTaskCommandRoutesProtectAndValidate(t *testing.T) {
	service := &taskCommandHTTPStub{}
	readOnly := registeredTaskCommandHandler(t, service, application.PermissionOperationsRead)
	taskID := commandHTTPID("019f1452-90f7-7992-a87a-ca2727898311")
	request := httptest.NewRequest(http.MethodPost, "/api/market-info/v1/ingestion-tasks/"+taskID.String()+"/retry", strings.NewReader(`{"reason":"retry"}`))
	response := httptest.NewRecorder()
	readOnly.ServeHTTP(response, request)
	if response.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated status=%d", response.Code)
	}
	request = taskCommandRequestForTest(taskID, "retry", `{"reason":"retry"}`)
	response = httptest.NewRecorder()
	readOnly.ServeHTTP(response, request)
	if response.Code != http.StatusForbidden {
		t.Fatalf("forbidden status=%d body=%s", response.Code, response.Body.String())
	}

	admin := registeredTaskCommandHandler(t, service, application.PermissionIngestionManage)
	for _, pathAndBody := range []struct{ path, body string }{
		{"/api/market-info/v1/ingestion-tasks/not-a-uuid/retry", `{"reason":"retry"}`},
		{"/api/market-info/v1/ingestion-tasks/" + taskID.String() + "/retry?force=true", `{"reason":"retry"}`},
		{"/api/market-info/v1/ingestion-tasks/" + taskID.String() + "/retry", `{}`},
		{"/api/market-info/v1/ingestion-tasks/" + taskID.String() + "/cancel", `{"reason":" stop"}`},
		{"/api/market-info/v1/ingestion-tasks/" + taskID.String() + "/retry", `{"reason":"retry","force":true}`},
	} {
		request := httptest.NewRequest(http.MethodPost, pathAndBody.path, strings.NewReader(pathAndBody.body))
		request.Header.Set(authorizationHeader, "Bearer admin-token")
		response := httptest.NewRecorder()
		admin.ServeHTTP(response, request)
		if response.Code != http.StatusBadRequest {
			t.Fatalf("path=%s body=%s status=%d response=%s", pathAndBody.path, pathAndBody.body, response.Code, response.Body.String())
		}
	}
	if service.retryCalls != 0 || service.cancelCalls != 0 {
		t.Fatalf("unexpected service calls retry=%d cancel=%d", service.retryCalls, service.cancelCalls)
	}
	if err := RegisterIngestionTaskCommandRoutes(nil, nil, nil); err == nil {
		t.Fatal("RegisterIngestionTaskCommandRoutes(nil) error = nil")
	}
}

func TestIngestionTaskCommandRoutesMapFailures(t *testing.T) {
	taskID := commandHTTPID("019f1452-90f7-7992-a87a-ca2727898321")
	for _, test := range []struct {
		name, operation string
		err             error
		wantStatus      int
		wantCode        string
	}{
		{"retry duplicate", "retry", ingestion.ErrManualRetryAlreadyRunning, http.StatusConflict, "MANUAL_RETRY_ALREADY_RUNNING"},
		{"retry source", "retry", ingestion.ErrManualRetrySourceUnavailable, http.StatusConflict, "CONFLICT"},
		{"retry state", "retry", domain.ErrConflict, http.StatusConflict, "TASK_STATE_CONFLICT"},
		{"cancel state", "cancel", domain.ErrConflict, http.StatusConflict, "TASK_STATE_CONFLICT"},
		{"missing", "cancel", domain.ErrNotFound, http.StatusNotFound, "TASK_NOT_FOUND"},
		{"database", "cancel", domain.ErrDatabaseUnavailable, http.StatusServiceUnavailable, "DATABASE_UNAVAILABLE"},
	} {
		t.Run(test.name, func(t *testing.T) {
			service := &taskCommandHTTPStub{}
			if test.operation == "retry" {
				service.retryErr = test.err
			} else {
				service.cancelErr = test.err
			}
			handler := registeredTaskCommandHandler(t, service, application.PermissionIngestionManage)
			request := taskCommandRequestForTest(taskID, test.operation, `{"reason":"operation reason"}`)
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, request)
			if response.Code != test.wantStatus || !strings.Contains(response.Body.String(), `"code":"`+test.wantCode+`"`) {
				t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
			}
		})
	}
	service := &taskCommandHTTPStub{retryResult: ingestion.ManualRetryResult{}, cancelResult: ingestion.TaskCancellationResult{}}
	handler := registeredTaskCommandHandler(t, service, application.PermissionIngestionManage)
	for _, operation := range []string{"retry", "cancel"} {
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, taskCommandRequestForTest(taskID, operation, `{"reason":"reason"}`))
		if response.Code != http.StatusInternalServerError {
			t.Fatalf("invalid %s result status=%d body=%s", operation, response.Code, response.Body.String())
		}
	}
}

func registeredTaskCommandHandler(t *testing.T, service *taskCommandHTTPStub, permissions ...application.Permission) http.Handler {
	t.Helper()
	principal, err := application.NewPrincipal("admin@example.com", application.ActorTypeUser, permissions...)
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	if err := RegisterIngestionTaskCommandRoutes(mux, service, &stubAuthenticator{principal: principal}); err != nil {
		t.Fatal(err)
	}
	return WithRequestID(mux)
}

func taskCommandRequestForTest(taskID domain.ID, operation, body string) *http.Request {
	request := httptest.NewRequest(http.MethodPost, "/api/market-info/v1/ingestion-tasks/"+taskID.String()+"/"+operation, strings.NewReader(body))
	request.Header.Set(authorizationHeader, "Bearer admin-token")
	return request
}

func commandHTTPID(value string) domain.ID { return domain.IDFromUUID(uuid.MustParse(value)) }
