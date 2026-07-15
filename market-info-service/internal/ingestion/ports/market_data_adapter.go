// Package ports defines provider-independent boundaries used by ingestion.
// Concrete provider implementations live under internal/providers and must not
// leak SDK or wire-format types through these contracts.
package ports

import (
	"context"
	"encoding/json"

	"xr-trading/market-info-service/internal/domain"
)

// MarketDataAdapter is the provider-independent market-data acquisition port.
type MarketDataAdapter interface {
	ProviderCode() domain.Code
	Capabilities(context.Context) (ProviderCapabilities, error)
	FetchLatestQuotes(context.Context, []ProviderInstrumentRef) ([]ProviderQuote, error)
	FetchBars(context.Context, FetchBarsRequest) (FetchBarsResult, error)
}

// AdapterRegistry is the provider-independent lookup and capability-validation
// boundary used by schedulers and ingestion services. Implementations must be
// safe for concurrent reads after construction.
type AdapterRegistry interface {
	Get(domain.Code) (MarketDataAdapter, bool)
	List() []MarketDataAdapter
	Capabilities(domain.Code) (ProviderCapabilities, bool)
	ValidateLatestQuoteRequest(domain.Code, []ProviderInstrumentRef) error
	ValidateBarsRequest(FetchBarsRequest) error
}

// ProviderCapabilities describes the markets and limits implemented by one
// adapter. ProviderInstrument capabilities remain the authoritative per-symbol
// configuration; this declaration protects startup and task generation.
type ProviderCapabilities struct {
	ProviderCode domain.Code
	Markets      []ProviderMarketCapability
}

// ProviderMarketCapability declares one provider-market boundary.
type ProviderMarketCapability struct {
	ProviderMarket    string
	AssetTypes        []domain.AssetType
	InstrumentTypes   []domain.InstrumentType
	SupportsQuote     bool
	SupportsBars      bool
	Intervals         []domain.BarInterval
	MaxBatchSize      int
	MaxBarsPerRequest int
}

// ProviderInstrumentRef is the complete identity and provider mapping needed
// for an external request. ProviderInstrumentID is the persisted source
// identity; AssetID is supporting context and never replaces it.
type ProviderInstrumentRef struct {
	ProviderInstrumentID   domain.ID
	ProviderInstrumentCode domain.Code
	InstrumentID           domain.ID
	AssetID                domain.ID
	ProviderCode           domain.Code
	ProviderMarket         string
	AssetType              domain.AssetType
	InstrumentType         domain.InstrumentType
	ExternalSymbol         string
	InstrumentCode         domain.Code
	InstrumentSymbol       string
	QuoteCurrency          string
	Metadata               json.RawMessage
}

// ProviderQuote is a normalized provider snapshot before quality validation
// and persistence. Optional market fields remain pointers so missing data does
// not become a synthetic zero.
type ProviderQuote struct {
	ProviderInstrumentID domain.ID
	InstrumentID         domain.ID
	AssetID              domain.ID
	ProviderCode         domain.Code
	LastPrice            domain.Decimal
	BidPrice             *domain.Decimal
	BidSize              *domain.Decimal
	AskPrice             *domain.Decimal
	AskSize              *domain.Decimal
	Open24H              *domain.Decimal
	High24H              *domain.Decimal
	Low24H               *domain.Decimal
	BaseVolume24H        *domain.Decimal
	QuoteVolume24H       *domain.Decimal
	MarketTime           domain.UTCInstant
	ReceivedAt           domain.UTCInstant
	RawPayload           json.RawMessage
}

// FetchBarsRequest identifies one exact source and a bounded [start,end)
// window. Cursor is provider-opaque and empty on the first page.
type FetchBarsRequest struct {
	Instrument ProviderInstrumentRef
	Interval   domain.BarInterval
	StartTime  domain.UTCInstant
	EndTime    domain.UTCInstant
	Limit      int
	Cursor     string
}

// FetchBarsResult is one ascending page. NextCursor is meaningful only when
// HasMore is true and must be sent back unchanged on the next request.
type FetchBarsResult struct {
	Bars       []ProviderBar
	NextCursor string
	HasMore    bool
}

// ProviderBar is a normalized, unpersisted OHLCV bar. Repository revision and
// quality fields are intentionally absent because ingestion assigns them after
// adapter output validation.
type ProviderBar struct {
	ProviderInstrumentID domain.ID
	InstrumentID         domain.ID
	AssetID              domain.ID
	ProviderCode         domain.Code
	Interval             domain.BarInterval
	OpenTime             domain.UTCInstant
	CloseTime            domain.UTCInstant
	Open                 domain.Decimal
	High                 domain.Decimal
	Low                  domain.Decimal
	Close                domain.Decimal
	BaseVolume           *domain.Decimal
	QuoteVolume          *domain.Decimal
	TradeCount           *int64
	IsClosed             bool
	ProviderUpdatedAt    *domain.UTCInstant
	ReceivedAt           domain.UTCInstant
	RawPayload           json.RawMessage
}
