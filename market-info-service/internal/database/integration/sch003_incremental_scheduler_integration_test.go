//go:build integration

package integration_test

import (
	"context"
	"sync"
	"testing"
	"time"

	"xr-trading/market-info-service/internal/domain"
	"xr-trading/market-info-service/internal/markettime"
	"xr-trading/market-info-service/internal/scheduler"
)

func TestSCH003ConcurrentSchedulersCreateOneStableRunAndTask(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	fixture := newING004Fixture(t, ctx, "sch003", 5)
	withCoinGeckoFXSubscriptionDisabled(t, ctx, fixture.admin)
	if _, err := fixture.admin.Exec(ctx, "DELETE FROM market_data.ingestion_tasks WHERE run_id = $1", fixture.run.ID.UUID()); err != nil {
		t.Fatalf("delete setup task: %v", err)
	}
	if _, err := fixture.admin.Exec(ctx, "DELETE FROM market_data.ingestion_runs WHERE id = $1", fixture.run.ID.UUID()); err != nil {
		t.Fatalf("delete setup run: %v", err)
	}
	observedAt := fixture.now.Truncate(time.Hour).Add(30 * time.Minute)
	activatedAt := observedAt.Add(-time.Hour)
	if _, err := fixture.admin.Exec(ctx, `UPDATE market_data.collection_subscriptions
SET created_at = $1, updated_at = $1, close_delay_seconds = 0, revision_delay_seconds = NULL
WHERE id = $2`, activatedAt, fixture.subscription.ID.UUID()); err != nil {
		t.Fatalf("prepare scheduler subscription: %v", err)
	}
	t.Cleanup(func() {
		cleanupCtx := context.Background()
		_, _ = fixture.admin.Exec(cleanupCtx, "DELETE FROM market_data.ingestion_tasks WHERE subscription_id = $1", fixture.subscription.ID.UUID())
		_, _ = fixture.admin.Exec(cleanupCtx, `DELETE FROM market_data.ingestion_runs
WHERE context->>'subscription_id' = $1`, fixture.subscription.ID.String())
	})

	calendar, err := markettime.NewNYSECalendar()
	if err != nil {
		t.Fatalf("NewNYSECalendar() error = %v", err)
	}
	first, err := scheduler.NewIncrementalScheduler(scheduler.IncrementalConfig{PageSize: 10}, fixture.store, integrationFixedClock{now: observedAt}, domain.NewID, calendar)
	if err != nil {
		t.Fatalf("NewIncrementalScheduler(first) error = %v", err)
	}
	second, err := scheduler.NewIncrementalScheduler(scheduler.IncrementalConfig{PageSize: 10}, fixture.store, integrationFixedClock{now: observedAt}, domain.NewID, calendar)
	if err != nil {
		t.Fatalf("NewIncrementalScheduler(second) error = %v", err)
	}

	results := make(chan scheduler.IncrementalResult, 2)
	errorsChannel := make(chan error, 2)
	var waitGroup sync.WaitGroup
	for _, instance := range []*scheduler.IncrementalScheduler{first, second} {
		waitGroup.Add(1)
		go func(instance *scheduler.IncrementalScheduler) {
			defer waitGroup.Done()
			result, runErr := instance.RunOnce(ctx)
			results <- result
			errorsChannel <- runErr
		}(instance)
	}
	waitGroup.Wait()
	close(results)
	close(errorsChannel)
	for runErr := range errorsChannel {
		if runErr != nil {
			t.Fatalf("RunOnce() error = %v", runErr)
		}
	}
	var created, existing int
	for result := range results {
		if result.ScannedSubscriptions != 1 || result.DueWindows != 1 {
			t.Fatalf("RunOnce() result = %#v", result)
		}
		created += result.CreatedRuns
		existing += result.ExistingRuns
	}
	if created != 1 || existing != 1 {
		t.Fatalf("concurrent scheduler results created=%d existing=%d", created, existing)
	}

	var runCount, taskCount int
	var runKey, runType, triggerType, runStatus, taskStatus string
	var rangeStart, rangeEnd, scheduledAt time.Time
	if err := fixture.admin.QueryRow(ctx, `SELECT count(*) OVER (),
       (SELECT count(*) FROM market_data.ingestion_tasks WHERE subscription_id = $1),
       runs.run_key, runs.run_type, runs.trigger_type, runs.status, runs.scheduled_at,
       tasks.status, tasks.range_start, tasks.range_end
FROM market_data.ingestion_runs AS runs
JOIN market_data.ingestion_tasks AS tasks ON tasks.run_id = runs.id
WHERE tasks.subscription_id = $1`, fixture.subscription.ID.UUID()).Scan(
		&runCount, &taskCount, &runKey, &runType, &triggerType, &runStatus, &scheduledAt,
		&taskStatus, &rangeStart, &rangeEnd,
	); err != nil {
		t.Fatalf("query scheduled batch: %v", err)
	}
	wantEnd := observedAt.Truncate(time.Hour)
	wantStart := wantEnd.Add(-time.Hour)
	wantKey := scheduler.StableRunKey(scheduler.WindowTriggerClose, fixture.subscription.ID, wantStart, wantEnd)
	if runCount != 1 || taskCount != 1 || runKey != wantKey || runType != "incremental" || triggerType != "scheduler" ||
		runStatus != "pending" || taskStatus != "pending" || !scheduledAt.Equal(wantEnd) || !rangeStart.Equal(wantStart) || !rangeEnd.Equal(wantEnd) {
		t.Fatalf("persisted run count=%d tasks=%d key=%s type=%s/%s status=%s/%s scheduled=%v range=[%v,%v)",
			runCount, taskCount, runKey, runType, triggerType, runStatus, taskStatus, scheduledAt, rangeStart, rangeEnd)
	}
	claim, err := fixture.store.ClaimNextTask(ctx, "worker-sch003", observedAt, time.Minute)
	if err != nil || claim == nil || claim.Task.SubscriptionID != fixture.subscription.ID || !claim.Task.RangeStart.Equal(wantStart) || !claim.Task.RangeEnd.Equal(wantEnd) {
		t.Fatalf("ClaimNextTask() = (%#v, %v)", claim, err)
	}
}

type integrationFixedClock struct{ now time.Time }

func (clock integrationFixedClock) Now() time.Time { return clock.now }
