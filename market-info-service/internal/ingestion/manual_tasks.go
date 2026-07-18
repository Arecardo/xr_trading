package ingestion

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"xr-trading/market-info-service/internal/domain"
)

const defaultManualRetryMaximumAttempts = 5

var (
	// ErrManualRetryAlreadyRunning means the failed Task already has an active
	// administrator-created successor.
	ErrManualRetryAlreadyRunning = errors.New("manual retry is already running")
	// ErrManualRetrySourceUnavailable means the original subscription or its
	// ProviderInstrument is no longer eligible for collection.
	ErrManualRetrySourceUnavailable = errors.New("manual retry source is unavailable")
)

// ManualTaskConfig controls the retry budget assigned to newly created Tasks.
type ManualTaskConfig struct {
	MaximumAttempts int
}

// TaskOperationAudit is the durable attribution attached to a retry or cancel.
type TaskOperationAudit struct {
	RequestedBy string
	ActorType   string
	RequestID   string
	Reason      string
}

// ManualRetryCreation contains generated identities plus authenticated audit
// data. The Store copies subscription and range fields from the locked source
// Task so the successor cannot be redirected by the caller.
type ManualRetryCreation struct {
	OriginalTaskID  domain.ID
	RunID           domain.ID
	TaskID          domain.ID
	MaximumAttempts int
	Audit           TaskOperationAudit
	CreatedAt       time.Time
	RunContext      json.RawMessage
}

// ManualRetryResult identifies the one pending Run and Task created.
type ManualRetryResult struct {
	RunID     domain.ID
	TaskID    domain.ID
	Status    string
	CreatedAt time.Time
}

// TaskCancellationResult is the committed cooperative-cancel state.
type TaskCancellationResult struct {
	TaskID     domain.ID
	RunID      domain.ID
	Status     string
	CanceledAt time.Time
}

// ManualTaskStore performs the state checks and writes under PostgreSQL row
// locks. Provider calls are never made by these operations.
type ManualTaskStore interface {
	CreateManualRetry(context.Context, ManualRetryCreation) error
	CancelTaskWithAudit(context.Context, domain.ID, TaskOperationAudit, time.Time) (domain.ID, error)
}

// RunRefresher rebuilds the denormalized Run cache after Task mutation.
type RunRefresher interface {
	Refresh(context.Context, domain.ID) (RunSummary, error)
}

// ManualTaskService implements administrator retry and cooperative cancel.
type ManualTaskService struct {
	store           ManualTaskStore
	runs            RunRefresher
	now             func() time.Time
	newID           IDGenerator
	maximumAttempts int
}

func NewManualTaskService(config ManualTaskConfig, store ManualTaskStore, runs RunRefresher, now func() time.Time, newID IDGenerator) (*ManualTaskService, error) {
	if config.MaximumAttempts == 0 {
		config.MaximumAttempts = defaultManualRetryMaximumAttempts
	}
	if config.MaximumAttempts < 1 {
		return nil, errors.New("manual retry maximum attempts must be positive")
	}
	if store == nil || runs == nil || now == nil || newID == nil {
		return nil, errors.New("manual task service dependencies are required")
	}
	return &ManualTaskService{store: store, runs: runs, now: now, newID: newID, maximumAttempts: config.MaximumAttempts}, nil
}

// Retry preserves the failed Task and creates one new repair Run/Task pair.
func (service *ManualTaskService) Retry(ctx context.Context, originalTaskID domain.ID, audit TaskOperationAudit) (ManualRetryResult, error) {
	if ctx == nil || originalTaskID.IsZero() || !validTaskOperationAudit(audit) {
		return ManualRetryResult{}, fmt.Errorf("retry ingestion task: %w", domain.ErrInvalidData)
	}
	createdAt := service.now().UTC()
	if createdAt.IsZero() {
		return ManualRetryResult{}, fmt.Errorf("retry ingestion task time: %w", domain.ErrInvalidData)
	}
	runID, err := service.newID()
	if err != nil || !validBackfillID(runID) || runID == originalTaskID {
		if err == nil {
			err = domain.ErrInvalidData
		}
		return ManualRetryResult{}, fmt.Errorf("generate manual retry run ID: %w", err)
	}
	taskID, err := service.newID()
	if err != nil || !validBackfillID(taskID) || taskID == runID || taskID == originalTaskID {
		if err == nil {
			err = domain.ErrInvalidData
		}
		return ManualRetryResult{}, fmt.Errorf("generate manual retry task ID: %w", err)
	}
	runContext, err := json.Marshal(map[string]string{
		"actor_type": audit.ActorType, "request_id": audit.RequestID, "reason": audit.Reason,
		"retry_of_task_id": originalTaskID.String(),
	})
	if err != nil {
		return ManualRetryResult{}, fmt.Errorf("encode manual retry context: %w", err)
	}
	creation := ManualRetryCreation{
		OriginalTaskID: originalTaskID, RunID: runID, TaskID: taskID,
		MaximumAttempts: service.maximumAttempts, Audit: audit, CreatedAt: createdAt, RunContext: runContext,
	}
	if err := service.store.CreateManualRetry(ctx, creation); err != nil {
		return ManualRetryResult{}, fmt.Errorf("create manual retry: %w", err)
	}
	return ManualRetryResult{RunID: runID, TaskID: taskID, Status: "pending", CreatedAt: createdAt}, nil
}

// Cancel commits a cooperative cancellation and then best-effort refreshes the
// parent Run cache. Query APIs still derive current state from Task truth if the
// refresh encounters a concurrent or transient failure.
func (service *ManualTaskService) Cancel(ctx context.Context, taskID domain.ID, audit TaskOperationAudit) (TaskCancellationResult, error) {
	if ctx == nil || taskID.IsZero() || !validTaskOperationAudit(audit) {
		return TaskCancellationResult{}, fmt.Errorf("cancel ingestion task: %w", domain.ErrInvalidData)
	}
	canceledAt := service.now().UTC()
	if canceledAt.IsZero() {
		return TaskCancellationResult{}, fmt.Errorf("cancel ingestion task time: %w", domain.ErrInvalidData)
	}
	runID, err := service.store.CancelTaskWithAudit(ctx, taskID, audit, canceledAt)
	if err != nil {
		return TaskCancellationResult{}, fmt.Errorf("cancel ingestion task: %w", err)
	}
	// Cancellation is already committed. A cache refresh failure must not turn a
	// successful mutation into a retryable HTTP error and cause duplicate writes.
	_, _ = service.runs.Refresh(ctx, runID)
	return TaskCancellationResult{TaskID: taskID, RunID: runID, Status: "canceled", CanceledAt: canceledAt}, nil
}

func validTaskOperationAudit(audit TaskOperationAudit) bool {
	return validBackfillText(audit.RequestedBy, maximumBackfillAuditTextLength) &&
		validBackfillText(audit.RequestID, maximumBackfillAuditTextLength) &&
		validBackfillText(audit.Reason, maximumBackfillReasonLength) &&
		(audit.ActorType == "user" || audit.ActorType == "service")
}
