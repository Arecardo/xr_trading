//go:build integration

package integration_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	poolpostgres "xr-trading/market-info-service/internal/database/postgres"
	"xr-trading/market-info-service/internal/domain"
	"xr-trading/market-info-service/internal/ingestion"
	"xr-trading/market-info-service/internal/ingestion/ports"
	"xr-trading/market-info-service/internal/providers"
	repositorypostgres "xr-trading/market-info-service/internal/repository/postgres"
	"xr-trading/market-info-service/internal/testkit"
)

type ing004BlockingAdapter struct {
	providerCode domain.Code
	gate         *testkit.Gate
}

func (adapter *ing004BlockingAdapter) ProviderCode() domain.Code { return adapter.providerCode }
func (adapter *ing004BlockingAdapter) Capabilities(ctx context.Context) (ports.ProviderCapabilities, error) {
	return (&ing002Adapter{providerCode: adapter.providerCode}).Capabilities(ctx)
}
func (*ing004BlockingAdapter) FetchLatestQuotes(context.Context, []ports.ProviderInstrumentRef) ([]ports.ProviderQuote, error) {
	return nil, errors.New("not implemented")
}
func (adapter *ing004BlockingAdapter) FetchBars(ctx context.Context, request ports.FetchBarsRequest) (ports.FetchBarsResult, error) {
	if err := adapter.gate.Wait(ctx); err != nil {
		return ports.FetchBarsResult{}, err
	}
	return (&ing002Adapter{providerCode: adapter.providerCode}).FetchBars(ctx, request)
}

type ing004Fixture struct {
	admin        *pgx.Conn
	store        *repositorypostgres.IngestionRepository
	provider     domain.Provider
	mapping      domain.ProviderInstrument
	subscription domain.CollectionSubscription
	run          domain.IngestionRun
	task         domain.IngestionTask
	now          time.Time
}

func TestING004RunningCancellationFencesOldWorkerAgainstPostgres(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	fixture := newING004Fixture(t, ctx, "cancel", 5)
	claim, err := fixture.store.ClaimNextTask(ctx, "worker-ing004-cancel", fixture.now, time.Minute)
	if err != nil || claim == nil || claim.Task.ID != fixture.task.ID {
		t.Fatalf("ClaimNextTask() = (%#v, %v)", claim, err)
	}
	gate := testkit.NewGate()
	adapter := &ing004BlockingAdapter{providerCode: fixture.provider.Code, gate: gate}
	registry, err := providers.NewRegistry(ctx, adapter)
	if err != nil {
		t.Fatalf("NewRegistry() error = %v", err)
	}
	finishedAt := fixture.now.Add(time.Second)
	service, _ := ingestion.NewService(ingestion.Config{BarsPerPage: 100, MaximumPages: 10}, fixture.store, registry, ingestion.NewStructuralBarQualityValidator(), func() time.Time { return finishedAt })
	result := make(chan error, 1)
	go func() { result <- service.ExecuteTask(ctx, *claim) }()
	if err := gate.AwaitEntered(ctx); err != nil {
		t.Fatalf("wait for old worker Provider call: %v", err)
	}
	canceledAt := fixture.now.Add(500 * time.Millisecond)
	if err := fixture.store.CancelTask(ctx, fixture.task.ID, "admin", "incorrect range", canceledAt); err != nil {
		t.Fatalf("CancelTask() error = %v", err)
	}
	gate.Release()
	if err := <-result; !errors.Is(err, ingestion.ErrTaskLeaseLost) {
		t.Fatalf("old ExecuteTask() error = %v", err)
	}

	var status string
	var lockedBy *string
	var persistedFinishedAt time.Time
	if err := fixture.admin.QueryRow(ctx, `SELECT status, locked_by, finished_at
FROM market_data.ingestion_tasks WHERE id = $1`, fixture.task.ID.UUID()).Scan(&status, &lockedBy, &persistedFinishedAt); err != nil {
		t.Fatalf("query canceled task: %v", err)
	}
	if status != "canceled" || lockedBy != nil || !persistedFinishedAt.Equal(canceledAt) {
		t.Fatalf("canceled task status=%s lockedBy=%v finished=%v", status, lockedBy, persistedFinishedAt)
	}
	assertING004NoMarketWrites(t, ctx, fixture)
	var checkpoints int
	if err := fixture.admin.QueryRow(ctx, "SELECT count(*) FROM market_data.ingestion_checkpoints WHERE subscription_id = $1", fixture.subscription.ID.UUID()).Scan(&checkpoints); err != nil || checkpoints != 0 {
		t.Fatalf("canceled checkpoint count=%d error=%v", checkpoints, err)
	}
}

func TestING004ExpiredLeaseRecoversAndFencesOldWorkerAgainstPostgres(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	fixture := newING004Fixture(t, ctx, "recover", 5)
	claim, err := fixture.store.ClaimNextTask(ctx, "worker-ing004-old", fixture.now, time.Second)
	if err != nil || claim == nil || claim.Task.ID != fixture.task.ID {
		t.Fatalf("ClaimNextTask() = (%#v, %v)", claim, err)
	}
	gate := testkit.NewGate()
	adapter := &ing004BlockingAdapter{providerCode: fixture.provider.Code, gate: gate}
	registry, _ := providers.NewRegistry(ctx, adapter)
	oldFinishedAt := fixture.now.Add(500 * time.Millisecond)
	service, _ := ingestion.NewService(ingestion.Config{BarsPerPage: 100, MaximumPages: 10}, fixture.store, registry, ingestion.NewStructuralBarQualityValidator(), func() time.Time { return oldFinishedAt })
	oldResult := make(chan error, 1)
	go func() { oldResult <- service.ExecuteTask(ctx, *claim) }()
	if err := gate.AwaitEntered(ctx); err != nil {
		t.Fatalf("wait for expired worker Provider call: %v", err)
	}

	recoveredAt := fixture.now.Add(2 * time.Second)
	type recoveryResult struct {
		count int64
		err   error
	}
	results := make(chan recoveryResult, 2)
	for range 2 {
		go func() {
			count, err := fixture.store.RecoverExpiredTasks(ctx, recoveredAt)
			results <- recoveryResult{count: count, err: err}
		}()
	}
	var recoveredTotal int64
	for range 2 {
		result := <-results
		if result.err != nil {
			t.Fatalf("RecoverExpiredTasks() error = %v", result.err)
		}
		recoveredTotal += result.count
	}
	if recoveredTotal != 1 {
		t.Fatalf("concurrent recovered count = %d", recoveredTotal)
	}
	var status, errorCode string
	var attemptCount, consecutiveFailures int
	var lastAttemptAt time.Time
	var lastSuccessOpenTime *time.Time
	if err := fixture.admin.QueryRow(ctx, `SELECT status, attempt_count, error_code
FROM market_data.ingestion_tasks WHERE id = $1`, fixture.task.ID.UUID()).Scan(&status, &attemptCount, &errorCode); err != nil {
		t.Fatalf("query recovered task: %v", err)
	}
	if status != "pending" || attemptCount != 1 || errorCode != "lease_expired" {
		t.Fatalf("recovered task status=%s attempt=%d code=%s", status, attemptCount, errorCode)
	}
	if err := fixture.admin.QueryRow(ctx, `SELECT last_attempt_at, last_success_open_time, consecutive_failures
FROM market_data.ingestion_checkpoints WHERE subscription_id = $1`, fixture.subscription.ID.UUID()).Scan(&lastAttemptAt, &lastSuccessOpenTime, &consecutiveFailures); err != nil {
		t.Fatalf("query recovery checkpoint: %v", err)
	}
	if !lastAttemptAt.Equal(recoveredAt) || lastSuccessOpenTime != nil || consecutiveFailures != 1 {
		t.Fatalf("recovery checkpoint attempt=%v successOpen=%v failures=%d", lastAttemptAt, lastSuccessOpenTime, consecutiveFailures)
	}
	gate.Release()
	if err := <-oldResult; !errors.Is(err, ingestion.ErrTaskLeaseLost) {
		t.Fatalf("old ExecuteTask() error = %v", err)
	}
	assertING004NoMarketWrites(t, ctx, fixture)

	newClaim, err := fixture.store.ClaimNextTask(ctx, "worker-ing004-new", recoveredAt, time.Minute)
	if err != nil || newClaim == nil || newClaim.Task.ID != fixture.task.ID || newClaim.Task.AttemptCount != 2 {
		t.Fatalf("ClaimNextTask(recovered) = (%#v, %v)", newClaim, err)
	}
	successRegistry, _ := providers.NewRegistry(ctx, &ing002Adapter{providerCode: fixture.provider.Code})
	service, _ = ingestion.NewService(ingestion.Config{BarsPerPage: 100, MaximumPages: 10}, fixture.store, successRegistry, ingestion.NewStructuralBarQualityValidator(), func() time.Time { return recoveredAt.Add(time.Second) })
	if err := service.ExecuteTask(ctx, *newClaim); err != nil {
		t.Fatalf("new ExecuteTask() error = %v", err)
	}
	if err := fixture.admin.QueryRow(ctx, "SELECT status, attempt_count FROM market_data.ingestion_tasks WHERE id = $1", fixture.task.ID.UUID()).Scan(&status, &attemptCount); err != nil || status != "success" || attemptCount != 2 {
		t.Fatalf("completed recovered task status=%s attempt=%d error=%v", status, attemptCount, err)
	}
	if err := fixture.admin.QueryRow(ctx, "SELECT consecutive_failures FROM market_data.ingestion_checkpoints WHERE subscription_id = $1", fixture.subscription.ID.UUID()).Scan(&consecutiveFailures); err != nil || consecutiveFailures != 0 {
		t.Fatalf("completed checkpoint failures=%d error=%v", consecutiveFailures, err)
	}
}

func TestING004ExpiredLeaseAtAttemptLimitBecomesFailedAgainstPostgres(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	fixture := newING004Fixture(t, ctx, "exhausted", 1)
	claim, err := fixture.store.ClaimNextTask(ctx, "worker-ing004-exhausted", fixture.now, time.Second)
	if err != nil || claim == nil || claim.Task.ID != fixture.task.ID || claim.Task.AttemptCount != 1 {
		t.Fatalf("ClaimNextTask() = (%#v, %v)", claim, err)
	}
	recoveredAt := fixture.now.Add(2 * time.Second)
	if recovered, err := fixture.store.RecoverExpiredTasks(ctx, recoveredAt); err != nil || recovered != 1 {
		t.Fatalf("RecoverExpiredTasks() = (%d, %v)", recovered, err)
	}
	var status, errorCode string
	var finishedAt time.Time
	if err := fixture.admin.QueryRow(ctx, `SELECT status, error_code, finished_at
FROM market_data.ingestion_tasks WHERE id = $1`, fixture.task.ID.UUID()).Scan(&status, &errorCode, &finishedAt); err != nil {
		t.Fatalf("query exhausted task: %v", err)
	}
	if status != "failed" || errorCode != "lease_expired" || !finishedAt.Equal(recoveredAt) {
		t.Fatalf("exhausted task status=%s code=%s finished=%v", status, errorCode, finishedAt)
	}
	var consecutiveFailures int
	if err := fixture.admin.QueryRow(ctx, "SELECT consecutive_failures FROM market_data.ingestion_checkpoints WHERE subscription_id = $1", fixture.subscription.ID.UUID()).Scan(&consecutiveFailures); err != nil || consecutiveFailures != 1 {
		t.Fatalf("exhausted checkpoint failures=%d error=%v", consecutiveFailures, err)
	}
	assertING004NoMarketWrites(t, ctx, fixture)
}

func newING004Fixture(t *testing.T, ctx context.Context, suffix string, maxAttempts int) ing004Fixture {
	t.Helper()
	admin := openAdminConnection(t, ctx)
	t.Cleanup(func() { _ = admin.Close(context.Background()) })
	assetID, instrumentID, _, _ := createCoreFixture(t, ctx, admin)
	providerID := newIntegrationID(t)
	now := time.Now().UTC().Truncate(time.Second)
	provider := domain.Provider{
		ID: providerID, Code: integrationCode(t, "bybit-ing004-"+suffix+"-"+providerID.String()), Name: "ING004 Bybit",
		ProviderType: domain.ProviderTypeExchange, Status: domain.ProviderStatusActive, CreatedAt: now, UpdatedAt: now,
	}
	pool, err := poolpostgres.OpenPool(ctx, poolpostgres.Config{DatabaseURL: integrationDatabaseURL(t), MaxConns: 5, MinConns: 0, MaxConnLifetime: time.Minute, HealthCheckPeriod: time.Second})
	if err != nil {
		t.Fatalf("OpenPool() error = %v", err)
	}
	t.Cleanup(pool.Close)
	catalog, _ := repositorypostgres.NewCatalogRepository(pool)
	subscriptions, _ := repositorypostgres.NewSubscriptionRepository(pool)
	store, _ := repositorypostgres.NewIngestionRepository(pool)
	if err := catalog.CreateProvider(ctx, provider); err != nil {
		t.Fatalf("CreateProvider() error = %v", err)
	}
	mapping := db012Mapping(t, providerID, instrumentID, now, "ing004-"+suffix)
	if err := catalog.CreateProviderInstrument(ctx, mapping); err != nil {
		t.Fatalf("CreateProviderInstrument() error = %v", err)
	}
	subscription := domain.CollectionSubscription{
		ID: newIntegrationID(t), ProviderInstrumentID: mapping.ID, Interval: "1h", Enabled: true,
		Priority: 1, CloseDelaySeconds: 0, CreatedAt: now, UpdatedAt: now,
	}
	if err := subscriptions.CreateSubscription(ctx, subscription); err != nil {
		t.Fatalf("CreateSubscription() error = %v", err)
	}
	run, task := db013RunTask(t, subscription.ID, now, "ing004-"+suffix)
	fixtureOrder := time.Unix(0, 0).UTC()
	run.CreatedAt = fixtureOrder
	task.CreatedAt = fixtureOrder
	task.UpdatedAt = fixtureOrder
	task.MaxAttempts = maxAttempts
	if err := store.CreateRunWithTask(ctx, run, task); err != nil {
		t.Fatalf("CreateRunWithTask() error = %v", err)
	}
	t.Cleanup(func() {
		cleanupING002Fixture(t, context.Background(), admin, run.ID, subscription.ID, providerID, mapping.ID, instrumentID, assetID)
	})
	return ing004Fixture{admin: admin, store: store, provider: provider, mapping: mapping, subscription: subscription, run: run, task: task, now: now}
}

func assertING004NoMarketWrites(t *testing.T, ctx context.Context, fixture ing004Fixture) {
	t.Helper()
	var barCount, issueCount int
	if err := fixture.admin.QueryRow(ctx, "SELECT count(*) FROM market_data.market_bars WHERE provider_instrument_id = $1", fixture.mapping.ID.UUID()).Scan(&barCount); err != nil {
		t.Fatalf("query market bars: %v", err)
	}
	if err := fixture.admin.QueryRow(ctx, "SELECT count(*) FROM market_data.data_quality_issues WHERE provider_instrument_id = $1", fixture.mapping.ID.UUID()).Scan(&issueCount); err != nil {
		t.Fatalf("query quality issues: %v", err)
	}
	if barCount != 0 || issueCount != 0 {
		t.Fatalf("unexpected writes bars=%d issues=%d", barCount, issueCount)
	}
}
