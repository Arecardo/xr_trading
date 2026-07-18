package postgres

import (
	"context"
	"database/sql"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"xr-trading/market-info-service/internal/domain"
)

func TestSubscriptionPageLimit(t *testing.T) {
	for _, test := range []struct {
		input int
		want  int
		err   bool
	}{
		{input: 0, want: defaultSubscriptionPageSize},
		{input: 1, want: 1},
		{input: maximumSubscriptionPageSize, want: maximumSubscriptionPageSize},
		{input: -1, err: true},
		{input: maximumSubscriptionPageSize + 1, err: true},
	} {
		got, err := subscriptionPageLimit(test.input)
		if test.err {
			if !errors.Is(err, domain.ErrInvalidData) {
				t.Fatalf("subscriptionPageLimit(%d) error = %v", test.input, err)
			}
			continue
		}
		if err != nil || got != test.want {
			t.Fatalf("subscriptionPageLimit(%d) = (%d, %v), want (%d, nil)", test.input, got, err, test.want)
		}
	}
}

func TestSubscriptionRepositoryQueriesWritesAndPages(t *testing.T) {
	now := time.Now().UTC()
	firstID := uuid.MustParse("019f1452-90f7-7992-a87a-ca2727891601")
	secondID := uuid.MustParse("019f1452-90f7-7992-a87a-ca2727891602")
	mappingID := uuid.MustParse("019f1452-90f7-7992-a87a-ca2727891603")
	rows := &fakeRows{rows: []scanFunc{
		subscriptionRow(firstID, mappingID, now, true),
		subscriptionRow(secondID, mappingID, now, false),
	}}
	database := fakeCatalogDatabase{
		exec: func(_ context.Context, _ string, _ ...any) (pgconn.CommandTag, error) {
			return pgconn.NewCommandTag("UPDATE 1"), nil
		},
		queryRow: func(_ context.Context, _ string, _ ...any) pgx.Row {
			return subscriptionRow(firstID, mappingID, now, true)
		},
		query: func(_ context.Context, _ string, _ ...any) (pgx.Rows, error) { return rows, nil },
	}
	repository, err := newSubscriptionRepository(database)
	if err != nil {
		t.Fatalf("newSubscriptionRepository() error = %v", err)
	}
	firstDomainID := domain.IDFromUUID(firstID)
	mappingDomainID := domain.IDFromUUID(mappingID)
	subscription := domain.CollectionSubscription{
		ID:                   firstDomainID,
		ProviderInstrumentID: mappingDomainID,
		Interval:             "1h",
		Enabled:              true,
		Priority:             10,
		CloseDelaySeconds:    120,
		CreatedAt:            now,
		UpdatedAt:            now,
	}
	if err := repository.CreateSubscription(context.Background(), subscription); err != nil {
		t.Fatalf("CreateSubscription() error = %v", err)
	}
	loaded, err := repository.GetSubscription(context.Background(), firstDomainID)
	if err != nil || loaded.ID != firstDomainID {
		t.Fatalf("GetSubscription() = (%#v, %v)", loaded, err)
	}
	page, err := repository.ListSubscriptions(context.Background(), domain.SubscriptionFilter{Limit: 1, AfterID: &firstDomainID})
	if err != nil || len(page.Items) != 1 || page.NextAfterID == nil || *page.NextAfterID != firstDomainID || !rows.closed {
		t.Fatalf("ListSubscriptions() = (%#v, %v, closed=%t)", page, err, rows.closed)
	}
	if err := repository.UpdateSubscriptionSettings(context.Background(), firstDomainID, domain.SubscriptionSettings{Enabled: false, Priority: 3, CloseDelaySeconds: 60}, subscriptionAudit(now)); err != nil {
		t.Fatalf("UpdateSubscriptionSettings() error = %v", err)
	}
}

func TestSubscriptionRepositoryMapsErrorsAndValidatesIdentity(t *testing.T) {
	database := fakeCatalogDatabase{
		exec: func(_ context.Context, _ string, _ ...any) (pgconn.CommandTag, error) {
			return pgconn.NewCommandTag("UPDATE 0"), nil
		},
		queryRow: func(context.Context, string, ...any) pgx.Row {
			return scanFunc(func(...any) error { return sql.ErrNoRows })
		},
		query: func(context.Context, string, ...any) (pgx.Rows, error) { return nil, &pgconn.PgError{Code: "08006"} },
	}
	repository, err := newSubscriptionRepository(database)
	if err != nil {
		t.Fatalf("newSubscriptionRepository() error = %v", err)
	}
	if err := repository.CreateSubscription(context.Background(), domain.CollectionSubscription{}); !errors.Is(err, domain.ErrInvalidData) {
		t.Fatalf("CreateSubscription(zero) error = %v", err)
	}
	if _, err := repository.GetSubscription(context.Background(), domain.ID{}); !errors.Is(err, domain.ErrInvalidData) {
		t.Fatalf("GetSubscription(zero) error = %v", err)
	}
	if _, err := repository.GetSubscription(context.Background(), domain.IDFromUUID(uuid.New())); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("GetSubscription(missing) error = %v", err)
	}
	if _, err := repository.ListSubscriptions(context.Background(), domain.SubscriptionFilter{Limit: -1}); !errors.Is(err, domain.ErrInvalidData) {
		t.Fatalf("ListSubscriptions(invalid page) error = %v", err)
	}
	if _, err := repository.ListSubscriptions(context.Background(), domain.SubscriptionFilter{}); !errors.Is(err, domain.ErrDatabaseUnavailable) {
		t.Fatalf("ListSubscriptions(unavailable) error = %v", err)
	}
	if err := repository.UpdateSubscriptionSettings(context.Background(), domain.ID{}, domain.SubscriptionSettings{}, subscriptionAudit(time.Now().UTC())); !errors.Is(err, domain.ErrInvalidData) {
		t.Fatalf("UpdateSubscriptionSettings(zero) error = %v", err)
	}
	if err := repository.UpdateSubscriptionSettings(context.Background(), domain.IDFromUUID(uuid.New()), domain.SubscriptionSettings{}, subscriptionAudit(time.Now().UTC())); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("UpdateSubscriptionSettings(missing) error = %v", err)
	}
}

func subscriptionAudit(now time.Time) domain.SubscriptionAuditEntry {
	return domain.SubscriptionAuditEntry{Action: "update", RequestedBy: "test@example.com", ActorType: "user", RequestID: "req_test", Reason: "test update", OccurredAt: now.UTC()}
}

func subscriptionRow(id, mappingID uuid.UUID, now time.Time, enabled bool) scanFunc {
	return func(destinations ...any) error {
		*destinations[0].(*uuid.UUID) = id
		*destinations[1].(*uuid.UUID) = mappingID
		*destinations[2].(*string) = "1h"
		*destinations[3].(*bool) = enabled
		*destinations[4].(*int16) = 10
		*destinations[5].(*int) = 120
		*destinations[6].(**int) = nil
		*destinations[7].(*[]byte) = []byte(`{}`)
		*destinations[8].(*time.Time) = now
		*destinations[9].(*time.Time) = now
		return nil
	}
}
