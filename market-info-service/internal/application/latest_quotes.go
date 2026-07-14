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

// LatestQuotesInput identifies an Asset or Instrument and optionally narrows
// the result to one Provider. At least one readable market identity is needed.
type LatestQuotesInput struct {
	AssetCode      string
	InstrumentCode string
	ProviderCode   string
}

// LatestQuoteCatalog resolves readable filters before querying market data.
// Unknown filters are therefore distinguishable from a valid empty result.
type LatestQuoteCatalog interface {
	FindAssetByCode(context.Context, string) (domain.Asset, error)
	FindAssetByID(context.Context, domain.ID) (domain.Asset, error)
	FindInstrumentByCode(context.Context, string) (domain.Instrument, error)
	FindProviderByCode(context.Context, string) (domain.Provider, error)
}

// LatestQuoteFilter is the query-specific storage contract.
type LatestQuoteFilter struct {
	AssetID      domain.ID
	InstrumentID *domain.ID
	ProviderID   *domain.ID
	EffectiveAt  time.Time
}

// LatestQuoteRecord combines a source-specific quote with readable catalog
// identities required by public clients.
type LatestQuoteRecord struct {
	InstrumentCode         domain.Code
	QuoteCurrency          string
	ProviderID             domain.ID
	ProviderCode           domain.Code
	ProviderInstrumentCode domain.Code
	ProviderSymbol         string
	Quote                  domain.LatestQuote
}

// LatestQuoteReader is implemented by a read-optimized PostgreSQL join.
type LatestQuoteReader interface {
	ListLatestQuoteRecords(context.Context, LatestQuoteFilter) ([]LatestQuoteRecord, error)
}

// LatestQuotesResult preserves every source row independently.
type LatestQuotesResult struct {
	Asset  domain.Asset
	Quotes []LatestQuoteRecord
}

// LatestQuotesService resolves filters and lists already-persisted snapshots.
type LatestQuotesService struct {
	catalog LatestQuoteCatalog
	reader  LatestQuoteReader
	now     func() time.Time
}

// NewLatestQuotesService constructs the public latest-quote query service.
func NewLatestQuotesService(catalog LatestQuoteCatalog, reader LatestQuoteReader, now func() time.Time) (*LatestQuotesService, error) {
	if catalog == nil || reader == nil || now == nil {
		return nil, errors.New("latest quote dependencies are required")
	}
	return &LatestQuotesService{catalog: catalog, reader: reader, now: now}, nil
}

// List returns one result row per ProviderInstrument and never merges sources.
func (service *LatestQuotesService) List(ctx context.Context, input LatestQuotesInput) (LatestQuotesResult, error) {
	parsed, err := parseLatestQuotesInput(input)
	if err != nil {
		return LatestQuotesResult{}, err
	}

	asset, instrument, err := service.resolveMarketIdentity(ctx, parsed)
	if err != nil {
		return LatestQuotesResult{}, err
	}
	provider, err := service.resolveProvider(ctx, parsed.providerCode)
	if err != nil {
		return LatestQuotesResult{}, err
	}

	result := LatestQuotesResult{Asset: asset, Quotes: []LatestQuoteRecord{}}
	effectiveAt := domain.UTC(service.now())
	if asset.Status != domain.AssetStatusActive ||
		(instrument != nil && (instrument.Status != domain.InstrumentStatusActive || !effectiveAtWithin(effectiveAt, instrument.ValidFrom, instrument.ValidTo))) ||
		(provider != nil && provider.Status == domain.ProviderStatusDisabled) {
		return result, nil
	}

	filter := LatestQuoteFilter{AssetID: asset.ID, EffectiveAt: effectiveAt}
	if instrument != nil {
		id := instrument.ID
		filter.InstrumentID = &id
	}
	if provider != nil {
		id := provider.ID
		filter.ProviderID = &id
	}
	records, err := service.reader.ListLatestQuoteRecords(ctx, filter)
	if err != nil {
		return LatestQuotesResult{}, classifyLatestQuoteFailure(err)
	}
	if err := validateLatestQuoteRecords(records, filter); err != nil {
		return LatestQuotesResult{}, classifyLatestQuoteFailure(err)
	}
	sort.SliceStable(records, func(left, right int) bool {
		if records[left].InstrumentCode != records[right].InstrumentCode {
			return records[left].InstrumentCode.String() < records[right].InstrumentCode.String()
		}
		if records[left].ProviderCode != records[right].ProviderCode {
			return records[left].ProviderCode.String() < records[right].ProviderCode.String()
		}
		return records[left].ProviderInstrumentCode.String() < records[right].ProviderInstrumentCode.String()
	})
	result.Quotes = records
	return result, nil
}

type parsedLatestQuotesInput struct {
	assetCode      *domain.Code
	instrumentCode *domain.Code
	providerCode   *domain.Code
}

func parseLatestQuotesInput(input LatestQuotesInput) (parsedLatestQuotesInput, error) {
	violations := make([]FieldViolation, 0, 3)
	parsed := parsedLatestQuotesInput{}
	if input.AssetCode == "" && input.InstrumentCode == "" {
		violations = append(violations,
			FieldViolation{Field: "asset_code", Reason: "asset_code or instrument_code is required"},
			FieldViolation{Field: "instrument_code", Reason: "asset_code or instrument_code is required"},
		)
	}
	if input.AssetCode != "" {
		code, err := domain.ParseCode(input.AssetCode)
		if err != nil || !strings.HasPrefix(input.AssetCode, "asset.") {
			violations = append(violations, FieldViolation{Field: "asset_code", Reason: "must be a valid asset code"})
		} else {
			parsed.assetCode = &code
		}
	}
	if input.InstrumentCode != "" {
		code, err := domain.ParseCode(input.InstrumentCode)
		if err != nil || !strings.HasPrefix(input.InstrumentCode, "instrument.") {
			violations = append(violations, FieldViolation{Field: "instrument_code", Reason: "must be a valid instrument code"})
		} else {
			parsed.instrumentCode = &code
		}
	}
	if input.ProviderCode != "" {
		code, err := domain.ParseCode(input.ProviderCode)
		if err != nil {
			violations = append(violations, FieldViolation{Field: "provider", Reason: "must be a valid provider code"})
		} else {
			parsed.providerCode = &code
		}
	}
	if len(violations) > 0 {
		return parsedLatestQuotesInput{}, ValidationError(violations)
	}
	return parsed, nil
}

func (service *LatestQuotesService) resolveMarketIdentity(ctx context.Context, input parsedLatestQuotesInput) (domain.Asset, *domain.Instrument, error) {
	var asset domain.Asset
	var instrument *domain.Instrument
	if input.assetCode != nil {
		loaded, err := service.catalog.FindAssetByCode(ctx, input.assetCode.String())
		if err != nil {
			return domain.Asset{}, nil, mapCatalogQueryError(err, ErrorCodeAssetNotFound, "asset not found")
		}
		if loaded.Code != *input.assetCode {
			return domain.Asset{}, nil, classifyLatestQuoteFailure(domain.ErrInvalidData)
		}
		asset = loaded
	}
	if input.instrumentCode != nil {
		loaded, err := service.catalog.FindInstrumentByCode(ctx, input.instrumentCode.String())
		if err != nil {
			return domain.Asset{}, nil, mapCatalogQueryError(err, ErrorCodeInstrumentNotFound, "instrument not found")
		}
		if loaded.Code != *input.instrumentCode {
			return domain.Asset{}, nil, classifyLatestQuoteFailure(domain.ErrInvalidData)
		}
		instrument = &loaded
		if input.assetCode == nil {
			asset, err = service.catalog.FindAssetByID(ctx, loaded.AssetID)
			if err != nil {
				return domain.Asset{}, nil, classifyLatestQuoteFailure(err)
			}
			if asset.ID != loaded.AssetID {
				return domain.Asset{}, nil, classifyLatestQuoteFailure(domain.ErrInvalidData)
			}
		}
	}
	if instrument != nil && instrument.AssetID != asset.ID {
		return domain.Asset{}, nil, ValidationError([]FieldViolation{{Field: "instrument_code", Reason: "does not belong to asset_code"}})
	}
	return asset, instrument, nil
}

func (service *LatestQuotesService) resolveProvider(ctx context.Context, code *domain.Code) (*domain.Provider, error) {
	if code == nil {
		return nil, nil
	}
	provider, err := service.catalog.FindProviderByCode(ctx, code.String())
	if errors.Is(err, domain.ErrNotFound) {
		return nil, ValidationError([]FieldViolation{{Field: "provider", Reason: "does not identify a known provider"}})
	}
	if err != nil {
		return nil, classifyLatestQuoteFailure(err)
	}
	if provider.Code != *code {
		return nil, classifyLatestQuoteFailure(domain.ErrInvalidData)
	}
	return &provider, nil
}

func validateLatestQuoteRecords(records []LatestQuoteRecord, filter LatestQuoteFilter) error {
	for _, record := range records {
		if err := record.Quote.Validate(); err != nil {
			return err
		}
		if !strings.HasPrefix(record.InstrumentCode.String(), "instrument.") ||
			!strings.HasPrefix(record.ProviderInstrumentCode.String(), "provider.") ||
			record.ProviderID.IsZero() || record.ProviderCode.IsZero() ||
			strings.TrimSpace(record.ProviderSymbol) == "" || strings.TrimSpace(record.QuoteCurrency) == "" {
			return fmt.Errorf("invalid latest quote projection: %w", domain.ErrInvalidData)
		}
		if filter.InstrumentID != nil && record.Quote.InstrumentID != *filter.InstrumentID {
			return fmt.Errorf("latest quote instrument does not match filter: %w", domain.ErrInvalidData)
		}
		if filter.ProviderID != nil && record.ProviderID != *filter.ProviderID {
			return fmt.Errorf("latest quote provider does not match filter: %w", domain.ErrInvalidData)
		}
	}
	return nil
}

func effectiveAtWithin(at time.Time, from, to *time.Time) bool {
	if at.IsZero() {
		return false
	}
	return (from == nil || !at.Before(*from)) && (to == nil || at.Before(*to))
}

func mapCatalogQueryError(err error, notFoundCode ErrorCode, message string) error {
	if errors.Is(err, domain.ErrNotFound) {
		return WrapError(err, notFoundCode, message, false, nil)
	}
	return classifyLatestQuoteFailure(err)
}

func classifyLatestQuoteFailure(err error) error {
	if errors.Is(err, domain.ErrDatabaseUnavailable) || errors.Is(err, domain.ErrRetryable) {
		return err
	}
	return WrapError(err, ErrorCodeInternal, "internal server error", false, nil)
}
