package observability

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

type checkerFunc func(context.Context) error

func (f checkerFunc) Check(ctx context.Context) error { return f(ctx) }

func TestNewHealthHandlerValidation(t *testing.T) {
	t.Parallel()

	if _, err := NewHealthHandler(nil, time.Second); err == nil {
		t.Fatal("NewHealthHandler(nil) error = nil, want error")
	}
	if _, err := NewHealthHandler(checkerFunc(func(context.Context) error { return nil }), 0); err == nil {
		t.Fatal("NewHealthHandler(timeout=0) error = nil, want error")
	}
}

func TestLiveness(t *testing.T) {
	t.Parallel()

	handler, _ := NewHealthHandler(checkerFunc(func(context.Context) error { return errors.New("must not run") }), time.Second)
	response := httptest.NewRecorder()
	handler.Liveness(response, httptest.NewRequest(http.MethodGet, "/healthz", nil))

	if response.Code != http.StatusOK || strings.TrimSpace(response.Body.String()) != `{"status":"ok"}` {
		t.Fatalf("unexpected response: code=%d body=%s", response.Code, response.Body.String())
	}
}

func TestReadiness(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		err        error
		wantStatus int
		wantBody   string
	}{
		{"ready", nil, http.StatusOK, `{"status":"ready"}`},
		{"database unavailable", ErrDatabaseUnavailable, http.StatusServiceUnavailable, `{"status":"not_ready","reason":"database_unavailable"}`},
		{"migration incompatible", ErrMigrationIncompatible, http.StatusServiceUnavailable, `{"status":"not_ready","reason":"migration_incompatible"}`},
		{"unknown dependency", errors.New("boom"), http.StatusServiceUnavailable, `{"status":"not_ready","reason":"dependency_unavailable"}`},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			handler, _ := NewHealthHandler(checkerFunc(func(context.Context) error { return test.err }), time.Second)
			response := httptest.NewRecorder()
			handler.Readiness(response, httptest.NewRequest(http.MethodGet, "/readyz", nil))
			if response.Code != test.wantStatus || strings.TrimSpace(response.Body.String()) != test.wantBody {
				t.Fatalf("unexpected response: code=%d body=%s", response.Code, response.Body.String())
			}
			if contentType := response.Header().Get("Content-Type"); contentType != "application/json" {
				t.Fatalf("Content-Type = %q", contentType)
			}
		})
	}
}

func TestReadinessAppliesTimeout(t *testing.T) {
	t.Parallel()

	handler, _ := NewHealthHandler(checkerFunc(func(ctx context.Context) error {
		<-ctx.Done()
		return ctx.Err()
	}), time.Millisecond)
	response := httptest.NewRecorder()
	handler.Readiness(response, httptest.NewRequest(http.MethodGet, "/readyz", nil))
	if response.Code != http.StatusServiceUnavailable || !strings.Contains(response.Body.String(), "dependency_unavailable") {
		t.Fatalf("unexpected response: code=%d body=%s", response.Code, response.Body.String())
	}
}

func TestRegister(t *testing.T) {
	t.Parallel()

	handler, _ := NewHealthHandler(checkerFunc(func(context.Context) error { return nil }), time.Second)
	mux := http.NewServeMux()
	handler.Register(mux)

	for _, path := range []string{"/healthz", "/readyz"} {
		response := httptest.NewRecorder()
		mux.ServeHTTP(response, httptest.NewRequest(http.MethodGet, path, nil))
		if response.Code != http.StatusOK {
			t.Fatalf("GET %s status = %d", path, response.Code)
		}
	}
}
