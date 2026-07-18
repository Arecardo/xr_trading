package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"xr-trading/market-info-service/internal/domain"
	"xr-trading/market-info-service/internal/ingestion"
	"xr-trading/market-info-service/internal/scheduler"
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

// ListSchedulingTargets scans currently valid enabled subscriptions in UUIDv7
// order. Full execution contexts are loaded after closing the ID query so a
// small pgx pool cannot deadlock on nested reads.
func (repository *IngestionRepository) ListSchedulingTargets(ctx context.Context, afterID *domain.ID, limit int, effectiveAt time.Time) (scheduler.SchedulingTargetPage, error) {
	if limit < 1 || limit > 100 || effectiveAt.IsZero() || (afterID != nil && afterID.IsZero()) {
		return scheduler.SchedulingTargetPage{}, fmt.Errorf("list scheduling targets: %w", domain.ErrInvalidData)
	}
	rows, err := repository.database.Query(ctx, `SELECT subscriptions.id
FROM market_data.collection_subscriptions AS subscriptions
JOIN market_data.provider_instruments AS mappings ON mappings.id = subscriptions.provider_instrument_id
JOIN market_data.providers AS providers ON providers.id = mappings.provider_id
JOIN core.instruments AS instruments ON instruments.id = mappings.instrument_id
JOIN core.assets AS assets ON assets.id = instruments.asset_id
WHERE subscriptions.enabled = true
  AND ($1::uuid IS NULL OR subscriptions.id > $1)
  AND mappings.enabled = true
  AND (mappings.valid_from IS NULL OR mappings.valid_from <= $2)
  AND (mappings.valid_to IS NULL OR mappings.valid_to > $2)
  AND providers.status IN ('active', 'degraded')
  AND instruments.status = 'active'
  AND (instruments.valid_from IS NULL OR instruments.valid_from <= $2)
  AND (instruments.valid_to IS NULL OR instruments.valid_to > $2)
  AND assets.status = 'active'
ORDER BY subscriptions.id ASC
LIMIT $3`, optionalIDToDatabase(afterID), TimeToDatabase(effectiveAt), limit+1)
	if err != nil {
		return scheduler.SchedulingTargetPage{}, fmt.Errorf("query scheduling targets: %w", MapError(err))
	}
	ids := make([]domain.ID, 0, limit+1)
	for rows.Next() {
		var id uuid.UUID
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return scheduler.SchedulingTargetPage{}, fmt.Errorf("scan scheduling target ID: %w", MapError(err))
		}
		ids = append(ids, IDFromDatabase(id))
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return scheduler.SchedulingTargetPage{}, fmt.Errorf("iterate scheduling target IDs: %w", MapError(err))
	}
	rows.Close()

	page := scheduler.SchedulingTargetPage{}
	if len(ids) > limit {
		next := ids[limit-1]
		page.NextAfterID = &next
		ids = ids[:limit]
	}
	page.Items = make([]scheduler.SchedulingTarget, 0, len(ids))
	for _, id := range ids {
		execution, err := repository.LoadExecutionContext(ctx, id)
		if err != nil {
			return scheduler.SchedulingTargetPage{}, fmt.Errorf("load scheduling target %s: %w", id, err)
		}
		target := scheduler.SchedulingTarget{
			Subscription: execution.Subscription, ProviderCode: execution.Provider.Code,
			ProviderMarket: execution.ProviderInstrument.ProviderMarket, InstrumentCode: execution.Instrument.Code,
			AssetType: execution.Asset.AssetType, InstrumentType: execution.Instrument.InstrumentType,
			Capabilities: execution.ProviderInstrument.Capabilities,
		}
		if err := target.Validate(); err != nil {
			return scheduler.SchedulingTargetPage{}, fmt.Errorf("validate scheduling target %s: %w", id, err)
		}
		page.Items = append(page.Items, target)
	}
	return page, nil
}

// LoadSchedulingCheckpoint returns the optional continuation hint for one
// subscription. Scheduler must still verify completeness against market_bars.
func (repository *IngestionRepository) LoadSchedulingCheckpoint(ctx context.Context, subscriptionID domain.ID) (*domain.IngestionCheckpoint, error) {
	if subscriptionID.IsZero() {
		return nil, fmt.Errorf("load scheduling checkpoint: %w", domain.ErrInvalidData)
	}
	checkpoint := domain.IngestionCheckpoint{SubscriptionID: subscriptionID}
	err := repository.database.QueryRow(ctx, `SELECT last_success_open_time, last_closed_open_time,
       last_attempt_at, last_success_at, consecutive_failures, updated_at
FROM market_data.ingestion_checkpoints WHERE subscription_id = $1`, IDToDatabase(subscriptionID)).Scan(
		&checkpoint.LastSuccessOpenTime, &checkpoint.LastClosedOpenTime, &checkpoint.LastAttemptAt,
		&checkpoint.LastSuccessAt, &checkpoint.ConsecutiveFailures, &checkpoint.UpdatedAt,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("load scheduling checkpoint: %w", MapError(err))
	}
	return &checkpoint, nil
}

// ListClosedBarOpenTimes reads the current closed bar facts for one scheduling
// target and half-open task range.
func (repository *IngestionRepository) ListClosedBarOpenTimes(ctx context.Context, target scheduler.SchedulingTarget, rangeStart, rangeEnd time.Time) ([]time.Time, error) {
	if err := target.Validate(); err != nil || rangeStart.IsZero() || rangeEnd.IsZero() || !rangeEnd.After(rangeStart) {
		return nil, fmt.Errorf("list closed bar open times: %w", domain.ErrInvalidData)
	}
	rows, err := repository.database.Query(ctx, `SELECT open_time
FROM market_data.market_bars
WHERE provider_instrument_id = $1 AND interval = $2
  AND open_time >= $3 AND open_time < $4
  AND is_current = true AND is_closed = true
  AND quality_status IN ('valid', 'warning')
ORDER BY open_time ASC`, IDToDatabase(target.Subscription.ProviderInstrumentID), target.Subscription.Interval,
		TimeToDatabase(rangeStart), TimeToDatabase(rangeEnd))
	if err != nil {
		return nil, fmt.Errorf("list closed bar open times: %w", MapError(err))
	}
	defer rows.Close()
	values := make([]time.Time, 0)
	for rows.Next() {
		var value time.Time
		if err := rows.Scan(&value); err != nil {
			return nil, fmt.Errorf("scan closed bar open time: %w", MapError(err))
		}
		values = append(values, value.UTC())
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate closed bar open times: %w", MapError(err))
	}
	return values, nil
}

// CreateScheduledBatch inserts one scheduler Run and Task atomically. A
// byte-for-byte equivalent logical batch returns created=false; a reused key
// describing different work remains a conflict.
func (repository *IngestionRepository) CreateScheduledBatch(ctx context.Context, batch scheduler.ScheduledBatch) (bool, error) {
	if err := batch.Validate(); err != nil {
		return false, fmt.Errorf("create scheduled batch: %w", err)
	}
	tx, err := repository.database.Begin(ctx)
	if err != nil {
		return false, fmt.Errorf("begin scheduled batch: %w", MapError(err))
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var enabled bool
	if err := tx.QueryRow(ctx, `SELECT enabled FROM market_data.collection_subscriptions WHERE id = $1 FOR SHARE`, IDToDatabase(batch.Task.SubscriptionID)).Scan(&enabled); err != nil {
		return false, fmt.Errorf("revalidate scheduled subscription: %w", MapError(err))
	}
	if !enabled {
		return false, fmt.Errorf("revalidate scheduled subscription: %w", domain.ErrInvalidState)
	}
	command, err := tx.Exec(ctx, `INSERT INTO market_data.ingestion_runs (id, run_key, run_type, trigger_type, status, scheduled_at, started_at, finished_at, requested_by, task_count, success_count, failed_count, context, error_summary, created_at) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, 1, 0, 0, $10::jsonb, $11::jsonb, $12) ON CONFLICT (run_key) DO NOTHING`, runArguments(batch.Run)...)
	if err != nil {
		return false, fmt.Errorf("insert scheduled run: %w", MapError(err))
	}
	if command.RowsAffected() == 0 {
		equivalent, err := equivalentScheduledBatch(ctx, tx, batch)
		if err != nil {
			return false, err
		}
		if !equivalent {
			return false, fmt.Errorf("scheduled run key describes different work: %w", domain.ErrConflict)
		}
		return false, nil
	}
	if _, err := tx.Exec(ctx, insertTaskSQL, taskArguments(batch.Task)...); err != nil {
		return false, fmt.Errorf("insert scheduled task: %w", MapError(err))
	}
	if err := tx.Commit(ctx); err != nil {
		return false, fmt.Errorf("commit scheduled batch: %w", MapError(err))
	}
	return true, nil
}

func equivalentScheduledBatch(ctx context.Context, tx marketDataTransaction, batch scheduler.ScheduledBatch) (bool, error) {
	var runType, triggerType string
	var scheduledAt time.Time
	var subscriptionID uuid.UUID
	var rangeStart, rangeEnd time.Time
	var taskCount int
	err := tx.QueryRow(ctx, `SELECT runs.run_type, runs.trigger_type, runs.scheduled_at,
       tasks.subscription_id, tasks.range_start, tasks.range_end,
       count(*) OVER ()::integer
FROM market_data.ingestion_runs AS runs
JOIN market_data.ingestion_tasks AS tasks ON tasks.run_id = runs.id
WHERE runs.run_key = $1`, batch.Run.RunKey).Scan(
		&runType, &triggerType, &scheduledAt, &subscriptionID, &rangeStart, &rangeEnd, &taskCount,
	)
	if err != nil {
		return false, fmt.Errorf("load existing scheduled batch: %w", MapError(err))
	}
	return taskCount == 1 && runType == batch.Run.RunType && triggerType == batch.Run.TriggerType &&
		scheduledAt.Equal(TimeToDatabase(*batch.Run.ScheduledAt)) && IDFromDatabase(subscriptionID) == batch.Task.SubscriptionID &&
		rangeStart.Equal(TimeToDatabase(batch.Task.RangeStart)) && rangeEnd.Equal(TimeToDatabase(batch.Task.RangeEnd)), nil
}

// ResolveBackfillTarget selects the same effective default mapping order used
// by public bar queries, then loads its complete executable subscription
// context. Disabled or otherwise invalid targets are rejected by BackfillService.
func (repository *IngestionRepository) ResolveBackfillTarget(ctx context.Context, providerCode, instrumentCode domain.Code, interval domain.BarInterval, effectiveAt time.Time) (ingestion.ExecutionContext, error) {
	if providerCode.IsZero() || instrumentCode.IsZero() || effectiveAt.IsZero() {
		return ingestion.ExecutionContext{}, fmt.Errorf("resolve backfill target: %w", domain.ErrInvalidData)
	}
	if _, err := domain.ParseBarInterval(string(interval)); err != nil {
		return ingestion.ExecutionContext{}, fmt.Errorf("resolve backfill target: %w", domain.ErrInvalidData)
	}
	var subscriptionID uuid.UUID
	err := repository.database.QueryRow(ctx, `SELECT subscriptions.id
FROM market_data.collection_subscriptions AS subscriptions
JOIN market_data.provider_instruments AS mappings
  ON mappings.id = subscriptions.provider_instrument_id
JOIN market_data.providers AS providers ON providers.id = mappings.provider_id
JOIN core.instruments AS instruments ON instruments.id = mappings.instrument_id
WHERE providers.code = $1 AND instruments.code = $2 AND subscriptions.interval = $3
	AND subscriptions.enabled = true
	AND instruments.status = 'active'
	AND (instruments.valid_from IS NULL OR instruments.valid_from <= $4)
	AND (instruments.valid_to IS NULL OR instruments.valid_to > $4)
	AND mappings.enabled = true
	AND (mappings.valid_from IS NULL OR mappings.valid_from <= $4)
	AND (mappings.valid_to IS NULL OR mappings.valid_to > $4)
	AND providers.status IN ('active', 'degraded')
ORDER BY mappings.is_default DESC, mappings.priority ASC, mappings.code ASC
LIMIT 1`, providerCode.String(), instrumentCode.String(), string(interval), TimeToDatabase(effectiveAt)).Scan(&subscriptionID)
	if err != nil {
		return ingestion.ExecutionContext{}, fmt.Errorf("resolve backfill subscription: %w", MapError(err))
	}
	execution, err := repository.LoadExecutionContext(ctx, IDFromDatabase(subscriptionID))
	if err != nil {
		return ingestion.ExecutionContext{}, err
	}
	return execution, nil
}

// CreateBackfillRunWithTask serializes one exact subscription/range with a
// transaction-scoped advisory lock, rejects an equivalent active backfill,
// then inserts exactly one Run and one Task atomically.
func (repository *IngestionRepository) CreateBackfillRunWithTask(ctx context.Context, run domain.IngestionRun, task domain.IngestionTask) error {
	if err := validateBackfillRunAndTask(run, task); err != nil {
		return fmt.Errorf("create backfill run with task: %w", err)
	}
	tx, err := repository.database.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin create backfill: %w", MapError(err))
	}
	defer func() { _ = tx.Rollback(ctx) }()
	lockKey := backfillRangeLockKey(task)
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1, 0))`, lockKey); err != nil {
		return fmt.Errorf("lock equivalent backfill range: %w", MapError(err))
	}
	var enabled bool
	if err := tx.QueryRow(ctx, `SELECT enabled FROM market_data.collection_subscriptions WHERE id = $1 FOR SHARE`, IDToDatabase(task.SubscriptionID)).Scan(&enabled); err != nil {
		return fmt.Errorf("revalidate backfill subscription: %w", MapError(err))
	}
	if !enabled {
		return fmt.Errorf("revalidate backfill subscription: %w", domain.ErrInvalidState)
	}
	var existingTaskID uuid.UUID
	err = tx.QueryRow(ctx, `SELECT tasks.id
FROM market_data.ingestion_tasks AS tasks
JOIN market_data.ingestion_runs AS runs ON runs.id = tasks.run_id
WHERE tasks.subscription_id = $1 AND tasks.range_start = $2 AND tasks.range_end = $3
  AND runs.run_type = 'backfill'
  AND tasks.status IN ('pending', 'running', 'retry_wait')
LIMIT 1`, IDToDatabase(task.SubscriptionID), TimeToDatabase(task.RangeStart), TimeToDatabase(task.RangeEnd)).Scan(&existingTaskID)
	if err == nil {
		return fmt.Errorf("active backfill task %s: %w", existingTaskID, ingestion.ErrBackfillAlreadyRunning)
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return fmt.Errorf("check equivalent active backfill: %w", MapError(err))
	}
	if _, err := tx.Exec(ctx, `INSERT INTO market_data.ingestion_runs (id, run_key, run_type, trigger_type, status, scheduled_at, started_at, finished_at, requested_by, task_count, success_count, failed_count, context, error_summary, created_at) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, 1, 0, 0, $10::jsonb, $11::jsonb, $12)`, runArguments(run)...); err != nil {
		return fmt.Errorf("insert backfill run: %w", MapError(err))
	}
	if _, err := tx.Exec(ctx, insertTaskSQL, taskArguments(task)...); err != nil {
		return fmt.Errorf("insert backfill task: %w", MapError(err))
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit create backfill: %w", MapError(err))
	}
	return nil
}

func validateBackfillRunAndTask(run domain.IngestionRun, task domain.IngestionTask) error {
	if run.ID.IsZero() || task.ID.IsZero() || run.ID.UUID().Version() != uuid.Version(7) || task.ID.UUID().Version() != uuid.Version(7) || task.ID == run.ID || task.RunID != run.ID || task.SubscriptionID.IsZero() ||
		run.RunKey != "backfill.manual."+run.ID.String() || run.RunType != "backfill" || run.TriggerType != "manual" || run.Status != "pending" ||
		run.ScheduledAt != nil || run.StartedAt != nil || run.FinishedAt != nil ||
		run.RequestedBy == nil || strings.TrimSpace(*run.RequestedBy) != *run.RequestedBy || *run.RequestedBy == "" || utf8.RuneCountInString(*run.RequestedBy) > 128 ||
		run.TaskCount != 1 || run.SuccessCount != 0 || run.FailedCount != 0 || run.CreatedAt.IsZero() ||
		task.Status != "pending" || task.AttemptCount != 0 || task.MaxAttempts < 1 || !task.RangeEnd.After(task.RangeStart) || task.RangeEnd.After(run.CreatedAt) ||
		task.RetryOfTaskID != nil || task.NextAttemptAt != nil || task.LockedBy != nil || task.LockedUntil != nil || task.StartedAt != nil || task.FinishedAt != nil ||
		task.ProviderRequestID != nil || task.ErrorCode != nil || task.ErrorMessage != nil || task.CanceledBy != nil || task.CancelReason != nil ||
		!task.CreatedAt.Equal(run.CreatedAt) || !task.UpdatedAt.Equal(run.CreatedAt) || !validJSONObject(run.Context) || !validJSONObject(run.ErrorSummary) || !validJSONObject(task.ErrorDetails) {
		return domain.ErrInvalidData
	}
	return nil
}

func validJSONObject(value json.RawMessage) bool {
	var object map[string]any
	return json.Valid(value) && json.Unmarshal(value, &object) == nil && object != nil
}

func backfillRangeLockKey(task domain.IngestionTask) string {
	return task.SubscriptionID.String() + "|" + TimeToDatabase(task.RangeStart).Format(time.RFC3339Nano) + "|" + TimeToDatabase(task.RangeEnd).Format(time.RFC3339Nano)
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

// CancelTask serializes cancellation with final Worker transactions by locking
// the same Task row. A terminal task is a state conflict, not a missing task.
func (repository *IngestionRepository) CancelTask(ctx context.Context, taskID domain.ID, canceledBy, reason string, now time.Time) error {
	if taskID.IsZero() || strings.TrimSpace(canceledBy) != canceledBy || canceledBy == "" || utf8.RuneCountInString(canceledBy) > 128 || strings.TrimSpace(reason) != reason || now.IsZero() {
		return fmt.Errorf("cancel task: %w", domain.ErrInvalidData)
	}
	tx, err := repository.database.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin cancel task: %w", MapError(err))
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var status string
	err = tx.QueryRow(ctx, `SELECT status FROM market_data.ingestion_tasks WHERE id = $1 FOR UPDATE`, IDToDatabase(taskID)).Scan(&status)
	if errors.Is(err, pgx.ErrNoRows) {
		return fmt.Errorf("cancel task: %w", domain.ErrNotFound)
	}
	if err != nil {
		return fmt.Errorf("lock task for cancellation: %w", MapError(err))
	}
	if status != "pending" && status != "retry_wait" && status != "running" {
		return fmt.Errorf("cancel task in status %s: %w", status, domain.ErrConflict)
	}
	command, err := tx.Exec(ctx, `UPDATE market_data.ingestion_tasks
SET status = 'canceled', canceled_by = $1, cancel_reason = $2,
    next_attempt_at = NULL, locked_by = NULL, locked_until = NULL,
    finished_at = $3, updated_at = $3
WHERE id = $4 AND status = $5`, canceledBy, nullableString(reason), TimeToDatabase(now), IDToDatabase(taskID), status)
	if err != nil {
		return fmt.Errorf("cancel task: %w", MapError(err))
	}
	if command.RowsAffected() != 1 {
		return fmt.Errorf("cancel task: %w", domain.ErrConflict)
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit cancel task: %w", MapError(err))
	}
	return nil
}

// RecoverExpiredTasks is the only path that turns an expired running lease
// back into durable queue state. It records the failed attempt and terminates
// tasks that have already consumed max_attempts.
func (repository *IngestionRepository) RecoverExpiredTasks(ctx context.Context, now time.Time) (int64, error) {
	if now.IsZero() {
		return 0, fmt.Errorf("recover expired tasks: %w", domain.ErrInvalidData)
	}
	var recovered, checkpointRows int64
	err := repository.database.QueryRow(ctx, `WITH expired AS (
    SELECT id, subscription_id, attempt_count >= max_attempts AS exhausted
    FROM market_data.ingestion_tasks
    WHERE status = 'running' AND locked_until < $1
    FOR UPDATE SKIP LOCKED
), updated AS (
    UPDATE market_data.ingestion_tasks AS tasks
    SET status = CASE WHEN expired.exhausted THEN 'failed'::varchar ELSE 'pending'::varchar END,
        next_attempt_at = NULL, locked_by = NULL, locked_until = NULL,
        finished_at = CASE WHEN expired.exhausted THEN $1 ELSE NULL END,
        error_code = 'lease_expired', error_message = 'worker lease expired before completion',
        error_details = '{}', updated_at = $1
    FROM expired WHERE tasks.id = expired.id
    RETURNING expired.subscription_id
), failure_counts AS (
    SELECT subscription_id, count(*)::integer AS failure_count
    FROM updated GROUP BY subscription_id
), checkpoint_updates AS (
    INSERT INTO market_data.ingestion_checkpoints (
        subscription_id, last_attempt_at, consecutive_failures, updated_at
    )
    SELECT subscription_id, $1, failure_count, $1 FROM failure_counts
    ON CONFLICT (subscription_id) DO UPDATE SET
        last_attempt_at = EXCLUDED.last_attempt_at,
        consecutive_failures = market_data.ingestion_checkpoints.consecutive_failures + EXCLUDED.consecutive_failures,
        updated_at = EXCLUDED.updated_at
    RETURNING subscription_id
)
SELECT (SELECT count(*) FROM updated), (SELECT count(*) FROM checkpoint_updates)`, TimeToDatabase(now)).Scan(&recovered, &checkpointRows)
	if err != nil {
		return 0, fmt.Errorf("recover expired tasks: %w", MapError(err))
	}
	return recovered, nil
}

// LoadRunTaskSnapshot reads the complete Task status distribution for one Run.
// The LEFT JOIN distinguishes an existing Run with no Tasks from a missing Run.
func (repository *IngestionRepository) LoadRunTaskSnapshot(ctx context.Context, runID domain.ID) (ingestion.RunTaskSnapshot, error) {
	if runID.IsZero() {
		return ingestion.RunTaskSnapshot{}, fmt.Errorf("load run task snapshot: %w", domain.ErrInvalidData)
	}
	snapshot := ingestion.RunTaskSnapshot{RunID: runID}
	err := repository.database.QueryRow(ctx, `SELECT
    count(tasks.id)::integer,
    count(tasks.id) FILTER (WHERE tasks.status = 'pending')::integer,
    count(tasks.id) FILTER (WHERE tasks.status = 'running')::integer,
    count(tasks.id) FILTER (WHERE tasks.status = 'retry_wait')::integer,
    count(tasks.id) FILTER (WHERE tasks.status = 'success')::integer,
    count(tasks.id) FILTER (WHERE tasks.status = 'failed')::integer,
    count(tasks.id) FILTER (WHERE tasks.status = 'canceled')::integer,
    min(tasks.started_at), max(tasks.finished_at)
FROM market_data.ingestion_runs AS runs
LEFT JOIN market_data.ingestion_tasks AS tasks ON tasks.run_id = runs.id
WHERE runs.id = $1
GROUP BY runs.id`, IDToDatabase(runID)).Scan(
		new(int), &snapshot.PendingCount, &snapshot.RunningCount, &snapshot.RetryWaitCount,
		&snapshot.SuccessCount, &snapshot.FailedCount, &snapshot.CanceledCount,
		&snapshot.EarliestStartedAt, &snapshot.LatestFinishedAt,
	)
	if err != nil {
		return ingestion.RunTaskSnapshot{}, fmt.Errorf("load run task snapshot: %w", MapError(err))
	}
	snapshot.EarliestStartedAt = optionalTimeFromDatabase(snapshot.EarliestStartedAt)
	snapshot.LatestFinishedAt = optionalTimeFromDatabase(snapshot.LatestFinishedAt)
	return snapshot, nil
}

// SaveRunSummary updates the Run query cache only while the per-status Task
// counts still match the service snapshot. A concurrent Task transition yields
// ErrConflict and is recomputed by RunService.
func (repository *IngestionRepository) SaveRunSummary(ctx context.Context, summary ingestion.RunSummary) error {
	if err := validateRunSummary(summary); err != nil {
		return fmt.Errorf("save run summary: %w", err)
	}
	command, err := repository.database.Exec(ctx, `UPDATE market_data.ingestion_runs AS runs
SET status = $2, task_count = $3, success_count = $4, failed_count = $5,
    started_at = COALESCE(runs.started_at, $6), finished_at = $7
WHERE runs.id = $1
  AND EXISTS (
    SELECT 1 FROM market_data.ingestion_tasks AS tasks
    WHERE tasks.run_id = runs.id
    GROUP BY tasks.run_id
    HAVING count(*)::integer = $3
       AND count(*) FILTER (WHERE tasks.status = 'pending')::integer = $8
       AND count(*) FILTER (WHERE tasks.status = 'running')::integer = $9
       AND count(*) FILTER (WHERE tasks.status = 'retry_wait')::integer = $10
       AND count(*) FILTER (WHERE tasks.status = 'success')::integer = $4
       AND count(*) FILTER (WHERE tasks.status = 'failed')::integer = $5
       AND count(*) FILTER (WHERE tasks.status = 'canceled')::integer = $11
  )`, IDToDatabase(summary.RunID), summary.Status, summary.TaskCount, summary.SuccessCount,
		summary.FailedCount, optionalTimeToDatabase(summary.EarliestStartedAt), optionalTimeToDatabase(summary.LatestFinishedAt),
		summary.PendingCount, summary.RunningCount, summary.RetryWaitCount, summary.CanceledCount)
	if err != nil {
		return fmt.Errorf("save run summary: %w", MapError(err))
	}
	if command.RowsAffected() != 1 {
		return fmt.Errorf("save run summary from stale task snapshot: %w", domain.ErrConflict)
	}
	return nil
}

func validateRunSummary(summary ingestion.RunSummary) error {
	snapshot := ingestion.RunTaskSnapshot{
		RunID: summary.RunID, PendingCount: summary.PendingCount, RunningCount: summary.RunningCount,
		RetryWaitCount: summary.RetryWaitCount, SuccessCount: summary.SuccessCount,
		FailedCount: summary.FailedCount, CanceledCount: summary.CanceledCount,
		EarliestStartedAt: summary.EarliestStartedAt, LatestFinishedAt: summary.LatestFinishedAt,
	}
	expected, err := ingestion.SummarizeRun(snapshot)
	if err != nil || summary.TaskCount != expected.TaskCount || summary.Status != expected.Status ||
		(summary.LatestFinishedAt == nil) != (expected.LatestFinishedAt == nil) {
		return domain.ErrInvalidData
	}
	return nil
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

// LoadExecutionContext resolves the durable subscription and its complete
// Asset -> Instrument -> ProviderInstrument -> Provider request context. These
// reads intentionally happen before any provider call and without a write
// transaction.
func (repository *IngestionRepository) LoadExecutionContext(ctx context.Context, subscriptionID domain.ID) (ingestion.ExecutionContext, error) {
	if subscriptionID.IsZero() {
		return ingestion.ExecutionContext{}, fmt.Errorf("load ingestion context: %w", domain.ErrInvalidData)
	}
	subscription, err := scanSubscription(repository.database.QueryRow(ctx, "SELECT "+joinColumns(subscriptionColumns)+" FROM market_data.collection_subscriptions WHERE id = $1", IDToDatabase(subscriptionID)))
	if err != nil {
		return ingestion.ExecutionContext{}, fmt.Errorf("load ingestion subscription: %w", MapError(err))
	}
	mapping, err := scanProviderInstrument(repository.database.QueryRow(ctx, "SELECT "+joinColumns(providerInstrumentColumns)+" FROM market_data.provider_instruments WHERE id = $1", IDToDatabase(subscription.ProviderInstrumentID)))
	if err != nil {
		return ingestion.ExecutionContext{}, fmt.Errorf("load ingestion provider instrument: %w", MapError(err))
	}
	provider, err := scanProvider(repository.database.QueryRow(ctx, "SELECT "+joinColumns(providerColumns)+" FROM market_data.providers WHERE id = $1", IDToDatabase(mapping.ProviderID)))
	if err != nil {
		return ingestion.ExecutionContext{}, fmt.Errorf("load ingestion provider: %w", MapError(err))
	}
	instrument, err := scanInstrument(repository.database.QueryRow(ctx, "SELECT "+joinColumns(instrumentColumns)+" FROM core.instruments WHERE id = $1", IDToDatabase(mapping.InstrumentID)))
	if err != nil {
		return ingestion.ExecutionContext{}, fmt.Errorf("load ingestion instrument: %w", MapError(err))
	}
	asset, err := scanAsset(repository.database.QueryRow(ctx, "SELECT "+joinColumns(assetColumns)+" FROM core.assets WHERE id = $1", IDToDatabase(instrument.AssetID)))
	if err != nil {
		return ingestion.ExecutionContext{}, fmt.Errorf("load ingestion asset: %w", MapError(err))
	}
	execution := ingestion.ExecutionContext{
		Subscription: subscription, Asset: asset, Instrument: instrument,
		Provider: provider, ProviderInstrument: mapping,
	}
	if err := execution.Validate(); err != nil {
		return ingestion.ExecutionContext{}, fmt.Errorf("validate ingestion context: %w", err)
	}
	return execution, nil
}

// CommitSuccess atomically fences the claim, writes all bar revisions and
// quality issues, advances the checkpoint monotonically, and completes the
// Task. No provider call is made while this transaction is open.
func (repository *IngestionRepository) CommitSuccess(ctx context.Context, request ingestion.SuccessCommit) error {
	if err := validateSuccessCommit(request); err != nil {
		return fmt.Errorf("commit ingestion success: %w", err)
	}
	tx, err := repository.database.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin ingestion success: %w", MapError(err))
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if err := lockAndValidateTaskClaim(ctx, tx, request); err != nil {
		return err
	}
	for index, bar := range request.Bars {
		if _, err := writeMarketBarInTransaction(ctx, tx, bar); err != nil {
			return fmt.Errorf("write ingestion bar %d: %w", index, err)
		}
	}
	for index, issue := range request.Issues {
		if _, err := openQualityIssue(ctx, tx, issue); err != nil {
			return fmt.Errorf("write ingestion quality issue %d: %w", index, err)
		}
	}
	if err := upsertSuccessfulCheckpoint(ctx, tx, request.Checkpoint); err != nil {
		return err
	}
	task := request.Claim.Task
	command, err := tx.Exec(ctx, `UPDATE market_data.ingestion_tasks
SET status = 'success', next_attempt_at = NULL, locked_by = NULL, locked_until = NULL,
    finished_at = $1, error_code = NULL, error_message = NULL, error_details = '{}', updated_at = $1
WHERE id = $2 AND status = 'running' AND attempt_count = $3
  AND locked_by = $4 AND locked_until = $5 AND locked_until > $1`,
		TimeToDatabase(request.FinishedAt), IDToDatabase(task.ID), task.AttemptCount, *task.LockedBy, TimeToDatabase(*task.LockedUntil))
	if err != nil {
		return fmt.Errorf("complete ingestion task: %w", MapError(err))
	}
	if command.RowsAffected() != 1 {
		return fmt.Errorf("complete ingestion task: %w", ingestion.ErrTaskLeaseLost)
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit ingestion success: %w", MapError(err))
	}
	return nil
}

// CommitFailure atomically fences the execution attempt, records its safe
// error, updates failure checkpoint metadata, and either schedules the same
// Task for retry or moves it to terminal failed.
func (repository *IngestionRepository) CommitFailure(ctx context.Context, request ingestion.FailureCommit) error {
	if err := validateFailureCommit(request); err != nil {
		return fmt.Errorf("commit ingestion failure: %w", err)
	}
	tx, err := repository.database.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin ingestion failure: %w", MapError(err))
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if err := lockAndValidateFailureClaim(ctx, tx, request); err != nil {
		return err
	}
	if err := upsertFailedCheckpoint(ctx, tx, request.Claim.Task.SubscriptionID, request.FinishedAt); err != nil {
		return err
	}
	task := request.Claim.Task
	command, err := tx.Exec(ctx, `UPDATE market_data.ingestion_tasks
SET status = $1::varchar, next_attempt_at = $2, locked_by = NULL, locked_until = NULL,
    finished_at = CASE WHEN $1::varchar = 'failed' THEN $3 ELSE NULL END,
    error_code = $4, error_message = $5, error_details = $6::jsonb, updated_at = $3
WHERE id = $7 AND status = 'running' AND attempt_count = $8
  AND locked_by = $9 AND locked_until = $10 AND locked_until > $3`,
		request.Status, optionalTimeToDatabase(request.NextAttemptAt), TimeToDatabase(request.FinishedAt),
		request.ErrorCode, request.ErrorMessage, request.ErrorDetails, IDToDatabase(task.ID),
		task.AttemptCount, *task.LockedBy, TimeToDatabase(*task.LockedUntil))
	if err != nil {
		return fmt.Errorf("transition ingestion task failure: %w", MapError(err))
	}
	if command.RowsAffected() != 1 {
		return fmt.Errorf("transition ingestion task failure: %w", ingestion.ErrTaskLeaseLost)
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit ingestion failure: %w", MapError(err))
	}
	return nil
}

func validateFailureCommit(request ingestion.FailureCommit) error {
	task := request.Claim.Task
	if task.ID.IsZero() || task.SubscriptionID.IsZero() || task.Status != "running" || task.AttemptCount <= 0 || task.MaxAttempts <= 0 || task.LockedBy == nil || *task.LockedBy == "" || task.LockedUntil == nil || request.FinishedAt.IsZero() || !task.LockedUntil.After(request.FinishedAt) {
		return domain.ErrInvalidData
	}
	if request.Status != "retry_wait" && request.Status != "failed" {
		return domain.ErrInvalidData
	}
	if (request.Status == "retry_wait") != (request.NextAttemptAt != nil) || (request.NextAttemptAt != nil && !request.NextAttemptAt.After(request.FinishedAt)) {
		return domain.ErrInvalidData
	}
	if strings.TrimSpace(request.ErrorCode) != request.ErrorCode || request.ErrorCode == "" || utf8.RuneCountInString(request.ErrorCode) > 64 || strings.TrimSpace(request.ErrorMessage) != request.ErrorMessage || request.ErrorMessage == "" || utf8.RuneCountInString(request.ErrorMessage) > 512 {
		return domain.ErrInvalidData
	}
	var details map[string]any
	if !json.Valid(request.ErrorDetails) || json.Unmarshal(request.ErrorDetails, &details) != nil || details == nil {
		return domain.ErrInvalidData
	}
	return nil
}

func lockAndValidateFailureClaim(ctx context.Context, tx marketDataTransaction, request ingestion.FailureCommit) error {
	var status string
	var attemptCount int
	var lockedBy *string
	var lockedUntil *time.Time
	var subscriptionID uuid.UUID
	err := tx.QueryRow(ctx, `SELECT status, attempt_count, locked_by, locked_until, subscription_id
FROM market_data.ingestion_tasks WHERE id = $1 FOR UPDATE`, IDToDatabase(request.Claim.Task.ID)).Scan(
		&status, &attemptCount, &lockedBy, &lockedUntil, &subscriptionID,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return fmt.Errorf("lock ingestion failed task: %w", ingestion.ErrTaskLeaseLost)
	}
	if err != nil {
		return fmt.Errorf("lock ingestion failed task: %w", MapError(err))
	}
	claim := request.Claim.Task
	if status != "running" || attemptCount != claim.AttemptCount || IDFromDatabase(subscriptionID) != claim.SubscriptionID || lockedBy == nil || *lockedBy != *claim.LockedBy || lockedUntil == nil || !lockedUntil.Equal(TimeToDatabase(*claim.LockedUntil)) || !lockedUntil.After(request.FinishedAt) {
		return fmt.Errorf("validate ingestion failed task lease: %w", ingestion.ErrTaskLeaseLost)
	}
	return nil
}

func validateSuccessCommit(request ingestion.SuccessCommit) error {
	task := request.Claim.Task
	if task.ID.IsZero() || task.SubscriptionID.IsZero() || task.Status != "running" || task.AttemptCount <= 0 || task.LockedBy == nil || *task.LockedBy == "" || task.LockedUntil == nil || request.FinishedAt.IsZero() || !task.LockedUntil.After(request.FinishedAt) {
		return domain.ErrInvalidData
	}
	if request.Checkpoint.SubscriptionID != task.SubscriptionID || request.Checkpoint.UpdatedAt.IsZero() || request.Checkpoint.LastAttemptAt == nil || request.Checkpoint.LastSuccessAt == nil {
		return domain.ErrInvalidData
	}
	seen := make(map[string]struct{}, len(request.Bars))
	for _, bar := range request.Bars {
		if _, err := domain.NewBar(bar); err != nil {
			return err
		}
		key := bar.InstrumentID.String() + ":" + bar.ProviderInstrumentID.String() + ":" + string(bar.Interval) + ":" + bar.OpenTime.String()
		if _, duplicate := seen[key]; duplicate {
			return domain.ErrInvalidData
		}
		seen[key] = struct{}{}
	}
	for _, issue := range request.Issues {
		if issue.ID.IsZero() || issue.InstrumentID.IsZero() || issue.RuleCode == "" || issue.Severity == "" || issue.Summary == "" || issue.DetectedAt.IsZero() || issue.CreatedAt.IsZero() || issue.UpdatedAt.IsZero() {
			return domain.ErrInvalidData
		}
	}
	return nil
}

func lockAndValidateTaskClaim(ctx context.Context, tx marketDataTransaction, request ingestion.SuccessCommit) error {
	var status string
	var attemptCount int
	var lockedBy *string
	var lockedUntil *time.Time
	var subscriptionID, providerInstrumentID, instrumentID uuid.UUID
	var interval string
	err := tx.QueryRow(ctx, `SELECT tasks.status, tasks.attempt_count, tasks.locked_by, tasks.locked_until,
    tasks.subscription_id, subscriptions.interval, subscriptions.provider_instrument_id,
    provider_instruments.instrument_id
FROM market_data.ingestion_tasks AS tasks
JOIN market_data.collection_subscriptions AS subscriptions ON subscriptions.id = tasks.subscription_id
JOIN market_data.provider_instruments AS provider_instruments ON provider_instruments.id = subscriptions.provider_instrument_id
WHERE tasks.id = $1
FOR UPDATE OF tasks`, IDToDatabase(request.Claim.Task.ID)).Scan(
		&status, &attemptCount, &lockedBy, &lockedUntil, &subscriptionID, &interval, &providerInstrumentID, &instrumentID,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return fmt.Errorf("lock ingestion task: %w", ingestion.ErrTaskLeaseLost)
	}
	if err != nil {
		return fmt.Errorf("lock ingestion task: %w", MapError(err))
	}
	claim := request.Claim.Task
	if status != "running" || attemptCount != claim.AttemptCount || IDFromDatabase(subscriptionID) != claim.SubscriptionID || lockedBy == nil || *lockedBy != *claim.LockedBy || lockedUntil == nil || !lockedUntil.Equal(TimeToDatabase(*claim.LockedUntil)) || !lockedUntil.After(request.FinishedAt) {
		return fmt.Errorf("validate ingestion task lease: %w", ingestion.ErrTaskLeaseLost)
	}
	expectedInstrumentID := IDFromDatabase(instrumentID)
	expectedProviderInstrumentID := IDFromDatabase(providerInstrumentID)
	for _, bar := range request.Bars {
		if bar.InstrumentID != expectedInstrumentID || bar.ProviderInstrumentID != expectedProviderInstrumentID || string(bar.Interval) != interval {
			return fmt.Errorf("validate ingestion bar source: %w", domain.ErrInvalidData)
		}
	}
	for _, issue := range request.Issues {
		if issue.InstrumentID != expectedInstrumentID || issue.ProviderInstrumentID == nil || *issue.ProviderInstrumentID != expectedProviderInstrumentID || (issue.Interval != nil && *issue.Interval != interval) {
			return fmt.Errorf("validate ingestion quality issue source: %w", domain.ErrInvalidData)
		}
	}
	return nil
}

func writeMarketBarInTransaction(ctx context.Context, tx marketDataTransaction, bar domain.MarketBar) (domain.MarketBarWriteResult, error) {
	bar, err := domain.NewBar(bar)
	if err != nil {
		return domain.MarketBarWriteResult{}, err
	}
	current, err := loadCurrentMarketBar(ctx, tx, bar)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return domain.MarketBarWriteResult{}, MapError(err)
	}
	if err == nil && bar.RawHash != "" && current.RawHash == bar.RawHash {
		return domain.MarketBarWriteResult{Applied: false, Revision: current.Revision}, nil
	}
	revision := 1
	if err == nil {
		revision = current.Revision + 1
		command, closeErr := tx.Exec(ctx, `UPDATE market_data.market_bars SET is_current = false
WHERE instrument_id = $1 AND provider_instrument_id = $2 AND interval = $3 AND open_time = $4 AND revision = $5 AND is_current = true`,
			IDToDatabase(bar.InstrumentID), IDToDatabase(bar.ProviderInstrumentID), string(bar.Interval), TimeToDatabase(bar.OpenTime.Time()), current.Revision)
		if closeErr != nil {
			return domain.MarketBarWriteResult{}, MapError(closeErr)
		}
		if command.RowsAffected() != 1 {
			return domain.MarketBarWriteResult{}, domain.ErrConflict
		}
	}
	bar.Revision = revision
	bar.IsCurrent = true
	if _, err := tx.Exec(ctx, insertMarketBarSQL, marketBarArguments(bar)...); err != nil {
		return domain.MarketBarWriteResult{}, MapError(err)
	}
	return domain.MarketBarWriteResult{Applied: true, Revision: revision}, nil
}

func upsertSuccessfulCheckpoint(ctx context.Context, tx marketDataTransaction, checkpoint domain.IngestionCheckpoint) error {
	_, err := tx.Exec(ctx, `INSERT INTO market_data.ingestion_checkpoints (
    subscription_id, last_success_open_time, last_closed_open_time, last_attempt_at,
    last_success_at, consecutive_failures, updated_at
) VALUES ($1, $2, $3, $4, $5, 0, $6)
ON CONFLICT (subscription_id) DO UPDATE SET
    last_success_open_time = CASE
      WHEN EXCLUDED.last_success_open_time IS NULL THEN market_data.ingestion_checkpoints.last_success_open_time
      WHEN market_data.ingestion_checkpoints.last_success_open_time IS NULL THEN EXCLUDED.last_success_open_time
      ELSE GREATEST(market_data.ingestion_checkpoints.last_success_open_time, EXCLUDED.last_success_open_time)
    END,
    last_closed_open_time = CASE
      WHEN EXCLUDED.last_closed_open_time IS NULL THEN market_data.ingestion_checkpoints.last_closed_open_time
      WHEN market_data.ingestion_checkpoints.last_closed_open_time IS NULL THEN EXCLUDED.last_closed_open_time
      ELSE GREATEST(market_data.ingestion_checkpoints.last_closed_open_time, EXCLUDED.last_closed_open_time)
    END,
    last_attempt_at = EXCLUDED.last_attempt_at, last_success_at = EXCLUDED.last_success_at,
    consecutive_failures = 0, updated_at = EXCLUDED.updated_at`,
		IDToDatabase(checkpoint.SubscriptionID), optionalTimeToDatabase(checkpoint.LastSuccessOpenTime),
		optionalTimeToDatabase(checkpoint.LastClosedOpenTime), optionalTimeToDatabase(checkpoint.LastAttemptAt),
		optionalTimeToDatabase(checkpoint.LastSuccessAt), TimeToDatabase(checkpoint.UpdatedAt))
	if err != nil {
		return fmt.Errorf("upsert successful ingestion checkpoint: %w", MapError(err))
	}
	return nil
}

func upsertFailedCheckpoint(ctx context.Context, tx marketDataTransaction, subscriptionID domain.ID, attemptedAt time.Time) error {
	_, err := tx.Exec(ctx, `INSERT INTO market_data.ingestion_checkpoints (
    subscription_id, last_attempt_at, consecutive_failures, updated_at
) VALUES ($1, $2, 1, $2)
ON CONFLICT (subscription_id) DO UPDATE SET
    last_attempt_at = EXCLUDED.last_attempt_at,
    consecutive_failures = market_data.ingestion_checkpoints.consecutive_failures + 1,
    updated_at = EXCLUDED.updated_at`, IDToDatabase(subscriptionID), TimeToDatabase(attemptedAt))
	if err != nil {
		return fmt.Errorf("upsert failed ingestion checkpoint: %w", MapError(err))
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
var _ ingestion.Store = (*IngestionRepository)(nil)
var _ ingestion.BackfillStore = (*IngestionRepository)(nil)
var _ scheduler.IncrementalStore = (*IngestionRepository)(nil)
