package httpapi

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
)

const testRequestID = "req_019f1452-90f7-7992-a87a-ca272789160f"

func TestRequestIDMiddlewarePreservesOrReplacesHeader(t *testing.T) {
	middleware, err := NewRequestIDMiddleware(func() (string, error) { return testRequestID, nil })
	if err != nil {
		t.Fatalf("NewRequestIDMiddleware() error = %v", err)
	}
	handler := middleware(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if RequestIDFromContext(request.Context()) != testRequestID {
			t.Fatalf("context request ID = %q", RequestIDFromContext(request.Context()))
		}
		writer.WriteHeader(http.StatusNoContent)
	}))

	for _, incoming := range []string{"", "invalid", testRequestID} {
		request := httptest.NewRequest(http.MethodGet, "/", nil)
		request.Header.Set(RequestIDHeader, incoming)
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		if response.Code != http.StatusNoContent || response.Header().Get(RequestIDHeader) != testRequestID {
			t.Fatalf("incoming %q response = (%d, %q)", incoming, response.Code, response.Header().Get(RequestIDHeader))
		}
	}
}

func TestRequestIDMiddlewareHandlesGeneratorAndHandlerFailures(t *testing.T) {
	if _, err := NewRequestIDMiddleware(nil); err == nil {
		t.Fatal("NewRequestIDMiddleware(nil) error = nil")
	}
	middleware, _ := NewRequestIDMiddleware(func() (string, error) { return "", errors.New("entropy unavailable") })
	response := httptest.NewRecorder()
	middleware(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Fatal("handler should not run")
	})).ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/", nil))
	if response.Code != http.StatusInternalServerError || response.Header().Get(RequestIDHeader) != fallbackRequestID {
		t.Fatalf("generator failure response = (%d, %q)", response.Code, response.Header().Get(RequestIDHeader))
	}

	middleware, _ = NewRequestIDMiddleware(func() (string, error) { return testRequestID, nil })
	response = httptest.NewRecorder()
	middleware(nil).ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/", nil))
	if response.Code != http.StatusInternalServerError {
		t.Fatalf("nil handler status = %d", response.Code)
	}
}

func TestRequestIDValidationAndDefaultGeneration(t *testing.T) {
	if !ValidRequestID(testRequestID) {
		t.Fatalf("ValidRequestID(%q) = false", testRequestID)
	}
	for _, invalid := range []string{"", "019f1452-90f7-7992-a87a-ca272789160f", "req_550e8400-e29b-41d4-a716-446655440000"} {
		if ValidRequestID(invalid) {
			t.Fatalf("ValidRequestID(%q) = true", invalid)
		}
	}
	generated, err := defaultRequestID()
	if err != nil || !ValidRequestID(generated) {
		t.Fatalf("defaultRequestID() = (%q, %v)", generated, err)
	}
	if RequestIDFromContext(nil) != "" {
		t.Fatal("RequestIDFromContext(nil) != empty")
	}

	response := httptest.NewRecorder()
	WithRequestID(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.WriteHeader(http.StatusNoContent)
	})).ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/", nil))
	if !ValidRequestID(response.Header().Get(RequestIDHeader)) {
		t.Fatalf("WithRequestID header = %q", response.Header().Get(RequestIDHeader))
	}
}
