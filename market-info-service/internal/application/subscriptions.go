package application

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strings"
	"time"

	"xr-trading/market-info-service/internal/domain"
)

const MaximumSubscriptionsPageSize = 100

// SubscriptionListInput is the transport-independent management query.
type SubscriptionListInput struct {
	ProviderCode   string
	InstrumentCode string
	Interval       string
	Enabled        *bool
	AfterID        *domain.ID
	Limit          int
}

// CreateSubscriptionInput contains the immutable identity, initial settings
// and explicit audit reason for one subscription.
type CreateSubscriptionInput struct {
	ProviderCode         string
	InstrumentCode       string
	Interval             string
	Enabled              bool
	Priority             int
	CloseDelaySeconds    int
	RevisionDelaySeconds *int
	Reason               string
}

// UpdateSubscriptionInput contains only mutable settings. Nil fields retain
// their current values; RevisionDelaySecondsSet distinguishes absent from null.
type UpdateSubscriptionInput struct {
	ID                      domain.ID
	Enabled                 *bool
	Priority                *int
	CloseDelaySeconds       *int
	RevisionDelaySeconds    *int
	RevisionDelaySecondsSet bool
	Reason                  string
}

// SubscriptionRecord is the management read model returned to the API.
type SubscriptionRecord struct {
	Subscription           domain.CollectionSubscription
	ProviderCode           domain.Code
	InstrumentCode         domain.Code
	ProviderInstrumentCode domain.Code
	ProviderSymbol         string
}

// SubscriptionSource is a candidate immutable source resolved by readable
// Provider and Instrument codes.
type SubscriptionSource struct {
	ProviderID             domain.ID
	ProviderCode           domain.Code
	ProviderStatus         domain.ProviderStatus
	InstrumentID           domain.ID
	InstrumentCode         domain.Code
	InstrumentStatus       domain.InstrumentStatus
	ProviderInstrumentID   domain.ID
	ProviderInstrumentCode domain.Code
	ProviderSymbol         string
	Capabilities           domain.ProviderCapabilities
	Enabled                bool
	ValidFrom              *time.Time
	ValidTo                *time.Time
}

// SubscriptionReadFilter is the repository-facing list projection filter.
type SubscriptionReadFilter struct {
	ProviderCode   string
	InstrumentCode string
	Interval       string
	Enabled        *bool
	AfterID        *domain.ID
	Limit          int
}

// SubscriptionReader supplies the joined identities needed by the management
// API and resolves creation candidates without exposing SQL to the service.
type SubscriptionReader interface {
	ListSubscriptionRecords(context.Context, SubscriptionReadFilter) ([]SubscriptionRecord, error)
	FindSubscriptionSources(context.Context, string, string, time.Time) ([]SubscriptionSource, error)
	GetSubscriptionRecord(context.Context, domain.ID) (SubscriptionRecord, error)
}

// SubscriptionPage is one stable UUID-ordered management page.
type SubscriptionPage struct {
	Items       []SubscriptionRecord
	NextAfterID *domain.ID
}

// SubscriptionService implements subscription list/create/update semantics.
type SubscriptionService struct {
	store  domain.SubscriptionRepository
	reader SubscriptionReader
	now    func() time.Time
	newID  func() (domain.ID, error)
}

// NewSubscriptionService constructs the subscription management use case.
func NewSubscriptionService(store domain.SubscriptionRepository, reader SubscriptionReader, now func() time.Time, newID func() (domain.ID, error)) (*SubscriptionService, error) {
	if store == nil || reader == nil || now == nil || newID == nil {
		return nil, errors.New("subscription service dependencies are required")
	}
	return &SubscriptionService{store: store, reader: reader, now: now, newID: newID}, nil
}

// List returns joined readable identities without filtering disabled rows out.
func (service *SubscriptionService) List(ctx context.Context, input SubscriptionListInput) (SubscriptionPage, error) {
	filter, err := validateSubscriptionListInput(input)
	if err != nil {
		return SubscriptionPage{}, err
	}
	records, err := service.reader.ListSubscriptionRecords(ctx, filter)
	if err != nil {
		return SubscriptionPage{}, classifySubscriptionFailure(err)
	}
	for _, record := range records {
		if err := validateSubscriptionRecord(record); err != nil {
			return SubscriptionPage{}, classifySubscriptionFailure(err)
		}
	}
	page := SubscriptionPage{Items: records}
	if len(records) > input.Limit {
		page.Items = records[:input.Limit]
		next := page.Items[len(page.Items)-1].Subscription.ID
		page.NextAfterID = &next
	}
	return page, nil
}

// Create resolves exactly one active ProviderInstrument, verifies interval
// capability and persists an audit entry with the new subscription.
func (service *SubscriptionService) Create(ctx context.Context, input CreateSubscriptionInput) (SubscriptionRecord, error) {
	providerCode, instrumentCode, interval, settings, err := validateCreateSubscriptionInput(input)
	if err != nil {
		return SubscriptionRecord{}, err
	}
	now := domain.UTC(service.now())
	audit, err := subscriptionAuditFromContext(ctx, "create", input.Reason, now)
	if err != nil {
		return SubscriptionRecord{}, err
	}
	sources, err := service.reader.FindSubscriptionSources(ctx, providerCode.String(), instrumentCode.String(), now)
	if err != nil {
		return SubscriptionRecord{}, classifySubscriptionFailure(err)
	}
	if len(sources) != 1 {
		return SubscriptionRecord{}, ValidationError([]FieldViolation{{Field: "provider", Reason: "must resolve to exactly one provider instrument for instrument_code"}})
	}
	source := sources[0]
	if err := validateSubscriptionSource(source, providerCode, instrumentCode, now); err != nil {
		return SubscriptionRecord{}, WrapError(err, ErrorCodeConflict, "provider instrument is not available for subscription", false, nil)
	}
	if !source.Capabilities.Historical || !supportsBarInterval(source.Capabilities.Intervals, interval) {
		return SubscriptionRecord{}, NewError(ErrorCodeUnsupportedInterval, "provider instrument does not support the requested interval", false, map[string]any{"interval": interval})
	}
	id, err := service.newID()
	if err != nil {
		return SubscriptionRecord{}, classifySubscriptionFailure(fmt.Errorf("generate subscription ID: %w", err))
	}
	if id.IsZero() {
		return SubscriptionRecord{}, classifySubscriptionFailure(fmt.Errorf("generate subscription ID: %w", domain.ErrInvalidData))
	}
	metadata, err := json.Marshal(map[string]any{"audit_log": []domain.SubscriptionAuditEntry{audit}})
	if err != nil {
		return SubscriptionRecord{}, classifySubscriptionFailure(err)
	}
	subscription := domain.CollectionSubscription{
		ID: id, ProviderInstrumentID: source.ProviderInstrumentID, Interval: string(interval),
		Enabled: settings.Enabled, Priority: settings.Priority, CloseDelaySeconds: settings.CloseDelaySeconds,
		RevisionDelaySeconds: settings.RevisionDelaySeconds, Metadata: metadata, CreatedAt: now, UpdatedAt: now,
	}
	if err := service.store.CreateSubscription(ctx, subscription); err != nil {
		if errors.Is(err, domain.ErrConflict) {
			return SubscriptionRecord{}, WrapError(err, ErrorCodeSubscriptionAlreadyExists, "collection subscription already exists", false, nil)
		}
		return SubscriptionRecord{}, classifySubscriptionFailure(err)
	}
	return recordFromSubscription(subscription, source), nil
}

// Update merges a partial patch into mutable settings. Immutable identity
// fields are absent from the input contract and therefore cannot be changed.
func (service *SubscriptionService) Update(ctx context.Context, input UpdateSubscriptionInput) (SubscriptionRecord, error) {
	if err := validateUpdateSubscriptionInput(input); err != nil {
		return SubscriptionRecord{}, err
	}
	now := domain.UTC(service.now())
	audit, err := subscriptionAuditFromContext(ctx, "update", input.Reason, now)
	if err != nil {
		return SubscriptionRecord{}, err
	}
	record, err := service.reader.GetSubscriptionRecord(ctx, input.ID)
	if errors.Is(err, domain.ErrNotFound) {
		return SubscriptionRecord{}, WrapError(err, ErrorCodeSubscriptionNotFound, "collection subscription not found", false, nil)
	}
	if err != nil {
		return SubscriptionRecord{}, classifySubscriptionFailure(err)
	}
	if err := validateSubscriptionRecord(record); err != nil {
		return SubscriptionRecord{}, classifySubscriptionFailure(err)
	}
	settings := domain.SubscriptionSettings{
		Enabled: record.Subscription.Enabled, Priority: record.Subscription.Priority,
		CloseDelaySeconds:    record.Subscription.CloseDelaySeconds,
		RevisionDelaySeconds: copyInt(record.Subscription.RevisionDelaySeconds),
	}
	if input.Enabled != nil {
		settings.Enabled = *input.Enabled
	}
	if input.Priority != nil {
		settings.Priority = int16(*input.Priority)
	}
	if input.CloseDelaySeconds != nil {
		settings.CloseDelaySeconds = *input.CloseDelaySeconds
	}
	if input.RevisionDelaySecondsSet {
		settings.RevisionDelaySeconds = copyInt(input.RevisionDelaySeconds)
	}
	if err := service.store.UpdateSubscriptionSettings(ctx, input.ID, settings, audit); err != nil {
		if errors.Is(err, domain.ErrNotFound) {
			return SubscriptionRecord{}, WrapError(err, ErrorCodeSubscriptionNotFound, "collection subscription not found", false, nil)
		}
		return SubscriptionRecord{}, classifySubscriptionFailure(err)
	}
	record.Subscription.Enabled = settings.Enabled
	record.Subscription.Priority = settings.Priority
	record.Subscription.CloseDelaySeconds = settings.CloseDelaySeconds
	record.Subscription.RevisionDelaySeconds = copyInt(settings.RevisionDelaySeconds)
	record.Subscription.UpdatedAt = now
	return record, nil
}

func validateSubscriptionListInput(input SubscriptionListInput) (SubscriptionReadFilter, error) {
	violations := make([]FieldViolation, 0, 5)
	if input.ProviderCode != "" {
		if _, err := domain.ParseCode(input.ProviderCode); err != nil {
			violations = append(violations, FieldViolation{Field: "provider", Reason: "must be a valid provider code"})
		}
	}
	if input.InstrumentCode != "" {
		if code, err := domain.ParseCode(input.InstrumentCode); err != nil || !strings.HasPrefix(code.String(), "instrument.") {
			violations = append(violations, FieldViolation{Field: "instrument_code", Reason: "must be a valid instrument code"})
		}
	}
	if input.Interval != "" {
		if _, err := domain.ParseBarInterval(input.Interval); err != nil {
			violations = append(violations, FieldViolation{Field: "interval", Reason: "must be a supported interval value"})
		}
	}
	if input.AfterID != nil && input.AfterID.IsZero() {
		violations = append(violations, FieldViolation{Field: "cursor", Reason: "contains an invalid subscription position"})
	}
	if input.Limit <= 0 || input.Limit > MaximumSubscriptionsPageSize {
		violations = append(violations, FieldViolation{Field: "limit", Reason: fmt.Sprintf("must be an integer between 1 and %d", MaximumSubscriptionsPageSize)})
	}
	if len(violations) > 0 {
		return SubscriptionReadFilter{}, ValidationError(violations)
	}
	return SubscriptionReadFilter{ProviderCode: input.ProviderCode, InstrumentCode: input.InstrumentCode, Interval: input.Interval, Enabled: input.Enabled, AfterID: input.AfterID, Limit: input.Limit + 1}, nil
}

func validateCreateSubscriptionInput(input CreateSubscriptionInput) (domain.Code, domain.Code, domain.BarInterval, domain.SubscriptionSettings, error) {
	violations := validateSubscriptionSettings(input.Priority, input.CloseDelaySeconds, input.RevisionDelaySeconds)
	providerCode, err := domain.ParseCode(input.ProviderCode)
	if err != nil {
		violations = append(violations, FieldViolation{Field: "provider", Reason: "must be a valid provider code"})
	}
	instrumentCode, err := domain.ParseCode(input.InstrumentCode)
	if err != nil || !strings.HasPrefix(input.InstrumentCode, "instrument.") {
		violations = append(violations, FieldViolation{Field: "instrument_code", Reason: "must be a valid instrument code"})
	}
	interval, err := domain.ParseBarInterval(input.Interval)
	if err != nil {
		violations = append(violations, FieldViolation{Field: "interval", Reason: "must be a supported interval value"})
	}
	if err := validateSubscriptionReason(input.Reason); err != nil {
		violations = append(violations, FieldViolation{Field: "reason", Reason: "must be non-empty, trimmed and at most 512 characters"})
	}
	if len(violations) > 0 {
		return domain.Code{}, domain.Code{}, "", domain.SubscriptionSettings{}, ValidationError(violations)
	}
	return providerCode, instrumentCode, interval, domain.SubscriptionSettings{
		Enabled: input.Enabled, Priority: int16(input.Priority), CloseDelaySeconds: input.CloseDelaySeconds,
		RevisionDelaySeconds: copyInt(input.RevisionDelaySeconds),
	}, nil
}

func validateUpdateSubscriptionInput(input UpdateSubscriptionInput) error {
	violations := make([]FieldViolation, 0, 5)
	if input.ID.IsZero() {
		violations = append(violations, FieldViolation{Field: "subscription_id", Reason: "must be a valid UUID"})
	}
	if input.Enabled == nil && input.Priority == nil && input.CloseDelaySeconds == nil && !input.RevisionDelaySecondsSet {
		violations = append(violations, FieldViolation{Field: "body", Reason: "must include at least one mutable setting"})
	}
	priority := 0
	if input.Priority != nil {
		priority = *input.Priority
	}
	closeDelay := 0
	if input.CloseDelaySeconds != nil {
		closeDelay = *input.CloseDelaySeconds
	}
	for _, violation := range validateSubscriptionSettings(priority, closeDelay, input.RevisionDelaySeconds) {
		if (violation.Field == "priority" && input.Priority == nil) || (violation.Field == "close_delay_seconds" && input.CloseDelaySeconds == nil) || (violation.Field == "revision_delay_seconds" && !input.RevisionDelaySecondsSet) {
			continue
		}
		violations = append(violations, violation)
	}
	if err := validateSubscriptionReason(input.Reason); err != nil {
		violations = append(violations, FieldViolation{Field: "reason", Reason: "must be non-empty, trimmed and at most 512 characters"})
	}
	if len(violations) > 0 {
		return ValidationError(violations)
	}
	return nil
}

func validateSubscriptionSettings(priority, closeDelay int, revisionDelay *int) []FieldViolation {
	violations := make([]FieldViolation, 0, 3)
	if priority < 0 || priority > math.MaxInt16 {
		violations = append(violations, FieldViolation{Field: "priority", Reason: "must be between 0 and 32767"})
	}
	if closeDelay < 0 || int64(closeDelay) > math.MaxInt32 {
		violations = append(violations, FieldViolation{Field: "close_delay_seconds", Reason: "must be between 0 and 2147483647"})
	}
	if revisionDelay != nil && (*revisionDelay < 0 || int64(*revisionDelay) > math.MaxInt32) {
		violations = append(violations, FieldViolation{Field: "revision_delay_seconds", Reason: "must be null or between 0 and 2147483647"})
	}
	return violations
}

func validateSubscriptionReason(reason string) error {
	if reason == "" || reason != strings.TrimSpace(reason) || len([]rune(reason)) > 512 {
		return domain.ErrInvalidData
	}
	return nil
}

func subscriptionAuditFromContext(ctx context.Context, action, reason string, now time.Time) (domain.SubscriptionAuditEntry, error) {
	auditContext, exists := AuditContextFromContext(ctx)
	if !exists {
		return domain.SubscriptionAuditEntry{}, WrapError(domain.ErrInvalidData, ErrorCodeInternal, "internal server error", false, nil)
	}
	entry := domain.SubscriptionAuditEntry{Action: action, RequestedBy: auditContext.RequestedBy(), ActorType: string(auditContext.ActorType()), RequestID: auditContext.RequestID(), Reason: reason, OccurredAt: now}
	if err := entry.Validate(); err != nil {
		return domain.SubscriptionAuditEntry{}, WrapError(err, ErrorCodeInternal, "internal server error", false, nil)
	}
	return entry, nil
}

func validateSubscriptionSource(source SubscriptionSource, providerCode, instrumentCode domain.Code, now time.Time) error {
	if source.ProviderID.IsZero() || source.InstrumentID.IsZero() || source.ProviderInstrumentID.IsZero() || source.ProviderCode != providerCode || source.InstrumentCode != instrumentCode ||
		source.ProviderInstrumentCode.IsZero() || source.ProviderSymbol == "" ||
		(source.ProviderStatus != domain.ProviderStatusActive && source.ProviderStatus != domain.ProviderStatusDegraded) ||
		source.InstrumentStatus != domain.InstrumentStatusActive || !source.Enabled || !effectiveAtWithin(now, source.ValidFrom, source.ValidTo) {
		return domain.ErrInvalidState
	}
	return source.Capabilities.Validate()
}

func validateSubscriptionRecord(record SubscriptionRecord) error {
	subscription := record.Subscription
	if subscription.ID.IsZero() || subscription.ProviderInstrumentID.IsZero() || record.ProviderCode.IsZero() || record.InstrumentCode.IsZero() || record.ProviderInstrumentCode.IsZero() || record.ProviderSymbol == "" || subscription.Priority < 0 || subscription.CloseDelaySeconds < 0 || (subscription.RevisionDelaySeconds != nil && *subscription.RevisionDelaySeconds < 0) || subscription.CreatedAt.IsZero() || subscription.UpdatedAt.IsZero() {
		return domain.ErrInvalidData
	}
	_, err := domain.ParseBarInterval(subscription.Interval)
	return err
}

func recordFromSubscription(subscription domain.CollectionSubscription, source SubscriptionSource) SubscriptionRecord {
	return SubscriptionRecord{Subscription: subscription, ProviderCode: source.ProviderCode, InstrumentCode: source.InstrumentCode, ProviderInstrumentCode: source.ProviderInstrumentCode, ProviderSymbol: source.ProviderSymbol}
}

func classifySubscriptionFailure(err error) error {
	if errors.Is(err, domain.ErrDatabaseUnavailable) || errors.Is(err, domain.ErrRetryable) {
		return err
	}
	return WrapError(err, ErrorCodeInternal, "internal server error", false, nil)
}

func copyInt(value *int) *int {
	if value == nil {
		return nil
	}
	copied := *value
	return &copied
}
