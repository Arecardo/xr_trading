//go:build smoke

package longbridge

import (
	"context"
	"os"
	"testing"
	"time"

	"xr-trading/market-info-service/internal/domain"
	"xr-trading/market-info-service/internal/ingestion/ports"
)

// TestSmokeMarketData is intentionally opt-in. Run with LONGBRIDGE_SMOKE=1
// make smoke-longbridge and quote-only credentials in the SDK's LONGBRIDGE_*
// environment variables.
func TestSmokeMarketData(t *testing.T) {
	if os.Getenv("LONGBRIDGE_SMOKE") != "1" {
		t.Skip("set LONGBRIDGE_SMOKE=1 with quote credentials to call Longbridge")
	}
	adapter, err := NewFromEnvironment(Config{})
	if err != nil {
		t.Fatalf("NewFromEnvironment() error = %v", err)
	}
	defer adapter.Close()
	reference := longbridgeReference(t, "AAPL.US", domain.AssetTypeStock, domain.InstrumentTypeEquity)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	quotes, err := adapter.FetchLatestQuotes(ctx, []ports.ProviderInstrumentRef{reference})
	if err != nil || len(quotes) != 1 {
		t.Fatalf("FetchLatestQuotes() = (%#v, %v)", quotes, err)
	}
	end := time.Now().UTC()
	request := ports.FetchBarsRequest{Instrument: reference, Interval: domain.BarInterval1Hour, StartTime: mustLongbridgeInstant(t, end.Add(-48*time.Hour)), EndTime: mustLongbridgeInstant(t, end), Limit: 2}
	result, err := adapter.FetchBars(ctx, request)
	if err != nil || len(result.Bars) == 0 {
		t.Fatalf("FetchBars() = (%#v, %v)", result, err)
	}
}
