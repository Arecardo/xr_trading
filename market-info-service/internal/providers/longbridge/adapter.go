// Package longbridge implements the Longbridge US stock and ETF market-data adapter.
package longbridge

import (
	"context"
	"fmt"
	"reflect"
	"time"

	lbconfig "github.com/longbridge/openapi-go/config"
	lbquote "github.com/longbridge/openapi-go/quote"

	"xr-trading/market-info-service/internal/domain"
	"xr-trading/market-info-service/internal/ingestion/ports"
)

const (
	providerName      = "longbridge"
	usMarket          = "us"
	maxQuoteBatchSize = 500
	maxBarsPerRequest = 1000
)

// Client is the narrow Longbridge SDK boundary owned by this provider package.
// It exists so ordinary tests never create a WebSocket connection.
type Client interface {
	Quote(context.Context, []string) ([]*lbquote.SecurityQuote, error)
	HistoryCandlesticksByOffset(context.Context, string, lbquote.Period, lbquote.AdjustType, bool, *time.Time, int32, ...lbquote.CandlestickRequestOption) ([]*lbquote.Candlestick, error)
	Close() error
}

// Config contains adapter-local dependencies. Client is required; production
// bootstrap can construct it with NewFromSDKConfig or NewFromEnvironment.
type Config struct {
	Client         Client
	Now            func() time.Time
	MarketLocation *time.Location
}

// Adapter maps Longbridge SDK responses to provider-independent DTOs.
type Adapter struct {
	providerCode   domain.Code
	client         Client
	now            func() time.Time
	marketLocation *time.Location
}

// New constructs an adapter around an already initialized client.
func New(config Config) (*Adapter, error) {
	if isNilClient(config.Client) {
		return nil, fmt.Errorf("construct Longbridge adapter: client is required: %w", domain.ErrInvalidData)
	}
	providerCode, err := domain.ParseCode(providerName)
	if err != nil {
		return nil, fmt.Errorf("construct Longbridge provider code: %w", err)
	}
	now := config.Now
	if now == nil {
		now = time.Now
	}
	location := config.MarketLocation
	if location == nil {
		location, err = time.LoadLocation("America/New_York")
		if err != nil {
			return nil, fmt.Errorf("load US market location: %w", err)
		}
	}
	return &Adapter{providerCode: providerCode, client: config.Client, now: now, marketLocation: location}, nil
}

// NewFromSDKConfig creates and owns a production Longbridge QuoteContext.
func NewFromSDKConfig(sdkConfig *lbconfig.Config, config Config) (*Adapter, error) {
	if sdkConfig == nil {
		return nil, fmt.Errorf("construct Longbridge SDK client: config is required: %w", domain.ErrInvalidData)
	}
	client, err := lbquote.NewFromCfg(sdkConfig)
	if err != nil {
		return nil, fmt.Errorf("construct Longbridge SDK client: %w", err)
	}
	config.Client = client
	adapter, err := New(config)
	if err != nil {
		_ = client.Close()
		return nil, err
	}
	return adapter, nil
}

// NewFromEnvironment loads the official SDK's LONGBRIDGE_* configuration.
func NewFromEnvironment(config Config) (*Adapter, error) {
	sdkConfig, err := lbconfig.New()
	if err != nil {
		return nil, fmt.Errorf("load Longbridge SDK configuration: %w", err)
	}
	return NewFromSDKConfig(sdkConfig, config)
}

// ProviderCode returns the stable provider identity.
func (adapter *Adapter) ProviderCode() domain.Code {
	if adapter == nil {
		return domain.Code{}
	}
	return adapter.providerCode
}

// Capabilities declares the first-phase US stock and ETF boundary.
func (adapter *Adapter) Capabilities(ctx context.Context) (ports.ProviderCapabilities, error) {
	if adapter == nil {
		return ports.ProviderCapabilities{}, fmt.Errorf("Longbridge capabilities: nil adapter: %w", domain.ErrInvalidState)
	}
	if ctx == nil {
		return ports.ProviderCapabilities{}, fmt.Errorf("Longbridge capabilities: nil context: %w", domain.ErrInvalidData)
	}
	if err := ctx.Err(); err != nil {
		return ports.ProviderCapabilities{}, err
	}
	return ports.ProviderCapabilities{
		ProviderCode: adapter.providerCode,
		Markets: []ports.ProviderMarketCapability{{
			ProviderMarket:  usMarket,
			AssetTypes:      []domain.AssetType{domain.AssetTypeStock, domain.AssetTypeETF},
			InstrumentTypes: []domain.InstrumentType{domain.InstrumentTypeEquity, domain.InstrumentTypeETF},
			SupportsQuote:   true, SupportsBars: true,
			Intervals:    []domain.BarInterval{domain.BarInterval1Hour, domain.BarInterval1Day},
			MaxBatchSize: maxQuoteBatchSize, MaxBarsPerRequest: maxBarsPerRequest,
		}},
	}, nil
}

// Close releases the SDK's persistent quote connection.
func (adapter *Adapter) Close() error {
	if adapter == nil || isNilClient(adapter.client) {
		return nil
	}
	return adapter.client.Close()
}

func isNilClient(client Client) bool {
	if client == nil {
		return true
	}
	value := reflect.ValueOf(client)
	return value.Kind() == reflect.Ptr && value.IsNil()
}

var _ ports.MarketDataAdapter = (*Adapter)(nil)
