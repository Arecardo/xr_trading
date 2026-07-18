package ingestion

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"

	"xr-trading/market-info-service/internal/domain"
)

type manualTaskStoreStub struct {
	creation ingestionCreationCapture
	cancelID domain.ID
	audit    TaskOperationAudit
	err      error
	runID    domain.ID
}

type ingestionCreationCapture struct {
	value ManualRetryCreation
	calls int
}

func (stub *manualTaskStoreStub) CreateManualRetry(_ context.Context, creation ManualRetryCreation) error {
	stub.creation.value, stub.creation.calls = creation, stub.creation.calls+1
	return stub.err
}
func (stub *manualTaskStoreStub) CancelTaskWithAudit(_ context.Context, taskID domain.ID, audit TaskOperationAudit, _ time.Time) (domain.ID, error) {
	stub.cancelID, stub.audit = taskID, audit
	return stub.runID, stub.err
}

type runRefresherStub struct {
	runID domain.ID
	err   error
	calls int
}

func (stub *runRefresherStub) Refresh(_ context.Context, runID domain.ID) (RunSummary, error) {
	stub.runID, stub.calls = runID, stub.calls+1
	return RunSummary{RunID: runID}, stub.err
}

func TestManualTaskServiceCreatesAuditedRetry(t *testing.T) {
	now := time.Date(2026, 7, 19, 12, 0, 0, 0, time.FixedZone("east", 8*60*60))
	originalID := manualTaskID("019f1452-90f7-7992-a87a-ca2727898101")
	runID := manualTaskID("019f1452-90f7-7992-a87a-ca2727898102")
	taskID := manualTaskID("019f1452-90f7-7992-a87a-ca2727898103")
	store := &manualTaskStoreStub{}
	runs := &runRefresherStub{}
	service, err := NewManualTaskService(ManualTaskConfig{MaximumAttempts: 3}, store, runs, func() time.Time { return now }, sequenceIDGenerator(runID, taskID))
	if err != nil {
		t.Fatal(err)
	}
	audit := validManualTaskAudit()
	result, err := service.Retry(context.Background(), originalID, audit)
	if err != nil || result.RunID != runID || result.TaskID != taskID || result.Status != "pending" || result.CreatedAt.Location() != time.UTC {
		t.Fatalf("Retry() = (%#v, %v)", result, err)
	}
	creation := store.creation.value
	if store.creation.calls != 1 || creation.OriginalTaskID != originalID || creation.MaximumAttempts != 3 || creation.Audit != audit || creation.CreatedAt.Location() != time.UTC ||
		!containsJSONText(creation.RunContext, `"retry_of_task_id":"`+originalID.String()+`"`) || !containsJSONText(creation.RunContext, `"request_id":"req_manual_test"`) {
		t.Fatalf("creation = %#v calls=%d", creation, store.creation.calls)
	}
}

func TestManualTaskServiceCancelsAndRefreshesBestEffort(t *testing.T) {
	now := time.Date(2026, 7, 19, 12, 0, 0, 0, time.UTC)
	taskID := manualTaskID("019f1452-90f7-7992-a87a-ca2727898111")
	runID := manualTaskID("019f1452-90f7-7992-a87a-ca2727898112")
	store := &manualTaskStoreStub{runID: runID}
	runs := &runRefresherStub{err: domain.ErrRetryable}
	service, _ := NewManualTaskService(ManualTaskConfig{}, store, runs, func() time.Time { return now }, sequenceIDGenerator())
	result, err := service.Cancel(context.Background(), taskID, validManualTaskAudit())
	if err != nil || result.TaskID != taskID || result.RunID != runID || result.Status != "canceled" || !result.CanceledAt.Equal(now) {
		t.Fatalf("Cancel() = (%#v, %v)", result, err)
	}
	if store.cancelID != taskID || runs.calls != 1 || runs.runID != runID {
		t.Fatalf("store task=%s refresh=%d/%s", store.cancelID, runs.calls, runs.runID)
	}
}

func TestManualTaskServiceValidatesAndPropagatesFailures(t *testing.T) {
	now := time.Now().UTC()
	validID := manualTaskID("019f1452-90f7-7992-a87a-ca2727898121")
	for _, test := range []struct {
		name   string
		config ManualTaskConfig
		store  ManualTaskStore
		runs   RunRefresher
		now    func() time.Time
		newID  IDGenerator
	}{
		{"attempts", ManualTaskConfig{MaximumAttempts: -1}, &manualTaskStoreStub{}, &runRefresherStub{}, func() time.Time { return now }, sequenceIDGenerator()},
		{"store", ManualTaskConfig{}, nil, &runRefresherStub{}, func() time.Time { return now }, sequenceIDGenerator()},
		{"runs", ManualTaskConfig{}, &manualTaskStoreStub{}, nil, func() time.Time { return now }, sequenceIDGenerator()},
		{"clock", ManualTaskConfig{}, &manualTaskStoreStub{}, &runRefresherStub{}, nil, sequenceIDGenerator()},
	} {
		t.Run(test.name, func(t *testing.T) {
			if _, err := NewManualTaskService(test.config, test.store, test.runs, test.now, test.newID); err == nil {
				t.Fatal("NewManualTaskService() error = nil")
			}
		})
	}
	store := &manualTaskStoreStub{err: ErrManualRetryAlreadyRunning, runID: validID}
	service, _ := NewManualTaskService(ManualTaskConfig{}, store, &runRefresherStub{}, func() time.Time { return now }, sequenceIDGenerator(
		manualTaskID("019f1452-90f7-7992-a87a-ca2727898122"), manualTaskID("019f1452-90f7-7992-a87a-ca2727898123"),
	))
	if _, err := service.Retry(context.Background(), validID, validManualTaskAudit()); !errors.Is(err, ErrManualRetryAlreadyRunning) {
		t.Fatalf("Retry(store error) = %v", err)
	}
	if _, err := service.Cancel(context.Background(), validID, validManualTaskAudit()); !errors.Is(err, ErrManualRetryAlreadyRunning) {
		t.Fatalf("Cancel(store error) = %v", err)
	}
	badAudits := []TaskOperationAudit{{}, {RequestedBy: " admin", ActorType: "user", RequestID: "req", Reason: "reason"}, {RequestedBy: "admin", ActorType: "robot", RequestID: "req", Reason: "reason"}}
	for _, audit := range badAudits {
		if _, err := service.Retry(context.Background(), validID, audit); !errors.Is(err, domain.ErrInvalidData) {
			t.Fatalf("Retry(bad audit) = %v", err)
		}
	}
	if _, err := service.Cancel(nil, domain.ID{}, validManualTaskAudit()); !errors.Is(err, domain.ErrInvalidData) {
		t.Fatalf("Cancel(invalid) = %v", err)
	}
}

func validManualTaskAudit() TaskOperationAudit {
	return TaskOperationAudit{RequestedBy: "admin@example.com", ActorType: "user", RequestID: "req_manual_test", Reason: "credentials renewed"}
}

func manualTaskID(value string) domain.ID { return domain.IDFromUUID(uuid.MustParse(value)) }

func containsJSONText(value []byte, expected string) bool {
	for index := 0; index+len(expected) <= len(value); index++ {
		if string(value[index:index+len(expected)]) == expected {
			return true
		}
	}
	return false
}
