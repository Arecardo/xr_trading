package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"xr-trading/market-info-service/internal/domain"
	"xr-trading/market-info-service/internal/ingestion"
)

// CreateManualRetry locks and verifies the failed source Task, checks its
// current collection source, then creates exactly one successor Run/Task.
func (repository *IngestionRepository) CreateManualRetry(ctx context.Context, creation ingestion.ManualRetryCreation) error {
	if err := validateManualRetryCreation(creation); err != nil {
		return fmt.Errorf("create manual retry: %w", err)
	}
	tx, err := repository.database.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin manual retry: %w", MapError(err))
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var status string
	var subscriptionID uuid.UUID
	var rangeStart, rangeEnd time.Time
	var sourceAvailable bool
	err = tx.QueryRow(ctx, `SELECT tasks.status, tasks.subscription_id, tasks.range_start, tasks.range_end,
       subscriptions.enabled
       AND mappings.enabled
       AND (mappings.valid_from IS NULL OR mappings.valid_from <= $2)
       AND (mappings.valid_to IS NULL OR mappings.valid_to > $2)
       AND mappings.capabilities @> '{"historical":true}'::jsonb
       AND mappings.capabilities->'intervals' ? subscriptions.interval
       AND providers.status IN ('active', 'degraded')
       AND instruments.status = 'active'
       AND (instruments.valid_from IS NULL OR instruments.valid_from <= $2)
       AND (instruments.valid_to IS NULL OR instruments.valid_to > $2)
       AND assets.status = 'active' AS source_available
FROM market_data.ingestion_tasks AS tasks
JOIN market_data.collection_subscriptions AS subscriptions ON subscriptions.id = tasks.subscription_id
JOIN market_data.provider_instruments AS mappings ON mappings.id = subscriptions.provider_instrument_id
JOIN market_data.providers AS providers ON providers.id = mappings.provider_id
JOIN core.instruments AS instruments ON instruments.id = mappings.instrument_id
JOIN core.assets AS assets ON assets.id = instruments.asset_id
WHERE tasks.id = $1
FOR UPDATE OF tasks`, IDToDatabase(creation.OriginalTaskID), TimeToDatabase(creation.CreatedAt)).Scan(
		&status, &subscriptionID, &rangeStart, &rangeEnd, &sourceAvailable,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return fmt.Errorf("lock manual retry source task: %w", domain.ErrNotFound)
	}
	if err != nil {
		return fmt.Errorf("lock manual retry source task: %w", MapError(err))
	}
	if status != "failed" {
		return fmt.Errorf("manual retry source task status %s: %w", status, domain.ErrConflict)
	}
	if !sourceAvailable {
		return fmt.Errorf("manual retry collection source: %w", ingestion.ErrManualRetrySourceUnavailable)
	}

	var active bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS (
    SELECT 1 FROM market_data.ingestion_tasks
    WHERE retry_of_task_id = $1 AND status IN ('pending', 'running', 'retry_wait')
)`, IDToDatabase(creation.OriginalTaskID)).Scan(&active); err != nil {
		return fmt.Errorf("check active manual retry: %w", MapError(err))
	}
	if active {
		return fmt.Errorf("check active manual retry: %w", ingestion.ErrManualRetryAlreadyRunning)
	}

	requestedBy := creation.Audit.RequestedBy
	run := domain.IngestionRun{
		ID: creation.RunID, RunKey: "repair.manual." + creation.RunID.String(),
		RunType: "repair", TriggerType: "manual", Status: "pending", RequestedBy: &requestedBy,
		TaskCount: 1, Context: creation.RunContext, ErrorSummary: json.RawMessage(`{}`), CreatedAt: creation.CreatedAt,
	}
	originalTaskID := creation.OriginalTaskID
	task := domain.IngestionTask{
		ID: creation.TaskID, RunID: creation.RunID, SubscriptionID: IDFromDatabase(subscriptionID),
		RetryOfTaskID: &originalTaskID, RangeStart: rangeStart.UTC(), RangeEnd: rangeEnd.UTC(),
		Status: "pending", MaxAttempts: creation.MaximumAttempts, ErrorDetails: json.RawMessage(`{}`),
		CreatedAt: creation.CreatedAt, UpdatedAt: creation.CreatedAt,
	}
	if _, err := tx.Exec(ctx, `INSERT INTO market_data.ingestion_runs (id, run_key, run_type, trigger_type, status, scheduled_at, started_at, finished_at, requested_by, task_count, success_count, failed_count, context, error_summary, created_at) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, 1, 0, 0, $10::jsonb, $11::jsonb, $12)`, runArguments(run)...); err != nil {
		return fmt.Errorf("insert manual retry run: %w", MapError(err))
	}
	if _, err := tx.Exec(ctx, insertTaskSQL, taskArguments(task)...); err != nil {
		if manualRetryUniqueViolation(err) {
			return fmt.Errorf("insert manual retry task: %w", ingestion.ErrManualRetryAlreadyRunning)
		}
		return fmt.Errorf("insert manual retry task: %w", MapError(err))
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit manual retry: %w", MapError(err))
	}
	return nil
}

// CancelTaskWithAudit serializes cancellation with Worker final commits and
// appends the authenticated operation to the parent Run context atomically.
func (repository *IngestionRepository) CancelTaskWithAudit(ctx context.Context, taskID domain.ID, audit ingestion.TaskOperationAudit, now time.Time) (domain.ID, error) {
	if taskID.IsZero() || now.IsZero() || !validTaskOperationAudit(audit) {
		return domain.ID{}, fmt.Errorf("cancel task with audit: %w", domain.ErrInvalidData)
	}
	tx, err := repository.database.Begin(ctx)
	if err != nil {
		return domain.ID{}, fmt.Errorf("begin audited task cancellation: %w", MapError(err))
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var status string
	var runUUID uuid.UUID
	err = tx.QueryRow(ctx, `SELECT status, run_id FROM market_data.ingestion_tasks WHERE id = $1 FOR UPDATE`, IDToDatabase(taskID)).Scan(&status, &runUUID)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.ID{}, fmt.Errorf("cancel task with audit: %w", domain.ErrNotFound)
	}
	if err != nil {
		return domain.ID{}, fmt.Errorf("lock task for audited cancellation: %w", MapError(err))
	}
	if status != "pending" && status != "retry_wait" && status != "running" {
		return domain.ID{}, fmt.Errorf("cancel task in status %s: %w", status, domain.ErrConflict)
	}
	command, err := tx.Exec(ctx, `UPDATE market_data.ingestion_tasks
SET status = 'canceled', canceled_by = $1, cancel_reason = $2,
    next_attempt_at = NULL, locked_by = NULL, locked_until = NULL,
    finished_at = $3, updated_at = $3
WHERE id = $4 AND status = $5`, audit.RequestedBy, audit.Reason, TimeToDatabase(now), IDToDatabase(taskID), status)
	if err != nil {
		return domain.ID{}, fmt.Errorf("cancel task with audit: %w", MapError(err))
	}
	if command.RowsAffected() != 1 {
		return domain.ID{}, fmt.Errorf("cancel task with audit: %w", domain.ErrConflict)
	}
	auditJSON, err := json.Marshal([]map[string]string{{
		"action": "cancel", "task_id": taskID.String(), "requested_by": audit.RequestedBy,
		"actor_type": audit.ActorType, "request_id": audit.RequestID, "reason": audit.Reason,
		"occurred_at": now.UTC().Format(time.RFC3339Nano),
	}})
	if err != nil {
		return domain.ID{}, fmt.Errorf("encode task cancellation audit: %w", err)
	}
	runID := IDFromDatabase(runUUID)
	command, err = tx.Exec(ctx, `UPDATE market_data.ingestion_runs
SET context = jsonb_set(
    context, '{operations}',
    COALESCE(context->'operations', '[]'::jsonb) || $1::jsonb,
    true
)
WHERE id = $2`, auditJSON, IDToDatabase(runID))
	if err != nil {
		return domain.ID{}, fmt.Errorf("append task cancellation audit: %w", MapError(err))
	}
	if command.RowsAffected() != 1 {
		return domain.ID{}, fmt.Errorf("append task cancellation audit: %w", domain.ErrReferenceViolation)
	}
	if err := tx.Commit(ctx); err != nil {
		return domain.ID{}, fmt.Errorf("commit audited task cancellation: %w", MapError(err))
	}
	return runID, nil
}

func validateManualRetryCreation(creation ingestion.ManualRetryCreation) error {
	if creation.OriginalTaskID.IsZero() || creation.RunID.IsZero() || creation.TaskID.IsZero() ||
		creation.OriginalTaskID == creation.RunID || creation.OriginalTaskID == creation.TaskID || creation.RunID == creation.TaskID ||
		creation.MaximumAttempts < 1 || creation.CreatedAt.IsZero() || !validTaskOperationAudit(creation.Audit) ||
		!validJSONObject(creation.RunContext) {
		return domain.ErrInvalidData
	}
	return nil
}

func validTaskOperationAudit(audit ingestion.TaskOperationAudit) bool {
	return validRepositoryAuditText(audit.RequestedBy, 128) && validRepositoryAuditText(audit.RequestID, 128) &&
		validRepositoryAuditText(audit.Reason, 512) && (audit.ActorType == "user" || audit.ActorType == "service")
}

func validRepositoryAuditText(value string, maximum int) bool {
	if value == "" || value != strings.TrimSpace(value) || utf8.RuneCountInString(value) > maximum {
		return false
	}
	for _, character := range value {
		if unicode.IsControl(character) {
			return false
		}
	}
	return true
}

func manualRetryUniqueViolation(err error) bool {
	var databaseError *pgconn.PgError
	return errors.As(err, &databaseError) && databaseError.Code == "23505" && databaseError.ConstraintName == "uq_active_manual_retry"
}

var _ ingestion.ManualTaskStore = (*IngestionRepository)(nil)
