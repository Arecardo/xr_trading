//go:build integration

package integration_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"xr-trading/market-info-service/internal/domain"
	"xr-trading/market-info-service/internal/ingestion"
	"xr-trading/market-info-service/internal/ingestion/ports"
	"xr-trading/market-info-service/internal/providers"
)

type ing006PagedAdapter struct {
	providerCode domain.Code
	mu           sync.Mutex
	cursors      []string
}

func (adapter *ing006PagedAdapter) ProviderCode() domain.Code { return adapter.providerCode }
func (adapter *ing006PagedAdapter) Capabilities(ctx context.Context) (ports.ProviderCapabilities, error) {
	return (&ing002Adapter{providerCode: adapter.providerCode}).Capabilities(ctx)
}
func (*ing006PagedAdapter) FetchLatestQuotes(context.Context, []ports.ProviderInstrumentRef) ([]ports.ProviderQuote, error) {
	return nil, errors.New("not implemented")
}
func (adapter *ing006PagedAdapter) FetchBars(ctx context.Context, request ports.FetchBarsRequest) (ports.FetchBarsResult, error) {
	adapter.mu.Lock()
	adapter.cursors = append(adapter.cursors, request.Cursor)
	adapter.mu.Unlock()
	if request.Cursor != "" && request.Cursor != "second" {
		return ports.FetchBarsResult{}, errors.New("unexpected cursor")
	}
	if request.Cursor == "second" {
		secondStart, err := domain.NewUTCInstant(request.StartTime.Time().Add(time.Hour))
		if err != nil {
			return ports.FetchBarsResult{}, err
		}
		request.StartTime = secondStart
	}
	result, err := (&ing002Adapter{providerCode: adapter.providerCode}).FetchBars(ctx, request)
	if err != nil {
		return ports.FetchBarsResult{}, err
	}
	if request.Cursor == "" {
		result.HasMore = true
		result.NextCursor = "second"
	}
	return result, nil
}

func TestING006ConcurrentSingleTaskBackfillExecutesPaginationAndAllowsRevision(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	fixture := newING004Fixture(t, ctx, "bf", 5)
	if _, err := fixture.admin.Exec(ctx, "DELETE FROM market_data.ingestion_tasks WHERE run_id = $1", fixture.run.ID.UUID()); err != nil {
		t.Fatalf("delete setup task: %v", err)
	}
	if _, err := fixture.admin.Exec(ctx, "DELETE FROM market_data.ingestion_runs WHERE id = $1", fixture.run.ID.UUID()); err != nil {
		t.Fatalf("delete setup run: %v", err)
	}
	t.Cleanup(func() {
		cleanupCtx := context.Background()
		_, _ = fixture.admin.Exec(cleanupCtx, `DELETE FROM market_data.ingestion_tasks
WHERE run_id IN (SELECT id FROM market_data.ingestion_runs WHERE run_type = 'backfill' AND context->>'request_id' = 'req_ing006_integration')`)
		_, _ = fixture.admin.Exec(cleanupCtx, `DELETE FROM market_data.ingestion_runs
WHERE run_type = 'backfill' AND context->>'request_id' = 'req_ing006_integration'`)
	})

	var instrumentCode string
	if err := fixture.admin.QueryRow(ctx, "SELECT code FROM core.instruments WHERE id = $1", fixture.mapping.InstrumentID.UUID()).Scan(&instrumentCode); err != nil {
		t.Fatalf("query instrument code: %v", err)
	}
	startTime := fixture.now.Add(-72 * time.Hour)
	endTime := fixture.now.Add(-24 * time.Hour)
	input := ingestion.BackfillInput{
		ProviderCode: fixture.provider.Code.String(), InstrumentCode: instrumentCode, Interval: "1h",
		StartTime: startTime, EndTime: endTime, Reason: "initialize integration history",
		RequestedBy: "integration-admin", ActorType: "user", RequestID: "req_ing006_integration",
	}
	backfills, err := ingestion.NewBackfillService(ingestion.BackfillConfig{}, fixture.store, func() time.Time { return fixture.now }, domain.NewID)
	if err != nil {
		t.Fatalf("NewBackfillService() error = %v", err)
	}

	type createResult struct {
		result ingestion.BackfillResult
		err    error
	}
	results := make(chan createResult, 2)
	for range 2 {
		go func() {
			result, createErr := backfills.Create(ctx, input)
			results <- createResult{result: result, err: createErr}
		}()
	}
	var created ingestion.BackfillResult
	var successes, conflicts int
	for range 2 {
		result := <-results
		switch {
		case result.err == nil:
			successes++
			created = result.result
		case errors.Is(result.err, ingestion.ErrBackfillAlreadyRunning):
			conflicts++
		default:
			t.Fatalf("concurrent Create() error = %v", result.err)
		}
	}
	if successes != 1 || conflicts != 1 || created.RunID.IsZero() || created.TaskID.IsZero() {
		t.Fatalf("concurrent results success=%d conflict=%d created=%#v", successes, conflicts, created)
	}
	var runType, triggerType, runStatus, taskStatus, requestedBy string
	var taskCount, maximumAttempts int
	if err := fixture.admin.QueryRow(ctx, `SELECT runs.run_type, runs.trigger_type, runs.status, runs.requested_by,
       runs.task_count, tasks.status, tasks.max_attempts
FROM market_data.ingestion_runs AS runs
JOIN market_data.ingestion_tasks AS tasks ON tasks.run_id = runs.id
WHERE runs.id = $1 AND tasks.id = $2`, created.RunID.UUID(), created.TaskID.UUID()).Scan(
		&runType, &triggerType, &runStatus, &requestedBy, &taskCount, &taskStatus, &maximumAttempts,
	); err != nil {
		t.Fatalf("query created backfill: %v", err)
	}
	if runType != "backfill" || triggerType != "manual" || runStatus != "pending" || requestedBy != input.RequestedBy || taskCount != 1 || taskStatus != "pending" || maximumAttempts != 5 {
		t.Fatalf("created run=%s/%s/%s requested=%s tasks=%d task=%s max=%d", runType, triggerType, runStatus, requestedBy, taskCount, taskStatus, maximumAttempts)
	}

	if _, err := fixture.admin.Exec(ctx, "UPDATE market_data.ingestion_tasks SET created_at = $1, updated_at = $1 WHERE id = $2", time.Unix(0, 0).UTC(), created.TaskID.UUID()); err != nil {
		t.Fatalf("prioritize backfill task: %v", err)
	}
	claim, err := fixture.store.ClaimNextTask(ctx, "worker-ing006", fixture.now, time.Minute)
	if err != nil || claim == nil || claim.Task.ID != created.TaskID || !claim.Task.RangeStart.Equal(startTime) || !claim.Task.RangeEnd.Equal(endTime) {
		t.Fatalf("ClaimNextTask() = (%#v, %v)", claim, err)
	}
	adapter := &ing006PagedAdapter{providerCode: fixture.provider.Code}
	registry, err := providers.NewRegistry(ctx, adapter)
	if err != nil {
		t.Fatalf("NewRegistry() error = %v", err)
	}
	executor, err := ingestion.NewService(
		ingestion.Config{BarsPerPage: 100, MaximumPages: 10}, fixture.store, registry,
		ingestion.NewStructuralBarQualityValidator(), func() time.Time { return fixture.now.Add(time.Second) },
	)
	if err != nil {
		t.Fatalf("NewService() error = %v", err)
	}
	if err := executor.ExecuteTask(ctx, *claim); err != nil {
		t.Fatalf("ExecuteTask() error = %v", err)
	}
	adapter.mu.Lock()
	cursors := append([]string(nil), adapter.cursors...)
	adapter.mu.Unlock()
	if len(cursors) != 2 || cursors[0] != "" || cursors[1] != "second" {
		t.Fatalf("adapter cursors = %#v", cursors)
	}
	var bars int
	if err := fixture.admin.QueryRow(ctx, "SELECT count(*) FROM market_data.market_bars WHERE provider_instrument_id = $1", fixture.mapping.ID.UUID()).Scan(&bars); err != nil || bars != 2 {
		t.Fatalf("backfill bars=%d error=%v", bars, err)
	}
	if err := fixture.admin.QueryRow(ctx, "SELECT status FROM market_data.ingestion_runs WHERE id = $1", created.RunID.UUID()).Scan(&runStatus); err != nil || runStatus != "success" {
		t.Fatalf("completed run status=%s error=%v", runStatus, err)
	}

	revision, err := backfills.Create(ctx, input)
	if err != nil || revision.RunID == created.RunID || revision.TaskID == created.TaskID {
		t.Fatalf("Create(after terminal) = (%#v, %v)", revision, err)
	}
	var runCount, persistedTaskCount int
	if err := fixture.admin.QueryRow(ctx, `SELECT
    (SELECT count(*) FROM market_data.ingestion_runs WHERE run_type = 'backfill' AND context->>'request_id' = 'req_ing006_integration'),
    (SELECT count(*) FROM market_data.ingestion_tasks WHERE run_id IN (
        SELECT id FROM market_data.ingestion_runs WHERE run_type = 'backfill' AND context->>'request_id' = 'req_ing006_integration'
    ))`).Scan(&runCount, &persistedTaskCount); err != nil || runCount != 2 || persistedTaskCount != 2 {
		t.Fatalf("revision counts runs=%d tasks=%d error=%v", runCount, persistedTaskCount, err)
	}
}
