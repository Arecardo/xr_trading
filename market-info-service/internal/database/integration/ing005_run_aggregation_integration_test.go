//go:build integration

package integration_test

import (
	"context"
	"testing"
	"time"

	"xr-trading/market-info-service/internal/ingestion"
)

func TestING005RunServiceAggregatesPostgresTaskTruth(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	fixture := newING004Fixture(t, ctx, "run", 5)
	secondTaskID := newIntegrationID(t)
	secondRangeStart := fixture.task.RangeEnd
	secondRangeEnd := secondRangeStart.Add(time.Hour)
	if _, err := fixture.admin.Exec(ctx, `INSERT INTO market_data.ingestion_tasks (
    id, run_id, subscription_id, range_start, range_end, status,
    attempt_count, max_attempts, error_details, created_at, updated_at
) VALUES ($1, $2, $3, $4, $5, 'pending', 0, 5, '{}', $6, $6)`,
		secondTaskID.UUID(), fixture.run.ID.UUID(), fixture.subscription.ID.UUID(),
		secondRangeStart, secondRangeEnd, fixture.task.CreatedAt.Add(time.Second)); err != nil {
		t.Fatalf("insert second ingestion task: %v", err)
	}

	runs, err := ingestion.NewRunService(fixture.store)
	if err != nil {
		t.Fatalf("NewRunService() error = %v", err)
	}
	summary, err := runs.Refresh(ctx, fixture.run.ID)
	if err != nil || summary.Status != "pending" || summary.TaskCount != 2 {
		t.Fatalf("Refresh(pending) = (%#v, %v)", summary, err)
	}
	assertING005Run(t, ctx, fixture, "pending", 2, 0, 0, nil, nil)

	firstStartedAt := fixture.now.Add(time.Second)
	firstFinishedAt := fixture.now.Add(2 * time.Second)
	if _, err := fixture.admin.Exec(ctx, `UPDATE market_data.ingestion_tasks
SET status = 'success', started_at = $1, finished_at = $2, updated_at = $2
WHERE id = $3`, firstStartedAt, firstFinishedAt, fixture.task.ID.UUID()); err != nil {
		t.Fatalf("mark first task success: %v", err)
	}
	summary, err = runs.Refresh(ctx, fixture.run.ID)
	if err != nil || summary.Status != "running" || summary.SuccessCount != 1 || summary.PendingCount != 1 || summary.LatestFinishedAt != nil {
		t.Fatalf("Refresh(active mix) = (%#v, %v)", summary, err)
	}
	assertING005Run(t, ctx, fixture, "running", 2, 1, 0, &firstStartedAt, nil)

	secondFinishedAt := fixture.now.Add(3 * time.Second)
	if _, err := fixture.admin.Exec(ctx, `UPDATE market_data.ingestion_tasks
SET status = 'canceled', finished_at = $1, updated_at = $1
WHERE id = $2`, secondFinishedAt, secondTaskID.UUID()); err != nil {
		t.Fatalf("mark second task canceled: %v", err)
	}
	summary, err = runs.Refresh(ctx, fixture.run.ID)
	if err != nil || summary.Status != "partial" || summary.SuccessCount != 1 || summary.CanceledCount != 1 {
		t.Fatalf("Refresh(partial) = (%#v, %v)", summary, err)
	}
	assertING005Run(t, ctx, fixture, "partial", 2, 1, 0, &firstStartedAt, &secondFinishedAt)

	if _, err := fixture.admin.Exec(ctx, `UPDATE market_data.ingestion_tasks
SET status = 'failed', started_at = COALESCE(started_at, $1), finished_at = $2, updated_at = $2
WHERE run_id = $3`, firstStartedAt, secondFinishedAt, fixture.run.ID.UUID()); err != nil {
		t.Fatalf("mark all tasks failed: %v", err)
	}
	summary, err = runs.Refresh(ctx, fixture.run.ID)
	if err != nil || summary.Status != "failed" || summary.FailedCount != 2 {
		t.Fatalf("Refresh(failed) = (%#v, %v)", summary, err)
	}
	assertING005Run(t, ctx, fixture, "failed", 2, 0, 2, &firstStartedAt, &secondFinishedAt)

	if _, err := fixture.admin.Exec(ctx, `UPDATE market_data.ingestion_tasks
SET status = 'canceled', updated_at = $1 WHERE run_id = $2`, secondFinishedAt.Add(time.Second), fixture.run.ID.UUID()); err != nil {
		t.Fatalf("mark all tasks canceled: %v", err)
	}
	summary, err = runs.Refresh(ctx, fixture.run.ID)
	if err != nil || summary.Status != "canceled" || summary.CanceledCount != 2 {
		t.Fatalf("Refresh(canceled) = (%#v, %v)", summary, err)
	}
	assertING005Run(t, ctx, fixture, "canceled", 2, 0, 0, &firstStartedAt, &secondFinishedAt)
}

func assertING005Run(t *testing.T, ctx context.Context, fixture ing004Fixture, wantStatus string, wantTasks, wantSuccess, wantFailed int, wantStartedAt, wantFinishedAt *time.Time) {
	t.Helper()
	var status string
	var taskCount, successCount, failedCount int
	var startedAt, finishedAt *time.Time
	if err := fixture.admin.QueryRow(ctx, `SELECT status, task_count, success_count, failed_count, started_at, finished_at
FROM market_data.ingestion_runs WHERE id = $1`, fixture.run.ID.UUID()).Scan(
		&status, &taskCount, &successCount, &failedCount, &startedAt, &finishedAt,
	); err != nil {
		t.Fatalf("query ingestion run: %v", err)
	}
	if status != wantStatus || taskCount != wantTasks || successCount != wantSuccess || failedCount != wantFailed || !equalING005Time(startedAt, wantStartedAt) || !equalING005Time(finishedAt, wantFinishedAt) {
		t.Fatalf("run status=%s tasks=%d success=%d failed=%d started=%v finished=%v", status, taskCount, successCount, failedCount, startedAt, finishedAt)
	}
}

func equalING005Time(left, right *time.Time) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	return left.Equal(*right)
}
