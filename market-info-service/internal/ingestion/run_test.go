package ingestion

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"

	"xr-trading/market-info-service/internal/domain"
)

type runStoreStub struct {
	snapshots []RunTaskSnapshot
	loadErr   error
	saveErrs  []error
	loads     int
	saves     int
	saved     RunSummary
}

func (store *runStoreStub) LoadRunTaskSnapshot(context.Context, domain.ID) (RunTaskSnapshot, error) {
	store.loads++
	if store.loadErr != nil {
		return RunTaskSnapshot{}, store.loadErr
	}
	index := store.loads - 1
	if index >= len(store.snapshots) {
		index = len(store.snapshots) - 1
	}
	return store.snapshots[index], nil
}

func (store *runStoreStub) SaveRunSummary(_ context.Context, summary RunSummary) error {
	store.saves++
	store.saved = summary
	if store.saves <= len(store.saveErrs) {
		return store.saveErrs[store.saves-1]
	}
	return nil
}

func TestSummarizeRunCompleteStateTable(t *testing.T) {
	runID := runTestID()
	startedAt := time.Date(2026, time.July, 17, 1, 0, 0, 0, time.UTC)
	finishedAt := startedAt.Add(time.Hour)
	tests := []struct {
		name     string
		snapshot RunTaskSnapshot
		status   string
		finished bool
	}{
		{"all pending", RunTaskSnapshot{PendingCount: 2}, "pending", false},
		{"pending and success", RunTaskSnapshot{PendingCount: 1, SuccessCount: 1}, "running", false},
		{"all running", RunTaskSnapshot{RunningCount: 2}, "running", false},
		{"all retry wait", RunTaskSnapshot{RetryWaitCount: 2}, "running", false},
		{"active and failed", RunTaskSnapshot{RunningCount: 1, FailedCount: 1}, "running", false},
		{"all success", RunTaskSnapshot{SuccessCount: 2}, "success", true},
		{"success and failed", RunTaskSnapshot{SuccessCount: 1, FailedCount: 1}, "partial", true},
		{"success and canceled", RunTaskSnapshot{SuccessCount: 1, CanceledCount: 1}, "partial", true},
		{"failed and canceled", RunTaskSnapshot{FailedCount: 1, CanceledCount: 1}, "partial", true},
		{"all failed", RunTaskSnapshot{FailedCount: 2}, "failed", true},
		{"all canceled", RunTaskSnapshot{CanceledCount: 2}, "canceled", true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			test.snapshot.RunID = runID
			test.snapshot.EarliestStartedAt = &startedAt
			test.snapshot.LatestFinishedAt = &finishedAt
			summary, err := SummarizeRun(test.snapshot)
			if err != nil {
				t.Fatalf("SummarizeRun() error = %v", err)
			}
			if summary.Status != test.status || summary.TaskCount != test.snapshot.TaskCount() || summary.SuccessCount != test.snapshot.SuccessCount || summary.FailedCount != test.snapshot.FailedCount || summary.CanceledCount != test.snapshot.CanceledCount {
				t.Fatalf("summary = %#v", summary)
			}
			if (summary.LatestFinishedAt != nil) != test.finished {
				t.Fatalf("finishedAt = %v, want present=%t", summary.LatestFinishedAt, test.finished)
			}
		})
	}
}

func TestSummarizeRunRejectsInvalidSnapshots(t *testing.T) {
	for _, snapshot := range []RunTaskSnapshot{
		{},
		{RunID: runTestID()},
		{RunID: runTestID(), PendingCount: -1, SuccessCount: 2},
	} {
		if _, err := SummarizeRun(snapshot); !errors.Is(err, domain.ErrInvalidData) {
			t.Fatalf("SummarizeRun(%#v) error = %v", snapshot, err)
		}
	}
}

func TestRunServiceRefreshRetriesConcurrentSnapshotChange(t *testing.T) {
	runID := runTestID()
	store := &runStoreStub{
		snapshots: []RunTaskSnapshot{
			{RunID: runID, RunningCount: 1},
			{RunID: runID, SuccessCount: 1},
		},
		saveErrs: []error{domain.ErrConflict},
	}
	service, err := NewRunService(store)
	if err != nil {
		t.Fatalf("NewRunService() error = %v", err)
	}
	summary, err := service.Refresh(context.Background(), runID)
	if err != nil || summary.Status != "success" || store.loads != 2 || store.saves != 2 || store.saved.Status != "success" {
		t.Fatalf("Refresh() = (%#v, %v), loads=%d saves=%d saved=%#v", summary, err, store.loads, store.saves, store.saved)
	}
}

func TestRunServiceErrors(t *testing.T) {
	runID := runTestID()
	if _, err := NewRunService(nil); err == nil {
		t.Fatal("NewRunService(nil) error = nil")
	}
	service, _ := NewRunService(&runStoreStub{snapshots: []RunTaskSnapshot{{RunID: runID, PendingCount: 1}}})
	if _, err := service.Refresh(nil, runID); !errors.Is(err, domain.ErrInvalidData) {
		t.Fatalf("Refresh(nil) error = %v", err)
	}
	if _, err := service.Refresh(context.Background(), domain.ID{}); !errors.Is(err, domain.ErrInvalidData) {
		t.Fatalf("Refresh(zero) error = %v", err)
	}

	loadFailure := errors.New("load failed")
	service, _ = NewRunService(&runStoreStub{loadErr: loadFailure})
	if _, err := service.Refresh(context.Background(), runID); !errors.Is(err, loadFailure) {
		t.Fatalf("Refresh(load) error = %v", err)
	}

	wrongRunID := domain.IDFromUUID(uuid.MustParse("019f1452-90f7-7992-a87a-ca2727891698"))
	service, _ = NewRunService(&runStoreStub{snapshots: []RunTaskSnapshot{{RunID: wrongRunID, PendingCount: 1}}})
	if _, err := service.Refresh(context.Background(), runID); !errors.Is(err, domain.ErrInvalidData) {
		t.Fatalf("Refresh(identity mismatch) error = %v", err)
	}

	saveFailure := errors.New("save failed")
	service, _ = NewRunService(&runStoreStub{snapshots: []RunTaskSnapshot{{RunID: runID, PendingCount: 1}}, saveErrs: []error{saveFailure}})
	if _, err := service.Refresh(context.Background(), runID); !errors.Is(err, saveFailure) {
		t.Fatalf("Refresh(save) error = %v", err)
	}

	service, _ = NewRunService(&runStoreStub{
		snapshots: []RunTaskSnapshot{{RunID: runID, PendingCount: 1}},
		saveErrs:  []error{domain.ErrConflict, domain.ErrConflict, domain.ErrConflict},
	})
	if _, err := service.Refresh(context.Background(), runID); !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("Refresh(conflicts) error = %v", err)
	}
}

func runTestID() domain.ID {
	return domain.IDFromUUID(uuid.MustParse("019f1452-90f7-7992-a87a-ca2727891699"))
}
