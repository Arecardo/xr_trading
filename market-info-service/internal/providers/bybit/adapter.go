// Package bybit implements the public Bybit V5 Spot market-data adapter.
package bybit

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
	DefaultBaseURL        = "https://api.bybit.com"
	defaultRequestTimeout = 10 * time.Second
	defaultResponseLimit  = int64(4 << 20)
	maxQuoteBatchSize     = 100
	maxBarsPerRequest     = 1000
	spotMarket            = "spot"
)

// Config contains transport dependencies. Public market endpoints do not need
// an API key; account credentials must not be added to this adapter.
type Config struct {
	BaseURL          string
	HTTPClient       *http.Client
	Now              func() time.Time
	UserAgent        string
	MaxResponseBytes int64
}

// Adapter maps Bybit V5 Spot wire responses to provider-independent DTOs.
type Adapter struct {
	providerCode     domain.Code
	baseURL          *url.URL
	httpClient       *http.Client
	now              func() time.Time
	userAgent        string
	maxResponseBytes int64
}

// New constructs a Bybit adapter. A zero Config uses production-safe defaults.
func New(config Config) (*Adapter, error) {
	baseURL := strings.TrimSpace(config.BaseURL)
	if baseURL == "" {
		baseURL = DefaultBaseURL
	}
	parsedURL, err := url.Parse(baseURL)
	if err != nil || (parsedURL.Scheme != "http" && parsedURL.Scheme != "https") || parsedURL.Host == "" ||
		parsedURL.User != nil || parsedURL.RawQuery != "" || parsedURL.Fragment != "" {
		return nil, fmt.Errorf("construct bybit adapter: invalid base URL: %w", domain.ErrInvalidData)
	}
	parsedURL.Path = strings.TrimRight(parsedURL.Path, "/")

	providerCode, err := domain.ParseCode("bybit")
	if err != nil {
		return nil, fmt.Errorf("construct bybit provider code: %w", err)
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
		return nil, fmt.Errorf("construct bybit adapter: response limit must be positive: %w", domain.ErrInvalidData)
	}
	userAgent := strings.TrimSpace(config.UserAgent)
	if userAgent == "" {
		userAgent = "xr-trading-market-info/1"
	}
	if strings.ContainsAny(userAgent, "\r\n") {
		return nil, fmt.Errorf("construct bybit adapter: invalid user agent: %w", domain.ErrInvalidData)
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

// Capabilities declares the first-phase Bybit Spot adapter boundary.
func (adapter *Adapter) Capabilities(ctx context.Context) (ports.ProviderCapabilities, error) {
	if adapter == nil {
		return ports.ProviderCapabilities{}, fmt.Errorf("bybit capabilities: nil adapter: %w", domain.ErrInvalidState)
	}
	if ctx == nil {
		return ports.ProviderCapabilities{}, fmt.Errorf("bybit capabilities: nil context: %w", domain.ErrInvalidData)
	}
	if err := ctx.Err(); err != nil {
		return ports.ProviderCapabilities{}, err
	}
	return ports.ProviderCapabilities{
		ProviderCode: adapter.providerCode,
		Markets: []ports.ProviderMarketCapability{{
			ProviderMarket: spotMarket, AssetTypes: []domain.AssetType{domain.AssetTypeCrypto},
			InstrumentTypes: []domain.InstrumentType{domain.InstrumentTypeSpot},
			SupportsQuote:   true, SupportsBars: true,
			Intervals:    []domain.BarInterval{domain.BarInterval1Hour, domain.BarInterval1Day},
			MaxBatchSize: maxQuoteBatchSize, MaxBarsPerRequest: maxBarsPerRequest,
		}},
	}, nil
}

var _ ports.MarketDataAdapter = (*Adapter)(nil)
