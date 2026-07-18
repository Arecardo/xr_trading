package postgres

import (
	"context"
	"fmt"

	"github.com/google/uuid"

	"xr-trading/market-info-service/internal/application"
	"xr-trading/market-info-service/internal/database/sqlbuilder"
	"xr-trading/market-info-service/internal/domain"
)

const runManagementCTE = `WITH run_records AS (
SELECT
    runs.id, runs.run_key, runs.run_type, runs.trigger_type, runs.status,
    runs.scheduled_at, runs.started_at, runs.finished_at, runs.requested_by,
    runs.task_count, runs.success_count, runs.failed_count, runs.context,
    runs.error_summary, runs.created_at,
    count(tasks.id) FILTER (WHERE tasks.status = 'pending')::integer AS pending_count,
    count(tasks.id) FILTER (WHERE tasks.status = 'running')::integer AS running_count,
    count(tasks.id) FILTER (WHERE tasks.status = 'retry_wait')::integer AS retry_wait_count,
    count(tasks.id) FILTER (WHERE tasks.status = 'success')::integer AS actual_success_count,
    count(tasks.id) FILTER (WHERE tasks.status = 'failed')::integer AS actual_failed_count,
    count(tasks.id) FILTER (WHERE tasks.status = 'canceled')::integer AS canceled_count,
    min(tasks.started_at) AS actual_started_at,
    max(tasks.finished_at) AS actual_finished_at,
    CASE
        WHEN count(tasks.id) FILTER (WHERE tasks.status = 'pending') = count(tasks.id) THEN 'pending'
        WHEN count(tasks.id) FILTER (WHERE tasks.status IN ('pending', 'running', 'retry_wait')) > 0 THEN 'running'
        WHEN count(tasks.id) FILTER (WHERE tasks.status = 'success') = count(tasks.id) THEN 'success'
        WHEN count(tasks.id) FILTER (WHERE tasks.status = 'failed') = count(tasks.id) THEN 'failed'
        WHEN count(tasks.id) FILTER (WHERE tasks.status = 'canceled') = count(tasks.id) THEN 'canceled'
        ELSE 'partial'
    END AS derived_status
FROM market_data.ingestion_runs AS runs
LEFT JOIN market_data.ingestion_tasks AS tasks ON tasks.run_id = runs.id
GROUP BY runs.id
) `

var runManagementColumns = []string{
	"records.id", "records.run_key", "records.run_type", "records.trigger_type", "records.status",
	"records.scheduled_at", "records.started_at", "records.finished_at", "records.requested_by",
	"records.task_count", "records.success_count", "records.failed_count", "records.context",
	"records.error_summary", "records.created_at", "records.pending_count", "records.running_count",
	"records.retry_wait_count", "records.actual_success_count", "records.actual_failed_count",
	"records.canceled_count", "records.actual_started_at", "records.actual_finished_at",
}

const taskManagementFrom = `market_data.ingestion_tasks AS tasks
JOIN market_data.ingestion_runs AS runs ON runs.id = tasks.run_id
JOIN market_data.collection_subscriptions AS subscriptions ON subscriptions.id = tasks.subscription_id
JOIN market_data.provider_instruments AS mappings ON mappings.id = subscriptions.provider_instrument_id
JOIN market_data.providers AS providers ON providers.id = mappings.provider_id
JOIN core.instruments AS instruments ON instruments.id = mappings.instrument_id`

var taskManagementColumns = []string{
	"tasks.id", "tasks.run_id", "tasks.subscription_id", "tasks.retry_of_task_id", "tasks.range_start", "tasks.range_end",
	"tasks.status", "tasks.attempt_count", "tasks.max_attempts", "tasks.next_attempt_at", "tasks.locked_by", "tasks.locked_until",
	"tasks.started_at", "tasks.finished_at", "tasks.provider_request_id", "tasks.error_code", "tasks.error_message",
	"tasks.error_details", "tasks.canceled_by", "tasks.cancel_reason", "tasks.created_at", "tasks.updated_at",
	"runs.run_type", "runs.trigger_type", "subscriptions.interval", "providers.id", "providers.code", "instruments.id",
	"instruments.code", "mappings.id", "mappings.code", "mappings.external_symbol",
}

func (repository *IngestionRepository) ListRunRecords(ctx context.Context, filter application.RunReadFilter) ([]application.RunRecord, error) {
	if filter.Limit < 1 || filter.Limit > application.MaximumIngestionManagementPageSize+1 || (filter.AfterID != nil && filter.AfterID.IsZero()) {
		return nil, fmt.Errorf("list ingestion runs: %w", domain.ErrInvalidData)
	}
	query := sqlbuilder.Select(runManagementColumns...).From("run_records AS records")
	if filter.RunType != "" {
		query.And(sqlbuilder.Eq("records.run_type", filter.RunType))
	}
	if filter.TriggerType != "" {
		query.And(sqlbuilder.Eq("records.trigger_type", filter.TriggerType))
	}
	if filter.Status != "" {
		query.And(sqlbuilder.Eq("records.derived_status", filter.Status))
	}
	if filter.RequestedBy != "" {
		query.And(sqlbuilder.Eq("records.requested_by", filter.RequestedBy))
	}
	if filter.CreatedFrom != nil {
		query.And(sqlbuilder.Gte("records.created_at", TimeToDatabase(*filter.CreatedFrom)))
	}
	if filter.CreatedTo != nil {
		query.And(sqlbuilder.Lt("records.created_at", TimeToDatabase(*filter.CreatedTo)))
	}
	if filter.AfterID != nil {
		query.And(sqlbuilder.Lt("records.id", IDToDatabase(*filter.AfterID)))
	}
	statement, args, err := query.OrderBy("records.id DESC").Limit(filter.Limit).Build()
	if err != nil {
		return nil, fmt.Errorf("build ingestion run list: %w", err)
	}
	rows, err := repository.database.Query(ctx, runManagementCTE+statement, args...)
	if err != nil {
		return nil, fmt.Errorf("list ingestion runs: %w", MapError(err))
	}
	defer rows.Close()
	records := make([]application.RunRecord, 0, filter.Limit)
	for rows.Next() {
		record, err := scanRunManagementRecord(rows)
		if err != nil {
			return nil, fmt.Errorf("scan ingestion run: %w", MapError(err))
		}
		records = append(records, record)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate ingestion runs: %w", MapError(err))
	}
	return records, nil
}

func (repository *IngestionRepository) GetRunRecord(ctx context.Context, id domain.ID) (application.RunRecord, error) {
	if id.IsZero() {
		return application.RunRecord{}, fmt.Errorf("get ingestion run: %w", domain.ErrInvalidData)
	}
	statement, args, err := sqlbuilder.Select(runManagementColumns...).From("run_records AS records").Where(sqlbuilder.Eq("records.id", IDToDatabase(id))).Build()
	if err != nil {
		return application.RunRecord{}, fmt.Errorf("build ingestion run detail: %w", err)
	}
	record, err := scanRunManagementRecord(repository.database.QueryRow(ctx, runManagementCTE+statement, args...))
	if err != nil {
		return application.RunRecord{}, fmt.Errorf("get ingestion run: %w", MapError(err))
	}
	return record, nil
}

func (repository *IngestionRepository) ListTaskRecords(ctx context.Context, filter application.TaskReadFilter) ([]application.TaskRecord, error) {
	if filter.Limit < 1 || filter.Limit > application.MaximumIngestionManagementPageSize+1 || (filter.RunID != nil && filter.RunID.IsZero()) || (filter.AfterID != nil && filter.AfterID.IsZero()) {
		return nil, fmt.Errorf("list ingestion tasks: %w", domain.ErrInvalidData)
	}
	query := sqlbuilder.Select(taskManagementColumns...).From(taskManagementFrom)
	if filter.RunID != nil {
		query.And(sqlbuilder.Eq("tasks.run_id", IDToDatabase(*filter.RunID)))
	}
	if filter.Status != "" {
		query.And(sqlbuilder.Eq("tasks.status", filter.Status))
	}
	if filter.ProviderCode != "" {
		query.And(sqlbuilder.Eq("providers.code", filter.ProviderCode))
	}
	if filter.InstrumentCode != "" {
		query.And(sqlbuilder.Eq("instruments.code", filter.InstrumentCode))
	}
	if filter.Interval != "" {
		query.And(sqlbuilder.Eq("subscriptions.interval", filter.Interval))
	}
	if filter.CreatedFrom != nil {
		query.And(sqlbuilder.Gte("tasks.created_at", TimeToDatabase(*filter.CreatedFrom)))
	}
	if filter.CreatedTo != nil {
		query.And(sqlbuilder.Lt("tasks.created_at", TimeToDatabase(*filter.CreatedTo)))
	}
	if filter.AfterID != nil {
		query.And(sqlbuilder.Lt("tasks.id", IDToDatabase(*filter.AfterID)))
	}
	statement, args, err := query.OrderBy("tasks.id DESC").Limit(filter.Limit).Build()
	if err != nil {
		return nil, fmt.Errorf("build ingestion task list: %w", err)
	}
	rows, err := repository.database.Query(ctx, statement, args...)
	if err != nil {
		return nil, fmt.Errorf("list ingestion tasks: %w", MapError(err))
	}
	defer rows.Close()
	records := make([]application.TaskRecord, 0, filter.Limit)
	for rows.Next() {
		record, err := scanTaskManagementRecord(rows)
		if err != nil {
			return nil, fmt.Errorf("scan ingestion task: %w", MapError(err))
		}
		records = append(records, record)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate ingestion tasks: %w", MapError(err))
	}
	return records, nil
}

func (repository *IngestionRepository) GetTaskRecord(ctx context.Context, id domain.ID) (application.TaskRecord, error) {
	if id.IsZero() {
		return application.TaskRecord{}, fmt.Errorf("get ingestion task: %w", domain.ErrInvalidData)
	}
	statement, args, err := sqlbuilder.Select(taskManagementColumns...).From(taskManagementFrom).Where(sqlbuilder.Eq("tasks.id", IDToDatabase(id))).Build()
	if err != nil {
		return application.TaskRecord{}, fmt.Errorf("build ingestion task detail: %w", err)
	}
	record, err := scanTaskManagementRecord(repository.database.QueryRow(ctx, statement, args...))
	if err != nil {
		return application.TaskRecord{}, fmt.Errorf("get ingestion task: %w", MapError(err))
	}
	return record, nil
}

func scanRunManagementRecord(row scanner) (application.RunRecord, error) {
	var record application.RunRecord
	var id uuid.UUID
	var runContext, errorSummary []byte
	if err := row.Scan(
		&id, &record.Run.RunKey, &record.Run.RunType, &record.Run.TriggerType, &record.Run.Status,
		&record.Run.ScheduledAt, &record.Run.StartedAt, &record.Run.FinishedAt, &record.Run.RequestedBy,
		&record.Run.TaskCount, &record.Run.SuccessCount, &record.Run.FailedCount, &runContext, &errorSummary,
		&record.Run.CreatedAt, &record.Snapshot.PendingCount, &record.Snapshot.RunningCount,
		&record.Snapshot.RetryWaitCount, &record.Snapshot.SuccessCount, &record.Snapshot.FailedCount,
		&record.Snapshot.CanceledCount, &record.Snapshot.EarliestStartedAt, &record.Snapshot.LatestFinishedAt,
	); err != nil {
		return application.RunRecord{}, err
	}
	record.Run.ID = IDFromDatabase(id)
	record.Snapshot.RunID = record.Run.ID
	record.Run.Context, record.Run.ErrorSummary = copyJSON(runContext), copyJSON(errorSummary)
	record.Run.CreatedAt = TimeToDatabase(record.Run.CreatedAt)
	record.Run.ScheduledAt, record.Run.StartedAt, record.Run.FinishedAt = optionalTimeFromDatabase(record.Run.ScheduledAt), optionalTimeFromDatabase(record.Run.StartedAt), optionalTimeFromDatabase(record.Run.FinishedAt)
	record.Snapshot.EarliestStartedAt, record.Snapshot.LatestFinishedAt = optionalTimeFromDatabase(record.Snapshot.EarliestStartedAt), optionalTimeFromDatabase(record.Snapshot.LatestFinishedAt)
	return record, nil
}

func scanTaskManagementRecord(row scanner) (application.TaskRecord, error) {
	var record application.TaskRecord
	var id, runID, subscriptionID, providerID, instrumentID, mappingID uuid.UUID
	var retryOf *uuid.UUID
	var errorDetails []byte
	var providerCode, instrumentCode, mappingCode string
	if err := row.Scan(
		&id, &runID, &subscriptionID, &retryOf, &record.Task.RangeStart, &record.Task.RangeEnd,
		&record.Task.Status, &record.Task.AttemptCount, &record.Task.MaxAttempts, &record.Task.NextAttemptAt,
		&record.Task.LockedBy, &record.Task.LockedUntil, &record.Task.StartedAt, &record.Task.FinishedAt,
		&record.Task.ProviderRequestID, &record.Task.ErrorCode, &record.Task.ErrorMessage, &errorDetails,
		&record.Task.CanceledBy, &record.Task.CancelReason, &record.Task.CreatedAt, &record.Task.UpdatedAt,
		&record.RunType, &record.TriggerType, &record.SubscriptionInterval, &providerID, &providerCode,
		&instrumentID, &instrumentCode, &mappingID, &mappingCode, &record.ProviderSymbol,
	); err != nil {
		return application.TaskRecord{}, err
	}
	record.Task.ID, record.Task.RunID, record.Task.SubscriptionID = IDFromDatabase(id), IDFromDatabase(runID), IDFromDatabase(subscriptionID)
	record.Task.RetryOfTaskID = optionalIDFromDatabase(retryOf)
	record.ProviderID, record.InstrumentID, record.ProviderInstrumentID = IDFromDatabase(providerID), IDFromDatabase(instrumentID), IDFromDatabase(mappingID)
	var err error
	record.ProviderCode, err = domain.ParseCode(providerCode)
	if err != nil {
		return application.TaskRecord{}, err
	}
	record.InstrumentCode, err = domain.ParseCode(instrumentCode)
	if err != nil {
		return application.TaskRecord{}, err
	}
	record.ProviderInstrumentCode, err = domain.ParseCode(mappingCode)
	if err != nil {
		return application.TaskRecord{}, err
	}
	record.Task.RangeStart, record.Task.RangeEnd = TimeToDatabase(record.Task.RangeStart), TimeToDatabase(record.Task.RangeEnd)
	record.Task.CreatedAt, record.Task.UpdatedAt = TimeToDatabase(record.Task.CreatedAt), TimeToDatabase(record.Task.UpdatedAt)
	record.Task.NextAttemptAt, record.Task.LockedUntil = optionalTimeFromDatabase(record.Task.NextAttemptAt), optionalTimeFromDatabase(record.Task.LockedUntil)
	record.Task.StartedAt, record.Task.FinishedAt = optionalTimeFromDatabase(record.Task.StartedAt), optionalTimeFromDatabase(record.Task.FinishedAt)
	record.Task.ErrorDetails = copyJSON(errorDetails)
	return record, nil
}

var _ application.IngestionQueryReader = (*IngestionRepository)(nil)
