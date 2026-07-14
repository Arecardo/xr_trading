package domain

import (
	"context"
	"encoding/json"
	"fmt"
	"time"
	"unicode/utf8"
)

// QualityStatus classifies validation results without hiding questionable
// market data from operational inspection.
type QualityStatus string

const (
	QualityStatusUnchecked QualityStatus = "unchecked"
	QualityStatusValid     QualityStatus = "valid"
	QualityStatusWarning   QualityStatus = "warning"
	QualityStatusInvalid   QualityStatus = "invalid"
)

// ParseQualityStatus validates a market-data quality classification.
func ParseQualityStatus(value string) (QualityStatus, error) {
	parsed := QualityStatus(value)
	switch parsed {
	case QualityStatusUnchecked, QualityStatusValid, QualityStatusWarning, QualityStatusInvalid:
		return parsed, nil
	default:
		return "", invalidData("unsupported market-data quality status")
	}
}

// MarshalJSON encodes QualityStatus as a JSON string.
func (status QualityStatus) MarshalJSON() ([]byte, error) {
	return marshalCatalogEnum(string(status), "quality status")
}

// UnmarshalJSON validates a JSON quality status.
func (status *QualityStatus) UnmarshalJSON(data []byte) error {
	if status == nil {
		return fmt.Errorf("unmarshal quality status: nil receiver")
	}
	return unmarshalCatalogEnum(data, func(text string) error {
		parsed, err := ParseQualityStatus(text)
		*status = parsed
		return err
	})
}

// MarketDataSource is the storage identity of one concrete market-data source.
// ProviderInstrumentID is the source; InstrumentID prevents prices for
// different venues or pairs from being merged under one Asset.
type MarketDataSource struct {
	InstrumentID         ID
	ProviderInstrumentID ID
}

// NewMarketDataSource validates a complete source identity.
func NewMarketDataSource(instrumentID, providerInstrumentID ID) (MarketDataSource, error) {
	source := MarketDataSource{InstrumentID: instrumentID, ProviderInstrumentID: providerInstrumentID}
	if err := source.Validate(); err != nil {
		return MarketDataSource{}, err
	}
	return source, nil
}

// Validate rejects asset-only or provider-only market-data identities.
func (source MarketDataSource) Validate() error {
	if source.InstrumentID.IsZero() || source.ProviderInstrumentID.IsZero() {
		return invalidData("market-data source requires instrument and provider instrument IDs")
	}
	return nil
}

// ValidateProviderInstrument verifies that the source uses the expected
// provider mapping. The database preserves both IDs on every market-data row.
func (source MarketDataSource) ValidateProviderInstrument(mapping ProviderInstrument) error {
	if err := source.Validate(); err != nil {
		return err
	}
	if err := mapping.Validate(); err != nil {
		return err
	}
	if source.InstrumentID != mapping.InstrumentID || source.ProviderInstrumentID != mapping.ID {
		return invalidData("market-data source does not match provider instrument mapping")
	}
	return nil
}

// TimeRange is an optional-bounds UTC range. A present end is always exclusive,
// so adjacent [start,end) queries cannot return the same bar twice.
type TimeRange struct {
	Start *UTCInstant
	End   *UTCInstant
}

// NewTimeRange normalizes optional time bounds and validates their order.
func NewTimeRange(start, end *time.Time) (TimeRange, error) {
	rangeValue := TimeRange{}
	if start != nil {
		parsed, err := NewUTCInstant(*start)
		if err != nil {
			return TimeRange{}, fmt.Errorf("construct time range start: %w", err)
		}
		rangeValue.Start = &parsed
	}
	if end != nil {
		parsed, err := NewUTCInstant(*end)
		if err != nil {
			return TimeRange{}, fmt.Errorf("construct time range end: %w", err)
		}
		rangeValue.End = &parsed
	}
	if err := rangeValue.Validate(); err != nil {
		return TimeRange{}, err
	}
	return rangeValue, nil
}

// NewBoundedTimeRange constructs a range with both required bounds.
func NewBoundedTimeRange(start, end time.Time) (TimeRange, error) {
	return NewTimeRange(&start, &end)
}

// Validate checks initialized bounds and start < end when both are present.
func (rangeValue TimeRange) Validate() error {
	if rangeValue.Start != nil && rangeValue.Start.IsZero() {
		return invalidData("time range start cannot be zero")
	}
	if rangeValue.End != nil && rangeValue.End.IsZero() {
		return invalidData("time range end cannot be zero")
	}
	if rangeValue.Start != nil && rangeValue.End != nil && !rangeValue.Start.Time().Before(rangeValue.End.Time()) {
		return invalidData("time range start must be before end")
	}
	return nil
}

// Contains applies inclusive-start and exclusive-end semantics.
func (rangeValue TimeRange) Contains(instant UTCInstant) bool {
	if instant.IsZero() || rangeValue.Validate() != nil {
		return false
	}
	if rangeValue.Start != nil && instant.Time().Before(rangeValue.Start.Time()) {
		return false
	}
	return rangeValue.End == nil || instant.Time().Before(rangeValue.End.Time())
}

// IsBounded reports whether both bounds are present.
func (rangeValue TimeRange) IsBounded() bool {
	return rangeValue.Start != nil && rangeValue.End != nil
}

// Quote is the newest snapshot from one ProviderInstrument. It is never shared
// with another source, even when both sources describe the same Instrument.
type Quote struct {
	InstrumentID         ID
	ProviderInstrumentID ID
	MarketTime           UTCInstant
	LastPrice            Decimal
	BidPrice             *Decimal
	BidSize              *Decimal
	AskPrice             *Decimal
	AskSize              *Decimal
	Open24H              *Decimal
	High24H              *Decimal
	Low24H               *Decimal
	BaseVolume24H        *Decimal
	QuoteVolume24H       *Decimal
	QualityStatus        QualityStatus
	CollectedAt          UTCInstant
	Metadata             json.RawMessage
}

// LatestQuote names the latest-quote storage projection without introducing a
// second domain shape.
type LatestQuote = Quote

// NewQuote normalizes metadata and validates a source-specific quote.
func NewQuote(quote Quote) (Quote, error) {
	quote.Metadata = normalizedJSON(quote.Metadata)
	if err := quote.Validate(); err != nil {
		return Quote{}, err
	}
	return quote, nil
}

// Source returns the quote's complete storage identity.
func (quote Quote) Source() MarketDataSource {
	return MarketDataSource{InstrumentID: quote.InstrumentID, ProviderInstrumentID: quote.ProviderInstrumentID}
}

// Validate checks quote identity, time, price, volume and quality invariants.
func (quote Quote) Validate() error {
	if err := quote.Source().Validate(); err != nil {
		return err
	}
	if quote.MarketTime.IsZero() || quote.CollectedAt.IsZero() {
		return invalidData("quote market and collection times are required")
	}
	if quote.LastPrice.Exact().IsNegative() {
		return invalidData("quote last price cannot be negative")
	}
	for _, item := range []struct {
		value *Decimal
		name  string
	}{
		{quote.BidPrice, "bid price"}, {quote.BidSize, "bid size"},
		{quote.AskPrice, "ask price"}, {quote.AskSize, "ask size"},
		{quote.Open24H, "24h open"}, {quote.High24H, "24h high"},
		{quote.Low24H, "24h low"}, {quote.BaseVolume24H, "24h base volume"},
		{quote.QuoteVolume24H, "24h quote volume"},
	} {
		if item.value != nil && item.value.Exact().IsNegative() {
			return invalidData("quote " + item.name + " cannot be negative")
		}
	}
	if quote.BidPrice != nil && quote.AskPrice != nil && quote.BidPrice.Exact().GreaterThan(quote.AskPrice.Exact()) {
		return invalidData("quote bid price cannot exceed ask price")
	}
	if err := validateOptionalOHLC(quote.Open24H, quote.High24H, quote.Low24H); err != nil {
		return err
	}
	if _, err := ParseQualityStatus(string(quote.QualityStatus)); err != nil {
		return err
	}
	if !isJSONObject(quote.Metadata) {
		return invalidData("quote metadata must be a JSON object")
	}
	return nil
}

// Bar is one versioned OHLCV bar from a ProviderInstrument.
type Bar struct {
	InstrumentID         ID
	ProviderInstrumentID ID
	Interval             BarInterval
	OpenTime             UTCInstant
	Revision             int
	CloseTime            UTCInstant
	OpenPrice            Decimal
	HighPrice            Decimal
	LowPrice             Decimal
	ClosePrice           Decimal
	BaseVolume           *Decimal
	QuoteVolume          *Decimal
	TradeCount           *int64
	IsClosed             bool
	IsCurrent            bool
	QualityStatus        QualityStatus
	ProviderUpdatedAt    *UTCInstant
	CollectedAt          UTCInstant
	RawHash              string
	Metadata             json.RawMessage
}

// MarketBar names the persisted market-bar projection without duplicating Bar.
type MarketBar = Bar

// NewBar validates an incoming normalized bar. Revision zero is accepted
// because Repository assigns the first persistent revision atomically.
func NewBar(bar Bar) (Bar, error) {
	bar.Metadata = normalizedJSON(bar.Metadata)
	if err := bar.Validate(); err != nil {
		return Bar{}, err
	}
	return bar, nil
}

// NewStoredBar additionally requires a positive persisted revision.
func NewStoredBar(bar Bar) (Bar, error) {
	bar, err := NewBar(bar)
	if err != nil {
		return Bar{}, err
	}
	if bar.Revision <= 0 {
		return Bar{}, invalidData("stored bar revision must be positive")
	}
	return bar, nil
}

// Source returns the bar's complete storage identity.
func (bar Bar) Source() MarketDataSource {
	return MarketDataSource{InstrumentID: bar.InstrumentID, ProviderInstrumentID: bar.ProviderInstrumentID}
}

// Validate checks source, interval, OHLCV, time and closure invariants.
func (bar Bar) Validate() error {
	if err := bar.Source().Validate(); err != nil {
		return err
	}
	if _, err := ParseBarInterval(string(bar.Interval)); err != nil {
		return err
	}
	if bar.OpenTime.IsZero() || bar.CloseTime.IsZero() || bar.CollectedAt.IsZero() {
		return invalidData("bar open, close and collection times are required")
	}
	if !bar.OpenTime.Time().Before(bar.CloseTime.Time()) {
		return invalidData("bar close time must be after open time")
	}
	if bar.Revision < 0 {
		return invalidData("bar revision cannot be negative")
	}
	if err := validateOHLC(bar.OpenPrice, bar.HighPrice, bar.LowPrice, bar.ClosePrice); err != nil {
		return err
	}
	if bar.BaseVolume != nil && bar.BaseVolume.Exact().IsNegative() {
		return invalidData("bar base volume cannot be negative")
	}
	if bar.QuoteVolume != nil && bar.QuoteVolume.Exact().IsNegative() {
		return invalidData("bar quote volume cannot be negative")
	}
	if bar.TradeCount != nil && *bar.TradeCount < 0 {
		return invalidData("bar trade count cannot be negative")
	}
	if bar.IsClosed && bar.CollectedAt.Time().Before(bar.CloseTime.Time()) {
		return invalidData("closed bar cannot be collected before close time")
	}
	if bar.ProviderUpdatedAt != nil && bar.ProviderUpdatedAt.IsZero() {
		return invalidData("bar provider update time cannot be zero")
	}
	if _, err := ParseQualityStatus(string(bar.QualityStatus)); err != nil {
		return err
	}
	if utf8.RuneCountInString(bar.RawHash) > 64 {
		return invalidData("bar raw hash exceeds its maximum length")
	}
	if !isJSONObject(bar.Metadata) {
		return invalidData("bar metadata must be a JSON object")
	}
	return nil
}

// MarketBarFilter identifies one source and interval; those identity fields
// are required so K-line reads cannot silently combine providers.
type MarketBarFilter struct {
	InstrumentID         ID
	ProviderInstrumentID ID
	Interval             BarInterval
	Range                TimeRange
	BeforeOpenTime       *UTCInstant
	Limit                int
}

// Validate checks the domain portion of a market-bar filter. Repository owns
// storage-specific page-size limits.
func (filter MarketBarFilter) Validate() error {
	if err := (MarketDataSource{InstrumentID: filter.InstrumentID, ProviderInstrumentID: filter.ProviderInstrumentID}).Validate(); err != nil {
		return err
	}
	if _, err := ParseBarInterval(string(filter.Interval)); err != nil {
		return err
	}
	if err := filter.Range.Validate(); err != nil {
		return err
	}
	if filter.BeforeOpenTime != nil && filter.BeforeOpenTime.IsZero() {
		return invalidData("market bar cursor cannot be zero")
	}
	return nil
}

// MarketBarWriteResult reports whether a write created a current revision.
type MarketBarWriteResult struct {
	Applied  bool
	Revision int
}

// MarketDataRepository persists source-specific quotes and versioned bars.
type MarketDataRepository interface {
	UpsertLatestQuote(context.Context, LatestQuote) (bool, error)
	ListLatestQuotes(context.Context, ID) ([]LatestQuote, error)
	WriteMarketBar(context.Context, MarketBar) (MarketBarWriteResult, error)
	ListCurrentMarketBars(context.Context, MarketBarFilter) ([]MarketBar, error)
}

func validateOptionalOHLC(open, high, low *Decimal) error {
	if high != nil && low != nil && high.Exact().LessThan(low.Exact()) {
		return invalidData("quote 24h high cannot be below low")
	}
	if open != nil && high != nil && open.Exact().GreaterThan(high.Exact()) {
		return invalidData("quote 24h open cannot exceed high")
	}
	if open != nil && low != nil && open.Exact().LessThan(low.Exact()) {
		return invalidData("quote 24h open cannot be below low")
	}
	return nil
}

func validateOHLC(open, high, low, close Decimal) error {
	if open.Exact().IsNegative() || high.Exact().IsNegative() || low.Exact().IsNegative() || close.Exact().IsNegative() {
		return invalidData("bar prices cannot be negative")
	}
	if high.Exact().LessThan(low.Exact()) || open.Exact().GreaterThan(high.Exact()) || close.Exact().GreaterThan(high.Exact()) || open.Exact().LessThan(low.Exact()) || close.Exact().LessThan(low.Exact()) {
		return invalidData("bar OHLC prices are inconsistent")
	}
	return nil
}
