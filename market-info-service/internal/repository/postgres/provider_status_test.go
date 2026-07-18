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
)

func TestListProviderStatusSourcesUsesPersistedObservations(t *testing.T) {
	now := time.Date(2026, 7, 19, 12, 0, 0, 0, time.UTC)
	providerID := uuid.MustParse("019f1452-90f7-7992-a87a-ca2727898501")
	subscriptionID := uuid.MustParse("019f1452-90f7-7992-a87a-ca2727898502")
	lastClosed, lastSuccess, lastFailure := now.Add(-time.Hour), now.Add(-time.Minute), now.Add(-2*time.Minute)
	rows := &fakeRows{rows: []scanFunc{
		providerStatusRow(providerID, "bybit", "Bybit", "EXCHANGE", "active", &subscriptionID, stringPointerTest("spot"), stringPointerTest("CRYPTO"), stringPointerTest("1h"), intPointerTest(120), &lastClosed, &lastSuccess, &lastFailure, intPointerTest(2)),
		providerStatusRow(uuid.MustParse("019f1452-90f7-7992-a87a-ca2727898503"), "disabled", "Disabled", "BROKER", "disabled", nil, nil, nil, nil, nil, nil, nil, nil, nil),
	}}
	var query string
	var argument any
	database := fakeMarketDataDatabase{fakeCatalogDatabase: fakeCatalogDatabase{
		query: func(_ context.Context, statement string, arguments ...any) (pgx.Rows, error) {
			query, argument = statement, arguments[0]
			return rows, nil
		},
		queryRow: func(context.Context, string, ...any) pgx.Row { return nil },
		exec:     func(context.Context, string, ...any) (pgconn.CommandTag, error) { return pgconn.CommandTag{}, nil },
	}, begin: func(context.Context) (marketDataTransaction, error) { return nil, errors.New("not used") }}
	repository, _ := newIngestionRepository(database)
	sources, err := repository.ListProviderStatusSources(context.Background(), now)
	if err != nil || len(sources) != 2 || len(sources[0].Subscriptions) != 1 || len(sources[1].Subscriptions) != 0 || !rows.closed {
		t.Fatalf("ListProviderStatusSources() = (%#v, %v), closed=%t", sources, err, rows.closed)
	}
	observation := sources[0].Subscriptions[0]
	if sources[0].ProviderCode.String() != "bybit" || sources[0].ProviderType != domain.ProviderTypeExchange || observation.SubscriptionID.UUID() != subscriptionID ||
		observation.ProviderMarket != "spot" || observation.AssetType != domain.AssetTypeCrypto || observation.Interval != domain.BarInterval1Hour ||
		observation.ConsecutiveFailures != 2 || observation.LastSuccessAt == nil || !observation.LastSuccessAt.Equal(lastSuccess) {
		t.Fatalf("source = %#v", sources[0])
	}
	for _, fragment := range []string{"WITH task_stats AS", "active_sources AS", "ingestion_checkpoints", "status IN ('failed', 'retry_wait')", "LEFT JOIN active_sources", "capabilities->'intervals' ? subscriptions.interval"} {
		if !strings.Contains(query, fragment) {
			t.Fatalf("query missing %q: %s", fragment, query)
		}
	}
	if argument != now {
		t.Fatalf("effective argument=%#v", argument)
	}
}

func TestListProviderStatusSourcesValidatesAndMapsFailures(t *testing.T) {
	database := fakeMarketDataDatabase{fakeCatalogDatabase: fakeCatalogDatabase{
		query:    func(context.Context, string, ...any) (pgx.Rows, error) { return nil, &pgconn.PgError{Code: "08006"} },
		queryRow: func(context.Context, string, ...any) pgx.Row { return nil },
		exec:     func(context.Context, string, ...any) (pgconn.CommandTag, error) { return pgconn.CommandTag{}, nil },
	}, begin: func(context.Context) (marketDataTransaction, error) { return nil, errors.New("not used") }}
	repository, _ := newIngestionRepository(database)
	if _, err := repository.ListProviderStatusSources(context.Background(), time.Time{}); !errors.Is(err, domain.ErrInvalidData) {
		t.Fatalf("ListProviderStatusSources(invalid) = %v", err)
	}
	if _, err := repository.ListProviderStatusSources(context.Background(), time.Now()); !errors.Is(err, domain.ErrDatabaseUnavailable) {
		t.Fatalf("ListProviderStatusSources(database) = %v", err)
	}
}

func providerStatusRow(providerID uuid.UUID, code, name, providerType, status string, subscriptionID *uuid.UUID, market, assetType, interval *string, delay *int, lastClosed, lastSuccess, lastFailure *time.Time, failures *int) scanFunc {
	return func(destinations ...any) error {
		*destinations[0].(*uuid.UUID) = providerID
		*destinations[1].(*string) = code
		*destinations[2].(*string) = name
		*destinations[3].(*string) = providerType
		*destinations[4].(*string) = status
		*destinations[5].(**uuid.UUID) = subscriptionID
		*destinations[6].(**string) = market
		*destinations[7].(**string) = assetType
		*destinations[8].(**string) = interval
		*destinations[9].(**int) = delay
		*destinations[10].(**time.Time) = lastClosed
		*destinations[11].(**time.Time) = lastSuccess
		*destinations[12].(**time.Time) = lastFailure
		*destinations[13].(**int) = failures
		return nil
	}
}

func stringPointerTest(value string) *string { return &value }
func intPointerTest(value int) *int          { return &value }
