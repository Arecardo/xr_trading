package domain

import (
	"context"
	"encoding/json"
	"time"

	"github.com/shopspring/decimal"
)

// LatestQuote is the newest snapshot from one ProviderInstrument. It is never
// shared with another source, even when both describe the same Instrument.
type LatestQuote struct {
	InstrumentID         ID
	ProviderInstrumentID ID
	MarketTime           time.Time
	LastPrice            decimal.Decimal
	BidPrice             *decimal.Decimal
	BidSize              *decimal.Decimal
	AskPrice             *decimal.Decimal
	AskSize              *decimal.Decimal
	Open24H              *decimal.Decimal
	High24H              *decimal.Decimal
	Low24H               *decimal.Decimal
	BaseVolume24H        *decimal.Decimal
	QuoteVolume24H       *decimal.Decimal
	QualityStatus        string
	CollectedAt          time.Time
	Metadata             json.RawMessage
}

// MarketBar is one versioned OHLCV bar from a ProviderInstrument.
type MarketBar struct {
	InstrumentID         ID
	ProviderInstrumentID ID
	Interval             string
	OpenTime             time.Time
	Revision             int
	CloseTime            time.Time
	OpenPrice            decimal.Decimal
	HighPrice            decimal.Decimal
	LowPrice             decimal.Decimal
	ClosePrice           decimal.Decimal
	BaseVolume           *decimal.Decimal
	QuoteVolume          *decimal.Decimal
	TradeCount           *int64
	IsClosed             bool
	IsCurrent            bool
	QualityStatus        string
	ProviderUpdatedAt    *time.Time
	CollectedAt          time.Time
	RawHash              string
	Metadata             json.RawMessage
}

// MarketBarFilter identifies one source and interval; those identity fields
// are required so K-line reads cannot silently combine providers.
type MarketBarFilter struct {
	InstrumentID         ID
	ProviderInstrumentID ID
	Interval             string
	StartAt              *time.Time
	EndAt                *time.Time
	BeforeOpenTime       *time.Time
	Limit                int
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
