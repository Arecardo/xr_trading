//go:build smoke

package coingecko

import (
	"context"
	"os"
	"testing"
	"time"

	"xr-trading/market-info-service/internal/domain"
	"xr-trading/market-info-service/internal/ingestion/ports"
)

// TestSmokePublicMarketData is intentionally opt-in and uses only CoinGecko's
// free public endpoints (no API key). Run with COINGECKO_SMOKE=1 make
// smoke-coingecko and an optional COINGECKO_BASE_URL.
func TestSmokePublicMarketData(t *testing.T) {
	if os.Getenv("COINGECKO_SMOKE") != "1" {
		t.Skip("set COINGECKO_SMOKE=1 to call the public CoinGecko API")
	}
	baseURL := os.Getenv("COINGECKO_BASE_URL")
	adapter, err := New(Config{BaseURL: baseURL})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	reference := usdtUSDReference(t)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	quotes, err := adapter.FetchLatestQuotes(ctx, []ports.ProviderInstrumentRef{reference})
	if err != nil || len(quotes) != 1 {
		t.Fatalf("FetchLatestQuotes() = (%#v, %v)", quotes, err)
	}

	endTime := time.Now().UTC().Truncate(24 * time.Hour)
	startTime := endTime.AddDate(0, 0, -3)
	request := ports.FetchBarsRequest{
		Instrument: reference, Interval: domain.BarInterval1Day,
		StartTime: mustCoinGeckoInstant(t, startTime), EndTime: mustCoinGeckoInstant(t, endTime), Limit: 10,
	}
	bars, err := adapter.FetchBars(ctx, request)
	if err != nil || len(bars.Bars) == 0 {
		t.Fatalf("FetchBars() = (%#v, %v)", bars, err)
	}
}
