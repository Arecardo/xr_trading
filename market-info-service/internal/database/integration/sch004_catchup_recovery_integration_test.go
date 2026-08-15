//go:build integration

package integration_test

import (
	"context"
	"testing"
	"time"

	"xr-trading/market-info-service/internal/domain"
	"xr-trading/market-info-service/internal/markettime"
	"xr-trading/market-info-service/internal/scheduler"
)

func TestSCH004RecoversExpiredLeaseAndCreatesOnlyActualGap(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	fixture := newING004Fixture(t, ctx, "sch004", 5)
	withCoinGeckoFXSubscriptionDisabled(t, ctx, fixture.admin)
	t.Cleanup(func() {
		cleanupCtx := context.Background()
		_, _ = fixture.admin.Exec(cleanupCtx, "DELETE FROM market_data.ingestion_tasks WHERE subscription_id = $1", fixture.subscription.ID.UUID())
		_, _ = fixture.admin.Exec(cleanupCtx, `DELETE FROM market_data.ingestion_runs AS runs
WHERE NOT EXISTS (SELECT 1 FROM market_data.ingestion_tasks AS tasks WHERE tasks.run_id = runs.id)
  AND (runs.id = $1 OR runs.context->>'subscription_id' = $2)`, fixture.run.ID.UUID(), fixture.subscription.ID.String())
	})
	observedAt := fixture.now.Truncate(time.Hour).Add(30 * time.Minute)
	activatedAt := observedAt.Add(-5 * time.Hour)
	if _, err := fixture.admin.Exec(ctx, `UPDATE market_data.collection_subscriptions
SET created_at = $1, updated_at = $1, close_delay_seconds = 0, revision_delay_seconds = NULL
WHERE id = $2`, activatedAt, fixture.subscription.ID.UUID()); err != nil {
		t.Fatalf("prepare scheduler subscription: %v", err)
	}

	claimedAt := observedAt.Add(-2 * time.Minute)
	claim, err := fixture.store.ClaimNextTask(ctx, "worker-sch004-expired", claimedAt, time.Minute)
	if err != nil || claim == nil || claim.Task.ID != fixture.task.ID {
		t.Fatalf("ClaimNextTask() = (%#v, %v)", claim, err)
	}
	checkpointOpen := observedAt.Truncate(time.Hour).Add(-4 * time.Hour)
	existing := []time.Time{
		checkpointOpen.Add(-time.Hour),
		checkpointOpen,
		checkpointOpen.Add(time.Hour),
		checkpointOpen.Add(3 * time.Hour),
	}
	for _, open := range existing {
		if _, err := fixture.admin.Exec(ctx, `INSERT INTO market_data.market_bars (
instrument_id, provider_instrument_id, interval, open_time, revision, close_time,
open_price, high_price, low_price, close_price, is_closed, is_current,
quality_status, collected_at, metadata
) VALUES ($1, $2, '1h', $3, 1, $4, 100, 101, 99, 100, true, true, 'valid', $5, '{}')`,
			fixture.mapping.InstrumentID.UUID(), fixture.mapping.ID.UUID(), open, open.Add(time.Hour), observedAt); err != nil {
			t.Fatalf("insert closed bar %v: %v", open, err)
		}
	}
	if _, err := fixture.admin.Exec(ctx, `INSERT INTO market_data.ingestion_checkpoints (
subscription_id, last_success_open_time, last_closed_open_time, last_attempt_at,
last_success_at, consecutive_failures, updated_at
) VALUES ($1, $2, $2, $3, $3, 0, $3)`, fixture.subscription.ID.UUID(), checkpointOpen, observedAt.Add(-time.Minute)); err != nil {
		t.Fatalf("insert scheduling checkpoint: %v", err)
	}

	calendar, err := markettime.NewNYSECalendar()
	if err != nil {
		t.Fatalf("NewNYSECalendar() error = %v", err)
	}
	instance, err := scheduler.NewIncrementalScheduler(scheduler.IncrementalConfig{PageSize: 10}, fixture.store, integrationFixedClock{now: observedAt}, domain.NewID, calendar)
	if err != nil {
		t.Fatalf("NewIncrementalScheduler() error = %v", err)
	}
	result, err := instance.RunOnce(ctx)
	if err != nil || result.RecoveredTasks != 1 || result.ScannedSubscriptions != 1 || result.DueWindows != 1 || result.CreatedRuns != 1 {
		t.Fatalf("RunOnce(first) = (%#v, %v)", result, err)
	}

	wantStart := checkpointOpen.Add(2 * time.Hour)
	wantEnd := wantStart.Add(time.Hour)
	wantKey := scheduler.StableRunKey(scheduler.WindowTriggerClose, fixture.subscription.ID, wantStart, wantEnd)
	var taskStatus string
	var rangeStart, rangeEnd time.Time
	if err := fixture.admin.QueryRow(ctx, `SELECT tasks.status, tasks.range_start, tasks.range_end
FROM market_data.ingestion_runs AS runs
JOIN market_data.ingestion_tasks AS tasks ON tasks.run_id = runs.id
WHERE runs.run_key = $1`, wantKey).Scan(&taskStatus, &rangeStart, &rangeEnd); err != nil {
		t.Fatalf("query catch-up task: %v", err)
	}
	if taskStatus != "pending" || !rangeStart.Equal(wantStart) || !rangeEnd.Equal(wantEnd) {
		t.Fatalf("catch-up task status=%s range=[%v,%v)", taskStatus, rangeStart, rangeEnd)
	}
	var recoveredStatus, errorCode string
	if err := fixture.admin.QueryRow(ctx, `SELECT status, error_code FROM market_data.ingestion_tasks WHERE id = $1`, fixture.task.ID.UUID()).Scan(&recoveredStatus, &errorCode); err != nil {
		t.Fatalf("query recovered task: %v", err)
	}
	if recoveredStatus != "pending" || errorCode != "lease_expired" {
		t.Fatalf("recovered task status=%s error=%s", recoveredStatus, errorCode)
	}

	second, err := instance.RunOnce(ctx)
	if err != nil || second.RecoveredTasks != 0 || second.DueWindows != 1 || second.ExistingRuns != 1 || second.CreatedRuns != 0 {
		t.Fatalf("RunOnce(second) = (%#v, %v)", second, err)
	}
}
