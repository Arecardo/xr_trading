package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"xr-trading/market-info-service/internal/domain"
	"xr-trading/market-info-service/internal/ingestion"
)

func TestCommitFailureSchedulesRetryAtomically(t *testing.T) {
	fixture := newPostgresIngestionFixture(t)
	request := fixture.failureCommit(t, "retry_wait")
	var executions []string
	var taskArguments []any
	tx := &fakeMarketDataTransaction{
		query: func(context.Context, string, ...any) (pgx.Rows, error) { return nil, nil },
		queryRow: func(context.Context, string, ...any) pgx.Row {
			return ingestionFailureLeaseRow(request.Claim.Task)
		},
		exec: func(_ context.Context, query string, arguments ...any) (pgconn.CommandTag, error) {
			executions = append(executions, query)
			if strings.Contains(query, "UPDATE market_data.ingestion_tasks") {
				taskArguments = append([]any(nil), arguments...)
			}
			return pgconn.NewCommandTag("UPDATE 1"), nil
		},
	}
	repository, _ := newIngestionRepository(ingestionFakeDatabase(tx))
	if err := repository.CommitFailure(context.Background(), request); err != nil {
		t.Fatalf("CommitFailure() error = %v", err)
	}
	if !tx.committed || !tx.rolledBack || len(executions) != 2 || !strings.Contains(executions[0], "ingestion_checkpoints") || !strings.Contains(executions[1], "status = $1") {
		t.Fatalf("transaction committed=%t rolledBack=%t executions=%#v", tx.committed, tx.rolledBack, executions)
	}
	if taskArguments[0] != "retry_wait" || taskArguments[1] != TimeToDatabase(*request.NextAttemptAt) || taskArguments[3] != request.ErrorCode || string(taskArguments[5].(json.RawMessage)) != `{"provider_code":"bybit"}` {
		t.Fatalf("task arguments = %#v", taskArguments)
	}
}

func TestCommitFailureMovesTaskToTerminalFailed(t *testing.T) {
	fixture := newPostgresIngestionFixture(t)
	request := fixture.failureCommit(t, "failed")
	var taskArguments []any
	tx := &fakeMarketDataTransaction{
		query:    func(context.Context, string, ...any) (pgx.Rows, error) { return nil, nil },
		queryRow: func(context.Context, string, ...any) pgx.Row { return ingestionFailureLeaseRow(request.Claim.Task) },
		exec: func(_ context.Context, query string, arguments ...any) (pgconn.CommandTag, error) {
			if strings.Contains(query, "UPDATE market_data.ingestion_tasks") {
				taskArguments = append([]any(nil), arguments...)
			}
			return pgconn.NewCommandTag("UPDATE 1"), nil
		},
	}
	repository, _ := newIngestionRepository(ingestionFakeDatabase(tx))
	if err := repository.CommitFailure(context.Background(), request); err != nil {
		t.Fatalf("CommitFailure() error = %v", err)
	}
	if taskArguments[0] != "failed" || taskArguments[1] != nil || taskArguments[2] != TimeToDatabase(request.FinishedAt) {
		t.Fatalf("task arguments = %#v", taskArguments)
	}
}

func TestCommitFailureRejectsLostLeaseBeforeWrites(t *testing.T) {
	fixture := newPostgresIngestionFixture(t)
	request := fixture.failureCommit(t, "retry_wait")
	stale := request.Claim.Task
	stale.AttemptCount++
	tx := &fakeMarketDataTransaction{
		query:    func(context.Context, string, ...any) (pgx.Rows, error) { return nil, nil },
		queryRow: func(context.Context, string, ...any) pgx.Row { return ingestionFailureLeaseRow(stale) },
		exec: func(context.Context, string, ...any) (pgconn.CommandTag, error) {
			t.Fatal("write executed after lease mismatch")
			return pgconn.CommandTag{}, nil
		},
	}
	repository, _ := newIngestionRepository(ingestionFakeDatabase(tx))
	if err := repository.CommitFailure(context.Background(), request); !errors.Is(err, ingestion.ErrTaskLeaseLost) {
		t.Fatalf("CommitFailure(stale) error = %v", err)
	}
	if tx.committed || !tx.rolledBack {
		t.Fatalf("transaction committed=%t rolledBack=%t", tx.committed, tx.rolledBack)
	}
}

func TestCommitFailureRollsBackEveryPartialFailure(t *testing.T) {
	fixture := newPostgresIngestionFixture(t)
	request := fixture.failureCommit(t, "retry_wait")
	databaseFailure := &pgconn.PgError{Code: "08006"}
	for _, failureAt := range []string{"begin", "lock", "checkpoint", "task", "commit"} {
		t.Run(failureAt, func(t *testing.T) {
			tx := &fakeMarketDataTransaction{
				query: func(context.Context, string, ...any) (pgx.Rows, error) { return nil, nil },
				queryRow: func(context.Context, string, ...any) pgx.Row {
					if failureAt == "lock" {
						return scanFunc(func(...any) error { return databaseFailure })
					}
					return ingestionFailureLeaseRow(request.Claim.Task)
				},
				exec: func(_ context.Context, query string, _ ...any) (pgconn.CommandTag, error) {
					switch {
					case failureAt == "checkpoint" && strings.Contains(query, "ingestion_checkpoints"):
						return pgconn.CommandTag{}, databaseFailure
					case failureAt == "task" && strings.Contains(query, "ingestion_tasks"):
						return pgconn.NewCommandTag("UPDATE 0"), nil
					default:
						return pgconn.NewCommandTag("UPDATE 1"), nil
					}
				},
			}
			if failureAt == "commit" {
				tx.commit = func(context.Context) error { return databaseFailure }
			}
			database := ingestionFakeDatabase(tx)
			if failureAt == "begin" {
				database.begin = func(context.Context) (marketDataTransaction, error) { return nil, databaseFailure }
			}
			repository, _ := newIngestionRepository(database)
			err := repository.CommitFailure(context.Background(), request)
			if err == nil || (failureAt != "task" && failureAt != "commit" && failureAt != "begin" && !errors.Is(err, domain.ErrDatabaseUnavailable)) {
				t.Fatalf("CommitFailure() error = %v", err)
			}
			if failureAt != "begin" && !tx.rolledBack {
				t.Fatal("transaction was not rolled back")
			}
		})
	}
}

func TestCommitFailureValidatesInput(t *testing.T) {
	fixture := newPostgresIngestionFixture(t)
	repository, _ := newIngestionRepository(ingestionFakeDatabase(&fakeMarketDataTransaction{}))
	valid := fixture.failureCommit(t, "retry_wait")
	tests := []struct {
		name   string
		mutate func(*ingestion.FailureCommit)
	}{
		{"claim", func(value *ingestion.FailureCommit) { value.Claim.Task.LockedBy = nil }},
		{"status", func(value *ingestion.FailureCommit) { value.Status = "pending" }},
		{"retry time missing", func(value *ingestion.FailureCommit) { value.NextAttemptAt = nil }},
		{"retry time before finish", func(value *ingestion.FailureCommit) { at := value.FinishedAt; value.NextAttemptAt = &at }},
		{"code", func(value *ingestion.FailureCommit) { value.ErrorCode = " bad" }},
		{"message", func(value *ingestion.FailureCommit) { value.ErrorMessage = "" }},
		{"details", func(value *ingestion.FailureCommit) { value.ErrorDetails = []byte(`[]`) }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			request := valid
			test.mutate(&request)
			if err := repository.CommitFailure(context.Background(), request); !errors.Is(err, domain.ErrInvalidData) {
				t.Fatalf("CommitFailure() error = %v", err)
			}
		})
	}
}

func (fixture postgresIngestionFixture) failureCommit(t *testing.T, status string) ingestion.FailureCommit {
	t.Helper()
	claim := fixture.successCommit(t).Claim
	request := ingestion.FailureCommit{
		Claim: claim, Status: status, ErrorCode: "rate_limited", ErrorMessage: "provider rate limit reached",
		ErrorDetails: []byte(`{"provider_code":"bybit"}`), FinishedAt: fixture.now,
	}
	if status == "retry_wait" {
		nextAttemptAt := fixture.now.Add(time.Minute)
		request.NextAttemptAt = &nextAttemptAt
	}
	return request
}

func ingestionFailureLeaseRow(task domain.IngestionTask) scanFunc {
	return func(destinations ...any) error {
		*destinations[0].(*string) = task.Status
		*destinations[1].(*int) = task.AttemptCount
		*destinations[2].(**string) = task.LockedBy
		*destinations[3].(**time.Time) = task.LockedUntil
		*destinations[4].(*uuid.UUID) = task.SubscriptionID.UUID()
		return nil
	}
}
