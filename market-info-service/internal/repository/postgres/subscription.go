package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"

	"xr-trading/market-info-service/internal/application"
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
func NewSubscriptionRepository(pool CatalogDatabase) (*SubscriptionRepository, error) {
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
func (repository *SubscriptionRepository) UpdateSubscriptionSettings(ctx context.Context, id domain.ID, settings domain.SubscriptionSettings, audit domain.SubscriptionAuditEntry) error {
	if id.IsZero() || settings.Priority < 0 || settings.CloseDelaySeconds < 0 || (settings.RevisionDelaySeconds != nil && *settings.RevisionDelaySeconds < 0) {
		return fmt.Errorf("update subscription settings: %w", domain.ErrInvalidData)
	}
	if err := audit.Validate(); err != nil {
		return fmt.Errorf("update subscription settings: %w", err)
	}
	auditJSON, err := json.Marshal([]domain.SubscriptionAuditEntry{audit})
	if err != nil {
		return fmt.Errorf("encode subscription audit: %w", err)
	}
	commandTag, err := repository.pool.Exec(ctx, `
UPDATE market_data.collection_subscriptions
SET enabled = $1,
    priority = $2,
    close_delay_seconds = $3,
    revision_delay_seconds = $4,
    metadata = jsonb_set(
        metadata,
        '{audit_log}',
        CASE WHEN jsonb_typeof(metadata -> 'audit_log') = 'array'
            THEN metadata -> 'audit_log'
            ELSE '[]'::jsonb
        END || $5::jsonb,
        true
    ),
    updated_at = $6
WHERE id = $7`,
		settings.Enabled, settings.Priority, settings.CloseDelaySeconds,
		settings.RevisionDelaySeconds, string(auditJSON), TimeToDatabase(audit.OccurredAt), IDToDatabase(id),
	)
	if err != nil {
		return fmt.Errorf("update subscription settings: %w", MapError(err))
	}
	if commandTag.RowsAffected() == 0 {
		return fmt.Errorf("update subscription settings: %w", domain.ErrNotFound)
	}
	return nil
}

// ListSubscriptionRecords returns the management projection including readable
// Provider, Instrument and ProviderInstrument identities.
func (repository *SubscriptionRepository) ListSubscriptionRecords(ctx context.Context, filter application.SubscriptionReadFilter) ([]application.SubscriptionRecord, error) {
	if filter.Limit <= 0 || filter.Limit > application.MaximumSubscriptionsPageSize+1 || (filter.AfterID != nil && filter.AfterID.IsZero()) {
		return nil, fmt.Errorf("list subscription records: %w", domain.ErrInvalidData)
	}
	queryBuilder := sqlbuilder.Select(subscriptionRecordColumns...).From(`market_data.collection_subscriptions AS subscriptions
JOIN market_data.provider_instruments AS mappings ON mappings.id = subscriptions.provider_instrument_id
JOIN market_data.providers AS providers ON providers.id = mappings.provider_id
JOIN core.instruments AS instruments ON instruments.id = mappings.instrument_id`)
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
	query, args, err := queryBuilder.OrderBy("subscriptions.id ASC").Limit(filter.Limit).Build()
	if err != nil {
		return nil, fmt.Errorf("build subscription record list query: %w", err)
	}
	rows, err := repository.pool.Query(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("list subscription records: %w", MapError(err))
	}
	defer rows.Close()
	records := make([]application.SubscriptionRecord, 0, filter.Limit)
	for rows.Next() {
		record, scanErr := scanSubscriptionRecord(rows)
		if scanErr != nil {
			return nil, fmt.Errorf("scan subscription record: %w", MapError(scanErr))
		}
		records = append(records, record)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate subscription records: %w", MapError(err))
	}
	return records, nil
}

// GetSubscriptionRecord returns one management projection by subscription ID.
func (repository *SubscriptionRepository) GetSubscriptionRecord(ctx context.Context, id domain.ID) (application.SubscriptionRecord, error) {
	if id.IsZero() {
		return application.SubscriptionRecord{}, fmt.Errorf("get subscription record: %w", domain.ErrInvalidData)
	}
	queryBuilder := sqlbuilder.Select(subscriptionRecordColumns...).From(`market_data.collection_subscriptions AS subscriptions
JOIN market_data.provider_instruments AS mappings ON mappings.id = subscriptions.provider_instrument_id
JOIN market_data.providers AS providers ON providers.id = mappings.provider_id
JOIN core.instruments AS instruments ON instruments.id = mappings.instrument_id`).Where(sqlbuilder.Eq("subscriptions.id", IDToDatabase(id)))
	query, args, err := queryBuilder.Build()
	if err != nil {
		return application.SubscriptionRecord{}, fmt.Errorf("build subscription record query: %w", err)
	}
	record, err := scanSubscriptionRecord(repository.pool.QueryRow(ctx, query, args...))
	if err != nil {
		return application.SubscriptionRecord{}, fmt.Errorf("get subscription record: %w", MapError(err))
	}
	return record, nil
}

// FindSubscriptionSources resolves all currently-effective mappings for the
// readable Provider + Instrument identity. The service requires exactly one.
func (repository *SubscriptionRepository) FindSubscriptionSources(ctx context.Context, providerCode, instrumentCode string, effectiveAt time.Time) ([]application.SubscriptionSource, error) {
	if providerCode == "" || instrumentCode == "" || effectiveAt.IsZero() {
		return nil, fmt.Errorf("find subscription sources: %w", domain.ErrInvalidData)
	}
	rows, err := repository.pool.Query(ctx, `
SELECT providers.id, providers.code, providers.status,
       instruments.id, instruments.code, instruments.status,
       mappings.id, mappings.code, mappings.external_symbol,
       mappings.capabilities, mappings.enabled, mappings.valid_from, mappings.valid_to
FROM market_data.provider_instruments AS mappings
JOIN market_data.providers AS providers ON providers.id = mappings.provider_id
JOIN core.instruments AS instruments ON instruments.id = mappings.instrument_id
WHERE providers.code = $1
  AND instruments.code = $2
  AND providers.status IN ('active', 'degraded')
  AND instruments.status = 'active'
  AND mappings.enabled = true
  AND (mappings.valid_from IS NULL OR mappings.valid_from <= $3)
  AND (mappings.valid_to IS NULL OR mappings.valid_to > $3)
ORDER BY mappings.is_default DESC, mappings.priority ASC, mappings.code ASC
LIMIT 2`, providerCode, instrumentCode, TimeToDatabase(effectiveAt))
	if err != nil {
		return nil, fmt.Errorf("find subscription sources: %w", MapError(err))
	}
	defer rows.Close()
	sources := make([]application.SubscriptionSource, 0, 2)
	for rows.Next() {
		source, scanErr := scanSubscriptionSource(rows)
		if scanErr != nil {
			return nil, fmt.Errorf("scan subscription source: %w", MapError(scanErr))
		}
		sources = append(sources, source)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate subscription sources: %w", MapError(err))
	}
	return sources, nil
}

var subscriptionColumns = []string{
	"id", "provider_instrument_id", "interval", "enabled", "priority", "close_delay_seconds",
	"revision_delay_seconds", "metadata", "created_at", "updated_at",
}

var subscriptionColumnsWithAlias = []string{
	"subscriptions.id", "subscriptions.provider_instrument_id", "subscriptions.interval", "subscriptions.enabled", "subscriptions.priority", "subscriptions.close_delay_seconds",
	"subscriptions.revision_delay_seconds", "subscriptions.metadata", "subscriptions.created_at", "subscriptions.updated_at",
}

var subscriptionRecordColumns = append(append([]string{}, subscriptionColumnsWithAlias...),
	"providers.code", "instruments.code", "mappings.code", "mappings.external_symbol")

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

func scanSubscriptionRecord(row scanner) (application.SubscriptionRecord, error) {
	var record application.SubscriptionRecord
	var id, providerInstrumentID uuid.UUID
	var metadata []byte
	var providerCode, instrumentCode, providerInstrumentCode string
	if err := row.Scan(
		&id, &providerInstrumentID, &record.Subscription.Interval, &record.Subscription.Enabled,
		&record.Subscription.Priority, &record.Subscription.CloseDelaySeconds,
		&record.Subscription.RevisionDelaySeconds, &metadata, &record.Subscription.CreatedAt,
		&record.Subscription.UpdatedAt, &providerCode, &instrumentCode,
		&providerInstrumentCode, &record.ProviderSymbol,
	); err != nil {
		return application.SubscriptionRecord{}, err
	}
	parsedProviderCode, err := domain.ParseCode(providerCode)
	if err != nil {
		return application.SubscriptionRecord{}, err
	}
	parsedInstrumentCode, err := domain.ParseCode(instrumentCode)
	if err != nil {
		return application.SubscriptionRecord{}, err
	}
	parsedProviderInstrumentCode, err := domain.ParseCode(providerInstrumentCode)
	if err != nil {
		return application.SubscriptionRecord{}, err
	}
	record.Subscription.ID = IDFromDatabase(id)
	record.Subscription.ProviderInstrumentID = IDFromDatabase(providerInstrumentID)
	record.Subscription.Metadata = copyJSON(metadata)
	record.Subscription.CreatedAt = TimeToDatabase(record.Subscription.CreatedAt)
	record.Subscription.UpdatedAt = TimeToDatabase(record.Subscription.UpdatedAt)
	record.ProviderCode = parsedProviderCode
	record.InstrumentCode = parsedInstrumentCode
	record.ProviderInstrumentCode = parsedProviderInstrumentCode
	return record, nil
}

func scanSubscriptionSource(row scanner) (application.SubscriptionSource, error) {
	var source application.SubscriptionSource
	var providerID, instrumentID, mappingID uuid.UUID
	var providerCode, instrumentCode, mappingCode, providerStatus, instrumentStatus string
	var capabilities []byte
	if err := row.Scan(
		&providerID, &providerCode, &providerStatus, &instrumentID, &instrumentCode,
		&instrumentStatus, &mappingID, &mappingCode, &source.ProviderSymbol,
		&capabilities, &source.Enabled, &source.ValidFrom, &source.ValidTo,
	); err != nil {
		return application.SubscriptionSource{}, err
	}
	var err error
	if source.ProviderCode, err = domain.ParseCode(providerCode); err != nil {
		return application.SubscriptionSource{}, err
	}
	if source.InstrumentCode, err = domain.ParseCode(instrumentCode); err != nil {
		return application.SubscriptionSource{}, err
	}
	if source.ProviderInstrumentCode, err = domain.ParseCode(mappingCode); err != nil {
		return application.SubscriptionSource{}, err
	}
	if source.ProviderStatus, err = domain.ParseProviderStatus(providerStatus); err != nil {
		return application.SubscriptionSource{}, err
	}
	if source.InstrumentStatus, err = domain.ParseInstrumentStatus(instrumentStatus); err != nil {
		return application.SubscriptionSource{}, err
	}
	if source.Capabilities, err = domain.ParseProviderCapabilities(capabilities); err != nil {
		return application.SubscriptionSource{}, err
	}
	source.ProviderID = IDFromDatabase(providerID)
	source.InstrumentID = IDFromDatabase(instrumentID)
	source.ProviderInstrumentID = IDFromDatabase(mappingID)
	source.ValidFrom = optionalTimeFromDatabase(source.ValidFrom)
	source.ValidTo = optionalTimeFromDatabase(source.ValidTo)
	return source, nil
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
var _ application.SubscriptionReader = (*SubscriptionRepository)(nil)
