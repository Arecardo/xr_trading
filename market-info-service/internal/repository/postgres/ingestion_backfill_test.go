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

func TestResolveBackfillTargetUsesEffectiveDefaultMapping(t *testing.T) {
	fixture := newPostgresIngestionFixture(t)
	var resolveQuery string
	database := fakeMarketDataDatabase{fakeCatalogDatabase: fakeCatalogDatabase{
		exec:  func(context.Context, string, ...any) (pgconn.CommandTag, error) { return pgconn.CommandTag{}, nil },
		query: func(context.Context, string, ...any) (pgx.Rows, error) { return nil, nil },
		queryRow: func(_ context.Context, query string, _ ...any) pgx.Row {
			switch {
			case strings.Contains(query, "SELECT subscriptions.id"):
				resolveQuery = query
				return scanFunc(func(destinations ...any) error {
					*destinations[0].(*uuid.UUID) = fixture.subscriptionID.UUID()
					return nil
				})
			case strings.Contains(query, "collection_subscriptions"):
				return ingestionSubscriptionRow(fixture)
			case strings.Contains(query, "provider_instruments"):
				return ingestionMappingRow(fixture)
			case strings.Contains(query, "market_data.providers"):
				return ingestionProviderRow(fixture)
			case strings.Contains(query, "core.instruments"):
				return ingestionInstrumentRow(fixture)
			case strings.Contains(query, "core.assets"):
				return ingestionAssetRow(fixture)
			default:
				return scanFunc(func(...any) error { return errors.New("unexpected query") })
			}
		},
	}, begin: func(context.Context) (marketDataTransaction, error) { return nil, errors.New("not used") }}
	repository, _ := newIngestionRepository(database)
	providerCode, _ := domain.ParseCode("bybit")
	instrumentCode, _ := domain.ParseCode("instrument.bybit.spot.btc-usdt")
	execution, err := repository.ResolveBackfillTarget(context.Background(), providerCode, instrumentCode, domain.BarInterval1Hour, fixture.now)
	if err != nil || execution.Subscription.ID != fixture.subscriptionID {
		t.Fatalf("ResolveBackfillTarget() = (%#v, %v)", execution, err)
	}
	for _, fragment := range []string{"subscriptions.enabled = true", "instruments.status = 'active'", "mappings.enabled = true", "providers.status IN", "mappings.valid_from", "mappings.valid_to", "mappings.is_default DESC", "mappings.priority ASC", "LIMIT 1"} {
		if !strings.Contains(resolveQuery, fragment) {
			t.Fatalf("resolve query missing %q: %s", fragment, resolveQuery)
		}
	}
}

func TestResolveBackfillTargetRejectsInvalidAndMissingTargets(t *testing.T) {
	repository, _ := newIngestionRepository(fakeMarketDataDatabase{fakeCatalogDatabase: fakeCatalogDatabase{
		exec:  func(context.Context, string, ...any) (pgconn.CommandTag, error) { return pgconn.CommandTag{}, nil },
		query: func(context.Context, string, ...any) (pgx.Rows, error) { return nil, nil },
		queryRow: func(context.Context, string, ...any) pgx.Row {
			return scanFunc(func(...any) error { return pgx.ErrNoRows })
		},
	}, begin: func(context.Context) (marketDataTransaction, error) { return nil, errors.New("not used") }})
	code, _ := domain.ParseCode("bybit")
	if _, err := repository.ResolveBackfillTarget(context.Background(), domain.Code{}, code, domain.BarInterval1Hour, time.Now()); !errors.Is(err, domain.ErrInvalidData) {
		t.Fatalf("ResolveBackfillTarget(invalid) error = %v", err)
	}
	if _, err := repository.ResolveBackfillTarget(context.Background(), code, code, "5m", time.Now()); !errors.Is(err, domain.ErrInvalidData) {
		t.Fatalf("ResolveBackfillTarget(interval) error = %v", err)
	}
	if _, err := repository.ResolveBackfillTarget(context.Background(), code, code, domain.BarInterval1Hour, time.Now()); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("ResolveBackfillTarget(missing) error = %v", err)
	}
}

func TestCreateBackfillRunWithTaskLocksChecksAndCommits(t *testing.T) {
	run, task := postgresBackfillRunTask(t)
	var executions []string
	var duplicateQuery string
	tx := &fakeMarketDataTransaction{
		exec: func(_ context.Context, query string, _ ...any) (pgconn.CommandTag, error) {
			executions = append(executions, query)
			return pgconn.NewCommandTag("INSERT 0 1"), nil
		},
		query: func(context.Context, string, ...any) (pgx.Rows, error) { return nil, nil },
		queryRow: func(_ context.Context, query string, _ ...any) pgx.Row {
			if strings.Contains(query, "SELECT enabled") {
				return scanFunc(func(destinations ...any) error { *destinations[0].(*bool) = true; return nil })
			}
			duplicateQuery = query
			return scanFunc(func(...any) error { return pgx.ErrNoRows })
		},
	}
	repository, _ := newIngestionRepository(ingestionFakeDatabase(tx))
	if err := repository.CreateBackfillRunWithTask(context.Background(), run, task); err != nil {
		t.Fatalf("CreateBackfillRunWithTask() error = %v", err)
	}
	if !tx.committed || !tx.rolledBack || len(executions) != 3 || !strings.Contains(executions[0], "pg_advisory_xact_lock") || !strings.Contains(executions[1], "ingestion_runs") || !strings.Contains(executions[2], "ingestion_tasks") {
		t.Fatalf("committed=%t rollback=%t executions=%#v", tx.committed, tx.rolledBack, executions)
	}
	for _, fragment := range []string{"runs.run_type = 'backfill'", "'pending', 'running', 'retry_wait'", "tasks.range_start = $2", "tasks.range_end = $3"} {
		if !strings.Contains(duplicateQuery, fragment) {
			t.Fatalf("duplicate query missing %q: %s", fragment, duplicateQuery)
		}
	}
}

func TestCreateBackfillRunWithTaskRejectsDuplicateAndDisabledSubscription(t *testing.T) {
	run, task := postgresBackfillRunTask(t)
	for _, test := range []struct {
		name      string
		enabled   bool
		duplicate bool
		want      error
	}{
		{"disabled", false, false, domain.ErrInvalidState},
		{"duplicate", true, true, ingestion.ErrBackfillAlreadyRunning},
	} {
		t.Run(test.name, func(t *testing.T) {
			executions := 0
			tx := &fakeMarketDataTransaction{
				exec: func(context.Context, string, ...any) (pgconn.CommandTag, error) {
					executions++
					return pgconn.NewCommandTag("SELECT 1"), nil
				},
				query: func(context.Context, string, ...any) (pgx.Rows, error) { return nil, nil },
				queryRow: func(_ context.Context, query string, _ ...any) pgx.Row {
					if strings.Contains(query, "SELECT enabled") {
						return scanFunc(func(destinations ...any) error { *destinations[0].(*bool) = test.enabled; return nil })
					}
					return scanFunc(func(destinations ...any) error {
						if !test.duplicate {
							return pgx.ErrNoRows
						}
						*destinations[0].(*uuid.UUID) = task.ID.UUID()
						return nil
					})
				},
			}
			repository, _ := newIngestionRepository(ingestionFakeDatabase(tx))
			if err := repository.CreateBackfillRunWithTask(context.Background(), run, task); !errors.Is(err, test.want) {
				t.Fatalf("CreateBackfillRunWithTask() error = %v", err)
			}
			if tx.committed || !tx.rolledBack || executions != 1 {
				t.Fatalf("committed=%t rollback=%t executions=%d", tx.committed, tx.rolledBack, executions)
			}
		})
	}
}

func TestCreateBackfillRunWithTaskValidatesAndMapsWriteFailures(t *testing.T) {
	run, task := postgresBackfillRunTask(t)
	repository, _ := newIngestionRepository(fakeMarketDataDatabase{fakeCatalogDatabase: fakeCatalogDatabase{
		exec:  func(context.Context, string, ...any) (pgconn.CommandTag, error) { return pgconn.CommandTag{}, nil },
		query: func(context.Context, string, ...any) (pgx.Rows, error) { return nil, nil }, queryRow: func(context.Context, string, ...any) pgx.Row { return nil },
	}, begin: func(context.Context) (marketDataTransaction, error) { return nil, domain.ErrDatabaseUnavailable }})
	invalid := run
	invalid.RunType = "incremental"
	if err := repository.CreateBackfillRunWithTask(context.Background(), invalid, task); !errors.Is(err, domain.ErrInvalidData) {
		t.Fatalf("CreateBackfillRunWithTask(invalid) error = %v", err)
	}
	if err := repository.CreateBackfillRunWithTask(context.Background(), run, task); !errors.Is(err, domain.ErrDatabaseUnavailable) {
		t.Fatalf("CreateBackfillRunWithTask(begin) error = %v", err)
	}

	writeFailure := errors.New("insert failed")
	executes := 0
	tx := &fakeMarketDataTransaction{
		exec: func(context.Context, string, ...any) (pgconn.CommandTag, error) {
			executes++
			if executes == 2 {
				return pgconn.CommandTag{}, writeFailure
			}
			return pgconn.NewCommandTag("SELECT 1"), nil
		},
		query: func(context.Context, string, ...any) (pgx.Rows, error) { return nil, nil },
		queryRow: func(_ context.Context, query string, _ ...any) pgx.Row {
			if strings.Contains(query, "SELECT enabled") {
				return scanFunc(func(destinations ...any) error { *destinations[0].(*bool) = true; return nil })
			}
			return scanFunc(func(...any) error { return pgx.ErrNoRows })
		},
	}
	repository, _ = newIngestionRepository(ingestionFakeDatabase(tx))
	if err := repository.CreateBackfillRunWithTask(context.Background(), run, task); !errors.Is(err, writeFailure) || tx.committed || !tx.rolledBack {
		t.Fatalf("CreateBackfillRunWithTask(write) error=%v committed=%t rollback=%t", err, tx.committed, tx.rolledBack)
	}
}

func postgresBackfillRunTask(t *testing.T) (domain.IngestionRun, domain.IngestionTask) {
	t.Helper()
	now := time.Date(2026, time.July, 17, 10, 0, 0, 0, time.UTC)
	runID := domain.IDFromUUID(uuid.MustParse("019f1452-90f7-7992-a87a-ca2727891801"))
	taskID := domain.IDFromUUID(uuid.MustParse("019f1452-90f7-7992-a87a-ca2727891802"))
	subscriptionID := domain.IDFromUUID(uuid.MustParse("019f1452-90f7-7992-a87a-ca2727891803"))
	requestedBy := "admin@example.com"
	run := domain.IngestionRun{
		ID: runID, RunKey: "backfill.manual." + runID.String(), RunType: "backfill", TriggerType: "manual",
		Status: "pending", RequestedBy: &requestedBy, TaskCount: 1,
		Context: []byte(`{"reason":"initialize"}`), ErrorSummary: []byte(`{}`), CreatedAt: now,
	}
	task := domain.IngestionTask{
		ID: taskID, RunID: runID, SubscriptionID: subscriptionID, RangeStart: now.Add(-48 * time.Hour), RangeEnd: now.Add(-24 * time.Hour),
		Status: "pending", MaxAttempts: 5, ErrorDetails: []byte(`{}`), CreatedAt: now, UpdatedAt: now,
	}
	return run, task
}
