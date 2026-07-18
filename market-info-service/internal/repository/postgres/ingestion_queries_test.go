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

	"xr-trading/market-info-service/internal/application"
	"xr-trading/market-info-service/internal/domain"
)

func TestIngestionQueryRepositoryListsAndGetsJoinedRecords(t *testing.T) {
	now := time.Date(2026, 7, 19, 12, 0, 0, 0, time.UTC)
	runID := uuid.MustParse("019f1452-90f7-7992-a87a-ca2727897701")
	taskID := uuid.MustParse("019f1452-90f7-7992-a87a-ca2727897702")
	var runSQL, taskSQL string
	database := fakeMarketDataDatabase{fakeCatalogDatabase: fakeCatalogDatabase{
		query: func(_ context.Context, query string, _ ...any) (pgx.Rows, error) {
			if strings.Contains(query, "run_records") {
				runSQL = query
				return &fakeRows{rows: []scanFunc{ingestionRunManagementRow(runID, now)}}, nil
			}
			taskSQL = query
			return &fakeRows{rows: []scanFunc{ingestionTaskManagementRow(taskID, runID, now)}}, nil
		},
		queryRow: func(_ context.Context, query string, _ ...any) pgx.Row {
			if strings.Contains(query, "run_records") {
				return ingestionRunManagementRow(runID, now)
			}
			return ingestionTaskManagementRow(taskID, runID, now)
		},
		exec: func(context.Context, string, ...any) (pgconn.CommandTag, error) { return pgconn.CommandTag{}, nil },
	}}
	repository, _ := newIngestionRepository(database)
	after := domain.IDFromUUID(uuid.MustParse("019f1452-90f7-7992-a87a-ca2727897799"))
	from, to := now.Add(-time.Hour), now.Add(time.Hour)
	runs, err := repository.ListRunRecords(context.Background(), application.RunReadFilter{RunType: "backfill", TriggerType: "manual", Status: "running", RequestedBy: "admin", CreatedFrom: &from, CreatedTo: &to, AfterID: &after, Limit: 2})
	if err != nil || len(runs) != 1 || runs[0].Run.ID != domain.IDFromUUID(runID) || runs[0].Snapshot.RunningCount != 1 {
		t.Fatalf("ListRunRecords() = (%#v, %v)", runs, err)
	}
	for _, fragment := range []string{"WITH run_records", "records.derived_status", "records.requested_by", "records.created_at >=", "records.created_at <", "records.id <", "ORDER BY records.id DESC", "LIMIT 2"} {
		if !strings.Contains(runSQL, fragment) {
			t.Fatalf("run SQL missing %q: %s", fragment, runSQL)
		}
	}
	run, err := repository.GetRunRecord(context.Background(), domain.IDFromUUID(runID))
	if err != nil || run.Run.RunKey != "backfill.manual.test" {
		t.Fatalf("GetRunRecord() = (%#v, %v)", run, err)
	}
	tasks, err := repository.ListTaskRecords(context.Background(), application.TaskReadFilter{RunID: &run.Run.ID, Status: "retry_wait", ProviderCode: "bybit", InstrumentCode: "instrument.bybit.spot.btc-usdt", Interval: "1h", CreatedFrom: &from, CreatedTo: &to, AfterID: &after, Limit: 2})
	if err != nil || len(tasks) != 1 || tasks[0].Task.ID != domain.IDFromUUID(taskID) || tasks[0].ProviderCode.String() != "bybit" {
		t.Fatalf("ListTaskRecords() = (%#v, %v)", tasks, err)
	}
	for _, fragment := range []string{"tasks.run_id", "tasks.status", "providers.code", "instruments.code", "subscriptions.interval", "tasks.created_at >=", "tasks.created_at <", "tasks.id <", "ORDER BY tasks.id DESC", "LIMIT 2"} {
		if !strings.Contains(taskSQL, fragment) {
			t.Fatalf("task SQL missing %q: %s", fragment, taskSQL)
		}
	}
	task, err := repository.GetTaskRecord(context.Background(), domain.IDFromUUID(taskID))
	if err != nil || task.ProviderInstrumentCode.String() != "provider.bybit.spot.btcusdt" || string(task.Task.ErrorDetails) != `{"provider_code":"bybit","token":"secret"}` {
		t.Fatalf("GetTaskRecord() = (%#v, %v)", task, err)
	}
}

func TestIngestionQueryRepositoryValidatesAndMapsFailures(t *testing.T) {
	want := &pgconn.PgError{Code: "08006"}
	database := fakeMarketDataDatabase{fakeCatalogDatabase: fakeCatalogDatabase{
		query: func(context.Context, string, ...any) (pgx.Rows, error) { return nil, want },
		queryRow: func(context.Context, string, ...any) pgx.Row {
			return scanFunc(func(...any) error { return want })
		},
		exec: func(context.Context, string, ...any) (pgconn.CommandTag, error) { return pgconn.CommandTag{}, want },
	}}
	repository, _ := newIngestionRepository(database)
	if _, err := repository.ListRunRecords(context.Background(), application.RunReadFilter{}); !errors.Is(err, domain.ErrInvalidData) {
		t.Fatalf("ListRunRecords(invalid) error = %v", err)
	}
	if _, err := repository.ListTaskRecords(context.Background(), application.TaskReadFilter{}); !errors.Is(err, domain.ErrInvalidData) {
		t.Fatalf("ListTaskRecords(invalid) error = %v", err)
	}
	id := domain.IDFromUUID(uuid.MustParse("019f1452-90f7-7992-a87a-ca2727897701"))
	if _, err := repository.ListRunRecords(context.Background(), application.RunReadFilter{Limit: 1}); !errors.Is(err, domain.ErrDatabaseUnavailable) {
		t.Fatalf("ListRunRecords(database) error = %v", err)
	}
	if _, err := repository.ListTaskRecords(context.Background(), application.TaskReadFilter{Limit: 1}); !errors.Is(err, domain.ErrDatabaseUnavailable) {
		t.Fatalf("ListTaskRecords(database) error = %v", err)
	}
	if _, err := repository.GetRunRecord(context.Background(), domain.ID{}); !errors.Is(err, domain.ErrInvalidData) {
		t.Fatalf("GetRunRecord(invalid) error = %v", err)
	}
	if _, err := repository.GetRunRecord(context.Background(), id); !errors.Is(err, domain.ErrDatabaseUnavailable) {
		t.Fatalf("GetRunRecord(database) error = %v", err)
	}
	if _, err := repository.GetTaskRecord(context.Background(), domain.ID{}); !errors.Is(err, domain.ErrInvalidData) {
		t.Fatalf("GetTaskRecord(invalid) error = %v", err)
	}
	if _, err := repository.GetTaskRecord(context.Background(), id); !errors.Is(err, domain.ErrDatabaseUnavailable) {
		t.Fatalf("GetTaskRecord(database) error = %v", err)
	}
}

func ingestionRunManagementRow(id uuid.UUID, now time.Time) scanFunc {
	return func(destinations ...any) error {
		*destinations[0].(*uuid.UUID) = id
		*destinations[1].(*string) = "backfill.manual.test"
		*destinations[2].(*string) = "backfill"
		*destinations[3].(*string) = "manual"
		*destinations[4].(*string) = "pending"
		*destinations[5].(**time.Time) = nil
		*destinations[6].(**time.Time) = nil
		*destinations[7].(**time.Time) = nil
		requestedBy := "admin"
		*destinations[8].(**string) = &requestedBy
		*destinations[9].(*int) = 2
		*destinations[10].(*int) = 0
		*destinations[11].(*int) = 0
		*destinations[12].(*[]byte) = []byte(`{"reason":"history"}`)
		*destinations[13].(*[]byte) = []byte(`{}`)
		*destinations[14].(*time.Time) = now
		*destinations[15].(*int) = 0
		*destinations[16].(*int) = 1
		*destinations[17].(*int) = 0
		*destinations[18].(*int) = 1
		*destinations[19].(*int) = 0
		*destinations[20].(*int) = 0
		*destinations[21].(**time.Time) = nil
		*destinations[22].(**time.Time) = nil
		return nil
	}
}

func ingestionTaskManagementRow(id, runID uuid.UUID, now time.Time) scanFunc {
	return func(destinations ...any) error {
		subscriptionID := uuid.MustParse("019f1452-90f7-7992-a87a-ca2727897703")
		providerID := uuid.MustParse("019f1452-90f7-7992-a87a-ca2727897710")
		instrumentID := uuid.MustParse("019f1452-90f7-7992-a87a-ca2727897711")
		mappingID := uuid.MustParse("019f1452-90f7-7992-a87a-ca2727897712")
		*destinations[0].(*uuid.UUID), *destinations[1].(*uuid.UUID), *destinations[2].(*uuid.UUID) = id, runID, subscriptionID
		*destinations[3].(**uuid.UUID) = nil
		*destinations[4].(*time.Time), *destinations[5].(*time.Time) = now.Add(-2*time.Hour), now.Add(-time.Hour)
		*destinations[6].(*string) = "retry_wait"
		*destinations[7].(*int), *destinations[8].(*int) = 1, 5
		next := now.Add(time.Minute)
		*destinations[9].(**time.Time) = &next
		*destinations[10].(**string), *destinations[11].(**time.Time), *destinations[12].(**time.Time), *destinations[13].(**time.Time), *destinations[14].(**string) = nil, nil, nil, nil, nil
		errorCode, errorMessage := "network", "safe message"
		*destinations[15].(**string), *destinations[16].(**string) = &errorCode, &errorMessage
		*destinations[17].(*[]byte) = []byte(`{"provider_code":"bybit","token":"secret"}`)
		*destinations[18].(**string), *destinations[19].(**string) = nil, nil
		*destinations[20].(*time.Time), *destinations[21].(*time.Time) = now, now
		*destinations[22].(*string), *destinations[23].(*string), *destinations[24].(*string) = "backfill", "manual", "1h"
		*destinations[25].(*uuid.UUID), *destinations[26].(*string) = providerID, "bybit"
		*destinations[27].(*uuid.UUID), *destinations[28].(*string) = instrumentID, "instrument.bybit.spot.btc-usdt"
		*destinations[29].(*uuid.UUID), *destinations[30].(*string), *destinations[31].(*string) = mappingID, "provider.bybit.spot.btcusdt", "BTCUSDT"
		return nil
	}
}
