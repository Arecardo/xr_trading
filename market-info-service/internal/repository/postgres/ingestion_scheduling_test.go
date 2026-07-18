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
	"xr-trading/market-info-service/internal/scheduler"
)

func TestListSchedulingTargetsPagesActiveContexts(t *testing.T) {
	fixture := newPostgresIngestionFixture(t)
	secondID := uuid.MustParse("019f1452-90f7-7992-a87a-ca2727894006")
	rows := &fakeRows{rows: []scanFunc{
		func(destinations ...any) error {
			*destinations[0].(*uuid.UUID) = fixture.subscriptionID.UUID()
			return nil
		},
		func(destinations ...any) error { *destinations[0].(*uuid.UUID) = secondID; return nil },
	}}
	var listQuery string
	database := fakeMarketDataDatabase{fakeCatalogDatabase: fakeCatalogDatabase{
		exec: func(context.Context, string, ...any) (pgconn.CommandTag, error) { return pgconn.CommandTag{}, nil },
		query: func(_ context.Context, query string, arguments ...any) (pgx.Rows, error) {
			listQuery = query
			if len(arguments) != 3 || arguments[0] != nil || arguments[1] != fixture.now || arguments[2] != 2 {
				t.Fatalf("list scheduling arguments = %#v", arguments)
			}
			return rows, nil
		},
		queryRow: func(_ context.Context, query string, _ ...any) pgx.Row {
			switch {
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
	page, err := repository.ListSchedulingTargets(context.Background(), nil, 1, fixture.now)
	if err != nil || len(page.Items) != 1 || page.Items[0].Subscription.ID != fixture.subscriptionID ||
		page.NextAfterID == nil || *page.NextAfterID != fixture.subscriptionID || !rows.closed {
		t.Fatalf("ListSchedulingTargets() = (%#v, %v), closed=%t", page, err, rows.closed)
	}
	for _, fragment := range []string{"subscriptions.enabled = true", "mappings.enabled = true", "providers.status IN", "instruments.status = 'active'", "assets.status = 'active'", "ORDER BY subscriptions.id ASC"} {
		if !strings.Contains(listQuery, fragment) {
			t.Fatalf("list query missing %q: %s", fragment, listQuery)
		}
	}
}

func TestListSchedulingTargetsValidatesAndMapsReadFailures(t *testing.T) {
	repository, _ := newIngestionRepository(fakeMarketDataDatabase{fakeCatalogDatabase: fakeCatalogDatabase{
		exec:     func(context.Context, string, ...any) (pgconn.CommandTag, error) { return pgconn.CommandTag{}, nil },
		queryRow: func(context.Context, string, ...any) pgx.Row { return nil },
		query: func(context.Context, string, ...any) (pgx.Rows, error) {
			return nil, &pgconn.PgError{Code: "08006"}
		},
	}, begin: func(context.Context) (marketDataTransaction, error) { return nil, errors.New("not used") }})
	if _, err := repository.ListSchedulingTargets(context.Background(), nil, 0, time.Now()); !errors.Is(err, domain.ErrInvalidData) {
		t.Fatalf("ListSchedulingTargets(invalid limit) error = %v", err)
	}
	zero := domain.ID{}
	if _, err := repository.ListSchedulingTargets(context.Background(), &zero, 1, time.Now()); !errors.Is(err, domain.ErrInvalidData) {
		t.Fatalf("ListSchedulingTargets(zero cursor) error = %v", err)
	}
	if _, err := repository.ListSchedulingTargets(context.Background(), nil, 1, time.Now()); !errors.Is(err, domain.ErrDatabaseUnavailable) {
		t.Fatalf("ListSchedulingTargets(database) error = %v", err)
	}

	badRows := &fakeRows{rows: []scanFunc{func(...any) error { return errors.New("scan failed") }}}
	repository, _ = newIngestionRepository(schedulingListDatabase(badRows))
	if _, err := repository.ListSchedulingTargets(context.Background(), nil, 1, time.Now()); err == nil || !strings.Contains(err.Error(), "scan scheduling target ID") {
		t.Fatalf("ListSchedulingTargets(scan) error = %v", err)
	}
	iterationRows := &fakeRows{err: &pgconn.PgError{Code: "08006"}}
	repository, _ = newIngestionRepository(schedulingListDatabase(iterationRows))
	if _, err := repository.ListSchedulingTargets(context.Background(), nil, 1, time.Now()); !errors.Is(err, domain.ErrDatabaseUnavailable) {
		t.Fatalf("ListSchedulingTargets(iteration) error = %v", err)
	}
}

func TestLoadSchedulingCheckpointReturnsOptionalHint(t *testing.T) {
	fixture := newPostgresIngestionFixture(t)
	lastClosed := fixture.now.Add(-time.Hour)
	database := fakeMarketDataDatabase{fakeCatalogDatabase: fakeCatalogDatabase{
		queryRow: func(_ context.Context, query string, arguments ...any) pgx.Row {
			if !strings.Contains(query, "ingestion_checkpoints") || len(arguments) != 1 || arguments[0] != fixture.subscriptionID.UUID() {
				t.Fatalf("checkpoint query=%q arguments=%#v", query, arguments)
			}
			return scanFunc(func(destinations ...any) error {
				*destinations[0].(**time.Time) = &lastClosed
				*destinations[1].(**time.Time) = &lastClosed
				*destinations[2].(**time.Time) = nil
				*destinations[3].(**time.Time) = &fixture.now
				*destinations[4].(*int) = 0
				*destinations[5].(*time.Time) = fixture.now
				return nil
			})
		},
	}, begin: func(context.Context) (marketDataTransaction, error) { return nil, errors.New("not used") }}
	repository, _ := newIngestionRepository(database)
	checkpoint, err := repository.LoadSchedulingCheckpoint(context.Background(), fixture.subscriptionID)
	if err != nil || checkpoint == nil || checkpoint.SubscriptionID != fixture.subscriptionID || checkpoint.LastClosedOpenTime == nil || !checkpoint.LastClosedOpenTime.Equal(lastClosed) {
		t.Fatalf("LoadSchedulingCheckpoint() = (%#v, %v)", checkpoint, err)
	}

	database.fakeCatalogDatabase.queryRow = func(context.Context, string, ...any) pgx.Row {
		return scanFunc(func(...any) error { return pgx.ErrNoRows })
	}
	repository, _ = newIngestionRepository(database)
	if checkpoint, err := repository.LoadSchedulingCheckpoint(context.Background(), fixture.subscriptionID); err != nil || checkpoint != nil {
		t.Fatalf("LoadSchedulingCheckpoint(missing) = (%#v, %v)", checkpoint, err)
	}
	if _, err := repository.LoadSchedulingCheckpoint(context.Background(), domain.ID{}); !errors.Is(err, domain.ErrInvalidData) {
		t.Fatalf("LoadSchedulingCheckpoint(invalid) error = %v", err)
	}

	database.fakeCatalogDatabase.queryRow = func(context.Context, string, ...any) pgx.Row {
		return scanFunc(func(...any) error { return &pgconn.PgError{Code: "08006"} })
	}
	repository, _ = newIngestionRepository(database)
	if _, err := repository.LoadSchedulingCheckpoint(context.Background(), fixture.subscriptionID); !errors.Is(err, domain.ErrDatabaseUnavailable) {
		t.Fatalf("LoadSchedulingCheckpoint(database) error = %v", err)
	}
}

func TestListClosedBarOpenTimesUsesCurrentClosedFacts(t *testing.T) {
	fixture := newPostgresIngestionFixture(t)
	target := postgresSchedulingTarget(t, fixture)
	start, end := fixture.now.Add(-2*time.Hour), fixture.now
	first, second := start, start.Add(time.Hour)
	rows := &fakeRows{rows: []scanFunc{
		func(destinations ...any) error { *destinations[0].(*time.Time) = first; return nil },
		func(destinations ...any) error { *destinations[0].(*time.Time) = second; return nil },
	}}
	var query string
	database := fakeMarketDataDatabase{fakeCatalogDatabase: fakeCatalogDatabase{
		query: func(_ context.Context, statement string, arguments ...any) (pgx.Rows, error) {
			query = statement
			if len(arguments) != 4 || arguments[0] != target.Subscription.ProviderInstrumentID.UUID() || arguments[1] != "1h" || arguments[2] != start || arguments[3] != end {
				t.Fatalf("closed-bar arguments = %#v", arguments)
			}
			return rows, nil
		},
	}, begin: func(context.Context) (marketDataTransaction, error) { return nil, errors.New("not used") }}
	repository, _ := newIngestionRepository(database)
	values, err := repository.ListClosedBarOpenTimes(context.Background(), target, start, end)
	if err != nil || len(values) != 2 || values[0] != first || values[1] != second || !rows.closed {
		t.Fatalf("ListClosedBarOpenTimes() = (%#v, %v), closed=%t", values, err, rows.closed)
	}
	for _, fragment := range []string{"is_current = true", "is_closed = true", "quality_status IN ('valid', 'warning')", "ORDER BY open_time ASC"} {
		if !strings.Contains(query, fragment) {
			t.Fatalf("closed-bar query missing %q: %s", fragment, query)
		}
	}
}

func TestListClosedBarOpenTimesValidatesAndMapsFailures(t *testing.T) {
	fixture := newPostgresIngestionFixture(t)
	target := postgresSchedulingTarget(t, fixture)
	start, end := fixture.now.Add(-time.Hour), fixture.now
	repository, _ := newIngestionRepository(schedulingListDatabase(&fakeRows{}))
	if _, err := repository.ListClosedBarOpenTimes(context.Background(), scheduler.SchedulingTarget{}, start, end); !errors.Is(err, domain.ErrInvalidData) {
		t.Fatalf("ListClosedBarOpenTimes(invalid target) error = %v", err)
	}
	if _, err := repository.ListClosedBarOpenTimes(context.Background(), target, end, start); !errors.Is(err, domain.ErrInvalidData) {
		t.Fatalf("ListClosedBarOpenTimes(invalid range) error = %v", err)
	}

	database := schedulingListDatabase(nil)
	database.fakeCatalogDatabase.query = func(context.Context, string, ...any) (pgx.Rows, error) {
		return nil, &pgconn.PgError{Code: "08006"}
	}
	repository, _ = newIngestionRepository(database)
	if _, err := repository.ListClosedBarOpenTimes(context.Background(), target, start, end); !errors.Is(err, domain.ErrDatabaseUnavailable) {
		t.Fatalf("ListClosedBarOpenTimes(query) error = %v", err)
	}

	repository, _ = newIngestionRepository(schedulingListDatabase(&fakeRows{rows: []scanFunc{func(...any) error { return errors.New("scan") }}}))
	if _, err := repository.ListClosedBarOpenTimes(context.Background(), target, start, end); err == nil || !strings.Contains(err.Error(), "scan closed bar") {
		t.Fatalf("ListClosedBarOpenTimes(scan) error = %v", err)
	}
	repository, _ = newIngestionRepository(schedulingListDatabase(&fakeRows{err: &pgconn.PgError{Code: "08006"}}))
	if _, err := repository.ListClosedBarOpenTimes(context.Background(), target, start, end); !errors.Is(err, domain.ErrDatabaseUnavailable) {
		t.Fatalf("ListClosedBarOpenTimes(iteration) error = %v", err)
	}
}

func TestCreateScheduledBatchCommitsRunAndTask(t *testing.T) {
	batch := postgresScheduledBatch(t)
	var executions []string
	tx := &fakeMarketDataTransaction{
		queryRow: func(context.Context, string, ...any) pgx.Row { return boolRow(true) },
		query:    func(context.Context, string, ...any) (pgx.Rows, error) { return nil, nil },
		exec: func(_ context.Context, query string, _ ...any) (pgconn.CommandTag, error) {
			executions = append(executions, query)
			return pgconn.NewCommandTag("INSERT 0 1"), nil
		},
	}
	repository, _ := newIngestionRepository(ingestionFakeDatabase(tx))
	created, err := repository.CreateScheduledBatch(context.Background(), batch)
	if err != nil || !created || !tx.committed || !tx.rolledBack || len(executions) != 2 ||
		!strings.Contains(executions[0], "ON CONFLICT (run_key) DO NOTHING") || !strings.Contains(executions[1], "ingestion_tasks") {
		t.Fatalf("CreateScheduledBatch() = (%t, %v), commit=%t rollback=%t executions=%#v", created, err, tx.committed, tx.rolledBack, executions)
	}
}

func TestCreateScheduledBatchTreatsOnlyEquivalentKeyAsIdempotent(t *testing.T) {
	batch := postgresScheduledBatch(t)
	for _, test := range []struct {
		name       string
		taskCount  int
		rangeStart time.Time
		want       error
	}{
		{"equivalent", 1, batch.Task.RangeStart, nil},
		{"different range", 1, batch.Task.RangeStart.Add(-time.Hour), domain.ErrConflict},
		{"multiple tasks", 2, batch.Task.RangeStart, domain.ErrConflict},
	} {
		t.Run(test.name, func(t *testing.T) {
			tx := &fakeMarketDataTransaction{
				query: func(context.Context, string, ...any) (pgx.Rows, error) { return nil, nil },
				exec: func(context.Context, string, ...any) (pgconn.CommandTag, error) {
					return pgconn.NewCommandTag("INSERT 0 0"), nil
				},
				queryRow: func(_ context.Context, query string, _ ...any) pgx.Row {
					if strings.Contains(query, "SELECT enabled") {
						return boolRow(true)
					}
					return equivalentBatchRow(batch, test.rangeStart, test.taskCount)
				},
			}
			repository, _ := newIngestionRepository(ingestionFakeDatabase(tx))
			created, err := repository.CreateScheduledBatch(context.Background(), batch)
			if created || (test.want == nil && err != nil) || (test.want != nil && !errors.Is(err, test.want)) || tx.committed || !tx.rolledBack {
				t.Fatalf("CreateScheduledBatch() = (%t, %v), commit=%t rollback=%t", created, err, tx.committed, tx.rolledBack)
			}
		})
	}
}

func TestCreateScheduledBatchValidatesRechecksAndRollsBackFailures(t *testing.T) {
	batch := postgresScheduledBatch(t)
	repository, _ := newIngestionRepository(ingestionFakeDatabase(&fakeMarketDataTransaction{}))
	invalid := batch
	invalid.Run.RunKey = "wrong"
	if _, err := repository.CreateScheduledBatch(context.Background(), invalid); !errors.Is(err, domain.ErrInvalidData) {
		t.Fatalf("CreateScheduledBatch(invalid) error = %v", err)
	}

	tx := &fakeMarketDataTransaction{
		queryRow: func(context.Context, string, ...any) pgx.Row { return boolRow(false) },
		query:    func(context.Context, string, ...any) (pgx.Rows, error) { return nil, nil },
		exec:     func(context.Context, string, ...any) (pgconn.CommandTag, error) { return pgconn.CommandTag{}, nil },
	}
	repository, _ = newIngestionRepository(ingestionFakeDatabase(tx))
	if _, err := repository.CreateScheduledBatch(context.Background(), batch); !errors.Is(err, domain.ErrInvalidState) || !tx.rolledBack {
		t.Fatalf("CreateScheduledBatch(disabled) error = %v, rollback=%t", err, tx.rolledBack)
	}

	want := &pgconn.PgError{Code: "08006"}
	repository, _ = newIngestionRepository(fakeMarketDataDatabase{fakeCatalogDatabase: fakeCatalogDatabase{}, begin: func(context.Context) (marketDataTransaction, error) { return nil, want }})
	if _, err := repository.CreateScheduledBatch(context.Background(), batch); !errors.Is(err, domain.ErrDatabaseUnavailable) {
		t.Fatalf("CreateScheduledBatch(begin) error = %v", err)
	}

	execCount := 0
	tx = &fakeMarketDataTransaction{
		queryRow: func(context.Context, string, ...any) pgx.Row { return boolRow(true) },
		query:    func(context.Context, string, ...any) (pgx.Rows, error) { return nil, nil },
		exec: func(context.Context, string, ...any) (pgconn.CommandTag, error) {
			execCount++
			if execCount == 2 {
				return pgconn.CommandTag{}, want
			}
			return pgconn.NewCommandTag("INSERT 0 1"), nil
		},
	}
	repository, _ = newIngestionRepository(ingestionFakeDatabase(tx))
	if _, err := repository.CreateScheduledBatch(context.Background(), batch); !errors.Is(err, domain.ErrDatabaseUnavailable) || tx.committed || !tx.rolledBack {
		t.Fatalf("CreateScheduledBatch(task write) error = %v, commit=%t rollback=%t", err, tx.committed, tx.rolledBack)
	}

	tx = &fakeMarketDataTransaction{
		queryRow: func(context.Context, string, ...any) pgx.Row { return boolRow(true) },
		query:    func(context.Context, string, ...any) (pgx.Rows, error) { return nil, nil },
		exec: func(context.Context, string, ...any) (pgconn.CommandTag, error) {
			return pgconn.NewCommandTag("INSERT 0 1"), nil
		},
		commit: func(context.Context) error { return want },
	}
	repository, _ = newIngestionRepository(ingestionFakeDatabase(tx))
	if _, err := repository.CreateScheduledBatch(context.Background(), batch); !errors.Is(err, domain.ErrDatabaseUnavailable) || !tx.rolledBack {
		t.Fatalf("CreateScheduledBatch(commit) error = %v", err)
	}
}

func schedulingListDatabase(rows pgx.Rows) fakeMarketDataDatabase {
	return fakeMarketDataDatabase{fakeCatalogDatabase: fakeCatalogDatabase{
		exec:     func(context.Context, string, ...any) (pgconn.CommandTag, error) { return pgconn.CommandTag{}, nil },
		queryRow: func(context.Context, string, ...any) pgx.Row { return nil },
		query:    func(context.Context, string, ...any) (pgx.Rows, error) { return rows, nil },
	}, begin: func(context.Context) (marketDataTransaction, error) { return nil, errors.New("not used") }}
}

func postgresSchedulingTarget(t *testing.T, fixture postgresIngestionFixture) scheduler.SchedulingTarget {
	t.Helper()
	providerCode, err := domain.ParseCode("bybit")
	if err != nil {
		t.Fatalf("ParseCode(provider) error = %v", err)
	}
	instrumentCode, err := domain.ParseCode("instrument.test.scheduler")
	if err != nil {
		t.Fatalf("ParseCode(instrument) error = %v", err)
	}
	return scheduler.SchedulingTarget{
		Subscription: domain.CollectionSubscription{
			ID: fixture.subscriptionID, ProviderInstrumentID: fixture.mappingID, Interval: "1h", Enabled: true,
			CloseDelaySeconds: 120, CreatedAt: fixture.now.Add(-time.Hour), UpdatedAt: fixture.now,
		},
		ProviderCode: providerCode, ProviderMarket: "spot", InstrumentCode: instrumentCode,
		AssetType: domain.AssetTypeCrypto, InstrumentType: domain.InstrumentTypeSpot,
		Capabilities: domain.ProviderCapabilities{Historical: true, Intervals: []domain.BarInterval{domain.BarInterval1Hour}},
	}
}

func postgresScheduledBatch(t *testing.T) scheduler.ScheduledBatch {
	t.Helper()
	createdAt := time.Date(2026, time.July, 18, 12, 2, 0, 0, time.UTC)
	scheduledAt := createdAt
	start, end := createdAt.Add(-time.Hour-2*time.Minute), createdAt.Add(-2*time.Minute)
	runID := domain.IDFromUUID(uuid.MustParse("019f1452-90f7-7992-a87a-ca2727896101"))
	taskID := domain.IDFromUUID(uuid.MustParse("019f1452-90f7-7992-a87a-ca2727896102"))
	subscriptionID := domain.IDFromUUID(uuid.MustParse("019f1452-90f7-7992-a87a-ca2727896103"))
	run := domain.IngestionRun{
		ID: runID, RunKey: scheduler.StableRunKey(scheduler.WindowTriggerClose, subscriptionID, start, end),
		RunType: "incremental", TriggerType: "scheduler", Status: "pending", ScheduledAt: &scheduledAt,
		TaskCount: 1, Context: []byte(`{"trigger":"close"}`), ErrorSummary: []byte(`{}`), CreatedAt: createdAt,
	}
	task := domain.IngestionTask{
		ID: taskID, RunID: runID, SubscriptionID: subscriptionID, RangeStart: start, RangeEnd: end,
		Status: "pending", MaxAttempts: 5, ErrorDetails: []byte(`{}`), CreatedAt: createdAt, UpdatedAt: createdAt,
	}
	batch := scheduler.ScheduledBatch{Trigger: scheduler.WindowTriggerClose, Run: run, Task: task}
	if err := batch.Validate(); err != nil {
		t.Fatalf("fixture batch.Validate() error = %v", err)
	}
	return batch
}

func boolRow(value bool) scanFunc {
	return func(destinations ...any) error { *destinations[0].(*bool) = value; return nil }
}

func equivalentBatchRow(batch scheduler.ScheduledBatch, rangeStart time.Time, taskCount int) scanFunc {
	return func(destinations ...any) error {
		*destinations[0].(*string) = batch.Run.RunType
		*destinations[1].(*string) = batch.Run.TriggerType
		*destinations[2].(*time.Time) = *batch.Run.ScheduledAt
		*destinations[3].(*uuid.UUID) = batch.Task.SubscriptionID.UUID()
		*destinations[4].(*time.Time) = rangeStart
		*destinations[5].(*time.Time) = batch.Task.RangeEnd
		*destinations[6].(*int) = taskCount
		return nil
	}
}
