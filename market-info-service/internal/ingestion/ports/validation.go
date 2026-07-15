package ports

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"unicode/utf8"

	"xr-trading/market-info-service/internal/domain"
)

const maximumAdapterCursorLength = 2048

var providerMarketPattern = regexp.MustCompile(`^[a-z0-9]+(?:[._-][a-z0-9]+)*$`)

// Validate checks adapter-wide capabilities for contradictory declarations.
func (capabilities ProviderCapabilities) Validate() error {
	if capabilities.ProviderCode.IsZero() {
		return invalidContract("provider code is required")
	}
	if len(capabilities.Markets) == 0 {
		return invalidContract("at least one provider market is required")
	}
	seen := make(map[string]struct{}, len(capabilities.Markets))
	for index, market := range capabilities.Markets {
		if err := market.Validate(); err != nil {
			return fmt.Errorf("validate provider market %d: %w", index, err)
		}
		if _, exists := seen[market.ProviderMarket]; exists {
			return invalidContract("provider market %q is duplicated", market.ProviderMarket)
		}
		seen[market.ProviderMarket] = struct{}{}
	}
	return nil
}

// Validate checks one market capability and its operation-specific limits.
func (capability ProviderMarketCapability) Validate() error {
	if err := validateProviderMarket(capability.ProviderMarket); err != nil {
		return err
	}
	if err := validateAssetTypes(capability.AssetTypes); err != nil {
		return err
	}
	if err := validateInstrumentTypes(capability.InstrumentTypes); err != nil {
		return err
	}
	if !capability.SupportsQuote && !capability.SupportsBars {
		return invalidContract("provider market must support quote or bars")
	}
	if capability.SupportsQuote {
		if capability.MaxBatchSize <= 0 {
			return invalidContract("quote capability requires a positive max batch size")
		}
	} else if capability.MaxBatchSize != 0 {
		return invalidContract("max batch size requires quote capability")
	}
	if capability.SupportsBars {
		if capability.MaxBarsPerRequest <= 0 || len(capability.Intervals) == 0 {
			return invalidContract("bar capability requires intervals and a positive request limit")
		}
	} else if capability.MaxBarsPerRequest != 0 || len(capability.Intervals) != 0 {
		return invalidContract("bar intervals and limit require bar capability")
	}
	seenIntervals := make(map[domain.BarInterval]struct{}, len(capability.Intervals))
	for _, interval := range capability.Intervals {
		parsed, err := domain.ParseBarInterval(string(interval))
		if err != nil || parsed != interval {
			return invalidContract("provider market contains an unsupported interval")
		}
		if _, exists := seenIntervals[interval]; exists {
			return invalidContract("provider market contains a duplicate interval")
		}
		seenIntervals[interval] = struct{}{}
	}
	return nil
}

// Validate checks identity, readable diagnostics and provider request fields.
func (reference ProviderInstrumentRef) Validate() error {
	if reference.ProviderInstrumentID.IsZero() || reference.InstrumentID.IsZero() || reference.AssetID.IsZero() {
		return invalidContract("provider instrument, instrument and asset IDs are required")
	}
	if !strings.HasPrefix(reference.ProviderInstrumentCode.String(), "provider.") {
		return invalidContract("provider instrument code is invalid")
	}
	if !strings.HasPrefix(reference.InstrumentCode.String(), "instrument.") {
		return invalidContract("instrument code is invalid")
	}
	if reference.ProviderCode.IsZero() {
		return invalidContract("provider code is required")
	}
	if err := validateProviderMarket(reference.ProviderMarket); err != nil {
		return err
	}
	if _, err := domain.ParseAssetType(string(reference.AssetType)); err != nil {
		return invalidContract("provider instrument asset type is unsupported")
	}
	if _, err := domain.ParseInstrumentType(string(reference.InstrumentType)); err != nil {
		return invalidContract("provider instrument type is unsupported")
	}
	if err := validateAdapterText(reference.ExternalSymbol, 128, "external symbol"); err != nil {
		return err
	}
	if err := validateAdapterText(reference.InstrumentSymbol, 64, "instrument symbol"); err != nil {
		return err
	}
	if err := validateAdapterText(reference.QuoteCurrency, 16, "quote currency"); err != nil {
		return err
	}
	if !validOptionalJSONObject(reference.Metadata) {
		return invalidContract("provider instrument metadata must be a JSON object")
	}
	return nil
}

// Validate checks the normalized quote independently of one request batch.
func (quote ProviderQuote) Validate() error {
	if quote.AssetID.IsZero() || quote.ProviderCode.IsZero() {
		return invalidContract("quote asset ID and provider code are required")
	}
	_, err := domain.NewQuote(domain.Quote{
		InstrumentID: quote.InstrumentID, ProviderInstrumentID: quote.ProviderInstrumentID,
		MarketTime: quote.MarketTime, LastPrice: quote.LastPrice,
		BidPrice: quote.BidPrice, BidSize: quote.BidSize, AskPrice: quote.AskPrice, AskSize: quote.AskSize,
		Open24H: quote.Open24H, High24H: quote.High24H, Low24H: quote.Low24H,
		BaseVolume24H: quote.BaseVolume24H, QuoteVolume24H: quote.QuoteVolume24H,
		QualityStatus: domain.QualityStatusUnchecked, CollectedAt: quote.ReceivedAt, Metadata: json.RawMessage(`{}`),
	})
	if err != nil {
		return fmt.Errorf("validate provider quote: %w", err)
	}
	if !validOptionalJSON(quote.RawPayload) {
		return invalidContract("quote raw payload is invalid JSON")
	}
	return nil
}

// ValidateFor checks that a quote belongs to the exact requested mapping.
func (quote ProviderQuote) ValidateFor(reference ProviderInstrumentRef) error {
	if err := reference.Validate(); err != nil {
		return err
	}
	if err := quote.Validate(); err != nil {
		return err
	}
	if quote.ProviderInstrumentID != reference.ProviderInstrumentID || quote.InstrumentID != reference.InstrumentID ||
		quote.AssetID != reference.AssetID || quote.ProviderCode != reference.ProviderCode {
		return invalidContract("quote does not match provider instrument reference")
	}
	return nil
}

// ValidateLatestQuoteBatch rejects unrequested and duplicate source results.
// A requested source may be absent when the provider has no current snapshot.
func ValidateLatestQuoteBatch(providerCode domain.Code, references []ProviderInstrumentRef, quotes []ProviderQuote) error {
	if providerCode.IsZero() || len(references) == 0 {
		return invalidContract("provider code and quote references are required")
	}
	requested := make(map[domain.ID]ProviderInstrumentRef, len(references))
	for _, reference := range references {
		if err := reference.Validate(); err != nil {
			return err
		}
		if reference.ProviderCode != providerCode {
			return invalidContract("quote reference belongs to another provider")
		}
		if _, exists := requested[reference.ProviderInstrumentID]; exists {
			return invalidContract("quote reference is duplicated")
		}
		requested[reference.ProviderInstrumentID] = reference
	}
	seen := make(map[domain.ID]struct{}, len(quotes))
	for _, quote := range quotes {
		reference, exists := requested[quote.ProviderInstrumentID]
		if !exists {
			return invalidContract("quote result was not requested")
		}
		if _, duplicate := seen[quote.ProviderInstrumentID]; duplicate {
			return invalidContract("quote result is duplicated")
		}
		if err := quote.ValidateFor(reference); err != nil {
			return err
		}
		seen[quote.ProviderInstrumentID] = struct{}{}
	}
	return nil
}

// Validate checks a bounded bar request. Adapter-specific maximum limits are
// checked against Capabilities by the registry/ingestion layer.
func (request FetchBarsRequest) Validate() error {
	if err := request.Instrument.Validate(); err != nil {
		return err
	}
	if _, err := domain.ParseBarInterval(string(request.Interval)); err != nil {
		return invalidContract("bar interval is unsupported")
	}
	if request.StartTime.IsZero() || request.EndTime.IsZero() || !request.StartTime.Time().Before(request.EndTime.Time()) {
		return invalidContract("bar request requires a bounded increasing time range")
	}
	if request.Limit <= 0 {
		return invalidContract("bar request limit must be positive")
	}
	if len(request.Cursor) > maximumAdapterCursorLength {
		return invalidContract("bar request cursor is too long")
	}
	return nil
}

// Validate checks source identity, ascending order, range and cursor progress.
func (result FetchBarsResult) Validate(request FetchBarsRequest) error {
	if err := request.Validate(); err != nil {
		return err
	}
	if len(result.Bars) > request.Limit {
		return invalidContract("bar result exceeds request limit")
	}
	if result.HasMore {
		if result.NextCursor == "" || result.NextCursor == request.Cursor || len(result.Bars) == 0 {
			return invalidContract("bar result does not provide a progressing next cursor")
		}
		if len(result.NextCursor) > maximumAdapterCursorLength {
			return invalidContract("bar result cursor is too long")
		}
	} else if result.NextCursor != "" {
		return invalidContract("terminal bar result cannot contain a next cursor")
	}
	for index, bar := range result.Bars {
		if err := bar.ValidateFor(request.Instrument); err != nil {
			return err
		}
		if bar.Interval != request.Interval || bar.OpenTime.Time().Before(request.StartTime.Time()) || !bar.OpenTime.Time().Before(request.EndTime.Time()) {
			return invalidContract("bar result does not match interval or requested range")
		}
		if index > 0 && !bar.OpenTime.Time().After(result.Bars[index-1].OpenTime.Time()) {
			return invalidContract("bar result must be strictly ascending by open time")
		}
	}
	return nil
}

// Validate checks the normalized bar independently of a page.
func (bar ProviderBar) Validate() error {
	if bar.AssetID.IsZero() || bar.ProviderCode.IsZero() {
		return invalidContract("bar asset ID and provider code are required")
	}
	_, err := domain.NewBar(domain.Bar{
		InstrumentID: bar.InstrumentID, ProviderInstrumentID: bar.ProviderInstrumentID,
		Interval: bar.Interval, OpenTime: bar.OpenTime, CloseTime: bar.CloseTime,
		OpenPrice: bar.Open, HighPrice: bar.High, LowPrice: bar.Low, ClosePrice: bar.Close,
		BaseVolume: bar.BaseVolume, QuoteVolume: bar.QuoteVolume, TradeCount: bar.TradeCount,
		IsClosed: bar.IsClosed, QualityStatus: domain.QualityStatusUnchecked,
		ProviderUpdatedAt: bar.ProviderUpdatedAt, CollectedAt: bar.ReceivedAt, Metadata: json.RawMessage(`{}`),
	})
	if err != nil {
		return fmt.Errorf("validate provider bar: %w", err)
	}
	if !validOptionalJSON(bar.RawPayload) {
		return invalidContract("bar raw payload is invalid JSON")
	}
	return nil
}

// ValidateFor checks that a bar belongs to the exact requested mapping.
func (bar ProviderBar) ValidateFor(reference ProviderInstrumentRef) error {
	if err := reference.Validate(); err != nil {
		return err
	}
	if err := bar.Validate(); err != nil {
		return err
	}
	if bar.ProviderInstrumentID != reference.ProviderInstrumentID || bar.InstrumentID != reference.InstrumentID ||
		bar.AssetID != reference.AssetID || bar.ProviderCode != reference.ProviderCode {
		return invalidContract("bar does not match provider instrument reference")
	}
	return nil
}

func validateAssetTypes(values []domain.AssetType) error {
	if len(values) == 0 {
		return invalidContract("provider market asset types are required")
	}
	seen := make(map[domain.AssetType]struct{}, len(values))
	for _, value := range values {
		parsed, err := domain.ParseAssetType(string(value))
		if err != nil || parsed != value {
			return invalidContract("provider market contains an unsupported asset type")
		}
		if _, exists := seen[value]; exists {
			return invalidContract("provider market contains a duplicate asset type")
		}
		seen[value] = struct{}{}
	}
	return nil
}

func validateInstrumentTypes(values []domain.InstrumentType) error {
	if len(values) == 0 {
		return invalidContract("provider market instrument types are required")
	}
	seen := make(map[domain.InstrumentType]struct{}, len(values))
	for _, value := range values {
		parsed, err := domain.ParseInstrumentType(string(value))
		if err != nil || parsed != value {
			return invalidContract("provider market contains an unsupported instrument type")
		}
		if _, exists := seen[value]; exists {
			return invalidContract("provider market contains a duplicate instrument type")
		}
		seen[value] = struct{}{}
	}
	return nil
}

func validateProviderMarket(value string) error {
	if utf8.RuneCountInString(value) > 32 || !providerMarketPattern.MatchString(value) {
		return invalidContract("provider market is invalid")
	}
	return nil
}

func validateAdapterText(value string, maximum int, field string) error {
	if value == "" || strings.TrimSpace(value) != value || utf8.RuneCountInString(value) > maximum {
		return invalidContract("%s is invalid", field)
	}
	return nil
}

func validOptionalJSONObject(raw json.RawMessage) bool {
	if len(raw) == 0 {
		return true
	}
	var value map[string]json.RawMessage
	return json.Unmarshal(raw, &value) == nil && value != nil
}

func validOptionalJSON(raw json.RawMessage) bool {
	return len(raw) == 0 || json.Valid(raw)
}

func invalidContract(format string, args ...any) error {
	return fmt.Errorf("adapter contract: %s: %w", fmt.Sprintf(format, args...), domain.ErrInvalidData)
}
