package postgres

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"xr-trading/market-info-service/internal/domain"
)

// IngestionRepository stores durable run, task and checkpoint state.
type IngestionRepository struct{ database marketDataDatabase }

// NewIngestionRepository constructs an ingestion repository over pool.
func NewIngestionRepository(pool *pgxpool.Pool) (*IngestionRepository, error) {
	if pool == nil {
		return nil, errors.New("PostgreSQL pool is required")
	}
	return newIngestionRepository(pgxMarketDataDatabase{pool: pool})
}

func newIngestionRepository(database marketDataDatabase) (*IngestionRepository, error) {
	if database == nil {
		return nil, errors.New("PostgreSQL database is required")
	}
	return &IngestionRepository{database: database}, nil
}

// CreateRunWithTask creates one Run and its first Task atomically.
func (repository *IngestionRepository) CreateRunWithTask(ctx context.Context, run domain.IngestionRun, task domain.IngestionTask) error {
	if run.ID.IsZero() || task.ID.IsZero() || task.RunID != run.ID || task.SubscriptionID.IsZero() || !task.RangeEnd.After(task.RangeStart) {
		return fmt.Errorf("create run with task: %w", domain.ErrInvalidData)
	}
	tx, err := repository.database.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin create run with task: %w", MapError(err))
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := tx.Exec(ctx, `INSERT INTO market_data.ingestion_runs (id, run_key, run_type, trigger_type, status, scheduled_at, started_at, finished_at, requested_by, task_count, success_count, failed_count, context, error_summary, created_at) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, 1, 0, 0, $10::jsonb, $11::jsonb, $12)`, runArguments(run)...); err != nil {
		return fmt.Errorf("insert ingestion run: %w", MapError(err))
	}
	if _, err := tx.Exec(ctx, insertTaskSQL, taskArguments(task)...); err != nil {
		return fmt.Errorf("insert ingestion task: %w", MapError(err))
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit create run with task: %w", MapError(err))
	}
	return nil
}

// ClaimNextTask atomically selects an eligible task with SKIP LOCKED, enters
// running, increments attempt_count, and writes the Worker lease.
func (repository *IngestionRepository) ClaimNextTask(ctx context.Context, workerID string, now time.Time, leaseDuration time.Duration) (*domain.TaskClaim, error) {
	if workerID == "" || now.IsZero() || leaseDuration <= 0 {
		return nil, fmt.Errorf("claim next task: %w", domain.ErrInvalidData)
	}
	leaseUntil := now.Add(leaseDuration)
	task, err := scanTask(repository.database.QueryRow(ctx, `
WITH candidate AS (
    SELECT id FROM market_data.ingestion_tasks
    WHERE status = 'pending'
       OR (status = 'retry_wait' AND next_attempt_at <= $1)
       OR (status = 'running' AND locked_until < $1)
    ORDER BY created_at ASC
    FOR UPDATE SKIP LOCKED
    LIMIT 1
)
UPDATE market_data.ingestion_tasks AS tasks
SET status = 'running', locked_by = $2, locked_until = $3,
    attempt_count = tasks.attempt_count + 1, next_attempt_at = NULL,
    started_at = $1, updated_at = $1
FROM candidate
WHERE tasks.id = candidate.id
RETURNING `+taskColumnListQualified, TimeToDatabase(now), workerID, TimeToDatabase(leaseUntil)))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("claim next task: %w", MapError(err))
	}
	return &domain.TaskClaim{Task: task}, nil
}

// CancelTask marks an active task canceled. Workers must re-check this state
// before their final data write transaction.
func (repository *IngestionRepository) CancelTask(ctx context.Context, taskID domain.ID, canceledBy, reason string, now time.Time) error {
	if taskID.IsZero() || canceledBy == "" || now.IsZero() {
		return fmt.Errorf("cancel task: %w", domain.ErrInvalidData)
	}
	command, err := repository.database.Exec(ctx, `UPDATE market_data.ingestion_tasks SET status = 'canceled', canceled_by = $1, cancel_reason = $2, locked_by = NULL, locked_until = NULL, finished_at = $3, updated_at = $3 WHERE id = $4 AND status IN ('pending', 'retry_wait', 'running')`, canceledBy, nullableString(reason), TimeToDatabase(now), IDToDatabase(taskID))
	if err != nil {
		return fmt.Errorf("cancel task: %w", MapError(err))
	}
	if command.RowsAffected() == 0 {
		return fmt.Errorf("cancel task: %w", domain.ErrNotFound)
	}
	return nil
}

// RecoverExpiredTasks makes tasks with expired Worker leases eligible again.
func (repository *IngestionRepository) RecoverExpiredTasks(ctx context.Context, now time.Time) (int64, error) {
	if now.IsZero() {
		return 0, fmt.Errorf("recover expired tasks: %w", domain.ErrInvalidData)
	}
	command, err := repository.database.Exec(ctx, `UPDATE market_data.ingestion_tasks SET status = 'pending', locked_by = NULL, locked_until = NULL, next_attempt_at = NULL, updated_at = $1 WHERE status = 'running' AND locked_until < $1`, TimeToDatabase(now))
	if err != nil {
		return 0, fmt.Errorf("recover expired tasks: %w", MapError(err))
	}
	return command.RowsAffected(), nil
}

// UpsertCheckpoint creates or updates the scheduling checkpoint for one subscription.
func (repository *IngestionRepository) UpsertCheckpoint(ctx context.Context, checkpoint domain.IngestionCheckpoint) error {
	if checkpoint.SubscriptionID.IsZero() || checkpoint.UpdatedAt.IsZero() {
		return fmt.Errorf("upsert checkpoint: %w", domain.ErrInvalidData)
	}
	_, err := repository.database.Exec(ctx, `INSERT INTO market_data.ingestion_checkpoints (subscription_id, last_success_open_time, last_closed_open_time, last_attempt_at, last_success_at, consecutive_failures, updated_at) VALUES ($1, $2, $3, $4, $5, $6, $7) ON CONFLICT (subscription_id) DO UPDATE SET last_success_open_time = EXCLUDED.last_success_open_time, last_closed_open_time = EXCLUDED.last_closed_open_time, last_attempt_at = EXCLUDED.last_attempt_at, last_success_at = EXCLUDED.last_success_at, consecutive_failures = EXCLUDED.consecutive_failures, updated_at = EXCLUDED.updated_at`, IDToDatabase(checkpoint.SubscriptionID), optionalTimeToDatabase(checkpoint.LastSuccessOpenTime), optionalTimeToDatabase(checkpoint.LastClosedOpenTime), optionalTimeToDatabase(checkpoint.LastAttemptAt), optionalTimeToDatabase(checkpoint.LastSuccessAt), checkpoint.ConsecutiveFailures, TimeToDatabase(checkpoint.UpdatedAt))
	if err != nil {
		return fmt.Errorf("upsert checkpoint: %w", MapError(err))
	}
	return nil
}

const insertTaskSQL = `INSERT INTO market_data.ingestion_tasks (id, run_id, subscription_id, retry_of_task_id, range_start, range_end, status, attempt_count, max_attempts, next_attempt_at, locked_by, locked_until, started_at, finished_at, provider_request_id, error_code, error_message, error_details, canceled_by, cancel_reason, created_at, updated_at) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17, $18::jsonb, $19, $20, $21, $22)`
const taskColumnList = `id, run_id, subscription_id, retry_of_task_id, range_start, range_end, status, attempt_count, max_attempts, next_attempt_at, locked_by, locked_until, started_at, finished_at, provider_request_id, error_code, error_message, error_details, canceled_by, cancel_reason, created_at, updated_at`
const taskColumnListQualified = `tasks.id, tasks.run_id, tasks.subscription_id, tasks.retry_of_task_id, tasks.range_start, tasks.range_end, tasks.status, tasks.attempt_count, tasks.max_attempts, tasks.next_attempt_at, tasks.locked_by, tasks.locked_until, tasks.started_at, tasks.finished_at, tasks.provider_request_id, tasks.error_code, tasks.error_message, tasks.error_details, tasks.canceled_by, tasks.cancel_reason, tasks.created_at, tasks.updated_at`

func runArguments(run domain.IngestionRun) []any {
	return []any{IDToDatabase(run.ID), run.RunKey, run.RunType, run.TriggerType, run.Status, optionalTimeToDatabase(run.ScheduledAt), optionalTimeToDatabase(run.StartedAt), optionalTimeToDatabase(run.FinishedAt), optionalString(run.RequestedBy), jsonValue(run.Context), jsonValue(run.ErrorSummary), TimeToDatabase(run.CreatedAt)}
}
func taskArguments(task domain.IngestionTask) []any {
	return []any{IDToDatabase(task.ID), IDToDatabase(task.RunID), IDToDatabase(task.SubscriptionID), optionalIDToDatabase(task.RetryOfTaskID), TimeToDatabase(task.RangeStart), TimeToDatabase(task.RangeEnd), task.Status, task.AttemptCount, task.MaxAttempts, optionalTimeToDatabase(task.NextAttemptAt), optionalString(task.LockedBy), optionalTimeToDatabase(task.LockedUntil), optionalTimeToDatabase(task.StartedAt), optionalTimeToDatabase(task.FinishedAt), optionalString(task.ProviderRequestID), optionalString(task.ErrorCode), optionalString(task.ErrorMessage), jsonValue(task.ErrorDetails), optionalString(task.CanceledBy), optionalString(task.CancelReason), TimeToDatabase(task.CreatedAt), TimeToDatabase(task.UpdatedAt)}
}

func scanTask(row scanner) (domain.IngestionTask, error) {
	var task domain.IngestionTask
	var id, runID, subscriptionID uuid.UUID
	var retryOf *uuid.UUID
	var errorDetails []byte
	if err := row.Scan(&id, &runID, &subscriptionID, &retryOf, &task.RangeStart, &task.RangeEnd, &task.Status, &task.AttemptCount, &task.MaxAttempts, &task.NextAttemptAt, &task.LockedBy, &task.LockedUntil, &task.StartedAt, &task.FinishedAt, &task.ProviderRequestID, &task.ErrorCode, &task.ErrorMessage, &errorDetails, &task.CanceledBy, &task.CancelReason, &task.CreatedAt, &task.UpdatedAt); err != nil {
		return domain.IngestionTask{}, err
	}
	task.ID, task.RunID, task.SubscriptionID, task.RetryOfTaskID = IDFromDatabase(id), IDFromDatabase(runID), IDFromDatabase(subscriptionID), optionalIDFromDatabase(retryOf)
	task.RangeStart, task.RangeEnd, task.CreatedAt, task.UpdatedAt, task.ErrorDetails = TimeToDatabase(task.RangeStart), TimeToDatabase(task.RangeEnd), TimeToDatabase(task.CreatedAt), TimeToDatabase(task.UpdatedAt), copyJSON(errorDetails)
	task.NextAttemptAt, task.LockedUntil, task.StartedAt, task.FinishedAt = optionalTimeFromDatabase(task.NextAttemptAt), optionalTimeFromDatabase(task.LockedUntil), optionalTimeFromDatabase(task.StartedAt), optionalTimeFromDatabase(task.FinishedAt)
	return task, nil
}

func optionalIDToDatabase(value *domain.ID) any {
	if value == nil {
		return nil
	}
	return IDToDatabase(*value)
}
func optionalString(value *string) any {
	if value == nil {
		return nil
	}
	return *value
}

var _ domain.IngestionRepository = (*IngestionRepository)(nil)
