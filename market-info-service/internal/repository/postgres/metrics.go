package postgres

import (
	"context"
	"fmt"
	"time"

	"xr-trading/market-info-service/internal/domain"
	"xr-trading/market-info-service/internal/observability"
)

// ReadTaskMetrics reads one bounded aggregate from durable Task truth.
func (repository *IngestionRepository) ReadTaskMetrics(ctx context.Context, observedAt time.Time) (observability.TaskMetricsSnapshot, error) {
	if ctx == nil || observedAt.IsZero() {
		return observability.TaskMetricsSnapshot{}, fmt.Errorf("read task metrics: %w", domain.ErrInvalidData)
	}
	var pending, running, retryWait, success, failed, canceled int64
	var oldestBacklog *time.Time
	err := repository.database.QueryRow(ctx, `SELECT
    count(*) FILTER (WHERE status = 'pending'),
    count(*) FILTER (WHERE status = 'running'),
    count(*) FILTER (WHERE status = 'retry_wait'),
    count(*) FILTER (WHERE status = 'success'),
    count(*) FILTER (WHERE status = 'failed'),
    count(*) FILTER (WHERE status = 'canceled'),
    min(created_at) FILTER (WHERE status IN ('pending', 'retry_wait'))
FROM market_data.ingestion_tasks
WHERE created_at <= $1`, TimeToDatabase(observedAt)).Scan(
		&pending, &running, &retryWait, &success, &failed, &canceled, &oldestBacklog,
	)
	if err != nil {
		return observability.TaskMetricsSnapshot{}, fmt.Errorf("read task metrics: %w", MapError(err))
	}
	return observability.TaskMetricsSnapshot{
		Counts: map[string]int64{
			"pending": pending, "running": running, "retry_wait": retryWait,
			"success": success, "failed": failed, "canceled": canceled,
		},
		OldestBacklogCreatedAt: optionalTimeFromDatabase(oldestBacklog),
	}, nil
}

var _ observability.TaskMetricsReader = (*IngestionRepository)(nil)
