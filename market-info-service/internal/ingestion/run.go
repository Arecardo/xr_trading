package ingestion

import (
	"context"
	"errors"
	"fmt"

	"xr-trading/market-info-service/internal/domain"
)

const maximumRunRefreshAttempts = 3

// RunService derives the Run query cache from its durable Task rows.
type RunService struct {
	store RunStore
}

// NewRunService constructs the Run aggregation use case.
func NewRunService(store RunStore) (*RunService, error) {
	if store == nil {
		return nil, errors.New("run store is required")
	}
	return &RunService{store: store}, nil
}

// Refresh recalculates and persists one Run. A concurrent Task transition is
// retried from a new snapshot so an older observation cannot overwrite newer
// Run state.
func (service *RunService) Refresh(ctx context.Context, runID domain.ID) (RunSummary, error) {
	if ctx == nil || runID.IsZero() {
		return RunSummary{}, fmt.Errorf("refresh ingestion run: %w", domain.ErrInvalidData)
	}
	for attempt := 0; attempt < maximumRunRefreshAttempts; attempt++ {
		snapshot, err := service.store.LoadRunTaskSnapshot(ctx, runID)
		if err != nil {
			return RunSummary{}, fmt.Errorf("load ingestion run task snapshot: %w", err)
		}
		if snapshot.RunID != runID {
			return RunSummary{}, fmt.Errorf("load ingestion run task snapshot identity: %w", domain.ErrInvalidData)
		}
		summary, err := SummarizeRun(snapshot)
		if err != nil {
			return RunSummary{}, fmt.Errorf("summarize ingestion run: %w", err)
		}
		if err := service.store.SaveRunSummary(ctx, summary); err == nil {
			return summary, nil
		} else if !errors.Is(err, domain.ErrConflict) {
			return RunSummary{}, fmt.Errorf("save ingestion run summary: %w", err)
		}
	}
	return RunSummary{}, fmt.Errorf("save ingestion run summary after %d concurrent changes: %w", maximumRunRefreshAttempts, domain.ErrConflict)
}

// SummarizeRun is the complete deterministic Run state table. The all-pending
// case has priority over the general active-state rule; every mixed terminal
// combination is partial.
func SummarizeRun(snapshot RunTaskSnapshot) (RunSummary, error) {
	if err := validateRunTaskSnapshot(snapshot); err != nil {
		return RunSummary{}, err
	}
	total := snapshot.TaskCount()
	status := "partial"
	switch {
	case snapshot.PendingCount == total:
		status = "pending"
	case snapshot.PendingCount+snapshot.RunningCount+snapshot.RetryWaitCount > 0:
		status = "running"
	case snapshot.SuccessCount == total:
		status = "success"
	case snapshot.FailedCount == total:
		status = "failed"
	case snapshot.CanceledCount == total:
		status = "canceled"
	}

	finishedAt := snapshot.LatestFinishedAt
	if status == "pending" || status == "running" {
		finishedAt = nil
	}
	return RunSummary{
		RunID: snapshot.RunID, Status: status, TaskCount: total,
		PendingCount: snapshot.PendingCount, RunningCount: snapshot.RunningCount,
		RetryWaitCount: snapshot.RetryWaitCount, SuccessCount: snapshot.SuccessCount,
		FailedCount: snapshot.FailedCount, CanceledCount: snapshot.CanceledCount,
		EarliestStartedAt: snapshot.EarliestStartedAt, LatestFinishedAt: finishedAt,
	}, nil
}

func validateRunTaskSnapshot(snapshot RunTaskSnapshot) error {
	if snapshot.RunID.IsZero() || snapshot.PendingCount < 0 || snapshot.RunningCount < 0 ||
		snapshot.RetryWaitCount < 0 || snapshot.SuccessCount < 0 || snapshot.FailedCount < 0 ||
		snapshot.CanceledCount < 0 || snapshot.TaskCount() == 0 {
		return domain.ErrInvalidData
	}
	return nil
}
