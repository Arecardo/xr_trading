package domain

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"
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

// SubscriptionAuditEntry is one immutable configuration change record stored
// in collection_subscriptions.metadata.audit_log. Keeping it alongside the
// changed row provides first-phase auditability without a separate audit table.
type SubscriptionAuditEntry struct {
	Action      string    `json:"action"`
	RequestedBy string    `json:"requested_by"`
	ActorType   string    `json:"actor_type"`
	RequestID   string    `json:"request_id"`
	Reason      string    `json:"reason"`
	OccurredAt  time.Time `json:"occurred_at"`
}

// Validate checks the database-safe audit contract.
func (entry SubscriptionAuditEntry) Validate() error {
	if entry.Action != "create" && entry.Action != "update" {
		return fmt.Errorf("subscription audit action: %w", ErrInvalidData)
	}
	if entry.ActorType != "user" && entry.ActorType != "service" {
		return fmt.Errorf("subscription audit actor type: %w", ErrInvalidData)
	}
	for _, value := range []struct {
		text    string
		maximum int
	}{
		{entry.RequestedBy, 128}, {entry.RequestID, 128}, {entry.Reason, 512},
	} {
		if value.text == "" || value.text != strings.TrimSpace(value.text) || utf8.RuneCountInString(value.text) > value.maximum {
			return fmt.Errorf("subscription audit text: %w", ErrInvalidData)
		}
	}
	if entry.OccurredAt.IsZero() || entry.OccurredAt.Location() != time.UTC {
		return fmt.Errorf("subscription audit time: %w", ErrInvalidData)
	}
	return nil
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
	// FindInstrumentsByIDs returns every Instrument matching one of ids. It is
	// not an error for some ids to be absent from the result; callers compare
	// the requested set against the returned set to detect gaps.
	FindInstrumentsByIDs(context.Context, []ID) ([]Instrument, error)
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
	UpdateSubscriptionSettings(context.Context, ID, SubscriptionSettings, SubscriptionAuditEntry) error
}
