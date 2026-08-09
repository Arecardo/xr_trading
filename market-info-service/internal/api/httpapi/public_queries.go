package httpapi

import (
	"errors"
	"fmt"
	"net/http"
)

// PublicQueryRoutes groups the public, read-only market query use cases.
// Keeping the route set together makes production and integration-test
// assembly follow the same contract.
type PublicQueryRoutes struct {
	InstrumentOptions   InstrumentOptionsQuery
	InstrumentPrecision InstrumentPrecisionQuery
	LatestQuotes        LatestQuotesQuery
	Bars                BarsQuery
}

// RegisterPublicQueryRoutes attaches every public market query endpoint.
func RegisterPublicQueryRoutes(mux *http.ServeMux, routes PublicQueryRoutes) error {
	if mux == nil {
		return errors.New("public query mux is required")
	}
	instrumentOptions, err := NewInstrumentOptionsHandler(routes.InstrumentOptions)
	if err != nil {
		return fmt.Errorf("create instrument options handler: %w", err)
	}
	instrumentPrecision, err := NewInstrumentPrecisionHandler(routes.InstrumentPrecision)
	if err != nil {
		return fmt.Errorf("create instrument precision handler: %w", err)
	}
	latestQuotes, err := NewLatestQuotesHandler(routes.LatestQuotes)
	if err != nil {
		return fmt.Errorf("create latest quote handler: %w", err)
	}
	bars, err := NewBarsHandler(routes.Bars)
	if err != nil {
		return fmt.Errorf("create bars handler: %w", err)
	}
	if err := instrumentOptions.Register(mux); err != nil {
		return fmt.Errorf("register instrument options handler: %w", err)
	}
	if err := instrumentPrecision.Register(mux); err != nil {
		return fmt.Errorf("register instrument precision handler: %w", err)
	}
	if err := latestQuotes.Register(mux); err != nil {
		return fmt.Errorf("register latest quote handler: %w", err)
	}
	if err := bars.Register(mux); err != nil {
		return fmt.Errorf("register bars handler: %w", err)
	}
	return nil
}
