package scheduler

import (
	"context"
	"errors"
	"sort"
	"testing"
	"time"

	"github.com/google/uuid"

	"xr-trading/market-info-service/internal/domain"
	"xr-trading/market-info-service/internal/markettime"
	"xr-trading/market-info-service/internal/testkit"
)

func TestIncrementalSchedulerCreatesStableCloseAndRevisionBatches(t *testing.T) {
	now := mustTime(t, "2026-07-18T12:02:00Z")
	revisionSeconds := 3600
	target := schedulingTargetFixture(t, "019f1452-90f7-7992-a87a-ca2727891601", domain.AssetTypeCrypto, domain.InstrumentTypeSpot, "spot", now.Add(-24*time.Hour))
	target.Subscription.RevisionDelaySeconds = &revisionSeconds
	store := &incrementalStoreStub{targets: []SchedulingTarget{target}, batches: make(map[string]ScheduledBatch)}
	calendar := testNYSECalendar(t)
	clock := testkit.NewManualClock(now)
	ids := testkit.NewIDSequence(
		domain.IDFromUUID(uuid.MustParse("019f1452-90f7-7992-a87a-ca2727891602")),
		domain.IDFromUUID(uuid.MustParse("019f1452-90f7-7992-a87a-ca2727891603")),
		domain.IDFromUUID(uuid.MustParse("019f1452-90f7-7992-a87a-ca2727891604")),
		domain.IDFromUUID(uuid.MustParse("019f1452-90f7-7992-a87a-ca2727891605")),
		domain.IDFromUUID(uuid.MustParse("019f1452-90f7-7992-a87a-ca2727891606")),
		domain.IDFromUUID(uuid.MustParse("019f1452-90f7-7992-a87a-ca2727891607")),
		domain.IDFromUUID(uuid.MustParse("019f1452-90f7-7992-a87a-ca2727891608")),
		domain.IDFromUUID(uuid.MustParse("019f1452-90f7-7992-a87a-ca2727891609")),
	)
	scheduler, err := NewIncrementalScheduler(IncrementalConfig{PageSize: 1, MaximumAttempts: 3}, store, clock, ids.Next, calendar)
	if err != nil {
		t.Fatalf("NewIncrementalScheduler() error = %v", err)
	}

	first, err := scheduler.RunOnce(context.Background())
	if err != nil || first != (IncrementalResult{ScannedSubscriptions: 1, DueWindows: 2, CreatedRuns: 2}) {
		t.Fatalf("RunOnce(first) = (%#v, %v)", first, err)
	}
	if len(store.batches) != 2 {
		t.Fatalf("created batch count = %d", len(store.batches))
	}
	var closeBatch, revisionBatch ScheduledBatch
	for _, batch := range store.batches {
		if err := batch.Validate(); err != nil {
			t.Fatalf("batch.Validate() error = %v", err)
		}
		if batch.Task.MaxAttempts != 3 || batch.Run.RunKey != StableRunKey(batch.Trigger, target.Subscription.ID, batch.Task.RangeStart, batch.Task.RangeEnd) {
			t.Fatalf("batch = %#v", batch)
		}
		if batch.Trigger == WindowTriggerClose {
			closeBatch = batch
		} else {
			revisionBatch = batch
		}
	}
	assertBatchRange(t, closeBatch, "incremental", "2026-07-17T12:00:00Z", "2026-07-18T12:00:00Z", "2026-07-18T12:02:00Z")
	assertBatchRange(t, revisionBatch, "revision", "2026-07-18T10:00:00Z", "2026-07-18T11:00:00Z", "2026-07-18T12:00:00Z")
	if closeBatch.Run.ID.String() != "019f1452-90f7-7992-a87a-ca2727891602" || closeBatch.Task.ID.String() != "019f1452-90f7-7992-a87a-ca2727891603" ||
		revisionBatch.Run.ID.String() != "019f1452-90f7-7992-a87a-ca2727891604" || revisionBatch.Task.ID.String() != "019f1452-90f7-7992-a87a-ca2727891605" {
		t.Fatalf("deterministic batch identities close=(%s,%s) revision=(%s,%s)", closeBatch.Run.ID, closeBatch.Task.ID, revisionBatch.Run.ID, revisionBatch.Task.ID)
	}

	second, err := scheduler.RunOnce(context.Background())
	if err != nil || second != (IncrementalResult{ScannedSubscriptions: 1, DueWindows: 2, ExistingRuns: 2}) || len(store.batches) != 2 {
		t.Fatalf("RunOnce(second) = (%#v, %v), batches=%d", second, err, len(store.batches))
	}
	if ids.Calls() != 8 || ids.Remaining() != 0 || clock.Now() != now {
		t.Fatalf("deterministic dependencies calls=%d remaining=%d now=%v", ids.Calls(), ids.Remaining(), clock.Now())
	}
}

func TestIncrementalSchedulerPagesMarketsAndHonorsActivationBoundary(t *testing.T) {
	now := mustTime(t, "2026-07-06T14:32:00Z")
	crypto := schedulingTargetFixture(t, "019f1452-90f7-7992-a87a-ca2727891601", domain.AssetTypeCrypto, domain.InstrumentTypeSpot, "spot", now.Add(-time.Hour))
	stock := schedulingTargetFixture(t, "019f1452-90f7-7992-a87a-ca2727891602", domain.AssetTypeStock, domain.InstrumentTypeEquity, "us", now.Add(-time.Hour))
	tooNew := schedulingTargetFixture(t, "019f1452-90f7-7992-a87a-ca2727891603", domain.AssetTypeETF, domain.InstrumentTypeETF, "us", mustTime(t, "2026-07-06T14:30:00Z"))
	store := &incrementalStoreStub{targets: []SchedulingTarget{crypto, stock, tooNew}, batches: make(map[string]ScheduledBatch)}
	scheduler, _ := NewIncrementalScheduler(IncrementalConfig{PageSize: 1}, store, fixedClock{now: now}, domain.NewID, testNYSECalendar(t))
	result, err := scheduler.RunOnce(context.Background())
	if err != nil || result != (IncrementalResult{ScannedSubscriptions: 3, DueWindows: 2, CreatedRuns: 2}) || store.listCalls != 3 {
		t.Fatalf("RunOnce() = (%#v, %v), listCalls=%d", result, err, store.listCalls)
	}
	ranges := make(map[domain.ID][2]time.Time)
	for _, batch := range store.batches {
		ranges[batch.Task.SubscriptionID] = [2]time.Time{batch.Task.RangeStart, batch.Task.RangeEnd}
	}
	if got := ranges[crypto.Subscription.ID]; got != [2]time.Time{mustTime(t, "2026-07-06T13:00:00Z"), mustTime(t, "2026-07-06T14:00:00Z")} {
		t.Fatalf("crypto range = %v", got)
	}
	if got := ranges[stock.Subscription.ID]; got != [2]time.Time{mustTime(t, "2026-07-06T13:30:00Z"), mustTime(t, "2026-07-06T14:30:00Z")} {
		t.Fatalf("stock range = %v", got)
	}
	if _, exists := ranges[tooNew.Subscription.ID]; exists {
		t.Fatal("scheduler created a task whose bar closed before subscription activation")
	}
}

func TestLatestWindowCalculators(t *testing.T) {
	continuous, err := CalculateLatestContinuousWindow(domain.BarInterval1Day, WindowTriggerClose, mustTime(t, "2026-07-18T00:02:00Z"), 2*time.Minute)
	if err != nil || continuous == nil || continuous.RangeStart != mustTime(t, "2026-07-17T00:00:00Z") || continuous.RangeEnd != mustTime(t, "2026-07-18T00:00:00Z") {
		t.Fatalf("CalculateLatestContinuousWindow() = (%#v, %v)", continuous, err)
	}
	calendar := testNYSECalendar(t)
	early, err := CalculateLatestUSWindow(calendar, domain.BarInterval1Hour, WindowTriggerClose, mustTime(t, "2026-11-27T18:02:00Z"), 2*time.Minute)
	if err != nil || early == nil || early.RangeStart != mustTime(t, "2026-11-27T17:30:00Z") || early.RangeEnd != mustTime(t, "2026-11-27T18:00:00Z") {
		t.Fatalf("CalculateLatestUSWindow(early close) = (%#v, %v)", early, err)
	}
	beforeRange, err := CalculateLatestUSWindow(calendar, domain.BarInterval1Hour, WindowTriggerClose, mustTime(t, "2026-01-01T15:00:00Z"), 2*time.Minute)
	if err != nil || beforeRange != nil {
		t.Fatalf("CalculateLatestUSWindow(calendar start) = (%#v, %v)", beforeRange, err)
	}
	if _, err := CalculateLatestContinuousWindow("5m", WindowTriggerClose, time.Now(), 0); !errors.Is(err, domain.ErrInvalidData) {
		t.Fatalf("CalculateLatestContinuousWindow(invalid) error = %v", err)
	}
	if _, err := CalculateLatestUSWindow(nil, domain.BarInterval1Hour, WindowTriggerClose, time.Now(), 0); !errors.Is(err, domain.ErrInvalidData) {
		t.Fatalf("CalculateLatestUSWindow(invalid) error = %v", err)
	}
}

func TestSchedulingTargetAndBatchValidation(t *testing.T) {
	now := mustTime(t, "2026-07-18T12:02:00Z")
	target := schedulingTargetFixture(t, "019f1452-90f7-7992-a87a-ca2727891601", domain.AssetTypeCrypto, domain.InstrumentTypeSpot, "spot", now.Add(-time.Hour))
	if err := target.Validate(); err != nil {
		t.Fatalf("SchedulingTarget.Validate() error = %v", err)
	}
	mutations := []func(*SchedulingTarget){
		func(value *SchedulingTarget) { value.Subscription.Enabled = false },
		func(value *SchedulingTarget) { value.Subscription.Interval = "5m" },
		func(value *SchedulingTarget) { value.Capabilities.Historical = false },
		func(value *SchedulingTarget) { value.AssetType = domain.AssetTypeCash },
		func(value *SchedulingTarget) { value.ProviderMarket = " us" },
	}
	for _, mutate := range mutations {
		value := target
		mutate(&value)
		if err := value.Validate(); err == nil {
			t.Fatalf("SchedulingTarget.Validate(%#v) error = nil", value)
		}
	}

	store := &incrementalStoreStub{targets: []SchedulingTarget{target}, batches: make(map[string]ScheduledBatch)}
	scheduler, _ := NewIncrementalScheduler(IncrementalConfig{}, store, fixedClock{now: now}, domain.NewID, testNYSECalendar(t))
	if _, err := scheduler.RunOnce(context.Background()); err != nil {
		t.Fatalf("RunOnce() error = %v", err)
	}
	var batch ScheduledBatch
	for _, batch = range store.batches {
	}
	if err := batch.Validate(); err != nil {
		t.Fatalf("ScheduledBatch.Validate() error = %v", err)
	}
	bad := batch
	bad.Run.RunKey = "wrong"
	if !errors.Is(bad.Validate(), domain.ErrInvalidData) {
		t.Fatalf("ScheduledBatch.Validate(wrong key) error = %v", bad.Validate())
	}
	bad = batch
	bad.Trigger = "repair"
	if !errors.Is(bad.Validate(), domain.ErrInvalidData) {
		t.Fatalf("ScheduledBatch.Validate(wrong trigger) error = %v", bad.Validate())
	}
	if StableRunKey(WindowTriggerClose, target.Subscription.ID, batch.Task.RangeStart, batch.Task.RangeEnd) ==
		StableRunKey(WindowTriggerRevision, target.Subscription.ID, batch.Task.RangeStart, batch.Task.RangeEnd) {
		t.Fatal("close and revision stable keys collide")
	}
}

func TestIncrementalSchedulerValidatesDependenciesAndFailures(t *testing.T) {
	calendar := testNYSECalendar(t)
	store := &incrementalStoreStub{batches: make(map[string]ScheduledBatch)}
	for _, test := range []struct {
		name   string
		config IncrementalConfig
		store  IncrementalStore
		clock  Clock
		newID  func() (domain.ID, error)
		cal    markettime.TradingCalendar
	}{
		{"page too large", IncrementalConfig{PageSize: 101}, store, fixedClock{now: time.Now()}, domain.NewID, calendar},
		{"negative attempts", IncrementalConfig{MaximumAttempts: -1}, store, fixedClock{now: time.Now()}, domain.NewID, calendar},
		{"catch-up bars too large", IncrementalConfig{MaximumCatchUpBars: 10001}, store, fixedClock{now: time.Now()}, domain.NewID, calendar},
		{"catch-up runs too large", IncrementalConfig{MaximumCatchUpRuns: 101}, store, fixedClock{now: time.Now()}, domain.NewID, calendar},
		{"nil store", IncrementalConfig{}, nil, fixedClock{now: time.Now()}, domain.NewID, calendar},
		{"nil clock", IncrementalConfig{}, store, nil, domain.NewID, calendar},
		{"nil IDs", IncrementalConfig{}, store, fixedClock{now: time.Now()}, nil, calendar},
		{"nil calendar", IncrementalConfig{}, store, fixedClock{now: time.Now()}, domain.NewID, nil},
	} {
		t.Run(test.name, func(t *testing.T) {
			if _, err := NewIncrementalScheduler(test.config, test.store, test.clock, test.newID, test.cal); err == nil {
				t.Fatal("NewIncrementalScheduler() error = nil")
			}
		})
	}
	var nilScheduler *IncrementalScheduler
	if _, err := nilScheduler.RunOnce(context.Background()); err == nil {
		t.Fatal("nil RunOnce() error = nil")
	}
	scheduler, _ := NewIncrementalScheduler(IncrementalConfig{}, store, fixedClock{now: time.Now()}, domain.NewID, calendar)
	if _, err := scheduler.RunOnce(nil); !errors.Is(err, domain.ErrInvalidData) {
		t.Fatalf("RunOnce(nil) error = %v", err)
	}
	zeroClockScheduler, _ := NewIncrementalScheduler(IncrementalConfig{}, store, fixedClock{}, domain.NewID, calendar)
	if _, err := zeroClockScheduler.RunOnce(context.Background()); !errors.Is(err, domain.ErrInvalidData) {
		t.Fatalf("RunOnce(zero clock) error = %v", err)
	}
}

func TestIncrementalSchedulerReportsStoreCursorAndIDFailures(t *testing.T) {
	now := mustTime(t, "2026-07-18T12:02:00Z")
	target := schedulingTargetFixture(t, "019f1452-90f7-7992-a87a-ca2727891601", domain.AssetTypeCrypto, domain.InstrumentTypeSpot, "spot", now.Add(-time.Hour))
	calendar := testNYSECalendar(t)
	want := errors.New("store failed")

	listFailure := &incrementalStoreStub{listErr: want}
	scheduler, _ := NewIncrementalScheduler(IncrementalConfig{}, listFailure, fixedClock{now: now}, domain.NewID, calendar)
	if _, err := scheduler.RunOnce(context.Background()); !errors.Is(err, want) {
		t.Fatalf("RunOnce(list failure) error = %v", err)
	}
	badCursor := &incrementalStoreStub{customPage: &SchedulingTargetPage{NextAfterID: &target.Subscription.ID}}
	scheduler, _ = NewIncrementalScheduler(IncrementalConfig{}, badCursor, fixedClock{now: now}, domain.NewID, calendar)
	if _, err := scheduler.RunOnce(context.Background()); !errors.Is(err, domain.ErrInvalidData) {
		t.Fatalf("RunOnce(bad cursor) error = %v", err)
	}
	invalidTarget := target
	invalidTarget.Subscription.Enabled = false
	invalidStore := &incrementalStoreStub{targets: []SchedulingTarget{invalidTarget}}
	scheduler, _ = NewIncrementalScheduler(IncrementalConfig{}, invalidStore, fixedClock{now: now}, domain.NewID, calendar)
	if _, err := scheduler.RunOnce(context.Background()); err == nil {
		t.Fatal("RunOnce(invalid target) error = nil")
	}
	IDFailure := &incrementalStoreStub{targets: []SchedulingTarget{target}}
	scheduler, _ = NewIncrementalScheduler(IncrementalConfig{}, IDFailure, fixedClock{now: now}, func() (domain.ID, error) { return domain.ID{}, want }, calendar)
	if _, err := scheduler.RunOnce(context.Background()); !errors.Is(err, want) {
		t.Fatalf("RunOnce(ID failure) error = %v", err)
	}
	persistFailure := &incrementalStoreStub{targets: []SchedulingTarget{target}, createErr: want}
	scheduler, _ = NewIncrementalScheduler(IncrementalConfig{}, persistFailure, fixedClock{now: now}, domain.NewID, calendar)
	if _, err := scheduler.RunOnce(context.Background()); !errors.Is(err, want) {
		t.Fatalf("RunOnce(persist failure) error = %v", err)
	}
}

type incrementalStoreStub struct {
	targets       []SchedulingTarget
	batches       map[string]ScheduledBatch
	checkpoints   map[domain.ID]*domain.IngestionCheckpoint
	closedBars    map[domain.ID][]time.Time
	listErr       error
	checkpointErr error
	closedBarsErr error
	createErr     error
	recoverErr    error
	recovered     int64
	customPage    *SchedulingTargetPage
	listCalls     int
}

func (store *incrementalStoreStub) LoadSchedulingCheckpoint(_ context.Context, subscriptionID domain.ID) (*domain.IngestionCheckpoint, error) {
	if store.checkpointErr != nil {
		return nil, store.checkpointErr
	}
	checkpoint := store.checkpoints[subscriptionID]
	if checkpoint == nil {
		return nil, nil
	}
	copy := *checkpoint
	return &copy, nil
}

func (store *incrementalStoreStub) ListClosedBarOpenTimes(_ context.Context, target SchedulingTarget, rangeStart, rangeEnd time.Time) ([]time.Time, error) {
	if store.closedBarsErr != nil {
		return nil, store.closedBarsErr
	}
	var result []time.Time
	for _, value := range store.closedBars[target.Subscription.ID] {
		if !value.Before(rangeStart) && value.Before(rangeEnd) {
			result = append(result, value)
		}
	}
	return result, nil
}

func (store *incrementalStoreStub) RecoverExpiredTasks(_ context.Context, _ time.Time) (int64, error) {
	return store.recovered, store.recoverErr
}

func (store *incrementalStoreStub) ListSchedulingTargets(_ context.Context, afterID *domain.ID, limit int, _ time.Time) (SchedulingTargetPage, error) {
	store.listCalls++
	if store.listErr != nil {
		return SchedulingTargetPage{}, store.listErr
	}
	if store.customPage != nil {
		return *store.customPage, nil
	}
	items := append([]SchedulingTarget(nil), store.targets...)
	sort.Slice(items, func(left, right int) bool {
		return items[left].Subscription.ID.String() < items[right].Subscription.ID.String()
	})
	start := 0
	if afterID != nil {
		for start < len(items) && items[start].Subscription.ID.String() <= afterID.String() {
			start++
		}
	}
	remaining := items[start:]
	page := SchedulingTargetPage{}
	if len(remaining) > limit {
		page.Items = remaining[:limit]
		next := page.Items[len(page.Items)-1].Subscription.ID
		page.NextAfterID = &next
	} else {
		page.Items = remaining
	}
	return page, nil
}

func (store *incrementalStoreStub) CreateScheduledBatch(_ context.Context, batch ScheduledBatch) (bool, error) {
	if store.createErr != nil {
		return false, store.createErr
	}
	if err := batch.Validate(); err != nil {
		return false, err
	}
	if store.batches == nil {
		store.batches = make(map[string]ScheduledBatch)
	}
	if _, exists := store.batches[batch.Run.RunKey]; exists {
		return false, nil
	}
	store.batches[batch.Run.RunKey] = batch
	return true, nil
}

func schedulingTargetFixture(t *testing.T, subscriptionUUID string, assetType domain.AssetType, instrumentType domain.InstrumentType, providerMarket string, updatedAt time.Time) SchedulingTarget {
	t.Helper()
	subscriptionID := domain.IDFromUUID(uuid.MustParse(subscriptionUUID))
	mappingID := domain.IDFromUUID(uuid.MustParse("019f1452-90f7-7992-a87a-ca2727891701"))
	providerCode, err := domain.ParseCode("bybit")
	if providerMarket == "us" {
		providerCode, err = domain.ParseCode("longbridge")
	}
	if err != nil {
		t.Fatalf("ParseCode(provider) error = %v", err)
	}
	instrumentCode, err := domain.ParseCode("instrument.test.scheduler")
	if err != nil {
		t.Fatalf("ParseCode(instrument) error = %v", err)
	}
	return SchedulingTarget{
		Subscription: domain.CollectionSubscription{
			ID: subscriptionID, ProviderInstrumentID: mappingID, Interval: "1h", Enabled: true,
			CloseDelaySeconds: 120, CreatedAt: updatedAt.Add(-time.Hour), UpdatedAt: updatedAt,
		},
		ProviderCode: providerCode, ProviderMarket: providerMarket, InstrumentCode: instrumentCode,
		AssetType: assetType, InstrumentType: instrumentType,
		Capabilities: domain.ProviderCapabilities{Historical: true, Intervals: []domain.BarInterval{domain.BarInterval1Hour, domain.BarInterval1Day}},
	}
}

func assertBatchRange(t *testing.T, batch ScheduledBatch, runType, start, end, scheduled string) {
	t.Helper()
	if batch.Run.RunType != runType || batch.Task.RangeStart != mustTime(t, start) || batch.Task.RangeEnd != mustTime(t, end) ||
		batch.Run.ScheduledAt == nil || *batch.Run.ScheduledAt != mustTime(t, scheduled) {
		t.Fatalf("batch = %#v", batch)
	}
}
