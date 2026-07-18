package ingestion

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"xr-trading/market-info-service/internal/domain"
)

type backfillStoreStub struct {
	target         ExecutionContext
	resolveErr     error
	createErr      error
	resolveCalls   int
	createCalls    int
	providerCode   domain.Code
	instrumentCode domain.Code
	interval       domain.BarInterval
	effectiveAt    time.Time
	createdRun     domain.IngestionRun
	createdTask    domain.IngestionTask
}

func (store *backfillStoreStub) ResolveBackfillTarget(_ context.Context, providerCode, instrumentCode domain.Code, interval domain.BarInterval, effectiveAt time.Time) (ExecutionContext, error) {
	store.resolveCalls++
	store.providerCode, store.instrumentCode, store.interval, store.effectiveAt = providerCode, instrumentCode, interval, effectiveAt
	return store.target, store.resolveErr
}

func (store *backfillStoreStub) CreateBackfillRunWithTask(_ context.Context, run domain.IngestionRun, task domain.IngestionTask) error {
	store.createCalls++
	store.createdRun, store.createdTask = run, task
	return store.createErr
}

func TestBackfillServiceCreatesOneAuditedRunAndTask(t *testing.T) {
	fixture := newIngestionFixture(t)
	createdAt := fixture.executeAt.UTC()
	runID := domain.IDFromUUID(uuid.MustParse("019f1452-90f7-7992-a87a-ca2727891701"))
	taskID := domain.IDFromUUID(uuid.MustParse("019f1452-90f7-7992-a87a-ca2727891702"))
	store := &backfillStoreStub{target: fixture.execution}
	service, err := NewBackfillService(BackfillConfig{}, store, func() time.Time { return createdAt }, sequenceIDGenerator(runID, taskID))
	if err != nil {
		t.Fatalf("NewBackfillService() error = %v", err)
	}
	start := createdAt.Add(-48 * time.Hour).In(time.FixedZone("west", -7*60*60))
	end := createdAt.Add(-24 * time.Hour).In(time.FixedZone("east", 8*60*60))
	input := validBackfillInput(fixture, start, end)
	result, err := service.Create(context.Background(), input)
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	if result.RunID != runID || result.TaskID != taskID || result.Status != "pending" || !result.CreatedAt.Equal(createdAt) {
		t.Fatalf("result = %#v", result)
	}
	if store.resolveCalls != 1 || store.createCalls != 1 || store.providerCode != fixture.execution.Provider.Code || store.instrumentCode != fixture.execution.Instrument.Code || store.interval != domain.BarInterval1Hour || !store.effectiveAt.Equal(createdAt) {
		t.Fatalf("resolve calls=%d create=%d provider=%s instrument=%s interval=%s at=%v", store.resolveCalls, store.createCalls, store.providerCode, store.instrumentCode, store.interval, store.effectiveAt)
	}
	run, task := store.createdRun, store.createdTask
	if run.ID != runID || run.RunKey != "backfill.manual."+runID.String() || run.RunType != "backfill" || run.TriggerType != "manual" || run.Status != "pending" || run.RequestedBy == nil || *run.RequestedBy != input.RequestedBy || run.TaskCount != 1 {
		t.Fatalf("run = %#v", run)
	}
	if task.ID != taskID || task.RunID != runID || task.SubscriptionID != fixture.execution.Subscription.ID || task.Status != "pending" || task.AttemptCount != 0 || task.MaxAttempts != defaultBackfillMaximumAttempts || task.RetryOfTaskID != nil || !task.RangeStart.Equal(start.UTC()) || !task.RangeEnd.Equal(end.UTC()) {
		t.Fatalf("task = %#v", task)
	}
	var audit map[string]string
	if err := json.Unmarshal(run.Context, &audit); err != nil || audit["reason"] != input.Reason || audit["actor_type"] != input.ActorType || audit["request_id"] != input.RequestID || audit["range_start"] != start.UTC().Format(time.RFC3339Nano) {
		t.Fatalf("run context = %s, error=%v", run.Context, err)
	}
}

func TestBackfillServiceRejectsInvalidInputBeforeResolution(t *testing.T) {
	fixture := newIngestionFixture(t)
	now := fixture.executeAt.UTC()
	valid := validBackfillInput(fixture, now.Add(-2*time.Hour), now.Add(-time.Hour))
	tests := []struct {
		name   string
		mutate func(*BackfillInput)
	}{
		{"provider", func(input *BackfillInput) { input.ProviderCode = "BYBIT" }},
		{"instrument", func(input *BackfillInput) { input.InstrumentCode = "" }},
		{"interval", func(input *BackfillInput) { input.Interval = "5m" }},
		{"zero start", func(input *BackfillInput) { input.StartTime = time.Time{} }},
		{"reversed", func(input *BackfillInput) { input.StartTime = input.EndTime }},
		{"future", func(input *BackfillInput) { input.EndTime = now.Add(time.Second) }},
		{"reason", func(input *BackfillInput) { input.Reason = " reason" }},
		{"long reason", func(input *BackfillInput) { input.Reason = strings.Repeat("r", maximumBackfillReasonLength+1) }},
		{"requested by", func(input *BackfillInput) { input.RequestedBy = "admin\nroot" }},
		{"actor", func(input *BackfillInput) { input.ActorType = "robot" }},
		{"request id", func(input *BackfillInput) { input.RequestID = "" }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			input := valid
			test.mutate(&input)
			store := &backfillStoreStub{target: fixture.execution}
			service, _ := NewBackfillService(BackfillConfig{}, store, func() time.Time { return now }, sequenceIDGenerator())
			if _, err := service.Create(context.Background(), input); !errors.Is(err, domain.ErrInvalidData) || store.resolveCalls != 0 || store.createCalls != 0 {
				t.Fatalf("Create() error=%v resolve=%d create=%d", err, store.resolveCalls, store.createCalls)
			}
		})
	}
	store := &backfillStoreStub{target: fixture.execution}
	service, _ := NewBackfillService(BackfillConfig{}, store, func() time.Time { return now }, sequenceIDGenerator())
	if _, err := service.Create(nil, valid); !errors.Is(err, domain.ErrInvalidData) {
		t.Fatalf("Create(nil) error = %v", err)
	}
}

func TestBackfillServiceValidatesDependenciesAndResolvedTarget(t *testing.T) {
	fixture := newIngestionFixture(t)
	now := fixture.executeAt.UTC()
	input := validBackfillInput(fixture, now.Add(-2*time.Hour), now.Add(-time.Hour))
	validStore := &backfillStoreStub{target: fixture.execution}
	for _, test := range []struct {
		name   string
		config BackfillConfig
		store  BackfillStore
		now    func() time.Time
		newID  IDGenerator
	}{
		{"attempts", BackfillConfig{MaximumAttempts: -1}, validStore, time.Now, domain.NewID},
		{"store", BackfillConfig{}, nil, time.Now, domain.NewID},
		{"clock", BackfillConfig{}, validStore, nil, domain.NewID},
		{"id generator", BackfillConfig{}, validStore, time.Now, nil},
	} {
		t.Run(test.name, func(t *testing.T) {
			if _, err := NewBackfillService(test.config, test.store, test.now, test.newID); err == nil {
				t.Fatal("NewBackfillService() error = nil")
			}
		})
	}

	resolveFailure := errors.New("resolve failed")
	store := &backfillStoreStub{resolveErr: resolveFailure}
	service, _ := NewBackfillService(BackfillConfig{}, store, func() time.Time { return now }, sequenceIDGenerator())
	if _, err := service.Create(context.Background(), input); !errors.Is(err, resolveFailure) || store.createCalls != 0 {
		t.Fatalf("Create(resolve) error=%v create=%d", err, store.createCalls)
	}

	mismatch := fixture.execution
	mismatch.Provider.Code, _ = domain.ParseCode("longbridge")
	store = &backfillStoreStub{target: mismatch}
	service, _ = NewBackfillService(BackfillConfig{}, store, func() time.Time { return now }, sequenceIDGenerator())
	if _, err := service.Create(context.Background(), input); !errors.Is(err, domain.ErrInvalidData) || store.createCalls != 0 {
		t.Fatalf("Create(mismatch) error=%v create=%d", err, store.createCalls)
	}

	disabled := fixture.execution
	disabled.Subscription.Enabled = false
	store = &backfillStoreStub{target: disabled}
	service, _ = NewBackfillService(BackfillConfig{}, store, func() time.Time { return now }, sequenceIDGenerator())
	if _, err := service.Create(context.Background(), input); !errors.Is(err, domain.ErrInvalidState) || store.createCalls != 0 {
		t.Fatalf("Create(disabled) error=%v create=%d", err, store.createCalls)
	}
}

func TestBackfillServicePreservesGenerationAndDuplicateFailures(t *testing.T) {
	fixture := newIngestionFixture(t)
	now := fixture.executeAt.UTC()
	input := validBackfillInput(fixture, now.Add(-2*time.Hour), now.Add(-time.Hour))
	generationFailure := errors.New("UUID failed")
	for _, generator := range []IDGenerator{
		func() (domain.ID, error) { return domain.ID{}, generationFailure },
		sequenceIDGenerator(domain.IDFromUUID(uuid.New())),
		sequenceIDGenerator(runTestID(), domain.ID{}),
		sequenceIDGenerator(runTestID(), runTestID()),
	} {
		store := &backfillStoreStub{target: fixture.execution}
		service, _ := NewBackfillService(BackfillConfig{}, store, func() time.Time { return now }, generator)
		if _, err := service.Create(context.Background(), input); err == nil || store.createCalls != 0 {
			t.Fatalf("Create(generator) error=%v create=%d", err, store.createCalls)
		}
	}

	store := &backfillStoreStub{target: fixture.execution, createErr: ErrBackfillAlreadyRunning}
	service, _ := NewBackfillService(BackfillConfig{MaximumAttempts: 3}, store, func() time.Time { return now }, sequenceIDGenerator(runTestID(), domain.IDFromUUID(uuid.MustParse("019f1452-90f7-7992-a87a-ca2727891703"))))
	if _, err := service.Create(context.Background(), input); !errors.Is(err, ErrBackfillAlreadyRunning) || store.createdTask.MaxAttempts != 3 {
		t.Fatalf("Create(duplicate) error=%v task=%#v", err, store.createdTask)
	}
}

func validBackfillInput(fixture ingestionFixture, start, end time.Time) BackfillInput {
	return BackfillInput{
		ProviderCode: fixture.execution.Provider.Code.String(), InstrumentCode: fixture.execution.Instrument.Code.String(),
		Interval: fixture.execution.Subscription.Interval, StartTime: start, EndTime: end,
		Reason: "initialize historical data", RequestedBy: "admin@example.com", ActorType: "user", RequestID: "req_ing006",
	}
}

func sequenceIDGenerator(ids ...domain.ID) IDGenerator {
	index := 0
	return func() (domain.ID, error) {
		if index >= len(ids) {
			return domain.ID{}, errors.New("no fixture ID")
		}
		id := ids[index]
		index++
		return id, nil
	}
}
