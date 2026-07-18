package postgres

import (
	"context"
	"database/sql"
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

func TestSubscriptionManagementRepositoryReadsJoinedRecordsAndSources(t *testing.T) {
	now := time.Date(2026, 7, 19, 12, 0, 0, 0, time.UTC)
	id := uuid.MustParse("019f1452-90f7-7992-a87a-ca2727897201")
	mappingID := uuid.MustParse("019f1452-90f7-7992-a87a-ca2727897202")
	providerID := uuid.MustParse("019f1452-90f7-7992-a87a-ca2727897203")
	instrumentID := uuid.MustParse("019f1452-90f7-7992-a87a-ca2727897204")
	var listSQL, sourceSQL, getSQL string
	var listArgs, sourceArgs []any
	database := fakeCatalogDatabase{
		exec: func(context.Context, string, ...any) (pgconn.CommandTag, error) {
			return pgconn.NewCommandTag("UPDATE 1"), nil
		},
		query: func(_ context.Context, query string, args ...any) (pgx.Rows, error) {
			if strings.Contains(query, "SELECT providers.id") {
				sourceSQL, sourceArgs = query, args
				return &fakeRows{rows: []scanFunc{subscriptionSourceRow(providerID, instrumentID, mappingID)}}, nil
			}
			listSQL, listArgs = query, args
			return &fakeRows{rows: []scanFunc{subscriptionRecordRow(id, mappingID, now)}}, nil
		},
		queryRow: func(_ context.Context, query string, _ ...any) pgx.Row {
			getSQL = query
			return subscriptionRecordRow(id, mappingID, now)
		},
	}
	repository, _ := newSubscriptionRepository(database)
	afterID := domain.IDFromUUID(uuid.MustParse("019f1452-90f7-7992-a87a-ca2727897200"))
	enabled := true
	records, err := repository.ListSubscriptionRecords(context.Background(), application.SubscriptionReadFilter{
		ProviderCode: "bybit", InstrumentCode: "instrument.bybit.spot.btc-usdt", Interval: "1h",
		Enabled: &enabled, AfterID: &afterID, Limit: 2,
	})
	if err != nil || len(records) != 1 || records[0].ProviderCode.String() != "bybit" || records[0].ProviderInstrumentCode.String() != "provider.bybit.spot.btcusdt" {
		t.Fatalf("ListSubscriptionRecords() = (%#v, %v)", records, err)
	}
	for _, fragment := range []string{"providers.code", "instruments.code", "subscriptions.interval", "subscriptions.enabled", "subscriptions.id >", "ORDER BY subscriptions.id ASC", "LIMIT"} {
		if !strings.Contains(listSQL, fragment) {
			t.Fatalf("list SQL missing %q: %s", fragment, listSQL)
		}
	}
	if len(listArgs) != 5 {
		t.Fatalf("list args = %#v", listArgs)
	}
	loaded, err := repository.GetSubscriptionRecord(context.Background(), domain.IDFromUUID(id))
	if err != nil || loaded.Subscription.ID != domain.IDFromUUID(id) || !strings.Contains(getSQL, "subscriptions.id") {
		t.Fatalf("GetSubscriptionRecord() = (%#v, %v), SQL=%s", loaded, err, getSQL)
	}
	sources, err := repository.FindSubscriptionSources(context.Background(), "bybit", "instrument.bybit.spot.btc-usdt", now)
	if err != nil || len(sources) != 1 || sources[0].ProviderInstrumentID != domain.IDFromUUID(mappingID) || !sources[0].Capabilities.Historical {
		t.Fatalf("FindSubscriptionSources() = (%#v, %v)", sources, err)
	}
	for _, fragment := range []string{"providers.status IN", "instruments.status = 'active'", "mappings.enabled = true", "mappings.valid_from", "mappings.valid_to", "mappings.is_default DESC", "LIMIT 2"} {
		if !strings.Contains(sourceSQL, fragment) {
			t.Fatalf("source SQL missing %q: %s", fragment, sourceSQL)
		}
	}
	if len(sourceArgs) != 3 || sourceArgs[0] != "bybit" || sourceArgs[1] != "instrument.bybit.spot.btc-usdt" {
		t.Fatalf("source args = %#v", sourceArgs)
	}
}

func TestSubscriptionManagementRepositoryAppendsAudit(t *testing.T) {
	now := time.Date(2026, 7, 19, 12, 0, 0, 0, time.UTC)
	id := domain.IDFromUUID(uuid.MustParse("019f1452-90f7-7992-a87a-ca2727897210"))
	var query string
	var args []any
	database := fakeCatalogDatabase{
		exec: func(_ context.Context, statement string, arguments ...any) (pgconn.CommandTag, error) {
			query, args = statement, arguments
			return pgconn.NewCommandTag("UPDATE 1"), nil
		},
		query: func(context.Context, string, ...any) (pgx.Rows, error) { return &fakeRows{}, nil },
		queryRow: func(context.Context, string, ...any) pgx.Row {
			return scanFunc(func(...any) error { return sql.ErrNoRows })
		},
	}
	repository, _ := newSubscriptionRepository(database)
	audit := domain.SubscriptionAuditEntry{Action: "update", RequestedBy: "admin@example.com", ActorType: "user", RequestID: "req_adm001", Reason: "disable source", OccurredAt: now}
	if err := repository.UpdateSubscriptionSettings(context.Background(), id, domain.SubscriptionSettings{Priority: 1}, audit); err != nil {
		t.Fatalf("UpdateSubscriptionSettings() error = %v", err)
	}
	if !strings.Contains(query, "jsonb_set") || !strings.Contains(query, "metadata -> 'audit_log'") || len(args) != 7 || !strings.Contains(args[4].(string), `"request_id":"req_adm001"`) {
		t.Fatalf("update SQL/args = %s / %#v", query, args)
	}
	badAudit := audit
	badAudit.Reason = ""
	if err := repository.UpdateSubscriptionSettings(context.Background(), id, domain.SubscriptionSettings{}, badAudit); !errors.Is(err, domain.ErrInvalidData) {
		t.Fatalf("UpdateSubscriptionSettings(bad audit) error = %v", err)
	}
}

func TestSubscriptionManagementRepositoryValidatesAndMapsFailures(t *testing.T) {
	want := &pgconn.PgError{Code: "08006"}
	repository, _ := newSubscriptionRepository(fakeCatalogDatabase{
		exec:     func(context.Context, string, ...any) (pgconn.CommandTag, error) { return pgconn.CommandTag{}, want },
		query:    func(context.Context, string, ...any) (pgx.Rows, error) { return nil, want },
		queryRow: func(context.Context, string, ...any) pgx.Row { return scanFunc(func(...any) error { return want }) },
	})
	if _, err := repository.ListSubscriptionRecords(context.Background(), application.SubscriptionReadFilter{}); !errors.Is(err, domain.ErrInvalidData) {
		t.Fatalf("ListSubscriptionRecords(invalid) error = %v", err)
	}
	if _, err := repository.ListSubscriptionRecords(context.Background(), application.SubscriptionReadFilter{Limit: 1}); !errors.Is(err, domain.ErrDatabaseUnavailable) {
		t.Fatalf("ListSubscriptionRecords(database) error = %v", err)
	}
	if _, err := repository.GetSubscriptionRecord(context.Background(), domain.ID{}); !errors.Is(err, domain.ErrInvalidData) {
		t.Fatalf("GetSubscriptionRecord(invalid) error = %v", err)
	}
	if _, err := repository.GetSubscriptionRecord(context.Background(), domain.IDFromUUID(uuid.New())); !errors.Is(err, domain.ErrDatabaseUnavailable) {
		t.Fatalf("GetSubscriptionRecord(database) error = %v", err)
	}
	if _, err := repository.FindSubscriptionSources(context.Background(), "", "", time.Time{}); !errors.Is(err, domain.ErrInvalidData) {
		t.Fatalf("FindSubscriptionSources(invalid) error = %v", err)
	}
	if _, err := repository.FindSubscriptionSources(context.Background(), "bybit", "instrument.bybit.spot.btc-usdt", time.Now().UTC()); !errors.Is(err, domain.ErrDatabaseUnavailable) {
		t.Fatalf("FindSubscriptionSources(database) error = %v", err)
	}
}

func subscriptionRecordRow(id, mappingID uuid.UUID, now time.Time) scanFunc {
	return func(destinations ...any) error {
		*destinations[0].(*uuid.UUID) = id
		*destinations[1].(*uuid.UUID) = mappingID
		*destinations[2].(*string) = "1h"
		*destinations[3].(*bool) = true
		*destinations[4].(*int16) = 100
		*destinations[5].(*int) = 120
		*destinations[6].(**int) = nil
		*destinations[7].(*[]byte) = []byte(`{"audit_log":[]}`)
		*destinations[8].(*time.Time) = now
		*destinations[9].(*time.Time) = now
		*destinations[10].(*string) = "bybit"
		*destinations[11].(*string) = "instrument.bybit.spot.btc-usdt"
		*destinations[12].(*string) = "provider.bybit.spot.btcusdt"
		*destinations[13].(*string) = "BTCUSDT"
		return nil
	}
}

func subscriptionSourceRow(providerID, instrumentID, mappingID uuid.UUID) scanFunc {
	return func(destinations ...any) error {
		*destinations[0].(*uuid.UUID) = providerID
		*destinations[1].(*string) = "bybit"
		*destinations[2].(*string) = "active"
		*destinations[3].(*uuid.UUID) = instrumentID
		*destinations[4].(*string) = "instrument.bybit.spot.btc-usdt"
		*destinations[5].(*string) = "active"
		*destinations[6].(*uuid.UUID) = mappingID
		*destinations[7].(*string) = "provider.bybit.spot.btcusdt"
		*destinations[8].(*string) = "BTCUSDT"
		*destinations[9].(*[]byte) = []byte(`{"historical":true,"intervals":["1h","1d"]}`)
		*destinations[10].(*bool) = true
		*destinations[11].(**time.Time) = nil
		*destinations[12].(**time.Time) = nil
		return nil
	}
}
