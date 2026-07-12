// Package observability provides operational HTTP endpoints.
package observability

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"time"
)

// ErrDatabaseUnavailable marks a failed PostgreSQL availability check.
var ErrDatabaseUnavailable = errors.New("database unavailable")

// ErrMigrationIncompatible marks an incompatible database schema version.
var ErrMigrationIncompatible = errors.New("migration incompatible")

// ReadinessChecker verifies dependencies required to serve requests.
type ReadinessChecker interface {
	Check(context.Context) error
}

// HealthHandler serves liveness and readiness responses.
type HealthHandler struct {
	checker ReadinessChecker
	timeout time.Duration
}

// NewHealthHandler constructs health endpoints.
func NewHealthHandler(checker ReadinessChecker, timeout time.Duration) (*HealthHandler, error) {
	if checker == nil {
		return nil, errors.New("readiness checker is required")
	}
	if timeout <= 0 {
		return nil, errors.New("readiness timeout must be positive")
	}
	return &HealthHandler{checker: checker, timeout: timeout}, nil
}

// Register adds health routes to mux.
func (h *HealthHandler) Register(mux *http.ServeMux) {
	mux.HandleFunc("GET /healthz", h.Liveness)
	mux.HandleFunc("GET /readyz", h.Readiness)
}

// Liveness reports whether the process can handle HTTP requests.
func (h *HealthHandler) Liveness(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, healthResponse{Status: "ok"})
}

// Readiness checks required internal dependencies with a short timeout.
func (h *HealthHandler) Readiness(w http.ResponseWriter, request *http.Request) {
	ctx, cancel := context.WithTimeout(request.Context(), h.timeout)
	defer cancel()

	if err := h.checker.Check(ctx); err != nil {
		writeJSON(w, http.StatusServiceUnavailable, healthResponse{
			Status: "not_ready",
			Reason: readinessReason(err),
		})
		return
	}
	writeJSON(w, http.StatusOK, healthResponse{Status: "ready"})
}

type healthResponse struct {
	Status string `json:"status"`
	Reason string `json:"reason,omitempty"`
}

func readinessReason(err error) string {
	switch {
	case errors.Is(err, ErrDatabaseUnavailable):
		return "database_unavailable"
	case errors.Is(err, ErrMigrationIncompatible):
		return "migration_incompatible"
	default:
		return "dependency_unavailable"
	}
}

func writeJSON(w http.ResponseWriter, status int, payload healthResponse) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(payload)
}
