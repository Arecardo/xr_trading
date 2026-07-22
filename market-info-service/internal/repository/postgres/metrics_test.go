package postgres

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"xr-trading/market-info-service/internal/domain"
)

func TestReadTaskMetricsReturnsDurableStatusCounts(t *testing.T) {
	t.Parallel()

	observedAt := time.Date(2026, 7, 23, 12, 0, 0, 0, time.UTC)
	oldest := observedAt.Add(-2 * time.Hour)
	var rowArgs []any
	database := fakeMarketDataDatabase{fakeCatalogDatabase: fakeCatalogDatabase{
		queryRow: func(_ context.Context, _ string, args ...any) pgx.Row {
			rowArgs = args
			return scanFunc(func(destinations ...any) error {
				for index, value := range []int64{3, 2, 1, 9, 4, 5} {
					*destinations[index].(*int64) = value
				}
				*destinations[6].(**time.Time) = &oldest
				return nil
			})
		},
	}, begin: func(context.Context) (marketDataTransaction, error) { return nil, errors.New("not used") }}
	repository, err := newIngestionRepository(database)
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := repository.ReadTaskMetrics(context.Background(), observedAt)
	if err != nil {
		t.Fatalf("ReadTaskMetrics() error = %v", err)
	}
	if snapshot.Counts["pending"] != 3 || snapshot.Counts["running"] != 2 || snapshot.Counts["retry_wait"] != 1 || snapshot.Counts["success"] != 9 || snapshot.Counts["failed"] != 4 || snapshot.Counts["canceled"] != 5 || snapshot.OldestBacklogCreatedAt == nil || !snapshot.OldestBacklogCreatedAt.Equal(oldest) {
		t.Fatalf("snapshot = %#v", snapshot)
	}
	if len(rowArgs) != 1 || rowArgs[0] != observedAt {
		t.Fatalf("query args = %#v", rowArgs)
	}
}

func TestReadTaskMetricsValidatesAndMapsFailures(t *testing.T) {
	t.Parallel()

	database := fakeMarketDataDatabase{fakeCatalogDatabase: fakeCatalogDatabase{
		queryRow: func(context.Context, string, ...any) pgx.Row {
			return scanFunc(func(...any) error { return &pgconn.PgError{Code: "08006"} })
		},
	}, begin: func(context.Context) (marketDataTransaction, error) { return nil, errors.New("not used") }}
	repository, _ := newIngestionRepository(database)
	if _, err := repository.ReadTaskMetrics(nil, time.Now()); !errors.Is(err, domain.ErrInvalidData) {
		t.Fatalf("ReadTaskMetrics(nil) error = %v", err)
	}
	if _, err := repository.ReadTaskMetrics(context.Background(), time.Time{}); !errors.Is(err, domain.ErrInvalidData) {
		t.Fatalf("ReadTaskMetrics(zero) error = %v", err)
	}
	if _, err := repository.ReadTaskMetrics(context.Background(), time.Now()); !errors.Is(err, domain.ErrDatabaseUnavailable) {
		t.Fatalf("ReadTaskMetrics(database) error = %v", err)
	}
}
