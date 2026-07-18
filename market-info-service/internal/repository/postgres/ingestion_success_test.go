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
	"github.com/shopspring/decimal"

	"xr-trading/market-info-service/internal/domain"
	"xr-trading/market-info-service/internal/ingestion"
)

func TestLoadExecutionContext(t *testing.T) {
	fixture := newPostgresIngestionFixture(t)
	database := fakeMarketDataDatabase{fakeCatalogDatabase: fakeCatalogDatabase{
		exec:  func(context.Context, string, ...any) (pgconn.CommandTag, error) { return pgconn.CommandTag{}, nil },
		query: func(context.Context, string, ...any) (pgx.Rows, error) { return nil, nil },
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
	execution, err := repository.LoadExecutionContext(context.Background(), fixture.subscriptionID)
	if err != nil {
		t.Fatalf("LoadExecutionContext() error = %v", err)
	}
	if execution.Subscription.ID != fixture.subscriptionID || execution.Asset.ID != fixture.assetID || execution.Instrument.ID != fixture.instrumentID || execution.Provider.ID != fixture.providerID || execution.ProviderInstrument.ID != fixture.mappingID {
		t.Fatalf("LoadExecutionContext() = %#v", execution)
	}
	if _, err := execution.Reference(); err != nil {
		t.Fatalf("Reference() error = %v", err)
	}
	if _, err := repository.LoadExecutionContext(context.Background(), domain.ID{}); !errors.Is(err, domain.ErrInvalidData) {
		t.Fatalf("LoadExecutionContext(zero) error = %v", err)
	}
}

func TestLoadExecutionContextMapsReadFailures(t *testing.T) {
	fixture := newPostgresIngestionFixture(t)
	database := fakeMarketDataDatabase{fakeCatalogDatabase: fakeCatalogDatabase{
		exec:  func(context.Context, string, ...any) (pgconn.CommandTag, error) { return pgconn.CommandTag{}, nil },
		query: func(context.Context, string, ...any) (pgx.Rows, error) { return nil, nil },
		queryRow: func(context.Context, string, ...any) pgx.Row {
			return scanFunc(func(...any) error { return pgx.ErrNoRows })
		},
	}, begin: func(context.Context) (marketDataTransaction, error) { return nil, errors.New("not used") }}
	repository, _ := newIngestionRepository(database)
	if _, err := repository.LoadExecutionContext(context.Background(), fixture.subscriptionID); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("LoadExecutionContext(missing) error = %v", err)
	}
}

func TestCommitSuccessWritesCompleteAtomicSet(t *testing.T) {
	fixture := newPostgresIngestionFixture(t)
	request := fixture.successCommit(t)
	var executions []string
	tx := &fakeMarketDataTransaction{
		query: func(context.Context, string, ...any) (pgx.Rows, error) { return nil, nil },
		queryRow: func(_ context.Context, query string, _ ...any) pgx.Row {
			switch {
			case strings.Contains(query, "SELECT tasks.status"):
				return ingestionLeaseRow(request.Claim.Task)
			case strings.Contains(query, "FROM market_data.market_bars"):
				return scanFunc(func(...any) error { return pgx.ErrNoRows })
			case strings.Contains(query, "INSERT INTO market_data.data_quality_issues"):
				return scanFunc(func(destinations ...any) error {
					*destinations[0].(*uuid.UUID) = request.Issues[0].ID.UUID()
					return nil
				})
			default:
				return scanFunc(func(...any) error { return errors.New("unexpected query") })
			}
		},
		exec: func(_ context.Context, query string, _ ...any) (pgconn.CommandTag, error) {
			executions = append(executions, query)
			return pgconn.NewCommandTag("UPDATE 1"), nil
		},
	}
	database := ingestionFakeDatabase(tx)
	repository, _ := newIngestionRepository(database)
	if err := repository.CommitSuccess(context.Background(), request); err != nil {
		t.Fatalf("CommitSuccess() error = %v", err)
	}
	if !tx.committed || !tx.rolledBack || len(executions) != 3 {
		t.Fatalf("transaction committed=%t rolledBack=%t executions=%d", tx.committed, tx.rolledBack, len(executions))
	}
	if !strings.Contains(executions[0], "INSERT INTO market_data.market_bars") || !strings.Contains(executions[1], "ingestion_checkpoints") || !strings.Contains(executions[2], "status = 'success'") {
		t.Fatalf("transaction execution order = %#v", executions)
	}
}

func TestCommitSuccessRejectsLostLeaseBeforeWrites(t *testing.T) {
	fixture := newPostgresIngestionFixture(t)
	request := fixture.successCommit(t)
	stale := request.Claim.Task
	stale.AttemptCount++
	tx := &fakeMarketDataTransaction{
		query:    func(context.Context, string, ...any) (pgx.Rows, error) { return nil, nil },
		queryRow: func(context.Context, string, ...any) pgx.Row { return ingestionLeaseRow(stale) },
		exec: func(context.Context, string, ...any) (pgconn.CommandTag, error) {
			t.Fatal("write executed after lease mismatch")
			return pgconn.CommandTag{}, nil
		},
	}
	repository, _ := newIngestionRepository(ingestionFakeDatabase(tx))
	if err := repository.CommitSuccess(context.Background(), request); !errors.Is(err, ingestion.ErrTaskLeaseLost) {
		t.Fatalf("CommitSuccess(stale) error = %v", err)
	}
	if tx.committed || !tx.rolledBack {
		t.Fatalf("transaction committed=%t rolledBack=%t", tx.committed, tx.rolledBack)
	}
}

func TestCommitSuccessRollsBackEveryPartialFailure(t *testing.T) {
	fixture := newPostgresIngestionFixture(t)
	request := fixture.successCommit(t)
	databaseFailure := &pgconn.PgError{Code: "08006"}
	for _, failureAt := range []string{"begin", "bar", "checkpoint", "task", "commit"} {
		t.Run(failureAt, func(t *testing.T) {
			tx := &fakeMarketDataTransaction{
				query: func(context.Context, string, ...any) (pgx.Rows, error) { return nil, nil },
				queryRow: func(_ context.Context, query string, _ ...any) pgx.Row {
					if strings.Contains(query, "SELECT tasks.status") {
						return ingestionLeaseRow(request.Claim.Task)
					}
					if strings.Contains(query, "market_bars") {
						return scanFunc(func(...any) error { return pgx.ErrNoRows })
					}
					return scanFunc(func(destinations ...any) error {
						*destinations[0].(*uuid.UUID) = request.Issues[0].ID.UUID()
						return nil
					})
				},
			}
			tx.exec = func(_ context.Context, query string, _ ...any) (pgconn.CommandTag, error) {
				switch {
				case failureAt == "bar" && strings.Contains(query, "market_bars"):
					return pgconn.CommandTag{}, databaseFailure
				case failureAt == "checkpoint" && strings.Contains(query, "ingestion_checkpoints"):
					return pgconn.CommandTag{}, databaseFailure
				case failureAt == "task" && strings.Contains(query, "ingestion_tasks"):
					return pgconn.NewCommandTag("UPDATE 0"), nil
				default:
					return pgconn.NewCommandTag("UPDATE 1"), nil
				}
			}
			if failureAt == "commit" {
				txCommit := errors.New("commit failed")
				tx.commit = func(context.Context) error { return txCommit }
			}
			database := ingestionFakeDatabase(tx)
			if failureAt == "begin" {
				database.begin = func(context.Context) (marketDataTransaction, error) { return nil, databaseFailure }
			}
			repository, _ := newIngestionRepository(database)
			err := repository.CommitSuccess(context.Background(), request)
			if err == nil {
				t.Fatal("CommitSuccess() error = nil")
			}
			if failureAt != "begin" && !tx.rolledBack {
				t.Fatal("transaction was not rolled back")
			}
		})
	}
}

func TestCommitSuccessValidatesInput(t *testing.T) {
	fixture := newPostgresIngestionFixture(t)
	repository, _ := newIngestionRepository(ingestionFakeDatabase(&fakeMarketDataTransaction{}))
	request := fixture.successCommit(t)
	request.Claim.Task.LockedBy = nil
	if err := repository.CommitSuccess(context.Background(), request); !errors.Is(err, domain.ErrInvalidData) {
		t.Fatalf("CommitSuccess(invalid claim) error = %v", err)
	}
	request = fixture.successCommit(t)
	request.Bars = append(request.Bars, request.Bars[0])
	if err := repository.CommitSuccess(context.Background(), request); !errors.Is(err, domain.ErrInvalidData) {
		t.Fatalf("CommitSuccess(duplicate bar) error = %v", err)
	}
	request = fixture.successCommit(t)
	request.Issues[0].ID = domain.ID{}
	if err := repository.CommitSuccess(context.Background(), request); !errors.Is(err, domain.ErrInvalidData) {
		t.Fatalf("CommitSuccess(invalid issue) error = %v", err)
	}
}

type postgresIngestionFixture struct {
	now            time.Time
	assetID        domain.ID
	instrumentID   domain.ID
	providerID     domain.ID
	mappingID      domain.ID
	subscriptionID domain.ID
}

func newPostgresIngestionFixture(t *testing.T) postgresIngestionFixture {
	t.Helper()
	return postgresIngestionFixture{
		now:            time.Date(2026, time.July, 16, 8, 0, 0, 0, time.UTC),
		assetID:        domain.IDFromUUID(uuid.MustParse("019f1452-90f7-7992-a87a-ca2727894001")),
		instrumentID:   domain.IDFromUUID(uuid.MustParse("019f1452-90f7-7992-a87a-ca2727894002")),
		providerID:     domain.IDFromUUID(uuid.MustParse("019f1452-90f7-7992-a87a-ca2727894003")),
		mappingID:      domain.IDFromUUID(uuid.MustParse("019f1452-90f7-7992-a87a-ca2727894004")),
		subscriptionID: domain.IDFromUUID(uuid.MustParse("019f1452-90f7-7992-a87a-ca2727894005")),
	}
}

func (fixture postgresIngestionFixture) successCommit(t *testing.T) ingestion.SuccessCommit {
	t.Helper()
	workerID := "worker-postgres"
	lockedUntil := fixture.now.Add(time.Minute)
	bar := testBar(t, fixture.now.Add(-time.Hour))
	bar.InstrumentID = fixture.instrumentID
	bar.ProviderInstrumentID = fixture.mappingID
	interval := "1h"
	openTime := bar.OpenTime.Time()
	providerInstrumentID := fixture.mappingID
	issue := domain.DataQualityIssue{
		ID: domain.IDFromUUID(uuid.MustParse("019f1452-90f7-7992-a87a-ca2727894008")), InstrumentID: fixture.instrumentID,
		ProviderInstrumentID: &providerInstrumentID, Interval: &interval, OpenTime: &openTime, RuleCode: "fixture_warning",
		Severity: "warning", Summary: "fixture warning", DetectedAt: fixture.now, CreatedAt: fixture.now, UpdatedAt: fixture.now,
	}
	lastAttempt := fixture.now
	lastOpen := bar.OpenTime.Time()
	claim := domain.TaskClaim{Task: domain.IngestionTask{
		ID:    domain.IDFromUUID(uuid.MustParse("019f1452-90f7-7992-a87a-ca2727894006")),
		RunID: domain.IDFromUUID(uuid.MustParse("019f1452-90f7-7992-a87a-ca2727894007")), SubscriptionID: fixture.subscriptionID,
		RangeStart: fixture.now.Add(-time.Hour), RangeEnd: fixture.now, Status: "running", AttemptCount: 1, MaxAttempts: 5,
		LockedBy: &workerID, LockedUntil: &lockedUntil,
	}}
	return ingestion.SuccessCommit{
		Claim: claim, Bars: []domain.MarketBar{bar}, Issues: []domain.DataQualityIssue{issue}, FinishedAt: fixture.now,
		Checkpoint: domain.IngestionCheckpoint{SubscriptionID: fixture.subscriptionID, LastSuccessOpenTime: &lastOpen, LastClosedOpenTime: &lastOpen, LastAttemptAt: &lastAttempt, LastSuccessAt: &lastAttempt, UpdatedAt: fixture.now},
	}
}

func ingestionFakeDatabase(tx *fakeMarketDataTransaction) fakeMarketDataDatabase {
	return fakeMarketDataDatabase{fakeCatalogDatabase: fakeCatalogDatabase{
		exec:     func(context.Context, string, ...any) (pgconn.CommandTag, error) { return pgconn.CommandTag{}, nil },
		queryRow: func(context.Context, string, ...any) pgx.Row { return nil },
		query:    func(context.Context, string, ...any) (pgx.Rows, error) { return nil, nil },
	}, begin: func(context.Context) (marketDataTransaction, error) { return tx, nil }}
}

func ingestionLeaseRow(task domain.IngestionTask) scanFunc {
	return func(destinations ...any) error {
		*destinations[0].(*string) = task.Status
		*destinations[1].(*int) = task.AttemptCount
		*destinations[2].(**string) = task.LockedBy
		*destinations[3].(**time.Time) = task.LockedUntil
		*destinations[4].(*uuid.UUID) = task.SubscriptionID.UUID()
		*destinations[5].(*string) = "1h"
		*destinations[6].(*uuid.UUID) = uuid.MustParse("019f1452-90f7-7992-a87a-ca2727894004")
		*destinations[7].(*uuid.UUID) = uuid.MustParse("019f1452-90f7-7992-a87a-ca2727894002")
		return nil
	}
}

func ingestionSubscriptionRow(fixture postgresIngestionFixture) scanFunc {
	return subscriptionRow(fixture.subscriptionID.UUID(), fixture.mappingID.UUID(), fixture.now, true)
}

func ingestionMappingRow(fixture postgresIngestionFixture) scanFunc {
	return func(destinations ...any) error {
		*destinations[0].(*uuid.UUID) = fixture.mappingID.UUID()
		*destinations[1].(*string) = "provider.bybit.spot.btcusdt"
		*destinations[2].(*uuid.UUID) = fixture.providerID.UUID()
		*destinations[3].(*uuid.UUID) = fixture.instrumentID.UUID()
		*destinations[4].(*string) = "BTCUSDT"
		*destinations[5].(*string) = "spot"
		*destinations[6].(*[]byte) = []byte(`{"quote":true,"historical":true,"intervals":["1h"]}`)
		*destinations[7].(*int16) = 1
		*destinations[8].(*bool) = true
		*destinations[9].(*bool) = true
		*destinations[10].(**time.Time) = nil
		*destinations[11].(**time.Time) = nil
		*destinations[12].(*[]byte) = []byte(`{}`)
		*destinations[13].(*time.Time) = fixture.now
		*destinations[14].(*time.Time) = fixture.now
		return nil
	}
}

func ingestionProviderRow(fixture postgresIngestionFixture) scanFunc {
	return func(destinations ...any) error {
		*destinations[0].(*uuid.UUID) = fixture.providerID.UUID()
		*destinations[1].(*string) = "bybit"
		*destinations[2].(*string) = "Bybit"
		*destinations[3].(*string) = "EXCHANGE"
		*destinations[4].(*string) = "active"
		*destinations[5].(*[]byte) = []byte(`{}`)
		*destinations[6].(*time.Time) = fixture.now
		*destinations[7].(*time.Time) = fixture.now
		return nil
	}
}

func ingestionInstrumentRow(fixture postgresIngestionFixture) scanFunc {
	return func(destinations ...any) error {
		*destinations[0].(*uuid.UUID) = fixture.instrumentID.UUID()
		*destinations[1].(*string) = "instrument.bybit.spot.btc-usdt"
		*destinations[2].(*uuid.UUID) = fixture.assetID.UUID()
		*destinations[3].(*string) = "BYBIT"
		*destinations[4].(*string) = "SPOT"
		*destinations[5].(*string) = "BTC-USDT"
		*destinations[6].(**uuid.UUID) = nil
		*destinations[7].(*string) = "USDT"
		*destinations[8].(*string) = "UTC"
		*destinations[9].(**int16) = nil
		*destinations[10].(**int16) = nil
		*destinations[11].(**decimal.Decimal) = nil
		*destinations[12].(**decimal.Decimal) = nil
		*destinations[13].(*string) = "active"
		*destinations[14].(**time.Time) = nil
		*destinations[15].(**time.Time) = nil
		*destinations[16].(*[]byte) = []byte(`{}`)
		*destinations[17].(*time.Time) = fixture.now
		*destinations[18].(*time.Time) = fixture.now
		return nil
	}
}

func ingestionAssetRow(fixture postgresIngestionFixture) scanFunc {
	return func(destinations ...any) error {
		*destinations[0].(*uuid.UUID) = fixture.assetID.UUID()
		*destinations[1].(*string) = "asset.crypto.btc"
		*destinations[2].(*string) = "CRYPTO"
		*destinations[3].(*string) = "BTC"
		*destinations[4].(*string) = "Bitcoin"
		*destinations[5].(*string) = "active"
		*destinations[6].(*[]byte) = []byte(`{}`)
		*destinations[7].(*time.Time) = fixture.now
		*destinations[8].(*time.Time) = fixture.now
		return nil
	}
}
