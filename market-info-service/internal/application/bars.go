package application

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"xr-trading/market-info-service/internal/domain"
)

const MaximumBarsPageSize = 1000

// BarOrder controls stable open_time ordering.
type BarOrder string

const (
	BarOrderAscending  BarOrder = "asc"
	BarOrderDescending BarOrder = "desc"
)

// BarsInput contains the resolved HTTP query values without transport details.
type BarsInput struct {
	InstrumentCode string
	ProviderCode   string
	Interval       string
	StartTime      *time.Time
	EndTime        *time.Time
	Order          BarOrder
	CursorOpenTime *time.Time
	Limit          int
}

// BarCatalog resolves exact readable identities.
type BarCatalog interface {
	FindInstrumentByCode(context.Context, string) (domain.Instrument, error)
	FindProviderByCode(context.Context, string) (domain.Provider, error)
}

// BarSourceFilter identifies the best currently available mapping.
type BarSourceFilter struct {
	InstrumentID domain.ID
	ProviderID   domain.ID
	EffectiveAt  time.Time
}

// BarSourceRecord contains stable display identities for one selected mapping.
type BarSourceRecord struct {
	InstrumentID           domain.ID
	BaseAssetCode          domain.Code
	QuoteAssetCode         *domain.Code
	QuoteCurrency          string
	ProviderID             domain.ID
	ProviderInstrumentID   domain.ID
	ProviderInstrumentCode domain.Code
	ProviderSymbol         string
	Capabilities           domain.ProviderCapabilities
}

// BarReadFilter is the query-specific current-revision storage contract.
type BarReadFilter struct {
	InstrumentID         domain.ID
	ProviderInstrumentID domain.ID
	Interval             domain.BarInterval
	Range                domain.TimeRange
	Order                BarOrder
	CursorOpenTime       *domain.UTCInstant
	Limit                int
}

// BarReader resolves one source and reads current bar revisions.
type BarReader interface {
	ResolveBarSource(context.Context, BarSourceFilter) (BarSourceRecord, error)
	ListBars(context.Context, BarReadFilter) ([]domain.MarketBar, error)
}

// BarsResult is one source-specific page.
type BarsResult struct {
	Instrument         domain.Instrument
	Provider           domain.Provider
	Source             BarSourceRecord
	Interval           domain.BarInterval
	Order              BarOrder
	Bars               []domain.MarketBar
	NextCursorOpenTime *domain.UTCInstant
}

// BarsService implements exact source selection and time pagination.
type BarsService struct {
	catalog BarCatalog
	reader  BarReader
	now     func() time.Time
}

// NewBarsService constructs the public bar query use case.
func NewBarsService(catalog BarCatalog, reader BarReader, now func() time.Time) (*BarsService, error) {
	if catalog == nil || reader == nil || now == nil {
		return nil, errors.New("bar query dependencies are required")
	}
	return &BarsService{catalog: catalog, reader: reader, now: now}, nil
}

// List returns current revisions for exactly one ProviderInstrument.
func (service *BarsService) List(ctx context.Context, input BarsInput) (BarsResult, error) {
	parsed, err := parseBarsInput(input)
	if err != nil {
		return BarsResult{}, err
	}
	instrument, err := service.catalog.FindInstrumentByCode(ctx, parsed.instrumentCode.String())
	if err != nil {
		return BarsResult{}, mapCatalogQueryError(err, ErrorCodeInstrumentNotFound, "instrument not found")
	}
	if instrument.Code != parsed.instrumentCode {
		return BarsResult{}, classifyBarQueryFailure(domain.ErrInvalidData)
	}
	provider, err := service.catalog.FindProviderByCode(ctx, parsed.providerCode.String())
	if errors.Is(err, domain.ErrNotFound) {
		return BarsResult{}, ValidationError([]FieldViolation{{Field: "provider", Reason: "does not identify a known provider"}})
	}
	if err != nil {
		return BarsResult{}, classifyBarQueryFailure(err)
	}
	if provider.Code != parsed.providerCode {
		return BarsResult{}, classifyBarQueryFailure(domain.ErrInvalidData)
	}

	effectiveAt := domain.UTC(service.now())
	if instrument.Status != domain.InstrumentStatusActive || !effectiveAtWithin(effectiveAt, instrument.ValidFrom, instrument.ValidTo) || provider.Status == domain.ProviderStatusDisabled {
		return BarsResult{}, unavailableBarSource()
	}
	source, err := service.reader.ResolveBarSource(ctx, BarSourceFilter{InstrumentID: instrument.ID, ProviderID: provider.ID, EffectiveAt: effectiveAt})
	if errors.Is(err, domain.ErrNotFound) {
		return BarsResult{}, unavailableBarSource()
	}
	if err != nil {
		return BarsResult{}, classifyBarQueryFailure(err)
	}
	if err := validateBarSource(source, instrument.ID, provider.ID); err != nil {
		return BarsResult{}, classifyBarQueryFailure(err)
	}
	if !source.Capabilities.Historical || !supportsBarInterval(source.Capabilities.Intervals, parsed.interval) {
		return BarsResult{}, NewError(ErrorCodeUnsupportedInterval, "provider does not support the requested interval", false, map[string]any{"interval": parsed.interval})
	}

	filter := BarReadFilter{
		InstrumentID: instrument.ID, ProviderInstrumentID: source.ProviderInstrumentID,
		Interval: parsed.interval, Range: parsed.timeRange, Order: parsed.order,
		CursorOpenTime: parsed.cursorOpenTime, Limit: input.Limit + 1,
	}
	bars, err := service.reader.ListBars(ctx, filter)
	if err != nil {
		return BarsResult{}, classifyBarQueryFailure(err)
	}
	if err := validateBarPage(bars, filter); err != nil {
		return BarsResult{}, classifyBarQueryFailure(err)
	}
	result := BarsResult{Instrument: instrument, Provider: provider, Source: source, Interval: parsed.interval, Order: parsed.order, Bars: bars}
	if len(result.Bars) > input.Limit {
		result.Bars = result.Bars[:input.Limit]
		position := result.Bars[len(result.Bars)-1].OpenTime
		result.NextCursorOpenTime = &position
	}
	return result, nil
}

type parsedBarsInput struct {
	instrumentCode domain.Code
	providerCode   domain.Code
	interval       domain.BarInterval
	timeRange      domain.TimeRange
	order          BarOrder
	cursorOpenTime *domain.UTCInstant
}

func parseBarsInput(input BarsInput) (parsedBarsInput, error) {
	violations := make([]FieldViolation, 0, 5)
	instrumentCode, err := domain.ParseCode(input.InstrumentCode)
	if err != nil || !strings.HasPrefix(input.InstrumentCode, "instrument.") {
		violations = append(violations, FieldViolation{Field: "instrument_code", Reason: "must be a valid instrument code"})
	}
	providerCode, err := domain.ParseCode(input.ProviderCode)
	if err != nil {
		violations = append(violations, FieldViolation{Field: "provider", Reason: "must be a valid provider code"})
	}
	interval, err := domain.ParseBarInterval(input.Interval)
	if err != nil {
		violations = append(violations, FieldViolation{Field: "interval", Reason: "must be a supported interval value"})
	}
	order := input.Order
	if order == "" {
		order = BarOrderDescending
	}
	if order != BarOrderAscending && order != BarOrderDescending {
		violations = append(violations, FieldViolation{Field: "order", Reason: "must be asc or desc"})
	}
	if input.Limit <= 0 || input.Limit > MaximumBarsPageSize {
		violations = append(violations, FieldViolation{Field: "limit", Reason: fmt.Sprintf("must be an integer between 1 and %d", MaximumBarsPageSize)})
	}
	if len(violations) > 0 {
		return parsedBarsInput{}, ValidationError(violations)
	}
	timeRange, err := domain.NewTimeRange(input.StartTime, input.EndTime)
	if err != nil {
		return parsedBarsInput{}, WrapError(err, ErrorCodeInvalidTimeRange, "start_time must be before end_time", false, nil)
	}
	var cursor *domain.UTCInstant
	if input.CursorOpenTime != nil {
		parsedCursor, cursorErr := domain.NewUTCInstant(*input.CursorOpenTime)
		if cursorErr != nil || !timeRange.Contains(parsedCursor) {
			return parsedBarsInput{}, ValidationError([]FieldViolation{{Field: "cursor", Reason: "contains a position outside the requested time range"}})
		}
		cursor = &parsedCursor
	}
	return parsedBarsInput{instrumentCode: instrumentCode, providerCode: providerCode, interval: interval, timeRange: timeRange, order: order, cursorOpenTime: cursor}, nil
}

func validateBarSource(source BarSourceRecord, instrumentID, providerID domain.ID) error {
	if source.InstrumentID != instrumentID || source.ProviderID != providerID || source.ProviderInstrumentID.IsZero() ||
		!strings.HasPrefix(source.BaseAssetCode.String(), "asset.") ||
		!strings.HasPrefix(source.ProviderInstrumentCode.String(), "provider.") ||
		strings.TrimSpace(source.ProviderSymbol) == "" || strings.TrimSpace(source.QuoteCurrency) == "" {
		return fmt.Errorf("invalid bar source projection: %w", domain.ErrInvalidData)
	}
	if source.QuoteAssetCode != nil && !strings.HasPrefix(source.QuoteAssetCode.String(), "asset.") {
		return fmt.Errorf("invalid quote asset projection: %w", domain.ErrInvalidData)
	}
	return source.Capabilities.Validate()
}

func validateBarPage(bars []domain.MarketBar, filter BarReadFilter) error {
	for index, bar := range bars {
		if _, err := domain.NewStoredBar(bar); err != nil {
			return err
		}
		if bar.InstrumentID != filter.InstrumentID || bar.ProviderInstrumentID != filter.ProviderInstrumentID || bar.Interval != filter.Interval || !filter.Range.Contains(bar.OpenTime) {
			return fmt.Errorf("bar does not match query filter: %w", domain.ErrInvalidData)
		}
		if filter.CursorOpenTime != nil {
			if filter.Order == BarOrderAscending && !bar.OpenTime.Time().After(filter.CursorOpenTime.Time()) {
				return fmt.Errorf("ascending bar cursor was not respected: %w", domain.ErrInvalidData)
			}
			if filter.Order == BarOrderDescending && !bar.OpenTime.Time().Before(filter.CursorOpenTime.Time()) {
				return fmt.Errorf("descending bar cursor was not respected: %w", domain.ErrInvalidData)
			}
		}
		if index > 0 {
			previous := bars[index-1].OpenTime.Time()
			current := bar.OpenTime.Time()
			if (filter.Order == BarOrderAscending && !current.After(previous)) || (filter.Order == BarOrderDescending && !current.Before(previous)) {
				return fmt.Errorf("bar page order is unstable: %w", domain.ErrInvalidData)
			}
		}
	}
	return nil
}

func supportsBarInterval(intervals []domain.BarInterval, target domain.BarInterval) bool {
	for _, interval := range intervals {
		if interval == target {
			return true
		}
	}
	return false
}

func unavailableBarSource() error {
	return ValidationError([]FieldViolation{{Field: "provider", Reason: "is not currently available for instrument_code"}})
}

func classifyBarQueryFailure(err error) error {
	if errors.Is(err, domain.ErrDatabaseUnavailable) || errors.Is(err, domain.ErrRetryable) {
		return err
	}
	return WrapError(err, ErrorCodeInternal, "internal server error", false, nil)
}
