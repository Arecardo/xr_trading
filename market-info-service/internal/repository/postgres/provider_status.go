package postgres

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"xr-trading/market-info-service/internal/application"
	"xr-trading/market-info-service/internal/domain"
)

// ListProviderStatusSources returns directory configuration plus persisted
// Task/checkpoint observations. It does not access any Provider adapter.
func (repository *IngestionRepository) ListProviderStatusSources(ctx context.Context, effectiveAt time.Time) ([]application.ProviderStatusSource, error) {
	if effectiveAt.IsZero() {
		return nil, fmt.Errorf("list provider status sources: %w", domain.ErrInvalidData)
	}
	rows, err := repository.database.Query(ctx, `WITH task_stats AS (
    SELECT subscription_id,
           max(finished_at) FILTER (WHERE status = 'success') AS last_success_at,
           max(updated_at) FILTER (WHERE status IN ('failed', 'retry_wait') AND error_code IS NOT NULL) AS last_failure_at
    FROM market_data.ingestion_tasks
    GROUP BY subscription_id
), active_sources AS (
    SELECT subscriptions.id AS subscription_id, mappings.provider_id,
           mappings.provider_market, assets.asset_type, subscriptions.interval,
           subscriptions.close_delay_seconds, checkpoints.last_closed_open_time,
           GREATEST(checkpoints.last_success_at, task_stats.last_success_at) AS last_success_at,
           task_stats.last_failure_at,
           COALESCE(checkpoints.consecutive_failures, 0) AS consecutive_failures
    FROM market_data.collection_subscriptions AS subscriptions
    JOIN market_data.provider_instruments AS mappings ON mappings.id = subscriptions.provider_instrument_id
    JOIN core.instruments AS instruments ON instruments.id = mappings.instrument_id
    JOIN core.assets AS assets ON assets.id = instruments.asset_id
    LEFT JOIN market_data.ingestion_checkpoints AS checkpoints ON checkpoints.subscription_id = subscriptions.id
    LEFT JOIN task_stats ON task_stats.subscription_id = subscriptions.id
    WHERE subscriptions.enabled = true
      AND mappings.enabled = true
      AND (mappings.valid_from IS NULL OR mappings.valid_from <= $1)
      AND (mappings.valid_to IS NULL OR mappings.valid_to > $1)
      AND mappings.capabilities @> '{"historical":true}'::jsonb
      AND mappings.capabilities->'intervals' ? subscriptions.interval
      AND instruments.status = 'active'
      AND (instruments.valid_from IS NULL OR instruments.valid_from <= $1)
      AND (instruments.valid_to IS NULL OR instruments.valid_to > $1)
      AND assets.status = 'active'
)
SELECT providers.id, providers.code, providers.name, providers.provider_type, providers.status,
       sources.subscription_id, sources.provider_market, sources.asset_type, sources.interval,
       sources.close_delay_seconds, sources.last_closed_open_time, sources.last_success_at,
       sources.last_failure_at, sources.consecutive_failures
FROM market_data.providers AS providers
LEFT JOIN active_sources AS sources ON sources.provider_id = providers.id
ORDER BY providers.code ASC, sources.provider_market ASC, sources.interval ASC, sources.subscription_id ASC`, TimeToDatabase(effectiveAt))
	if err != nil {
		return nil, fmt.Errorf("list provider status sources: %w", MapError(err))
	}
	defer rows.Close()
	values := make([]application.ProviderStatusSource, 0)
	indexes := make(map[domain.ID]int)
	for rows.Next() {
		providerID, providerCode, displayName, providerType, configuredStatus, observation, scanErr := scanProviderStatusSourceRow(rows)
		if scanErr != nil {
			return nil, fmt.Errorf("scan provider status source: %w", MapError(scanErr))
		}
		index, exists := indexes[providerID]
		if !exists {
			index = len(values)
			indexes[providerID] = index
			values = append(values, application.ProviderStatusSource{
				ProviderID: providerID, ProviderCode: providerCode, DisplayName: displayName,
				ProviderType: providerType, ConfiguredStatus: configuredStatus,
			})
		}
		if observation != nil {
			values[index].Subscriptions = append(values[index].Subscriptions, *observation)
		}
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate provider status sources: %w", MapError(err))
	}
	return values, nil
}

func scanProviderStatusSourceRow(row pgx.Row) (
	domain.ID, domain.Code, string, domain.ProviderType, domain.ProviderStatus,
	*application.ProviderSubscriptionObservation, error,
) {
	var providerUUID uuid.UUID
	var providerCodeText, displayName, providerTypeText, configuredStatusText string
	var subscriptionUUID *uuid.UUID
	var providerMarket, assetTypeText, intervalText *string
	var closeDelaySeconds, consecutiveFailures *int
	var lastClosedOpenTime, lastSuccessAt, lastFailureAt *time.Time
	if err := row.Scan(
		&providerUUID, &providerCodeText, &displayName, &providerTypeText, &configuredStatusText,
		&subscriptionUUID, &providerMarket, &assetTypeText, &intervalText, &closeDelaySeconds,
		&lastClosedOpenTime, &lastSuccessAt, &lastFailureAt, &consecutiveFailures,
	); err != nil {
		return domain.ID{}, domain.Code{}, "", "", "", nil, err
	}
	providerCode, err := domain.ParseCode(providerCodeText)
	if err != nil {
		return domain.ID{}, domain.Code{}, "", "", "", nil, err
	}
	providerType, err := domain.ParseProviderType(providerTypeText)
	if err != nil {
		return domain.ID{}, domain.Code{}, "", "", "", nil, err
	}
	configuredStatus, err := domain.ParseProviderStatus(configuredStatusText)
	if err != nil {
		return domain.ID{}, domain.Code{}, "", "", "", nil, err
	}
	providerID := IDFromDatabase(providerUUID)
	if subscriptionUUID == nil {
		return providerID, providerCode, displayName, providerType, configuredStatus, nil, nil
	}
	if providerMarket == nil || assetTypeText == nil || intervalText == nil || closeDelaySeconds == nil || consecutiveFailures == nil {
		return domain.ID{}, domain.Code{}, "", "", "", nil, domain.ErrInvalidData
	}
	assetType, err := domain.ParseAssetType(*assetTypeText)
	if err != nil {
		return domain.ID{}, domain.Code{}, "", "", "", nil, err
	}
	interval, err := domain.ParseBarInterval(*intervalText)
	if err != nil {
		return domain.ID{}, domain.Code{}, "", "", "", nil, err
	}
	observation := &application.ProviderSubscriptionObservation{
		SubscriptionID: IDFromDatabase(*subscriptionUUID), ProviderMarket: *providerMarket,
		AssetType: assetType, Interval: interval, CloseDelaySeconds: *closeDelaySeconds,
		LastClosedOpenTime: optionalTimeFromDatabase(lastClosedOpenTime), LastSuccessAt: optionalTimeFromDatabase(lastSuccessAt),
		LastFailureAt: optionalTimeFromDatabase(lastFailureAt), ConsecutiveFailures: *consecutiveFailures,
	}
	return providerID, providerCode, displayName, providerType, configuredStatus, observation, nil
}

var _ application.ProviderStatusReader = (*IngestionRepository)(nil)
