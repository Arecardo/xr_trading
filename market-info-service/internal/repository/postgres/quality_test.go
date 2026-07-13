package postgres

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"xr-trading/market-info-service/internal/domain"
)

func TestOpenQualityIssue(t *testing.T) {
	now := time.Now().UTC()
	issue := testQualityIssue(now)
	database := fakeMarketDataDatabase{fakeCatalogDatabase: fakeCatalogDatabase{
		exec: func(context.Context, string, ...any) (pgconn.CommandTag, error) { return pgconn.CommandTag{}, nil },
		queryRow: func(context.Context, string, ...any) pgx.Row {
			return scanFunc(func(destinations ...any) error { *destinations[0].(*uuid.UUID) = issue.ID.UUID(); return nil })
		},
		query: func(context.Context, string, ...any) (pgx.Rows, error) { return nil, nil },
	}, begin: func(context.Context) (marketDataTransaction, error) { return nil, errors.New("not used") }}
	repository, _ := newDataQualityIssueRepository(database)
	created, err := repository.OpenIssue(context.Background(), issue)
	if err != nil || !created {
		t.Fatalf("OpenIssue() = (%t, %v)", created, err)
	}
	if _, err := repository.OpenIssue(context.Background(), domain.DataQualityIssue{}); !errors.Is(err, domain.ErrInvalidData) {
		t.Fatalf("OpenIssue(invalid) error = %v", err)
	}
}

func TestOpenIssueDeduplicatesAndTransitions(t *testing.T) {
	now := time.Now().UTC()
	issue := testQualityIssue(now)
	queryRows := []pgx.Row{scanFunc(func(...any) error { return pgx.ErrNoRows }), scanFunc(func(destinations ...any) error { *destinations[0].(*bool) = true; return nil })}
	queryIndex := 0
	database := fakeMarketDataDatabase{fakeCatalogDatabase: fakeCatalogDatabase{
		exec: func(context.Context, string, ...any) (pgconn.CommandTag, error) {
			return pgconn.NewCommandTag("UPDATE 0"), nil
		},
		queryRow: func(context.Context, string, ...any) pgx.Row { row := queryRows[queryIndex]; queryIndex++; return row },
		query:    func(context.Context, string, ...any) (pgx.Rows, error) { return nil, nil },
	}, begin: func(context.Context) (marketDataTransaction, error) { return nil, errors.New("not used") }}
	repository, _ := newDataQualityIssueRepository(database)
	created, err := repository.OpenIssue(context.Background(), issue)
	if err != nil || created {
		t.Fatalf("OpenIssue(duplicate) = (%t, %v)", created, err)
	}
	if err := repository.AcknowledgeIssue(context.Background(), issue.ID, now); !errors.Is(err, domain.ErrInvalidState) {
		t.Fatalf("AcknowledgeIssue(invalid state) error = %v", err)
	}
}

func TestQualityIssueTransitions(t *testing.T) {
	now := time.Now().UTC()
	database := fakeMarketDataDatabase{fakeCatalogDatabase: fakeCatalogDatabase{
		exec: func(context.Context, string, ...any) (pgconn.CommandTag, error) {
			return pgconn.NewCommandTag("UPDATE 1"), nil
		},
		queryRow: func(context.Context, string, ...any) pgx.Row { return nil },
		query:    func(context.Context, string, ...any) (pgx.Rows, error) { return nil, nil },
	}, begin: func(context.Context) (marketDataTransaction, error) { return nil, errors.New("not used") }}
	repository, _ := newDataQualityIssueRepository(database)
	id := testQualityIssue(now).ID
	if err := repository.AcknowledgeIssue(context.Background(), id, now); err != nil {
		t.Fatalf("AcknowledgeIssue() error = %v", err)
	}
	if err := repository.ResolveIssue(context.Background(), id, "fixed", now); err != nil {
		t.Fatalf("ResolveIssue() error = %v", err)
	}
	if err := repository.IgnoreIssue(context.Background(), id, "expected", now); err != nil {
		t.Fatalf("IgnoreIssue() error = %v", err)
	}
	if err := repository.ResolveIssue(context.Background(), domain.ID{}, "", now); !errors.Is(err, domain.ErrInvalidData) {
		t.Fatalf("ResolveIssue(invalid) error = %v", err)
	}
}

func TestNewDataQualityIssueRepositoryRequiresDatabase(t *testing.T) {
	if _, err := NewDataQualityIssueRepository(nil); err == nil {
		t.Fatal("NewDataQualityIssueRepository(nil) error = nil")
	}
	if _, err := newDataQualityIssueRepository(nil); err == nil {
		t.Fatal("newDataQualityIssueRepository(nil) error = nil")
	}
}

func testQualityIssue(now time.Time) domain.DataQualityIssue {
	return domain.DataQualityIssue{ID: domain.IDFromUUID(uuid.MustParse("019f1452-90f7-7992-a87a-ca2727891601")), InstrumentID: domain.IDFromUUID(uuid.MustParse("019f1452-90f7-7992-a87a-ca2727891602")), RuleCode: "ohlc_invalid", Severity: "error", Summary: "invalid OHLC", DetectedAt: now, CreatedAt: now, UpdatedAt: now}
}
