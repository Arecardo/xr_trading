//go:build integration

package integration_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"xr-trading/market-info-service/internal/api/httpapi"
	"xr-trading/market-info-service/internal/application"
	"xr-trading/market-info-service/internal/auth"
	poolpostgres "xr-trading/market-info-service/internal/database/postgres"
	"xr-trading/market-info-service/internal/domain"
	"xr-trading/market-info-service/internal/ingestion"
	repositorypostgres "xr-trading/market-info-service/internal/repository/postgres"
)

func TestADM002ThroughADM004ManagementHTTPAgainstPostgres(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	admin := openAdminConnection(t, ctx)
	t.Cleanup(func() { _ = admin.Close(context.Background()) })
	assetID, instrumentID, _, instrumentCode := createCoreFixture(t, ctx, admin)
	providerID := newIntegrationID(t)
	requestID := "req_" + newIntegrationID(t).String()
	t.Cleanup(func() {
		cleanupADM002Fixture(t, context.Background(), admin, requestID, providerID, instrumentID, assetID)
	})

	pool, err := poolpostgres.OpenPool(ctx, poolpostgres.Config{
		DatabaseURL: integrationDatabaseURL(t), MaxConns: 3, MinConns: 0,
		MaxConnLifetime: time.Minute, HealthCheckPeriod: time.Second,
	})
	if err != nil {
		t.Fatalf("OpenPool() error = %v", err)
	}
	defer pool.Close()
	catalog, _ := repositorypostgres.NewCatalogRepository(pool)
	subscriptions, _ := repositorypostgres.NewSubscriptionRepository(pool)
	ingestionRepository, _ := repositorypostgres.NewIngestionRepository(pool)
	now := time.Now().UTC().Truncate(time.Microsecond)
	provider := domain.Provider{
		ID: providerID, Code: integrationCode(t, "bybit-adm002-"+providerID.String()), Name: "ADM002 Bybit",
		ProviderType: domain.ProviderTypeExchange, Status: domain.ProviderStatusActive, CreatedAt: now, UpdatedAt: now,
	}
	mapping := domain.ProviderInstrument{
		ID: newIntegrationID(t), Code: integrationCode(t, "provider.bybit.adm002-"+providerID.String()),
		ProviderID: provider.ID, InstrumentID: instrumentID, ExternalSymbol: "ADM002BTCUSDT", ProviderMarket: "spot",
		Capabilities: domain.ProviderCapabilities{Historical: true, Intervals: []domain.BarInterval{domain.BarInterval1Hour}},
		Enabled:      true, CreatedAt: now, UpdatedAt: now,
	}
	if err := catalog.CreateProvider(ctx, provider); err != nil {
		t.Fatalf("CreateProvider() error = %v", err)
	}
	if err := catalog.CreateProviderInstrument(ctx, mapping); err != nil {
		t.Fatalf("CreateProviderInstrument() error = %v", err)
	}
	subscription := domain.CollectionSubscription{
		ID: newIntegrationID(t), ProviderInstrumentID: mapping.ID, Interval: "1h", Enabled: true,
		Priority: 10, CloseDelaySeconds: 120, CreatedAt: now, UpdatedAt: now,
	}
	if err := subscriptions.CreateSubscription(ctx, subscription); err != nil {
		t.Fatalf("CreateSubscription() error = %v", err)
	}

	service, err := ingestion.NewBackfillService(ingestion.BackfillConfig{}, ingestionRepository, func() time.Time { return now }, domain.NewID)
	if err != nil {
		t.Fatal(err)
	}
	queryService, err := application.NewIngestionQueryService(ingestionRepository)
	if err != nil {
		t.Fatal(err)
	}
	runService, err := ingestion.NewRunService(ingestionRepository)
	if err != nil {
		t.Fatal(err)
	}
	taskCommands, err := ingestion.NewManualTaskService(ingestion.ManualTaskConfig{}, ingestionRepository, runService, func() time.Time { return now }, domain.NewID)
	if err != nil {
		t.Fatal(err)
	}
	principal, _ := application.NewPrincipal("adm002@example.com", application.ActorTypeUser, application.PermissionIngestionManage, application.PermissionOperationsRead)
	authenticator, _ := auth.NewStaticBearerAuthenticator([]auth.StaticCredential{{Token: "adm002-secret", Principal: principal}})
	mux := http.NewServeMux()
	if err := httpapi.RegisterBackfillRoutes(mux, service, authenticator); err != nil {
		t.Fatal(err)
	}
	if err := httpapi.RegisterIngestionQueryRoutes(mux, queryService, authenticator); err != nil {
		t.Fatal(err)
	}
	if err := httpapi.RegisterIngestionTaskCommandRoutes(mux, taskCommands, authenticator); err != nil {
		t.Fatal(err)
	}
	handler := httpapi.WithRequestID(mux)
	start := now.Add(-72 * time.Hour).Truncate(time.Hour)
	end := now.Add(-24 * time.Hour).Truncate(time.Hour)
	body := `{"provider":"` + provider.Code.String() + `","instrument_code":"` + instrumentCode + `","interval":"1h","start_time":"` + start.Format(time.RFC3339Nano) + `","end_time":"` + end.Format(time.RFC3339Nano) + `","reason":"initialize historical data"}`

	created := adm002Serve(t, handler, body, requestID, http.StatusAccepted)
	var response struct {
		RunID  string `json:"run_id"`
		TaskID string `json:"task_id"`
		Status string `json:"status"`
	}
	if err := json.Unmarshal(created.Body.Bytes(), &response); err != nil || response.RunID == "" || response.TaskID == "" || response.Status != "pending" {
		t.Fatalf("create response=%#v error=%v", response, err)
	}
	duplicate := adm002Serve(t, handler, body, requestID, http.StatusConflict)
	if !strings.Contains(duplicate.Body.String(), `"code":"BACKFILL_ALREADY_RUNNING"`) {
		t.Fatalf("duplicate response = %s", duplicate.Body.String())
	}

	var runType, triggerType, runStatus, requestedBy, taskStatus string
	var taskCount, maximumAttempts int
	var rangeStart, rangeEnd time.Time
	var runContext []byte
	if err := admin.QueryRow(ctx, `SELECT runs.run_type, runs.trigger_type, runs.status, runs.requested_by,
       runs.task_count, runs.context, tasks.status, tasks.max_attempts, tasks.range_start, tasks.range_end
FROM market_data.ingestion_runs AS runs
JOIN market_data.ingestion_tasks AS tasks ON tasks.run_id = runs.id
WHERE runs.id = $1 AND tasks.id = $2`, response.RunID, response.TaskID).Scan(
		&runType, &triggerType, &runStatus, &requestedBy, &taskCount, &runContext,
		&taskStatus, &maximumAttempts, &rangeStart, &rangeEnd,
	); err != nil {
		t.Fatalf("query created backfill: %v", err)
	}
	var audit map[string]string
	if err := json.Unmarshal(runContext, &audit); err != nil {
		t.Fatalf("decode run context: %v", err)
	}
	if runType != "backfill" || triggerType != "manual" || runStatus != "pending" || requestedBy != "adm002@example.com" || taskCount != 1 || taskStatus != "pending" || maximumAttempts != 5 || !rangeStart.Equal(start) || !rangeEnd.Equal(end) || audit["actor_type"] != "user" || audit["request_id"] != requestID || audit["reason"] != "initialize historical data" {
		t.Fatalf("persisted run/task=(%s,%s,%s,%s,%d,%s,%d,%v,%v,%v)", runType, triggerType, runStatus, requestedBy, taskCount, taskStatus, maximumAttempts, rangeStart, rangeEnd, audit)
	}

	if _, err := admin.Exec(ctx, `UPDATE market_data.ingestion_tasks
SET status = 'retry_wait', attempt_count = 1, next_attempt_at = $1,
    error_code = 'network', error_message = 'raw secret connection detail',
    error_details = $2::jsonb, updated_at = $3
WHERE id = $4`, now.Add(time.Minute), `{"provider_code":"`+provider.Code.String()+`","token":"must-not-leak"}`, now, response.TaskID); err != nil {
		t.Fatalf("prepare ADM-003 task state: %v", err)
	}
	if page, err := queryService.ListRuns(ctx, application.RunListInput{RunType: "backfill", TriggerType: "manual", Status: "running", RequestedBy: "adm002@example.com", Limit: 1}); err != nil || len(page.Items) != 1 {
		t.Fatalf("direct ADM-003 run query = (%#v, %v)", page, err)
	}
	runsPath := "/api/market-info/v1/ingestion-runs?run_type=backfill&trigger_type=manual&status=running&requested_by=adm002%40example.com&limit=1"
	runs := adm003Get(t, handler, runsPath, "req_"+newIntegrationID(t).String(), http.StatusOK)
	if !strings.Contains(runs.Body.String(), response.RunID) || !strings.Contains(runs.Body.String(), `"status":"running"`) || !strings.Contains(runs.Body.String(), `"retry_wait_count":1`) {
		t.Fatalf("run list response = %s", runs.Body.String())
	}
	runDetail := adm003Get(t, handler, "/api/market-info/v1/ingestion-runs/"+response.RunID, "req_"+newIntegrationID(t).String(), http.StatusOK)
	if !strings.Contains(runDetail.Body.String(), `"task_count":1`) || !strings.Contains(runDetail.Body.String(), `"reason":"initialize historical data"`) {
		t.Fatalf("run detail response = %s", runDetail.Body.String())
	}
	tasksPath := "/api/market-info/v1/ingestion-tasks?run_id=" + response.RunID + "&status=retry_wait&provider=" + provider.Code.String() + "&instrument_code=" + instrumentCode + "&interval=1h&limit=1"
	tasks := adm003Get(t, handler, tasksPath, "req_"+newIntegrationID(t).String(), http.StatusOK)
	if !strings.Contains(tasks.Body.String(), response.TaskID) || !strings.Contains(tasks.Body.String(), `"error_summary":"provider network request failed"`) || !strings.Contains(tasks.Body.String(), `"provider_code":"`+provider.Code.String()+`"`) || strings.Contains(tasks.Body.String(), "must-not-leak") || strings.Contains(tasks.Body.String(), "raw secret") {
		t.Fatalf("task list response = %s", tasks.Body.String())
	}
	taskDetail := adm003Get(t, handler, "/api/market-info/v1/ingestion-tasks/"+response.TaskID, "req_"+newIntegrationID(t).String(), http.StatusOK)
	if !strings.Contains(taskDetail.Body.String(), mapping.Code.String()) || !strings.Contains(taskDetail.Body.String(), instrumentCode) || strings.Contains(taskDetail.Body.String(), "must-not-leak") {
		t.Fatalf("task detail response = %s", taskDetail.Body.String())
	}
	missingRun := adm003Get(t, handler, "/api/market-info/v1/ingestion-runs/"+newIntegrationID(t).String(), "req_"+newIntegrationID(t).String(), http.StatusNotFound)
	if !strings.Contains(missingRun.Body.String(), `"code":"NOT_FOUND"`) {
		t.Fatalf("missing run response = %s", missingRun.Body.String())
	}

	if _, err := admin.Exec(ctx, `UPDATE market_data.ingestion_tasks
SET status = 'failed', next_attempt_at = NULL, finished_at = $1, updated_at = $1
WHERE id = $2`, now, response.TaskID); err != nil {
		t.Fatalf("prepare ADM-004 failed task: %v", err)
	}
	retryPath := "/api/market-info/v1/ingestion-tasks/" + response.TaskID + "/retry"
	retry := adm004Serve(t, handler, retryPath, `{"reason":"credentials renewed"}`, requestID, http.StatusAccepted)
	var retryResponse struct {
		RunID  string `json:"run_id"`
		TaskID string `json:"task_id"`
		Status string `json:"status"`
	}
	if err := json.Unmarshal(retry.Body.Bytes(), &retryResponse); err != nil || retryResponse.RunID == "" || retryResponse.TaskID == "" || retryResponse.Status != "pending" {
		t.Fatalf("manual retry response=%#v err=%v", retryResponse, err)
	}
	duplicateRetry := adm004Serve(t, handler, retryPath, `{"reason":"duplicate click"}`, "req_"+newIntegrationID(t).String(), http.StatusConflict)
	if !strings.Contains(duplicateRetry.Body.String(), `"code":"MANUAL_RETRY_ALREADY_RUNNING"`) {
		t.Fatalf("duplicate manual retry response=%s", duplicateRetry.Body.String())
	}
	var originalStatus, retryStatus, retryRunType, retryTrigger, retryRequestedBy string
	var retryOf *string
	var retryContext []byte
	if err := admin.QueryRow(ctx, `SELECT original.status, retry.status, retry.retry_of_task_id::text,
       runs.run_type, runs.trigger_type, runs.requested_by, runs.context
FROM market_data.ingestion_tasks AS original
JOIN market_data.ingestion_tasks AS retry ON retry.retry_of_task_id = original.id
JOIN market_data.ingestion_runs AS runs ON runs.id = retry.run_id
WHERE original.id = $1 AND retry.id = $2`, response.TaskID, retryResponse.TaskID).Scan(
		&originalStatus, &retryStatus, &retryOf, &retryRunType, &retryTrigger, &retryRequestedBy, &retryContext,
	); err != nil {
		t.Fatalf("query ADM-004 retry: %v", err)
	}
	if originalStatus != "failed" || retryStatus != "pending" || retryOf == nil || *retryOf != response.TaskID || retryRunType != "repair" || retryTrigger != "manual" || retryRequestedBy != "adm002@example.com" || !strings.Contains(string(retryContext), `"reason": "credentials renewed"`) {
		t.Fatalf("persisted retry=(%s,%s,%v,%s,%s,%s,%s)", originalStatus, retryStatus, retryOf, retryRunType, retryTrigger, retryRequestedBy, retryContext)
	}

	cancelPath := "/api/market-info/v1/ingestion-tasks/" + retryResponse.TaskID + "/cancel"
	canceled := adm004Serve(t, handler, cancelPath, `{"reason":"incorrect retry range"}`, requestID, http.StatusOK)
	if !strings.Contains(canceled.Body.String(), `"status":"canceled"`) {
		t.Fatalf("cancel response=%s", canceled.Body.String())
	}
	cancelConflict := adm004Serve(t, handler, cancelPath, `{"reason":"duplicate cancel"}`, "req_"+newIntegrationID(t).String(), http.StatusConflict)
	if !strings.Contains(cancelConflict.Body.String(), `"code":"TASK_STATE_CONFLICT"`) {
		t.Fatalf("cancel conflict response=%s", cancelConflict.Body.String())
	}
	var canceledStatus, retryRunStatus, canceledBy, cancelReason string
	var canceledContext []byte
	if err := admin.QueryRow(ctx, `SELECT tasks.status, runs.status, tasks.canceled_by, tasks.cancel_reason, runs.context
FROM market_data.ingestion_tasks AS tasks
JOIN market_data.ingestion_runs AS runs ON runs.id = tasks.run_id
WHERE tasks.id = $1`, retryResponse.TaskID).Scan(&canceledStatus, &retryRunStatus, &canceledBy, &cancelReason, &canceledContext); err != nil {
		t.Fatalf("query ADM-004 cancellation: %v", err)
	}
	if canceledStatus != "canceled" || retryRunStatus != "canceled" || canceledBy != "adm002@example.com" || cancelReason != "incorrect retry range" || !strings.Contains(string(canceledContext), `"action": "cancel"`) || !strings.Contains(string(canceledContext), `"request_id": "`+requestID+`"`) {
		t.Fatalf("persisted cancel=(%s,%s,%s,%s,%s)", canceledStatus, retryRunStatus, canceledBy, cancelReason, canceledContext)
	}
	failedCancel := adm004Serve(t, handler, "/api/market-info/v1/ingestion-tasks/"+response.TaskID+"/cancel", `{"reason":"terminal task"}`, "req_"+newIntegrationID(t).String(), http.StatusConflict)
	if !strings.Contains(failedCancel.Body.String(), `"code":"TASK_STATE_CONFLICT"`) {
		t.Fatalf("failed cancel response=%s", failedCancel.Body.String())
	}
	if _, err := admin.Exec(ctx, `UPDATE market_data.collection_subscriptions SET enabled = false, updated_at = $1 WHERE id = $2`, now, subscription.ID.UUID()); err != nil {
		t.Fatalf("disable ADM-004 retry source: %v", err)
	}
	staleSource := adm004Serve(t, handler, retryPath, `{"reason":"retry after source disabled"}`, "req_"+newIntegrationID(t).String(), http.StatusConflict)
	if !strings.Contains(staleSource.Body.String(), `"code":"CONFLICT"`) {
		t.Fatalf("stale source response=%s", staleSource.Body.String())
	}

	missingBody := strings.Replace(body, `"interval":"1h"`, `"interval":"1d"`, 1)
	missing := adm002Serve(t, handler, missingBody, "req_"+newIntegrationID(t).String(), http.StatusNotFound)
	if !strings.Contains(missing.Body.String(), `"code":"SUBSCRIPTION_NOT_FOUND"`) {
		t.Fatalf("missing subscription response = %s", missing.Body.String())
	}
	request := httptest.NewRequest(http.MethodPost, "/api/market-info/v1/ingestion-runs/backfill", strings.NewReader(body))
	unauthenticated := httptest.NewRecorder()
	handler.ServeHTTP(unauthenticated, request)
	if unauthenticated.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated response=%d body=%s", unauthenticated.Code, unauthenticated.Body.String())
	}
}

func adm003Get(t *testing.T, handler http.Handler, path, requestID string, wantStatus int) *httptest.ResponseRecorder {
	t.Helper()
	request := httptest.NewRequest(http.MethodGet, path, nil)
	request.Header.Set("Authorization", "Bearer adm002-secret")
	request.Header.Set(httpapi.RequestIDHeader, requestID)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != wantStatus || response.Header().Get(httpapi.RequestIDHeader) != requestID {
		t.Fatalf("GET %s response=%d headers=%v body=%s", path, response.Code, response.Header(), response.Body.String())
	}
	return response
}

func adm002Serve(t *testing.T, handler http.Handler, body, requestID string, wantStatus int) *httptest.ResponseRecorder {
	t.Helper()
	request := httptest.NewRequest(http.MethodPost, "/api/market-info/v1/ingestion-runs/backfill", strings.NewReader(body))
	request.Header.Set("Authorization", "Bearer adm002-secret")
	request.Header.Set(httpapi.RequestIDHeader, requestID)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != wantStatus || response.Header().Get(httpapi.RequestIDHeader) != requestID {
		t.Fatalf("response=%d headers=%v body=%s", response.Code, response.Header(), response.Body.String())
	}
	return response
}

func adm004Serve(t *testing.T, handler http.Handler, path, body, requestID string, wantStatus int) *httptest.ResponseRecorder {
	t.Helper()
	request := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
	request.Header.Set("Authorization", "Bearer adm002-secret")
	request.Header.Set(httpapi.RequestIDHeader, requestID)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != wantStatus || response.Header().Get(httpapi.RequestIDHeader) != requestID {
		t.Fatalf("POST %s response=%d headers=%v body=%s", path, response.Code, response.Header(), response.Body.String())
	}
	return response
}

func cleanupADM002Fixture(t *testing.T, ctx context.Context, admin *pgx.Conn, requestID string, providerID, instrumentID, assetID domain.ID) {
	t.Helper()
	_, _ = admin.Exec(ctx, `DELETE FROM market_data.ingestion_tasks WHERE run_id IN (SELECT id FROM market_data.ingestion_runs WHERE context->>'request_id' = $1)`, requestID)
	_, _ = admin.Exec(ctx, `DELETE FROM market_data.ingestion_runs WHERE context->>'request_id' = $1`, requestID)
	deleteDB011Fixture(t, ctx, admin, providerID, instrumentID, assetID)
}
