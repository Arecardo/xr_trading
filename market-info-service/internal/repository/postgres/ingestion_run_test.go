package postgres

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"xr-trading/market-info-service/internal/domain"
	"xr-trading/market-info-service/internal/ingestion"
)

func TestLoadRunTaskSnapshotMapsAllTaskStates(t *testing.T) {
	now := time.Now().UTC()
	run, _ := testRunAndTask(now)
	database := fakeMarketDataDatabase{fakeCatalogDatabase: fakeCatalogDatabase{
		exec:  func(context.Context, string, ...any) (pgconn.CommandTag, error) { return pgconn.CommandTag{}, nil },
		query: func(context.Context, string, ...any) (pgx.Rows, error) { return nil, nil },
		queryRow: func(_ context.Context, query string, _ ...any) pgx.Row {
			if !strings.Contains(query, "LEFT JOIN market_data.ingestion_tasks") || !strings.Contains(query, "FILTER (WHERE tasks.status = 'canceled')") {
				t.Fatalf("snapshot query = %s", query)
			}
			return scanFunc(func(destinations ...any) error {
				*destinations[0].(*int) = 9
				*destinations[1].(*int) = 1
				*destinations[2].(*int) = 2
				*destinations[3].(*int) = 1
				*destinations[4].(*int) = 3
				*destinations[5].(*int) = 1
				*destinations[6].(*int) = 1
				*destinations[7].(**time.Time) = &now
				finished := now.Add(time.Hour)
				*destinations[8].(**time.Time) = &finished
				return nil
			})
		},
	}, begin: func(context.Context) (marketDataTransaction, error) { return nil, errors.New("not used") }}
	repository, _ := newIngestionRepository(database)
	snapshot, err := repository.LoadRunTaskSnapshot(context.Background(), run.ID)
	if err != nil || snapshot.TaskCount() != 9 || snapshot.RunningCount != 2 || snapshot.SuccessCount != 3 || snapshot.CanceledCount != 1 || snapshot.EarliestStartedAt == nil || snapshot.LatestFinishedAt == nil {
		t.Fatalf("LoadRunTaskSnapshot() = (%#v, %v)", snapshot, err)
	}
}

func TestLoadRunTaskSnapshotErrors(t *testing.T) {
	repository, _ := newIngestionRepository(fakeMarketDataDatabase{fakeCatalogDatabase: fakeCatalogDatabase{
		exec:  func(context.Context, string, ...any) (pgconn.CommandTag, error) { return pgconn.CommandTag{}, nil },
		query: func(context.Context, string, ...any) (pgx.Rows, error) { return nil, nil },
		queryRow: func(context.Context, string, ...any) pgx.Row {
			return scanFunc(func(...any) error { return pgx.ErrNoRows })
		},
	}, begin: func(context.Context) (marketDataTransaction, error) { return nil, errors.New("not used") }})
	if _, err := repository.LoadRunTaskSnapshot(context.Background(), domain.ID{}); !errors.Is(err, domain.ErrInvalidData) {
		t.Fatalf("LoadRunTaskSnapshot(invalid) error = %v", err)
	}
	if _, err := repository.LoadRunTaskSnapshot(context.Background(), runTestRepositoryID()); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("LoadRunTaskSnapshot(missing) error = %v", err)
	}
}

func TestSaveRunSummaryUsesOptimisticTaskCounts(t *testing.T) {
	var query string
	var arguments []any
	database := fakeMarketDataDatabase{fakeCatalogDatabase: fakeCatalogDatabase{
		exec: func(_ context.Context, statement string, values ...any) (pgconn.CommandTag, error) {
			query, arguments = statement, values
			return pgconn.NewCommandTag("UPDATE 1"), nil
		},
		query:    func(context.Context, string, ...any) (pgx.Rows, error) { return nil, nil },
		queryRow: func(context.Context, string, ...any) pgx.Row { return nil },
	}, begin: func(context.Context) (marketDataTransaction, error) { return nil, errors.New("not used") }}
	repository, _ := newIngestionRepository(database)
	summary := ingestion.RunSummary{RunID: runTestRepositoryID(), Status: "partial", TaskCount: 3, SuccessCount: 1, FailedCount: 1, CanceledCount: 1}
	if err := repository.SaveRunSummary(context.Background(), summary); err != nil {
		t.Fatalf("SaveRunSummary() error = %v", err)
	}
	for _, fragment := range []string{"started_at = COALESCE", "HAVING count(*)", "status = 'retry_wait'", "status = 'canceled'"} {
		if !strings.Contains(query, fragment) {
			t.Fatalf("summary query missing %q: %s", fragment, query)
		}
	}
	if len(arguments) != 11 || arguments[2] != 3 || arguments[3] != 1 || arguments[4] != 1 || arguments[10] != 1 {
		t.Fatalf("summary arguments = %#v", arguments)
	}
}

func TestSaveRunSummaryRejectsInvalidAndStaleWrites(t *testing.T) {
	database := fakeMarketDataDatabase{fakeCatalogDatabase: fakeCatalogDatabase{
		exec: func(context.Context, string, ...any) (pgconn.CommandTag, error) {
			return pgconn.NewCommandTag("UPDATE 0"), nil
		},
		query:    func(context.Context, string, ...any) (pgx.Rows, error) { return nil, nil },
		queryRow: func(context.Context, string, ...any) pgx.Row { return nil },
	}, begin: func(context.Context) (marketDataTransaction, error) { return nil, errors.New("not used") }}
	repository, _ := newIngestionRepository(database)
	if err := repository.SaveRunSummary(context.Background(), ingestion.RunSummary{}); !errors.Is(err, domain.ErrInvalidData) {
		t.Fatalf("SaveRunSummary(invalid) error = %v", err)
	}
	finishedAt := time.Now().UTC()
	activeWithFinishedAt := ingestion.RunSummary{RunID: runTestRepositoryID(), Status: "running", TaskCount: 1, RunningCount: 1, LatestFinishedAt: &finishedAt}
	if err := repository.SaveRunSummary(context.Background(), activeWithFinishedAt); !errors.Is(err, domain.ErrInvalidData) {
		t.Fatalf("SaveRunSummary(active finished) error = %v", err)
	}
	stale := ingestion.RunSummary{RunID: runTestRepositoryID(), Status: "pending", TaskCount: 1, PendingCount: 1}
	if err := repository.SaveRunSummary(context.Background(), stale); !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("SaveRunSummary(stale) error = %v", err)
	}
}

func runTestRepositoryID() domain.ID {
	run, _ := testRunAndTask(time.Now().UTC())
	return run.ID
}
