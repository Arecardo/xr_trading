package application

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"

	"xr-trading/market-info-service/internal/domain"
	"xr-trading/market-info-service/internal/ingestion"
)

type ingestionQueryReaderStub struct {
	runs, listedRuns   []RunRecord
	tasks, listedTasks []TaskRecord
	run                RunRecord
	task               TaskRecord
	err                error
	runFilter          RunReadFilter
	taskFilter         TaskReadFilter
}

func (stub *ingestionQueryReaderStub) ListRunRecords(_ context.Context, filter RunReadFilter) ([]RunRecord, error) {
	stub.runFilter = filter
	return stub.runs, stub.err
}
func (stub *ingestionQueryReaderStub) GetRunRecord(context.Context, domain.ID) (RunRecord, error) {
	return stub.run, stub.err
}
func (stub *ingestionQueryReaderStub) ListTaskRecords(_ context.Context, filter TaskReadFilter) ([]TaskRecord, error) {
	stub.taskFilter = filter
	return stub.tasks, stub.err
}
func (stub *ingestionQueryReaderStub) GetTaskRecord(context.Context, domain.ID) (TaskRecord, error) {
	return stub.task, stub.err
}

func TestIngestionQueryServiceDerivesRunTruthAndPaginates(t *testing.T) {
	run, task := ingestionQueryFixtures(t)
	second := run
	second.Run.ID = domain.IDFromUUID(uuid.MustParse("019f1452-90f7-7992-a87a-ca2727897604"))
	second.Snapshot.RunID = second.Run.ID
	reader := &ingestionQueryReaderStub{runs: []RunRecord{run, second}, run: run, task: task}
	service, err := NewIngestionQueryService(reader)
	if err != nil {
		t.Fatal(err)
	}
	from := run.Run.CreatedAt.In(time.FixedZone("east", 8*60*60)).Add(-time.Hour)
	page, err := service.ListRuns(context.Background(), RunListInput{RunType: "backfill", TriggerType: "manual", Status: "running", RequestedBy: "admin@example.com", CreatedFrom: &from, Limit: 1})
	if err != nil || len(page.Items) != 1 || page.NextAfterID == nil || page.Items[0].Summary.Status != "running" || page.Items[0].Run.Status != "pending" {
		t.Fatalf("ListRuns() = (%#v, %v)", page, err)
	}
	if reader.runFilter.Limit != 2 || reader.runFilter.CreatedFrom.Location() != time.UTC || page.Items[0].Context["reason"] != "history" {
		t.Fatalf("filter=%#v record=%#v", reader.runFilter, page.Items[0])
	}
	if _, exists := page.Items[0].Context["database_url"]; exists {
		t.Fatalf("unsafe context = %#v", page.Items[0].Context)
	}
	loaded, err := service.GetRun(context.Background(), run.Run.ID)
	if err != nil || loaded.Summary.TaskCount != 2 {
		t.Fatalf("GetRun() = (%#v, %v)", loaded, err)
	}
}

func TestIngestionQueryServiceSanitizesTaskErrorsAndPaginates(t *testing.T) {
	_, task := ingestionQueryFixtures(t)
	second := task
	second.Task.ID = domain.IDFromUUID(uuid.MustParse("019f1452-90f7-7992-a87a-ca2727897605"))
	reader := &ingestionQueryReaderStub{tasks: []TaskRecord{task, second}, task: task}
	service, _ := NewIngestionQueryService(reader)
	page, err := service.ListTasks(context.Background(), TaskListInput{Status: "retry_wait", ProviderCode: "bybit", InstrumentCode: "instrument.bybit.spot.btc-usdt", Interval: "1h", Limit: 1})
	if err != nil || len(page.Items) != 1 || page.NextAfterID == nil || page.Items[0].ErrorSummary == nil || *page.Items[0].ErrorSummary != "provider network request failed" {
		t.Fatalf("ListTasks() = (%#v, %v)", page, err)
	}
	if page.Items[0].SafeErrorDetails["provider_code"] != "bybit" || len(page.Items[0].SafeErrorDetails) != 1 || reader.taskFilter.Limit != 2 {
		t.Fatalf("safe details/filter = %#v / %#v", page.Items[0].SafeErrorDetails, reader.taskFilter)
	}
	loaded, err := service.GetTask(context.Background(), task.Task.ID)
	if err != nil || loaded.ProviderCode.String() != "bybit" {
		t.Fatalf("GetTask() = (%#v, %v)", loaded, err)
	}
	task.Task.ErrorCode = stringPointer("unexpected_secret_error")
	task.Task.ErrorDetails = []byte(`{"provider_code":"BYBIT","token":"secret"}`)
	reader.task = task
	loaded, err = service.GetTask(context.Background(), task.Task.ID)
	if err != nil || loaded.Task.ErrorCode == nil || *loaded.Task.ErrorCode != "internal_error" || loaded.ErrorSummary == nil || *loaded.ErrorSummary != "ingestion task failed" || len(loaded.SafeErrorDetails) != 0 {
		t.Fatalf("sanitized unknown task = (%#v, %v)", loaded, err)
	}
}

func TestIngestionQueryServiceValidatesAndMapsFailures(t *testing.T) {
	if _, err := NewIngestionQueryService(nil); err == nil {
		t.Fatal("NewIngestionQueryService(nil) error = nil")
	}
	service, _ := NewIngestionQueryService(&ingestionQueryReaderStub{})
	badRunInputs := []RunListInput{{}, {RunType: "other", Limit: 1}, {TriggerType: "other", Limit: 1}, {Status: "retry_wait", Limit: 1}, {RequestedBy: " admin", Limit: 1}, {Limit: 101}}
	for _, input := range badRunInputs {
		if _, err := service.ListRuns(context.Background(), input); err == nil {
			t.Fatalf("ListRuns(%#v) error = nil", input)
		}
	}
	from, to := time.Now(), time.Now().Add(-time.Hour)
	badTaskInputs := []TaskListInput{{}, {Status: "partial", Limit: 1}, {ProviderCode: "BYBIT", Limit: 1}, {InstrumentCode: "asset.crypto.btc", Limit: 1}, {Interval: "5m", Limit: 1}, {CreatedFrom: &from, CreatedTo: &to, Limit: 1}}
	for _, input := range badTaskInputs {
		if _, err := service.ListTasks(context.Background(), input); err == nil {
			t.Fatalf("ListTasks(%#v) error = nil", input)
		}
	}
	if _, err := service.GetRun(nil, domain.ID{}); err == nil {
		t.Fatal("GetRun(invalid) error = nil")
	}
	if _, err := service.GetTask(nil, domain.ID{}); err == nil {
		t.Fatal("GetTask(invalid) error = nil")
	}
	for _, test := range []struct {
		err      error
		task     bool
		wantCode ErrorCode
	}{
		{domain.ErrNotFound, false, ErrorCodeNotFound}, {domain.ErrNotFound, true, ErrorCodeTaskNotFound},
		{errors.New("bad row"), false, ErrorCodeInternal},
	} {
		reader := &ingestionQueryReaderStub{err: test.err}
		service, _ := NewIngestionQueryService(reader)
		var err error
		if test.task {
			_, err = service.GetTask(context.Background(), ingestionQueryID("019f1452-90f7-7992-a87a-ca2727897601"))
		} else {
			_, err = service.GetRun(context.Background(), ingestionQueryID("019f1452-90f7-7992-a87a-ca2727897601"))
		}
		var appErr *Error
		if !errors.As(err, &appErr) || appErr.Code != test.wantCode {
			t.Fatalf("mapped error = %v", err)
		}
	}
}

func ingestionQueryFixtures(t *testing.T) (RunRecord, TaskRecord) {
	t.Helper()
	now := time.Date(2026, 7, 19, 12, 0, 0, 0, time.UTC)
	runID := ingestionQueryID("019f1452-90f7-7992-a87a-ca2727897601")
	taskID := ingestionQueryID("019f1452-90f7-7992-a87a-ca2727897602")
	subscriptionID := ingestionQueryID("019f1452-90f7-7992-a87a-ca2727897603")
	requestedBy := "admin@example.com"
	run := RunRecord{Run: domain.IngestionRun{ID: runID, RunKey: "backfill.manual.test", RunType: "backfill", TriggerType: "manual", Status: "pending", RequestedBy: &requestedBy, Context: []byte(`{"reason":"history","database_url":"postgres://secret"}`), CreatedAt: now}, Snapshot: ingestion.RunTaskSnapshot{RunID: runID, RunningCount: 1, SuccessCount: 1}}
	providerCode, _ := domain.ParseCode("bybit")
	instrumentCode, _ := domain.ParseCode("instrument.bybit.spot.btc-usdt")
	mappingCode, _ := domain.ParseCode("provider.bybit.spot.btcusdt")
	task := TaskRecord{Task: domain.IngestionTask{ID: taskID, RunID: runID, SubscriptionID: subscriptionID, RangeStart: now.Add(-2 * time.Hour), RangeEnd: now.Add(-time.Hour), Status: "retry_wait", AttemptCount: 1, MaxAttempts: 5, ErrorCode: stringPointer("network"), ErrorMessage: stringPointer("secret raw provider error"), ErrorDetails: []byte(`{"provider_code":"bybit","token":"secret"}`), CreatedAt: now, UpdatedAt: now}, RunType: "backfill", TriggerType: "manual", SubscriptionInterval: "1h", ProviderID: ingestionQueryID("019f1452-90f7-7992-a87a-ca2727897610"), ProviderCode: providerCode, InstrumentID: ingestionQueryID("019f1452-90f7-7992-a87a-ca2727897611"), InstrumentCode: instrumentCode, ProviderInstrumentID: ingestionQueryID("019f1452-90f7-7992-a87a-ca2727897612"), ProviderInstrumentCode: mappingCode, ProviderSymbol: "BTCUSDT"}
	return run, task
}

func ingestionQueryID(value string) domain.ID { return domain.IDFromUUID(uuid.MustParse(value)) }
func stringPointer(value string) *string      { return &value }
