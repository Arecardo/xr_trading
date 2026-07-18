package postgres

import (
	"context"
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

func TestCreateManualRetryLocksSourceAndCreatesPair(t *testing.T) {
	creation := repositoryManualRetryCreation()
	subscriptionID := uuid.MustParse("019f1452-90f7-7992-a87a-ca2727898210")
	queryRows := 0
	executes := make([]string, 0, 2)
	tx := &fakeMarketDataTransaction{
		queryRow: func(_ context.Context, query string, _ ...any) pgx.Row {
			queryRows++
			if queryRows == 1 {
				if !strings.Contains(query, "FOR UPDATE OF tasks") || !strings.Contains(query, "source_available") {
					t.Fatalf("source query = %s", query)
				}
				return scanFunc(func(destinations ...any) error {
					*destinations[0].(*string) = "failed"
					*destinations[1].(*uuid.UUID) = subscriptionID
					*destinations[2].(*time.Time) = creation.CreatedAt.Add(-2 * time.Hour)
					*destinations[3].(*time.Time) = creation.CreatedAt.Add(-time.Hour)
					*destinations[4].(*bool) = true
					return nil
				})
			}
			return scanFunc(func(destinations ...any) error {
				*destinations[0].(*bool) = false
				return nil
			})
		},
		query: func(context.Context, string, ...any) (pgx.Rows, error) { return nil, nil },
		exec: func(_ context.Context, query string, arguments ...any) (pgconn.CommandTag, error) {
			executes = append(executes, query)
			if len(executes) == 2 {
				if arguments[2] != subscriptionID || arguments[3] != creation.OriginalTaskID.UUID() {
					t.Fatalf("task subscription/retry args = %#v / %#v", arguments[2], arguments[3])
				}
			}
			return pgconn.NewCommandTag("INSERT 0 1"), nil
		},
	}
	repository, _ := newIngestionRepository(ingestionFakeDatabase(tx))
	if err := repository.CreateManualRetry(context.Background(), creation); err != nil {
		t.Fatalf("CreateManualRetry() error = %v", err)
	}
	if !tx.committed || len(executes) != 2 || !strings.Contains(executes[0], "ingestion_runs") || !strings.Contains(executes[1], "ingestion_tasks") {
		t.Fatalf("committed=%t executes=%#v", tx.committed, executes)
	}
}

func TestCreateManualRetryRejectsStateSourceAndDuplicate(t *testing.T) {
	creation := repositoryManualRetryCreation()
	for _, test := range []struct {
		name     string
		firstRow pgx.Row
		active   bool
		want     error
	}{
		{"missing", scanFunc(func(...any) error { return pgx.ErrNoRows }), false, domain.ErrNotFound},
		{"state", manualRetrySourceRow("success", true), false, domain.ErrConflict},
		{"source", manualRetrySourceRow("failed", false), false, ingestion.ErrManualRetrySourceUnavailable},
		{"duplicate", manualRetrySourceRow("failed", true), true, ingestion.ErrManualRetryAlreadyRunning},
	} {
		t.Run(test.name, func(t *testing.T) {
			queries := 0
			tx := &fakeMarketDataTransaction{
				queryRow: func(context.Context, string, ...any) pgx.Row {
					queries++
					if queries == 1 {
						return test.firstRow
					}
					return scanFunc(func(destinations ...any) error {
						*destinations[0].(*bool) = test.active
						return nil
					})
				},
				query: func(context.Context, string, ...any) (pgx.Rows, error) { return nil, nil },
				exec: func(context.Context, string, ...any) (pgconn.CommandTag, error) {
					return pgconn.CommandTag{}, errors.New("not expected")
				},
			}
			repository, _ := newIngestionRepository(ingestionFakeDatabase(tx))
			if err := repository.CreateManualRetry(context.Background(), creation); !errors.Is(err, test.want) {
				t.Fatalf("CreateManualRetry() = %v", err)
			}
			if tx.committed || !tx.rolledBack {
				t.Fatalf("committed=%t rolledBack=%t", tx.committed, tx.rolledBack)
			}
		})
	}
}

func TestCancelTaskWithAuditPersistsTaskAndRunOperation(t *testing.T) {
	creation := repositoryManualRetryCreation()
	runID := uuid.MustParse("019f1452-90f7-7992-a87a-ca2727898220")
	executes := make([]string, 0, 2)
	tx := &fakeMarketDataTransaction{
		queryRow: func(context.Context, string, ...any) pgx.Row {
			return scanFunc(func(destinations ...any) error {
				*destinations[0].(*string) = "running"
				*destinations[1].(*uuid.UUID) = runID
				return nil
			})
		},
		query: func(context.Context, string, ...any) (pgx.Rows, error) { return nil, nil },
		exec: func(_ context.Context, query string, arguments ...any) (pgconn.CommandTag, error) {
			executes = append(executes, query)
			if strings.Contains(query, "jsonb_set") && (!strings.Contains(string(arguments[0].([]byte)), `"request_id":"req_repository_manual"`) || !strings.Contains(string(arguments[0].([]byte)), `"action":"cancel"`)) {
				t.Fatalf("audit JSON = %s", arguments[0])
			}
			return pgconn.NewCommandTag("UPDATE 1"), nil
		},
	}
	repository, _ := newIngestionRepository(ingestionFakeDatabase(tx))
	gotRunID, err := repository.CancelTaskWithAudit(context.Background(), creation.OriginalTaskID, creation.Audit, creation.CreatedAt)
	if err != nil || gotRunID.UUID() != runID || !tx.committed || len(executes) != 2 {
		t.Fatalf("CancelTaskWithAudit() = (%s, %v), committed=%t executes=%d", gotRunID, err, tx.committed, len(executes))
	}
	if !strings.Contains(executes[0], "locked_until = NULL") || !strings.Contains(executes[1], "context->'operations'") {
		t.Fatalf("queries = %#v", executes)
	}
}

func TestManualTaskRepositoryValidatesAndMapsCancelConflicts(t *testing.T) {
	repository, _ := newIngestionRepository(ingestionFakeDatabase(&fakeMarketDataTransaction{
		queryRow: func(context.Context, string, ...any) pgx.Row { return statusAndRunRow("failed") },
		query:    func(context.Context, string, ...any) (pgx.Rows, error) { return nil, nil },
		exec:     func(context.Context, string, ...any) (pgconn.CommandTag, error) { return pgconn.CommandTag{}, nil },
	}))
	if err := repository.CreateManualRetry(context.Background(), ingestion.ManualRetryCreation{}); !errors.Is(err, domain.ErrInvalidData) {
		t.Fatalf("CreateManualRetry(invalid) = %v", err)
	}
	creation := repositoryManualRetryCreation()
	if _, err := repository.CancelTaskWithAudit(context.Background(), creation.OriginalTaskID, creation.Audit, creation.CreatedAt); !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("CancelTaskWithAudit(terminal) = %v", err)
	}
	if _, err := repository.CancelTaskWithAudit(context.Background(), domain.ID{}, creation.Audit, creation.CreatedAt); !errors.Is(err, domain.ErrInvalidData) {
		t.Fatalf("CancelTaskWithAudit(invalid) = %v", err)
	}
	if !manualRetryUniqueViolation(&pgconn.PgError{Code: "23505", ConstraintName: "uq_active_manual_retry"}) || manualRetryUniqueViolation(&pgconn.PgError{Code: "23505", ConstraintName: "other"}) {
		t.Fatal("manualRetryUniqueViolation() classification is incorrect")
	}
}

func repositoryManualRetryCreation() ingestion.ManualRetryCreation {
	now := time.Date(2026, 7, 19, 12, 0, 0, 0, time.UTC)
	return ingestion.ManualRetryCreation{
		OriginalTaskID:  manualRepositoryID("019f1452-90f7-7992-a87a-ca2727898201"),
		RunID:           manualRepositoryID("019f1452-90f7-7992-a87a-ca2727898202"),
		TaskID:          manualRepositoryID("019f1452-90f7-7992-a87a-ca2727898203"),
		MaximumAttempts: 5, CreatedAt: now, RunContext: []byte(`{"reason":"renewed"}`),
		Audit: ingestion.TaskOperationAudit{RequestedBy: "admin@example.com", ActorType: "user", RequestID: "req_repository_manual", Reason: "renewed"},
	}
}

func manualRetrySourceRow(status string, available bool) pgx.Row {
	return scanFunc(func(destinations ...any) error {
		*destinations[0].(*string) = status
		*destinations[1].(*uuid.UUID) = uuid.MustParse("019f1452-90f7-7992-a87a-ca2727898210")
		*destinations[2].(*time.Time) = time.Now().UTC().Add(-time.Hour)
		*destinations[3].(*time.Time) = time.Now().UTC()
		*destinations[4].(*bool) = available
		return nil
	})
}

func statusAndRunRow(status string) pgx.Row {
	return scanFunc(func(destinations ...any) error {
		*destinations[0].(*string) = status
		*destinations[1].(*uuid.UUID) = uuid.MustParse("019f1452-90f7-7992-a87a-ca2727898220")
		return nil
	})
}

func manualRepositoryID(value string) domain.ID { return domain.IDFromUUID(uuid.MustParse(value)) }
