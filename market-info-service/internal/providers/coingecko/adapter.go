// Package coingecko implements the public CoinGecko FX-reference-rate
// market-data adapter (RM0 DEC-006). It exists solely to source the daily
// USDT->USD conversion rate for non-base-currency crypto holdings; it never
// serves a tradable instrument. See doc/technical/roadmap/01_decisions.md
// DEC-006 and doc/ai_quant_trading_system_requirements.md §5.1.2.
package coingecko

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"xr-trading/market-info-service/internal/domain"
	"xr-trading/market-info-service/internal/ingestion/ports"
)

const (
	// DefaultBaseURL is CoinGecko's public (keyless) API. Do not point this at
	// the Pro API host; that requires a paid key and this adapter never reads
	// or sends any authentication credential.
	DefaultBaseURL        = "https://api.coingecko.com"
	defaultRequestTimeout = 10 * time.Second
	// FX reference-rate responses are tiny (a handful of price points) next to
	// Bybit kline pages; 1 MiB is a generous ceiling, not a measured maximum.
	defaultResponseLimit = int64(1 << 20)
	maxQuoteBatchSize    = 50
	// CoinGecko's free market_chart/range endpoint returns daily granularity
	// once the requested range exceeds a few days. 366 comfortably covers one
	// calendar year of daily FX reference points per request/page.
	maxBarsPerRequest = 366
	// fxMarket is the only ProviderMarket this adapter serves. FX reference
	// data has no real trading venue, so this is a capability label, not a
	// market in the trading-session sense used by equity/crypto adapters.
	fxMarket = "fx"
)

// Config contains transport dependencies. CoinGecko's free tier needs no API
// key; this adapter must never gain a credential field, since that would put
// FX reference collection behind the same keyed-access risk as a brokerage
// adapter for something that is not a trading venue at all.
type Config struct {
	BaseURL          string
	HTTPClient       *http.Client
	Now              func() time.Time
	UserAgent        string
	MaxResponseBytes int64
}

// Adapter maps CoinGecko's public REST responses to provider-independent DTOs.
type Adapter struct {
	providerCode     domain.Code
	baseURL          *url.URL
	httpClient       *http.Client
	now              func() time.Time
	userAgent        string
	maxResponseBytes int64
}

// New constructs a CoinGecko adapter. A zero Config uses production-safe
// defaults, mirroring internal/providers/bybit.
func New(config Config) (*Adapter, error) {
	baseURL := strings.TrimSpace(config.BaseURL)
	if baseURL == "" {
		baseURL = DefaultBaseURL
	}
	parsedURL, err := url.Parse(baseURL)
	if err != nil || (parsedURL.Scheme != "http" && parsedURL.Scheme != "https") || parsedURL.Host == "" ||
		parsedURL.User != nil || parsedURL.RawQuery != "" || parsedURL.Fragment != "" {
		return nil, fmt.Errorf("construct coingecko adapter: invalid base URL: %w", domain.ErrInvalidData)
	}
	parsedURL.Path = strings.TrimRight(parsedURL.Path, "/")

	providerCode, err := domain.ParseCode("coingecko")
	if err != nil {
		return nil, fmt.Errorf("construct coingecko provider code: %w", err)
	}
	httpClient := config.HTTPClient
	if httpClient == nil {
		httpClient = &http.Client{Timeout: defaultRequestTimeout}
	}
	now := config.Now
	if now == nil {
		now = time.Now
	}
	responseLimit := config.MaxResponseBytes
	if responseLimit == 0 {
		responseLimit = defaultResponseLimit
	}
	if responseLimit < 0 {
		return nil, fmt.Errorf("construct coingecko adapter: response limit must be positive: %w", domain.ErrInvalidData)
	}
	userAgent := strings.TrimSpace(config.UserAgent)
	if userAgent == "" {
		userAgent = "xr-trading-market-info/1"
	}
	if strings.ContainsAny(userAgent, "\r\n") {
		return nil, fmt.Errorf("construct coingecko adapter: invalid user agent: %w", domain.ErrInvalidData)
	}

	return &Adapter{
		providerCode: providerCode, baseURL: parsedURL, httpClient: httpClient,
		now: now, userAgent: userAgent, maxResponseBytes: responseLimit,
	}, nil
}

// ProviderCode returns the stable provider identity.
func (adapter *Adapter) ProviderCode() domain.Code {
	if adapter == nil {
		return domain.Code{}
	}
	return adapter.providerCode
}

// Capabilities declares the first-phase CoinGecko FX adapter boundary. Only
// the daily interval is declared: CoinGecko's free tier gives one price per
// day (not real intraday OHLC), so there is nothing honest to serve for "1h".
func (adapter *Adapter) Capabilities(ctx context.Context) (ports.ProviderCapabilities, error) {
	if adapter == nil {
		return ports.ProviderCapabilities{}, fmt.Errorf("coingecko capabilities: nil adapter: %w", domain.ErrInvalidState)
	}
	if ctx == nil {
		return ports.ProviderCapabilities{}, fmt.Errorf("coingecko capabilities: nil context: %w", domain.ErrInvalidData)
	}
	if err := ctx.Err(); err != nil {
		return ports.ProviderCapabilities{}, err
	}
	return ports.ProviderCapabilities{
		ProviderCode: adapter.providerCode,
		Markets: []ports.ProviderMarketCapability{{
			ProviderMarket: fxMarket, AssetTypes: []domain.AssetType{domain.AssetTypeFX},
			InstrumentTypes: []domain.InstrumentType{domain.InstrumentTypeFX},
			SupportsQuote:   true, SupportsBars: true,
			Intervals:    []domain.BarInterval{domain.BarInterval1Day},
			MaxBatchSize: maxQuoteBatchSize, MaxBarsPerRequest: maxBarsPerRequest,
		}},
	}, nil
}

var _ ports.MarketDataAdapter = (*Adapter)(nil)
