//go:build smoke

package bybit

import (
	"context"
	"os"
	"testing"
	"time"

	"xr-trading/market-info-service/internal/domain"
	"xr-trading/market-info-service/internal/ingestion/ports"
)

// TestSmokePublicMarketData is intentionally opt-in and uses only public
// market endpoints. Run with BYBIT_SMOKE=1 make smoke-bybit and an optional
// BYBIT_BASE_URL.
func TestSmokePublicMarketData(t *testing.T) {
	if os.Getenv("BYBIT_SMOKE") != "1" {
		t.Skip("set BYBIT_SMOKE=1 to call the public Bybit API")
	}
	baseURL := os.Getenv("BYBIT_BASE_URL")
	adapter, err := New(Config{BaseURL: baseURL})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	reference := bybitReference(t, "BTCUSDT")
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	quotes, err := adapter.FetchLatestQuotes(ctx, []ports.ProviderInstrumentRef{reference})
	if err != nil || len(quotes) != 1 {
		t.Fatalf("FetchLatestQuotes() = (%#v, %v)", quotes, err)
	}

	endTime := time.Now().UTC().Truncate(time.Hour)
	startTime := endTime.Add(-2 * time.Hour)
	request := ports.FetchBarsRequest{
		Instrument: reference, Interval: domain.BarInterval1Hour,
		StartTime: mustBybitInstant(t, startTime), EndTime: mustBybitInstant(t, endTime), Limit: 2,
	}
	bars, err := adapter.FetchBars(ctx, request)
	if err != nil || len(bars.Bars) == 0 {
		t.Fatalf("FetchBars() = (%#v, %v)", bars, err)
	}
}
