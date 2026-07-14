package application

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"xr-trading/market-info-service/internal/domain"
)

// MaximumInstrumentOptionsPageSize is the public hard limit for one page.
const MaximumInstrumentOptionsPageSize = 100

// InstrumentOptionsInput contains the transport-independent query position.
// Only enabled, currently-effective options are exposed by this use case.
type InstrumentOptionsInput struct {
	AssetCode           string
	AfterInstrumentCode string
	Limit               int
}

// InstrumentOptionsFilter is the read-port contract implemented by storage.
type InstrumentOptionsFilter struct {
	AssetID             domain.ID
	AfterInstrumentCode *domain.Code
	EffectiveAt         time.Time
	InstrumentLimit     int
}

// InstrumentProviderOptionRecord is one flattened read-model row. The
// application service groups rows by Instrument and orders Provider choices.
type InstrumentProviderOptionRecord struct {
	InstrumentID           domain.ID
	InstrumentCode         domain.Code
	InstrumentDisplayName  string
	ProviderInstrumentID   domain.ID
	ProviderInstrumentCode domain.Code
	ProviderCode           domain.Code
	ProviderDisplayName    string
	IsDefault              bool
	Priority               int16
	Capabilities           domain.ProviderCapabilities
}

// InstrumentOptionsReader is a query-specific port. It deliberately returns
// a read projection instead of forcing query handlers through entity-by-entity
// repository calls.
type InstrumentOptionsReader interface {
	ListInstrumentProviderOptions(context.Context, InstrumentOptionsFilter) ([]InstrumentProviderOptionRecord, error)
}

// ProviderOption is one selectable Provider for an Instrument.
type ProviderOption struct {
	Code               domain.Code
	DisplayName        string
	IsDefault          bool
	Priority           int16
	SupportedIntervals []domain.BarInterval
}

// InstrumentOption is one selectable Instrument and its Provider choices.
type InstrumentOption struct {
	ID          domain.ID
	Code        domain.Code
	DisplayName string
	Providers   []ProviderOption
}

// InstrumentOptionsPage is a stable code-ordered page. The next position is
// kept transport-independent; HTTP turns it into an opaque scoped cursor.
type InstrumentOptionsPage struct {
	Items                   []InstrumentOption
	NextAfterInstrumentCode *domain.Code
}

// InstrumentOptionsService implements the Asset -> Instrument -> Provider ->
// Interval option query.
type InstrumentOptionsService struct {
	catalog domain.CatalogRepository
	reader  InstrumentOptionsReader
	now     func() time.Time
}

// NewInstrumentOptionsService constructs the query service.
func NewInstrumentOptionsService(catalog domain.CatalogRepository, reader InstrumentOptionsReader, now func() time.Time) (*InstrumentOptionsService, error) {
	if catalog == nil || reader == nil || now == nil {
		return nil, errors.New("instrument options dependencies are required")
	}
	return &InstrumentOptionsService{catalog: catalog, reader: reader, now: now}, nil
}

// List returns only active Instruments with at least one enabled, effective
// and non-disabled Provider mapping.
func (service *InstrumentOptionsService) List(ctx context.Context, input InstrumentOptionsInput) (InstrumentOptionsPage, error) {
	assetCode, afterCode, err := validateInstrumentOptionsInput(input)
	if err != nil {
		return InstrumentOptionsPage{}, err
	}

	asset, err := service.catalog.FindAssetByCode(ctx, assetCode.String())
	if err != nil {
		if errors.Is(err, domain.ErrNotFound) {
			return InstrumentOptionsPage{}, WrapError(err, ErrorCodeAssetNotFound, "asset not found", false, nil)
		}
		return InstrumentOptionsPage{}, classifyInstrumentOptionsFailure(err)
	}
	if asset.Code != assetCode {
		return InstrumentOptionsPage{}, classifyInstrumentOptionsFailure(domain.ErrInvalidData)
	}
	if asset.Status != domain.AssetStatusActive {
		return InstrumentOptionsPage{Items: []InstrumentOption{}}, nil
	}

	rows, err := service.reader.ListInstrumentProviderOptions(ctx, InstrumentOptionsFilter{
		AssetID: asset.ID, AfterInstrumentCode: afterCode, EffectiveAt: domain.UTC(service.now()), InstrumentLimit: input.Limit + 1,
	})
	if err != nil {
		return InstrumentOptionsPage{}, classifyInstrumentOptionsFailure(err)
	}
	items, err := groupInstrumentOptions(rows)
	if err != nil {
		return InstrumentOptionsPage{}, classifyInstrumentOptionsFailure(err)
	}

	page := InstrumentOptionsPage{Items: items}
	if len(page.Items) > input.Limit {
		page.Items = page.Items[:input.Limit]
		position := page.Items[len(page.Items)-1].Code
		page.NextAfterInstrumentCode = &position
	}
	return page, nil
}

func validateInstrumentOptionsInput(input InstrumentOptionsInput) (domain.Code, *domain.Code, error) {
	violations := make([]FieldViolation, 0, 3)
	assetCode, err := domain.ParseCode(input.AssetCode)
	if err != nil || !strings.HasPrefix(input.AssetCode, "asset.") {
		violations = append(violations, FieldViolation{Field: "asset_code", Reason: "must be a valid asset code"})
	}
	if input.Limit <= 0 || input.Limit > MaximumInstrumentOptionsPageSize {
		violations = append(violations, FieldViolation{Field: "limit", Reason: fmt.Sprintf("must be an integer between 1 and %d", MaximumInstrumentOptionsPageSize)})
	}
	var afterCode *domain.Code
	if input.AfterInstrumentCode != "" {
		parsed, parseErr := domain.ParseCode(input.AfterInstrumentCode)
		if parseErr != nil || !strings.HasPrefix(input.AfterInstrumentCode, "instrument.") {
			violations = append(violations, FieldViolation{Field: "cursor", Reason: "contains an invalid instrument position"})
		} else {
			afterCode = &parsed
		}
	}
	if len(violations) > 0 {
		return domain.Code{}, nil, ValidationError(violations)
	}
	return assetCode, afterCode, nil
}

func groupInstrumentOptions(rows []InstrumentProviderOptionRecord) ([]InstrumentOption, error) {
	sorted := append([]InstrumentProviderOptionRecord(nil), rows...)
	for _, row := range sorted {
		if err := validateInstrumentProviderOptionRecord(row); err != nil {
			return nil, err
		}
	}
	sort.SliceStable(sorted, func(left, right int) bool {
		if sorted[left].InstrumentCode != sorted[right].InstrumentCode {
			return sorted[left].InstrumentCode.String() < sorted[right].InstrumentCode.String()
		}
		if sorted[left].IsDefault != sorted[right].IsDefault {
			return sorted[left].IsDefault
		}
		if sorted[left].Priority != sorted[right].Priority {
			return sorted[left].Priority < sorted[right].Priority
		}
		if sorted[left].ProviderCode != sorted[right].ProviderCode {
			return sorted[left].ProviderCode.String() < sorted[right].ProviderCode.String()
		}
		return sorted[left].ProviderInstrumentCode.String() < sorted[right].ProviderInstrumentCode.String()
	})

	items := make([]InstrumentOption, 0)
	for _, row := range sorted {
		if len(items) == 0 || items[len(items)-1].Code != row.InstrumentCode {
			items = append(items, InstrumentOption{ID: row.InstrumentID, Code: row.InstrumentCode, DisplayName: row.InstrumentDisplayName, Providers: []ProviderOption{}})
		}
		item := &items[len(items)-1]
		if item.ID != row.InstrumentID || item.DisplayName != row.InstrumentDisplayName {
			return nil, fmt.Errorf("inconsistent instrument option projection: %w", domain.ErrInvalidData)
		}
		if hasProviderOption(item.Providers, row.ProviderCode) {
			continue
		}
		item.Providers = append(item.Providers, ProviderOption{
			Code: row.ProviderCode, DisplayName: row.ProviderDisplayName, IsDefault: row.IsDefault,
			Priority: row.Priority, SupportedIntervals: append([]domain.BarInterval(nil), row.Capabilities.Intervals...),
		})
	}
	return items, nil
}

func validateInstrumentProviderOptionRecord(row InstrumentProviderOptionRecord) error {
	if row.InstrumentID.IsZero() || row.ProviderInstrumentID.IsZero() ||
		!strings.HasPrefix(row.InstrumentCode.String(), "instrument.") ||
		!strings.HasPrefix(row.ProviderInstrumentCode.String(), "provider.") ||
		row.ProviderCode.IsZero() || strings.TrimSpace(row.InstrumentDisplayName) == "" ||
		strings.TrimSpace(row.ProviderDisplayName) == "" || row.Priority < 0 {
		return fmt.Errorf("invalid instrument option projection: %w", domain.ErrInvalidData)
	}
	if err := row.Capabilities.Validate(); err != nil {
		return fmt.Errorf("invalid instrument option capabilities: %w", err)
	}
	return nil
}

func hasProviderOption(options []ProviderOption, code domain.Code) bool {
	for _, option := range options {
		if option.Code == code {
			return true
		}
	}
	return false
}

func classifyInstrumentOptionsFailure(err error) error {
	if errors.Is(err, domain.ErrDatabaseUnavailable) || errors.Is(err, domain.ErrRetryable) {
		return err
	}
	return WrapError(err, ErrorCodeInternal, "internal server error", false, nil)
}
