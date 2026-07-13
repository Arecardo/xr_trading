package domain

import (
	"context"
	"encoding/json"
	"time"
)

// CollectionSubscription describes one continuously collected interval for a
// ProviderInstrument. Its provider mapping and interval are immutable identity.
type CollectionSubscription struct {
	ID                   ID
	ProviderInstrumentID ID
	Interval             string
	Enabled              bool
	Priority             int16
	CloseDelaySeconds    int
	RevisionDelaySeconds *int
	Metadata             json.RawMessage
	CreatedAt            time.Time
	UpdatedAt            time.Time
}

// SubscriptionSettings contains the only mutable subscription fields.
type SubscriptionSettings struct {
	Enabled              bool
	Priority             int16
	CloseDelaySeconds    int
	RevisionDelaySeconds *int
}

// SubscriptionFilter defines cursor-based subscription list filtering.
type SubscriptionFilter struct {
	ProviderCode   string
	InstrumentCode string
	Interval       string
	Enabled        *bool
	AfterID        *ID
	Limit          int
}

// SubscriptionPage is one stable page of subscriptions ordered by UUIDv7 ID.
type SubscriptionPage struct {
	Items       []CollectionSubscription
	NextAfterID *ID
}

// CatalogRepository resolves core catalog entities. The market information
// service treats core as read-only.
type CatalogRepository interface {
	FindAssetByCode(context.Context, string) (Asset, error)
	FindInstrumentByCode(context.Context, string) (Instrument, error)
}

// ProviderRepository stores provider configuration and provider mappings.
type ProviderRepository interface {
	CreateProvider(context.Context, Provider) error
	FindProviderByCode(context.Context, string) (Provider, error)
	CreateProviderInstrument(context.Context, ProviderInstrument) error
	ListActiveProviderInstruments(context.Context, ID) ([]ProviderInstrument, error)
}

// SubscriptionRepository stores collection configuration. It deliberately has
// no delete operation so history remains available to task processing.
type SubscriptionRepository interface {
	CreateSubscription(context.Context, CollectionSubscription) error
	GetSubscription(context.Context, ID) (CollectionSubscription, error)
	ListSubscriptions(context.Context, SubscriptionFilter) (SubscriptionPage, error)
	UpdateSubscriptionSettings(context.Context, ID, SubscriptionSettings, time.Time) error
}
