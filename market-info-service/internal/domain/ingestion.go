package domain

import (
	"context"
	"encoding/json"
	"time"
)

// IngestionRun groups tasks created by one scheduler or manual request.
type IngestionRun struct {
	ID           ID
	RunKey       string
	RunType      string
	TriggerType  string
	Status       string
	ScheduledAt  *time.Time
	StartedAt    *time.Time
	FinishedAt   *time.Time
	RequestedBy  *string
	TaskCount    int
	SuccessCount int
	FailedCount  int
	Context      json.RawMessage
	ErrorSummary json.RawMessage
	CreatedAt    time.Time
}

// IngestionTask is the durable unit of work claimed by a Worker.
type IngestionTask struct {
	ID                ID
	RunID             ID
	SubscriptionID    ID
	RetryOfTaskID     *ID
	RangeStart        time.Time
	RangeEnd          time.Time
	Status            string
	AttemptCount      int
	MaxAttempts       int
	NextAttemptAt     *time.Time
	LockedBy          *string
	LockedUntil       *time.Time
	StartedAt         *time.Time
	FinishedAt        *time.Time
	ProviderRequestID *string
	ErrorCode         *string
	ErrorMessage      *string
	ErrorDetails      json.RawMessage
	CanceledBy        *string
	CancelReason      *string
	CreatedAt         time.Time
	UpdatedAt         time.Time
}

// IngestionCheckpoint accelerates incremental scheduling but is not the
// source of truth for market-data completeness.
type IngestionCheckpoint struct {
	SubscriptionID      ID
	LastSuccessOpenTime *time.Time
	LastClosedOpenTime  *time.Time
	LastAttemptAt       *time.Time
	LastSuccessAt       *time.Time
	ConsecutiveFailures int
	UpdatedAt           time.Time
}

// TaskClaim contains the Worker lease assigned during an atomic claim.
type TaskClaim struct {
	Task IngestionTask
}

// IngestionRepository stores runs, tasks and checkpoints.
type IngestionRepository interface {
	CreateRunWithTask(context.Context, IngestionRun, IngestionTask) error
	ClaimNextTask(context.Context, string, time.Time, time.Duration) (*TaskClaim, error)
	CancelTask(context.Context, ID, string, string, time.Time) error
	RecoverExpiredTasks(context.Context, time.Time) (int64, error)
	UpsertCheckpoint(context.Context, IngestionCheckpoint) error
}
