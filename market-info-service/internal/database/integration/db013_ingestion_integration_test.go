//go:build integration

package integration_test

import (
	"context"
	"sync"
	"testing"
	"time"

	poolpostgres "xr-trading/market-info-service/internal/database/postgres"
	"xr-trading/market-info-service/internal/domain"
	repositorypostgres "xr-trading/market-info-service/internal/repository/postgres"

	"github.com/jackc/pgx/v5"
)

func TestDB013IngestionClaimLeaseAndCheckpointAgainstPostgres(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	admin := openAdminConnection(t, ctx)
	t.Cleanup(func() { _ = admin.Close(context.Background()) })
	assetID, instrumentID, _, _ := createCoreFixture(t, ctx, admin)
	providerID := newIntegrationID(t)

	pool, err := poolpostgres.OpenPool(ctx, poolpostgres.Config{DatabaseURL: integrationDatabaseURL(t), MaxConns: 5, MinConns: 0, MaxConnLifetime: time.Minute, HealthCheckPeriod: time.Second})
	if err != nil {
		t.Fatalf("OpenPool() error = %v", err)
	}
	defer pool.Close()
	catalog, _ := repositorypostgres.NewCatalogRepository(pool)
	subscriptions, _ := repositorypostgres.NewSubscriptionRepository(pool)
	ingestion, err := repositorypostgres.NewIngestionRepository(pool)
	if err != nil {
		t.Fatalf("NewIngestionRepository() error = %v", err)
	}

	now := time.Now().UTC().Truncate(time.Second)
	provider := domain.Provider{ID: providerID, Code: integrationCode(t, "bybit-db013-"+providerID.String()), Name: "DB013 Bybit", ProviderType: "EXCHANGE", Status: "active", CreatedAt: now, UpdatedAt: now}
	if err := catalog.CreateProvider(ctx, provider); err != nil {
		t.Fatalf("CreateProvider() error = %v", err)
	}
	mapping := db012Mapping(t, providerID, instrumentID, now, "db013")
	if err := catalog.CreateProviderInstrument(ctx, mapping); err != nil {
		t.Fatalf("CreateProviderInstrument() error = %v", err)
	}
	subscription := domain.CollectionSubscription{ID: newIntegrationID(t), ProviderInstrumentID: mapping.ID, Interval: "1h", Enabled: true, Priority: 1, CloseDelaySeconds: 0, CreatedAt: now, UpdatedAt: now}
	if err := subscriptions.CreateSubscription(ctx, subscription); err != nil {
		t.Fatalf("CreateSubscription() error = %v", err)
	}
	runOne, taskOne := db013RunTask(t, subscription.ID, now, "one")
	runTwo, taskTwo := db013RunTask(t, subscription.ID, now, "two")
	t.Cleanup(func() {
		deleteDB013Fixture(t, context.Background(), admin, []domain.ID{runOne.ID, runTwo.ID}, subscription.ID, providerID, instrumentID, assetID)
	})
	if err := ingestion.CreateRunWithTask(ctx, runOne, taskOne); err != nil {
		t.Fatalf("CreateRunWithTask(one) error = %v", err)
	}
	if err := ingestion.CreateRunWithTask(ctx, runTwo, taskTwo); err != nil {
		t.Fatalf("CreateRunWithTask(two) error = %v", err)
	}

	claims := make(chan *domain.TaskClaim, 2)
	errs := make(chan error, 2)
	var workers sync.WaitGroup
	for _, worker := range []string{"worker-a", "worker-b"} {
		workers.Add(1)
		go func(worker string) {
			defer workers.Done()
			claim, err := ingestion.ClaimNextTask(ctx, worker, now, time.Minute)
			claims <- claim
			errs <- err
		}(worker)
	}
	workers.Wait()
	close(claims)
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("ClaimNextTask() error = %v", err)
		}
	}
	claimed := make(map[domain.ID]domain.IngestionTask)
	for claim := range claims {
		if claim == nil {
			t.Fatal("ClaimNextTask() returned nil")
		}
		claimed[claim.Task.ID] = claim.Task
	}
	if len(claimed) != 2 {
		t.Fatalf("claimed task count = %d, want 2", len(claimed))
	}
	for _, task := range claimed {
		if task.Status != "running" || task.AttemptCount != 1 || task.LockedBy == nil || task.LockedUntil == nil {
			t.Fatalf("claimed task = %#v", task)
		}
	}

	var cancelID, recoverID domain.ID
	for id := range claimed {
		if cancelID.IsZero() {
			cancelID = id
		} else {
			recoverID = id
		}
	}
	if err := ingestion.CancelTask(ctx, cancelID, "admin", "stop", now.Add(time.Second)); err != nil {
		t.Fatalf("CancelTask() error = %v", err)
	}
	if recovered, err := ingestion.RecoverExpiredTasks(ctx, now.Add(2*time.Minute)); err != nil || recovered != 1 {
		t.Fatalf("RecoverExpiredTasks() = (%d, %v)", recovered, err)
	}
	checkpoint := domain.IngestionCheckpoint{SubscriptionID: subscription.ID, LastAttemptAt: pointerToTime(now), ConsecutiveFailures: 1, UpdatedAt: now}
	if err := ingestion.UpsertCheckpoint(ctx, checkpoint); err != nil {
		t.Fatalf("UpsertCheckpoint() error = %v", err)
	}
	var failures int
	if err := admin.QueryRow(ctx, "SELECT consecutive_failures FROM market_data.ingestion_checkpoints WHERE subscription_id = $1", subscription.ID.UUID()).Scan(&failures); err != nil || failures != 1 {
		t.Fatalf("checkpoint failures = (%d, %v)", failures, err)
	}
	if recoverID.IsZero() {
		t.Fatal("recovery task ID was not selected")
	}
}

func db013RunTask(t *testing.T, subscriptionID domain.ID, now time.Time, suffix string) (domain.IngestionRun, domain.IngestionTask) {
	t.Helper()
	runID, taskID := newIntegrationID(t), newIntegrationID(t)
	run := domain.IngestionRun{ID: runID, RunKey: "db013-run-" + suffix + "-" + runID.String(), RunType: "incremental", TriggerType: "scheduler", Status: "pending", CreatedAt: now}
	task := domain.IngestionTask{ID: taskID, RunID: runID, SubscriptionID: subscriptionID, RangeStart: now.Add(-time.Hour), RangeEnd: now, Status: "pending", MaxAttempts: 5, CreatedAt: now, UpdatedAt: now}
	return run, task
}

func deleteDB013Fixture(t *testing.T, ctx context.Context, admin *pgx.Conn, runIDs []domain.ID, subscriptionID, providerID, instrumentID, assetID domain.ID) {
	t.Helper()
	for _, runID := range runIDs {
		if _, err := admin.Exec(ctx, "DELETE FROM market_data.ingestion_tasks WHERE run_id = $1", runID.UUID()); err != nil {
			t.Errorf("delete ingestion task fixtures: %v", err)
		}
		if _, err := admin.Exec(ctx, "DELETE FROM market_data.ingestion_runs WHERE id = $1", runID.UUID()); err != nil {
			t.Errorf("delete ingestion run fixtures: %v", err)
		}
	}
	if _, err := admin.Exec(ctx, "DELETE FROM market_data.ingestion_checkpoints WHERE subscription_id = $1", subscriptionID.UUID()); err != nil {
		t.Errorf("delete checkpoint fixture: %v", err)
	}
	deleteDB011Fixture(t, ctx, admin, providerID, instrumentID, assetID)
}

func pointerToTime(value time.Time) *time.Time { return &value }
