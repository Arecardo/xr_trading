package postgres

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"xr-trading/market-info-service/internal/database/sqlbuilder"
	"xr-trading/market-info-service/internal/domain"
)

const defaultSubscriptionPageSize = 50
const maximumSubscriptionPageSize = 100

// SubscriptionRepository stores collection_subscriptions in PostgreSQL.
type SubscriptionRepository struct {
	pool catalogDatabase
}

// NewSubscriptionRepository constructs a subscription repository over pool.
func NewSubscriptionRepository(pool *pgxpool.Pool) (*SubscriptionRepository, error) {
	if pool == nil {
		return nil, errors.New("PostgreSQL pool is required")
	}
	return newSubscriptionRepository(pool)
}

func newSubscriptionRepository(pool catalogDatabase) (*SubscriptionRepository, error) {
	if pool == nil {
		return nil, errors.New("PostgreSQL pool is required")
	}
	return &SubscriptionRepository{pool: pool}, nil
}

// CreateSubscription persists a new collection subscription.
func (repository *SubscriptionRepository) CreateSubscription(ctx context.Context, subscription domain.CollectionSubscription) error {
	if subscription.ID.IsZero() || subscription.ProviderInstrumentID.IsZero() {
		return fmt.Errorf("create subscription: %w", domain.ErrInvalidData)
	}
	_, err := repository.pool.Exec(ctx, `
INSERT INTO market_data.collection_subscriptions (
    id, provider_instrument_id, interval, enabled, priority, close_delay_seconds,
    revision_delay_seconds, metadata, created_at, updated_at
) VALUES ($1, $2, $3, $4, $5, $6, $7, $8::jsonb, $9, $10)`,
		IDToDatabase(subscription.ID), IDToDatabase(subscription.ProviderInstrumentID), subscription.Interval,
		subscription.Enabled, subscription.Priority, subscription.CloseDelaySeconds,
		subscription.RevisionDelaySeconds, jsonValue(subscription.Metadata),
		TimeToDatabase(subscription.CreatedAt), TimeToDatabase(subscription.UpdatedAt),
	)
	if err != nil {
		return fmt.Errorf("create subscription: %w", MapError(err))
	}
	return nil
}

// GetSubscription returns a subscription by immutable ID.
func (repository *SubscriptionRepository) GetSubscription(ctx context.Context, id domain.ID) (domain.CollectionSubscription, error) {
	if id.IsZero() {
		return domain.CollectionSubscription{}, fmt.Errorf("get subscription: %w", domain.ErrInvalidData)
	}
	query, args, err := sqlbuilder.Select(subscriptionColumns...).From("market_data.collection_subscriptions").Where(sqlbuilder.Eq("id", IDToDatabase(id))).Build()
	if err != nil {
		return domain.CollectionSubscription{}, fmt.Errorf("build subscription query: %w", err)
	}
	subscription, err := scanSubscription(repository.pool.QueryRow(ctx, query, args...))
	if err != nil {
		return domain.CollectionSubscription{}, fmt.Errorf("get subscription: %w", MapError(err))
	}
	return subscription, nil
}

// ListSubscriptions returns a cursor page ordered by the UUIDv7 subscription ID.
func (repository *SubscriptionRepository) ListSubscriptions(ctx context.Context, filter domain.SubscriptionFilter) (domain.SubscriptionPage, error) {
	limit, err := subscriptionPageLimit(filter.Limit)
	if err != nil {
		return domain.SubscriptionPage{}, err
	}
	queryBuilder := sqlbuilder.Select(subscriptionColumnsWithAlias...).From(`market_data.collection_subscriptions AS subscriptions
JOIN market_data.provider_instruments AS provider_instruments
  ON provider_instruments.id = subscriptions.provider_instrument_id
JOIN market_data.providers AS providers ON providers.id = provider_instruments.provider_id
JOIN core.instruments AS instruments ON instruments.id = provider_instruments.instrument_id`)
	if filter.ProviderCode != "" {
		queryBuilder.And(sqlbuilder.Eq("providers.code", filter.ProviderCode))
	}
	if filter.InstrumentCode != "" {
		queryBuilder.And(sqlbuilder.Eq("instruments.code", filter.InstrumentCode))
	}
	if filter.Interval != "" {
		queryBuilder.And(sqlbuilder.Eq("subscriptions.interval", filter.Interval))
	}
	if filter.Enabled != nil {
		queryBuilder.And(sqlbuilder.Eq("subscriptions.enabled", *filter.Enabled))
	}
	if filter.AfterID != nil {
		queryBuilder.And(sqlbuilder.Raw("subscriptions.id > ?", IDToDatabase(*filter.AfterID)))
	}
	query, args, err := queryBuilder.OrderBy("subscriptions.id ASC").Limit(limit + 1).Build()
	if err != nil {
		return domain.SubscriptionPage{}, fmt.Errorf("build subscription list query: %w", err)
	}

	rows, err := repository.pool.Query(ctx, query, args...)
	if err != nil {
		return domain.SubscriptionPage{}, fmt.Errorf("list subscriptions: %w", MapError(err))
	}
	defer rows.Close()

	items := make([]domain.CollectionSubscription, 0, limit+1)
	for rows.Next() {
		subscription, err := scanSubscription(rows)
		if err != nil {
			return domain.SubscriptionPage{}, fmt.Errorf("scan subscription: %w", MapError(err))
		}
		items = append(items, subscription)
	}
	if err := rows.Err(); err != nil {
		return domain.SubscriptionPage{}, fmt.Errorf("iterate subscriptions: %w", MapError(err))
	}

	page := domain.SubscriptionPage{Items: items}
	if len(items) > limit {
		next := items[limit-1].ID
		page.NextAfterID = &next
		page.Items = items[:limit]
	}
	return page, nil
}

// UpdateSubscriptionSettings changes only mutable subscription settings. The
// provider mapping and interval are intentionally absent from this statement.
func (repository *SubscriptionRepository) UpdateSubscriptionSettings(ctx context.Context, id domain.ID, settings domain.SubscriptionSettings, updatedAt time.Time) error {
	if id.IsZero() {
		return fmt.Errorf("update subscription settings: %w", domain.ErrInvalidData)
	}
	commandTag, err := repository.pool.Exec(ctx, `
UPDATE market_data.collection_subscriptions
SET enabled = $1,
    priority = $2,
    close_delay_seconds = $3,
    revision_delay_seconds = $4,
    updated_at = $5
WHERE id = $6`,
		settings.Enabled, settings.Priority, settings.CloseDelaySeconds,
		settings.RevisionDelaySeconds, TimeToDatabase(updatedAt), IDToDatabase(id),
	)
	if err != nil {
		return fmt.Errorf("update subscription settings: %w", MapError(err))
	}
	if commandTag.RowsAffected() == 0 {
		return fmt.Errorf("update subscription settings: %w", domain.ErrNotFound)
	}
	return nil
}

var subscriptionColumns = []string{
	"id", "provider_instrument_id", "interval", "enabled", "priority", "close_delay_seconds",
	"revision_delay_seconds", "metadata", "created_at", "updated_at",
}

var subscriptionColumnsWithAlias = []string{
	"subscriptions.id", "subscriptions.provider_instrument_id", "subscriptions.interval", "subscriptions.enabled", "subscriptions.priority", "subscriptions.close_delay_seconds",
	"subscriptions.revision_delay_seconds", "subscriptions.metadata", "subscriptions.created_at", "subscriptions.updated_at",
}

func scanSubscription(row scanner) (domain.CollectionSubscription, error) {
	var subscription domain.CollectionSubscription
	var id, providerInstrumentID uuid.UUID
	var metadata []byte
	if err := row.Scan(
		&id, &providerInstrumentID, &subscription.Interval, &subscription.Enabled, &subscription.Priority,
		&subscription.CloseDelaySeconds, &subscription.RevisionDelaySeconds, &metadata,
		&subscription.CreatedAt, &subscription.UpdatedAt,
	); err != nil {
		return domain.CollectionSubscription{}, err
	}
	subscription.ID = IDFromDatabase(id)
	subscription.ProviderInstrumentID = IDFromDatabase(providerInstrumentID)
	subscription.Metadata = copyJSON(metadata)
	subscription.CreatedAt = TimeToDatabase(subscription.CreatedAt)
	subscription.UpdatedAt = TimeToDatabase(subscription.UpdatedAt)
	return subscription, nil
}

func subscriptionPageLimit(value int) (int, error) {
	if value == 0 {
		return defaultSubscriptionPageSize, nil
	}
	if value < 0 || value > maximumSubscriptionPageSize {
		return 0, fmt.Errorf("subscription page limit: %w", domain.ErrInvalidData)
	}
	return value, nil
}

var _ domain.SubscriptionRepository = (*SubscriptionRepository)(nil)
