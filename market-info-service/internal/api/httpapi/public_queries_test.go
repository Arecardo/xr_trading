package httpapi

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"xr-trading/market-info-service/internal/application"
)

func TestRegisterPublicQueryRoutes(t *testing.T) {
	t.Parallel()

	mux := http.NewServeMux()
	err := RegisterPublicQueryRoutes(mux, PublicQueryRoutes{
		InstrumentOptions:   publicInstrumentOptionsStub{},
		InstrumentPrecision: publicInstrumentPrecisionStub{},
		LatestQuotes:        publicLatestQuotesStub{},
		Bars:                publicBarsStub{},
	})
	if err != nil {
		t.Fatalf("RegisterPublicQueryRoutes() error = %v", err)
	}

	getPaths := []string{
		"/api/market-info/v1/instruments?asset_code=asset.crypto.btc",
		"/api/market-info/v1/quotes/latest?asset_code=asset.crypto.btc",
		"/api/market-info/v1/bars?instrument_code=instrument.crypto.btc-usdt&provider=bybit&interval=1h",
	}
	for _, path := range getPaths {
		response := httptest.NewRecorder()
		mux.ServeHTTP(response, httptest.NewRequest(http.MethodGet, path, nil))
		if response.Code != http.StatusBadRequest {
			t.Fatalf("GET %s status = %d, body = %s", path, response.Code, response.Body.String())
		}
	}

	response := httptest.NewRecorder()
	mux.ServeHTTP(response, httptest.NewRequest(http.MethodPost, getPaths[0], nil))
	if response.Code != http.StatusMethodNotAllowed {
		t.Fatalf("POST public query status = %d, want %d", response.Code, http.StatusMethodNotAllowed)
	}

	precisionResponse := httptest.NewRecorder()
	mux.ServeHTTP(precisionResponse, httptest.NewRequest(http.MethodPost, instrumentPrecisionBatchPath, strings.NewReader(`{"instrument_ids":["019f1452-90f7-7992-a87a-ca272789160f"]}`)))
	if precisionResponse.Code != http.StatusBadRequest {
		t.Fatalf("POST %s status = %d, body = %s", instrumentPrecisionBatchPath, precisionResponse.Code, precisionResponse.Body.String())
	}
	precisionGetResponse := httptest.NewRecorder()
	mux.ServeHTTP(precisionGetResponse, httptest.NewRequest(http.MethodGet, instrumentPrecisionBatchPath, nil))
	if precisionGetResponse.Code != http.StatusMethodNotAllowed {
		t.Fatalf("GET %s status = %d, want %d", instrumentPrecisionBatchPath, precisionGetResponse.Code, http.StatusMethodNotAllowed)
	}
}

func TestRegisterPublicQueryRoutesRejectsMissingDependencies(t *testing.T) {
	t.Parallel()

	valid := PublicQueryRoutes{
		InstrumentOptions:   publicInstrumentOptionsStub{},
		InstrumentPrecision: publicInstrumentPrecisionStub{},
		LatestQuotes:        publicLatestQuotesStub{},
		Bars:                publicBarsStub{},
	}
	tests := []struct {
		name      string
		mux       *http.ServeMux
		routes    PublicQueryRoutes
		wantError string
	}{
		{name: "nil mux", routes: valid, wantError: "mux"},
		{name: "instrument options", mux: http.NewServeMux(), routes: PublicQueryRoutes{InstrumentPrecision: valid.InstrumentPrecision, LatestQuotes: valid.LatestQuotes, Bars: valid.Bars}, wantError: "instrument options"},
		{name: "instrument precision", mux: http.NewServeMux(), routes: PublicQueryRoutes{InstrumentOptions: valid.InstrumentOptions, LatestQuotes: valid.LatestQuotes, Bars: valid.Bars}, wantError: "instrument precision"},
		{name: "latest quotes", mux: http.NewServeMux(), routes: PublicQueryRoutes{InstrumentOptions: valid.InstrumentOptions, InstrumentPrecision: valid.InstrumentPrecision, Bars: valid.Bars}, wantError: "latest quote"},
		{name: "bars", mux: http.NewServeMux(), routes: PublicQueryRoutes{InstrumentOptions: valid.InstrumentOptions, InstrumentPrecision: valid.InstrumentPrecision, LatestQuotes: valid.LatestQuotes}, wantError: "bars"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			err := RegisterPublicQueryRoutes(test.mux, test.routes)
			if err == nil || !strings.Contains(err.Error(), test.wantError) {
				t.Fatalf("RegisterPublicQueryRoutes() error = %v, want containing %q", err, test.wantError)
			}
		})
	}
}

type publicInstrumentOptionsStub struct{}

func (publicInstrumentOptionsStub) List(context.Context, application.InstrumentOptionsInput) (application.InstrumentOptionsPage, error) {
	return application.InstrumentOptionsPage{}, publicQueryStubError()
}

type publicInstrumentPrecisionStub struct{}

func (publicInstrumentPrecisionStub) Batch(context.Context, application.InstrumentPrecisionInput) (application.InstrumentPrecisionResult, error) {
	return application.InstrumentPrecisionResult{}, publicQueryStubError()
}

type publicLatestQuotesStub struct{}

func (publicLatestQuotesStub) List(context.Context, application.LatestQuotesInput) (application.LatestQuotesResult, error) {
	return application.LatestQuotesResult{}, publicQueryStubError()
}

type publicBarsStub struct{}

func (publicBarsStub) List(context.Context, application.BarsInput) (application.BarsResult, error) {
	return application.BarsResult{}, publicQueryStubError()
}

func publicQueryStubError() error {
	return application.ValidationError([]application.FieldViolation{{Field: "fixture", Reason: "route reached"}})
}
