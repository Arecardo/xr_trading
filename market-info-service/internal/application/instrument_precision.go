package application

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"xr-trading/market-info-service/internal/domain"
)

// MaximumInstrumentPrecisionBatchSize caps one CONTRACT-005 request. The
// contract currently serves three instruments; this stays generous while
// still bounding a single request's catalog fan-out.
const MaximumInstrumentPrecisionBatchSize = 100

// InstrumentPrecisionInput is the transport-independent CONTRACT-005 request.
type InstrumentPrecisionInput struct {
	InstrumentIDs []string
}

// InstrumentPrecisionCatalog resolves Instruments for the batch precision
// query. It is a narrow port so the service does not depend on catalog
// capabilities it does not use.
type InstrumentPrecisionCatalog interface {
	FindInstrumentsByIDs(context.Context, []domain.ID) ([]domain.Instrument, error)
}

// InstrumentPrecisionItem is one resolved, precision-complete Instrument.
type InstrumentPrecisionItem struct {
	InstrumentID   domain.ID
	InstrumentCode domain.Code
	PriceScale     int16
	QuantityScale  int16
	LotSize        domain.Decimal
	MinQuantity    domain.Decimal
	AsOf           domain.UTCInstant
}

// InstrumentPrecisionResult is the transport-independent CONTRACT-005
// response. MissingInstrumentIDs explicitly lists every requested ID a
// caller cannot safely use for order-quantity rounding right now: unknown
// instruments, instruments with incomplete precision fields, and instruments
// that are not currently active/effective. Callers must fail closed on
// anything listed here instead of guessing a default (DEC-003).
type InstrumentPrecisionResult struct {
	Items                []InstrumentPrecisionItem
	MissingInstrumentIDs []domain.ID
}

// InstrumentPrecisionService implements CONTRACT-005's batch precision query.
type InstrumentPrecisionService struct {
	catalog InstrumentPrecisionCatalog
	now     func() time.Time
}

// NewInstrumentPrecisionService constructs the query service.
func NewInstrumentPrecisionService(catalog InstrumentPrecisionCatalog, now func() time.Time) (*InstrumentPrecisionService, error) {
	if catalog == nil || now == nil {
		return nil, errors.New("instrument precision dependencies are required")
	}
	return &InstrumentPrecisionService{catalog: catalog, now: now}, nil
}

// Batch resolves precision fields for every requested instrument ID. The
// result preserves the de-duplicated request order and never errors solely
// because some IDs are unknown or currently unusable; those go into
// MissingInstrumentIDs instead.
func (service *InstrumentPrecisionService) Batch(ctx context.Context, input InstrumentPrecisionInput) (InstrumentPrecisionResult, error) {
	ids, err := parseInstrumentPrecisionInput(input)
	if err != nil {
		return InstrumentPrecisionResult{}, err
	}

	instruments, err := service.catalog.FindInstrumentsByIDs(ctx, ids)
	if err != nil {
		return InstrumentPrecisionResult{}, classifyInstrumentPrecisionFailure(err)
	}
	byID := make(map[domain.ID]domain.Instrument, len(instruments))
	for _, instrument := range instruments {
		if instrument.ID.IsZero() || !strings.HasPrefix(instrument.Code.String(), "instrument.") {
			return InstrumentPrecisionResult{}, classifyInstrumentPrecisionFailure(fmt.Errorf("invalid instrument projection: %w", domain.ErrInvalidData))
		}
		byID[instrument.ID] = instrument
	}

	effectiveAt := domain.UTC(service.now())
	result := InstrumentPrecisionResult{Items: make([]InstrumentPrecisionItem, 0, len(ids)), MissingInstrumentIDs: make([]domain.ID, 0)}
	for _, id := range ids {
		instrument, found := byID[id]
		if !found || !instrumentHasUsablePrecision(instrument, effectiveAt) {
			result.MissingInstrumentIDs = append(result.MissingInstrumentIDs, id)
			continue
		}
		asOf, instantErr := domain.NewUTCInstant(instrument.UpdatedAt)
		if instantErr != nil {
			return InstrumentPrecisionResult{}, classifyInstrumentPrecisionFailure(instantErr)
		}
		result.Items = append(result.Items, InstrumentPrecisionItem{
			InstrumentID: instrument.ID, InstrumentCode: instrument.Code,
			PriceScale: *instrument.PriceScale, QuantityScale: *instrument.QuantityScale,
			LotSize: *instrument.LotSize, MinQuantity: *instrument.MinQuantity, AsOf: asOf,
		})
	}
	return result, nil
}

// instrumentHasUsablePrecision reports whether instrument carries complete
// precision data that is safe to hand to a caller right now. Beyond the
// literal CONTRACT-005 text (non-empty scale/lot/min fields), this also
// requires the Instrument to be active and currently effective, matching the
// fail-closed posture the other public query services already apply
// (see InstrumentOptionsService.List and LatestQuotesService.List).
func instrumentHasUsablePrecision(instrument domain.Instrument, effectiveAt time.Time) bool {
	return instrument.Status == domain.InstrumentStatusActive &&
		effectiveAtWithin(effectiveAt, instrument.ValidFrom, instrument.ValidTo) &&
		instrument.PriceScale != nil && instrument.QuantityScale != nil &&
		instrument.LotSize != nil && instrument.MinQuantity != nil
}

func parseInstrumentPrecisionInput(input InstrumentPrecisionInput) ([]domain.ID, error) {
	if len(input.InstrumentIDs) == 0 {
		return nil, ValidationError([]FieldViolation{{Field: "instrument_ids", Reason: "must contain at least one instrument ID"}})
	}
	if len(input.InstrumentIDs) > MaximumInstrumentPrecisionBatchSize {
		return nil, ValidationError([]FieldViolation{{Field: "instrument_ids", Reason: fmt.Sprintf("must not contain more than %d entries", MaximumInstrumentPrecisionBatchSize)}})
	}

	violations := make([]FieldViolation, 0)
	seen := make(map[domain.ID]struct{}, len(input.InstrumentIDs))
	ids := make([]domain.ID, 0, len(input.InstrumentIDs))
	for index, raw := range input.InstrumentIDs {
		id, err := domain.ParseID(raw)
		if err != nil {
			violations = append(violations, FieldViolation{Field: fmt.Sprintf("instrument_ids[%d]", index), Reason: "must be a canonical UUID"})
			continue
		}
		if _, exists := seen[id]; exists {
			continue
		}
		seen[id] = struct{}{}
		ids = append(ids, id)
	}
	if len(violations) > 0 {
		return nil, ValidationError(violations)
	}
	return ids, nil
}

func classifyInstrumentPrecisionFailure(err error) error {
	if errors.Is(err, domain.ErrDatabaseUnavailable) || errors.Is(err, domain.ErrRetryable) {
		return err
	}
	return WrapError(err, ErrorCodeInternal, "internal server error", false, nil)
}
