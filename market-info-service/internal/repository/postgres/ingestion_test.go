package postgres

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"xr-trading/market-info-service/internal/domain"
)

func TestCreateRunWithTaskAndClaim(t *testing.T) {
	now := time.Now().UTC()
	run, task := testRunAndTask(now)
	executes := 0
	tx := &fakeMarketDataTransaction{exec: func(context.Context, string, ...any) (pgconn.CommandTag, error) {
		executes++
		return pgconn.NewCommandTag("INSERT 0 1"), nil
	}, queryRow: func(context.Context, string, ...any) pgx.Row { return nil }, query: func(context.Context, string, ...any) (pgx.Rows, error) { return nil, nil }}
	database := fakeMarketDataDatabase{fakeCatalogDatabase: fakeCatalogDatabase{
		exec: func(context.Context, string, ...any) (pgconn.CommandTag, error) {
			return pgconn.NewCommandTag("UPDATE 1"), nil
		},
		queryRow: func(context.Context, string, ...any) pgx.Row { return taskRow(task) },
		query:    func(context.Context, string, ...any) (pgx.Rows, error) { return nil, nil },
	}, begin: func(context.Context) (marketDataTransaction, error) { return tx, nil }}
	repository, err := newIngestionRepository(database)
	if err != nil {
		t.Fatalf("newIngestionRepository() error = %v", err)
	}
	if err := repository.CreateRunWithTask(context.Background(), run, task); err != nil || executes != 2 || !tx.committed {
		t.Fatalf("CreateRunWithTask() = (%v, executes=%d, committed=%t)", err, executes, tx.committed)
	}
	claim, err := repository.ClaimNextTask(context.Background(), "worker-a", now, time.Minute)
	if err != nil || claim == nil || claim.Task.ID != task.ID {
		t.Fatalf("ClaimNextTask() = (%#v, %v)", claim, err)
	}
}

func TestIngestionRepositoryControls(t *testing.T) {
	now := time.Now().UTC()
	_, task := testRunAndTask(now)
	database := fakeMarketDataDatabase{fakeCatalogDatabase: fakeCatalogDatabase{
		exec: func(context.Context, string, ...any) (pgconn.CommandTag, error) {
			return pgconn.NewCommandTag("UPDATE 2"), nil
		},
		queryRow: func(context.Context, string, ...any) pgx.Row {
			return scanFunc(func(...any) error { return pgx.ErrNoRows })
		},
		query: func(context.Context, string, ...any) (pgx.Rows, error) { return nil, nil },
	}, begin: func(context.Context) (marketDataTransaction, error) { return nil, errors.New("not used") }}
	repository, _ := newIngestionRepository(database)
	claim, err := repository.ClaimNextTask(context.Background(), "worker", now, time.Minute)
	if err != nil || claim != nil {
		t.Fatalf("ClaimNextTask(empty) = (%#v, %v)", claim, err)
	}
	if err := repository.CancelTask(context.Background(), task.ID, "admin", "", now); err != nil {
		t.Fatalf("CancelTask() error = %v", err)
	}
	if count, err := repository.RecoverExpiredTasks(context.Background(), now); err != nil || count != 2 {
		t.Fatalf("RecoverExpiredTasks() = (%d, %v)", count, err)
	}
	if err := repository.UpsertCheckpoint(context.Background(), domain.IngestionCheckpoint{SubscriptionID: task.SubscriptionID, UpdatedAt: now}); err != nil {
		t.Fatalf("UpsertCheckpoint() error = %v", err)
	}
	if _, err := repository.ClaimNextTask(context.Background(), "", now, time.Minute); !errors.Is(err, domain.ErrInvalidData) {
		t.Fatalf("ClaimNextTask(invalid) error = %v", err)
	}
	if err := repository.CancelTask(context.Background(), domain.ID{}, "admin", "", now); !errors.Is(err, domain.ErrInvalidData) {
		t.Fatalf("CancelTask(invalid) error = %v", err)
	}
	if _, err := repository.RecoverExpiredTasks(context.Background(), time.Time{}); !errors.Is(err, domain.ErrInvalidData) {
		t.Fatalf("RecoverExpiredTasks(invalid) error = %v", err)
	}
	if err := repository.UpsertCheckpoint(context.Background(), domain.IngestionCheckpoint{}); !errors.Is(err, domain.ErrInvalidData) {
		t.Fatalf("UpsertCheckpoint(invalid) error = %v", err)
	}
}

func TestCreateRunWithTaskRejectsInvalidIdentity(t *testing.T) {
	repository, _ := newIngestionRepository(fakeMarketDataDatabase{fakeCatalogDatabase: fakeCatalogDatabase{exec: func(context.Context, string, ...any) (pgconn.CommandTag, error) { return pgconn.CommandTag{}, nil }, queryRow: func(context.Context, string, ...any) pgx.Row { return nil }, query: func(context.Context, string, ...any) (pgx.Rows, error) { return nil, nil }}, begin: func(context.Context) (marketDataTransaction, error) { return nil, nil }})
	if err := repository.CreateRunWithTask(context.Background(), domain.IngestionRun{}, domain.IngestionTask{}); !errors.Is(err, domain.ErrInvalidData) {
		t.Fatalf("CreateRunWithTask(invalid) error = %v", err)
	}
	if _, err := NewIngestionRepository(nil); err == nil {
		t.Fatal("NewIngestionRepository(nil) error = nil")
	}
}

func testRunAndTask(now time.Time) (domain.IngestionRun, domain.IngestionTask) {
	runID := domain.IDFromUUID(uuid.MustParse("019f1452-90f7-7992-a87a-ca2727891601"))
	return domain.IngestionRun{ID: runID, RunKey: "run.test", RunType: "incremental", TriggerType: "scheduler", Status: "pending", CreatedAt: now}, domain.IngestionTask{ID: domain.IDFromUUID(uuid.MustParse("019f1452-90f7-7992-a87a-ca2727891602")), RunID: runID, SubscriptionID: domain.IDFromUUID(uuid.MustParse("019f1452-90f7-7992-a87a-ca2727891603")), RangeStart: now, RangeEnd: now.Add(time.Hour), Status: "pending", MaxAttempts: 5, CreatedAt: now, UpdatedAt: now}
}

func taskRow(task domain.IngestionTask) scanFunc {
	return func(destinations ...any) error {
		*destinations[0].(*uuid.UUID) = task.ID.UUID()
		*destinations[1].(*uuid.UUID) = task.RunID.UUID()
		*destinations[2].(*uuid.UUID) = task.SubscriptionID.UUID()
		*destinations[3].(**uuid.UUID) = nil
		*destinations[4].(*time.Time) = task.RangeStart
		*destinations[5].(*time.Time) = task.RangeEnd
		*destinations[6].(*string) = "running"
		*destinations[7].(*int) = 1
		*destinations[8].(*int) = task.MaxAttempts
		*destinations[9].(**time.Time) = nil
		worker := "worker-a"
		*destinations[10].(**string) = &worker
		until := task.RangeEnd
		*destinations[11].(**time.Time) = &until
		*destinations[12].(**time.Time) = &task.RangeStart
		*destinations[13].(**time.Time) = nil
		*destinations[14].(**string) = nil
		*destinations[15].(**string) = nil
		*destinations[16].(**string) = nil
		*destinations[17].(*[]byte) = []byte(`{}`)
		*destinations[18].(**string) = nil
		*destinations[19].(**string) = nil
		*destinations[20].(*time.Time) = task.CreatedAt
		*destinations[21].(*time.Time) = task.UpdatedAt
		return nil
	}
}
