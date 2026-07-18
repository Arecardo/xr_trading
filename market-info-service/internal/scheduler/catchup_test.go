package scheduler

import (
	"context"
	"errors"
	"testing"
	"time"

	"xr-trading/market-info-service/internal/domain"
)

func TestCatchUpUsesVerifiedCheckpointAndMarketBarFacts(t *testing.T) {
	now := mustTime(t, "2026-07-18T12:02:00Z")
	target := schedulingTargetFixture(t, "019f1452-90f7-7992-a87a-ca2727891601", domain.AssetTypeCrypto, domain.InstrumentTypeSpot, "spot", mustTime(t, "2026-07-18T07:30:00Z"))
	checkpointOpen := mustTime(t, "2026-07-18T08:00:00Z")
	store := &incrementalStoreStub{
		targets: []SchedulingTarget{target}, batches: make(map[string]ScheduledBatch), recovered: 2,
		checkpoints: map[domain.ID]*domain.IngestionCheckpoint{target.Subscription.ID: {
			SubscriptionID: target.Subscription.ID, LastClosedOpenTime: &checkpointOpen, UpdatedAt: now.Add(-time.Minute),
		}},
		closedBars: map[domain.ID][]time.Time{target.Subscription.ID: {
			mustTime(t, "2026-07-18T07:00:00Z"), checkpointOpen,
			mustTime(t, "2026-07-18T09:00:00Z"), mustTime(t, "2026-07-18T11:00:00Z"),
		}},
	}
	scheduler, _ := NewIncrementalScheduler(IncrementalConfig{}, store, fixedClock{now: now}, domain.NewID, testNYSECalendar(t))
	result, err := scheduler.RunOnce(context.Background())
	if err != nil || result != (IncrementalResult{ScannedSubscriptions: 1, DueWindows: 1, CreatedRuns: 1, RecoveredTasks: 2}) {
		t.Fatalf("RunOnce() = (%#v, %v)", result, err)
	}
	for _, batch := range store.batches {
		assertBatchRange(t, batch, "incremental", "2026-07-18T10:00:00Z", "2026-07-18T11:00:00Z", "2026-07-18T11:02:00Z")
	}
}

func TestCatchUpRejectsUnverifiedCheckpointAndBoundsColdStart(t *testing.T) {
	now := mustTime(t, "2026-07-18T12:02:00Z")
	target := schedulingTargetFixture(t, "019f1452-90f7-7992-a87a-ca2727891601", domain.AssetTypeCrypto, domain.InstrumentTypeSpot, "spot", mustTime(t, "2026-07-18T08:30:00Z"))
	checkpointOpen := mustTime(t, "2026-07-18T09:00:00Z")
	store := &incrementalStoreStub{
		targets: []SchedulingTarget{target}, batches: make(map[string]ScheduledBatch),
		checkpoints: map[domain.ID]*domain.IngestionCheckpoint{target.Subscription.ID: {
			SubscriptionID: target.Subscription.ID, LastClosedOpenTime: &checkpointOpen, UpdatedAt: now,
		}},
		closedBars: map[domain.ID][]time.Time{target.Subscription.ID: {
			checkpointOpen, mustTime(t, "2026-07-18T10:00:00Z"), mustTime(t, "2026-07-18T11:00:00Z"),
		}},
	}
	scheduler, _ := NewIncrementalScheduler(IncrementalConfig{MaximumCatchUpBars: 2}, store, fixedClock{now: now}, domain.NewID, testNYSECalendar(t))
	result, err := scheduler.RunOnce(context.Background())
	if err != nil || result.CreatedRuns != 1 {
		t.Fatalf("RunOnce() = (%#v, %v)", result, err)
	}
	for _, batch := range store.batches {
		assertBatchRange(t, batch, "incremental", "2026-07-18T08:00:00Z", "2026-07-18T09:00:00Z", "2026-07-18T09:02:00Z")
	}
}

func TestExpectedUSCatchUpWindowsCrossEarlyCloseAndWeekend(t *testing.T) {
	calendar := testNYSECalendar(t)
	latest, err := CalculateLatestUSWindow(calendar, domain.BarInterval1Hour, WindowTriggerClose, mustTime(t, "2026-11-30T21:02:00Z"), 2*time.Minute)
	if err != nil || latest == nil {
		t.Fatalf("CalculateLatestUSWindow() = (%#v, %v)", latest, err)
	}
	windows, err := expectedUSWindows(calendar, domain.BarInterval1Hour, mustTime(t, "2026-11-27T17:00:00Z"), *latest, 2*time.Minute, 3)
	if err != nil || len(windows) != 3 {
		t.Fatalf("expectedUSWindows() = (%#v, %v)", windows, err)
	}
	want := [][2]time.Time{
		{mustTime(t, "2026-11-27T16:30:00Z"), mustTime(t, "2026-11-27T17:30:00Z")},
		{mustTime(t, "2026-11-27T17:30:00Z"), mustTime(t, "2026-11-27T18:00:00Z")},
		{mustTime(t, "2026-11-30T14:30:00Z"), mustTime(t, "2026-11-30T15:30:00Z")},
	}
	for index := range want {
		if windows[index].RangeStart != want[index][0] || windows[index].RangeEnd != want[index][1] {
			t.Fatalf("window[%d] = %#v", index, windows[index])
		}
	}
}

func TestMissingWindowGroupsPreservesExpectedOrderAndLimit(t *testing.T) {
	candidates, _ := expectedContinuousWindows(domain.BarInterval1Hour, mustTime(t, "2026-07-18T08:00:00Z"), CollectionWindow{RangeStart: mustTime(t, "2026-07-18T12:00:00Z")}, 2*time.Minute, 10)
	existing := []time.Time{mustTime(t, "2026-07-18T09:00:00Z"), mustTime(t, "2026-07-18T11:00:00Z")}
	groups := missingWindowGroups(candidates, existing, 10, 2)
	if len(groups) != 2 || groups[0].RangeStart != mustTime(t, "2026-07-18T08:00:00Z") || groups[0].RangeEnd != mustTime(t, "2026-07-18T09:00:00Z") ||
		groups[1].RangeStart != mustTime(t, "2026-07-18T10:00:00Z") || groups[1].RangeEnd != mustTime(t, "2026-07-18T11:00:00Z") {
		t.Fatalf("missingWindowGroups() = %#v", groups)
	}
	bounded := missingWindowGroups(candidates, nil, 2, 2)
	if len(bounded) != 2 || bounded[0].RangeStart != mustTime(t, "2026-07-18T08:00:00Z") || bounded[0].RangeEnd != mustTime(t, "2026-07-18T10:00:00Z") ||
		bounded[1].RangeStart != mustTime(t, "2026-07-18T10:00:00Z") || bounded[1].RangeEnd != mustTime(t, "2026-07-18T12:00:00Z") {
		t.Fatalf("missingWindowGroups(bounded) = %#v", bounded)
	}
}

func TestCatchUpReportsCheckpointBarAndRecoveryFailures(t *testing.T) {
	now := mustTime(t, "2026-07-18T12:02:00Z")
	target := schedulingTargetFixture(t, "019f1452-90f7-7992-a87a-ca2727891601", domain.AssetTypeCrypto, domain.InstrumentTypeSpot, "spot", now.Add(-time.Hour))
	want := errors.New("store failed")

	for _, store := range []*incrementalStoreStub{
		{targets: []SchedulingTarget{target}, recoverErr: want},
		{targets: []SchedulingTarget{target}, checkpointErr: want},
		{targets: []SchedulingTarget{target}, closedBarsErr: want, checkpoints: map[domain.ID]*domain.IngestionCheckpoint{target.Subscription.ID: validCheckpoint(target.Subscription.ID, mustTime(t, "2026-07-18T10:00:00Z"), now)}},
	} {
		scheduler, _ := NewIncrementalScheduler(IncrementalConfig{}, store, fixedClock{now: now}, domain.NewID, testNYSECalendar(t))
		if _, err := scheduler.RunOnce(context.Background()); !errors.Is(err, want) {
			t.Fatalf("RunOnce(store failure) error = %v", err)
		}
	}

	bad := validCheckpoint(domain.IDFromUUID(target.Subscription.ID.UUID()), mustTime(t, "2026-07-18T10:30:00Z"), now)
	store := &incrementalStoreStub{targets: []SchedulingTarget{target}, checkpoints: map[domain.ID]*domain.IngestionCheckpoint{target.Subscription.ID: bad}}
	scheduler, _ := NewIncrementalScheduler(IncrementalConfig{}, store, fixedClock{now: now}, domain.NewID, testNYSECalendar(t))
	if _, err := scheduler.RunOnce(context.Background()); !errors.Is(err, domain.ErrInvalidData) {
		t.Fatalf("RunOnce(unaligned checkpoint) error = %v", err)
	}

	wrongID := schedulingTargetFixture(t, "019f1452-90f7-7992-a87a-ca2727891602", domain.AssetTypeCrypto, domain.InstrumentTypeSpot, "spot", now).Subscription.ID
	store = &incrementalStoreStub{targets: []SchedulingTarget{target}, checkpoints: map[domain.ID]*domain.IngestionCheckpoint{target.Subscription.ID: validCheckpoint(wrongID, mustTime(t, "2026-07-18T10:00:00Z"), now)}}
	scheduler, _ = NewIncrementalScheduler(IncrementalConfig{}, store, fixedClock{now: now}, domain.NewID, testNYSECalendar(t))
	if _, err := scheduler.RunOnce(context.Background()); !errors.Is(err, domain.ErrInvalidData) {
		t.Fatalf("RunOnce(wrong checkpoint) error = %v", err)
	}
}

func validCheckpoint(subscriptionID domain.ID, open, updatedAt time.Time) *domain.IngestionCheckpoint {
	return &domain.IngestionCheckpoint{SubscriptionID: subscriptionID, LastClosedOpenTime: &open, UpdatedAt: updatedAt}
}
