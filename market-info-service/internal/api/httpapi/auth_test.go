package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"xr-trading/market-info-service/internal/application"
)

type stubAuthenticator struct {
	principal application.Principal
	err       error
	called    bool
	token     string
}

func (authenticator *stubAuthenticator) Authenticate(_ context.Context, token application.BearerToken) (application.Principal, error) {
	authenticator.called = true
	authenticator.token = token.Value()
	return authenticator.principal, authenticator.err
}

func TestAuthenticationMiddlewarePropagatesSafeIdentityAndAudit(t *testing.T) {
	principal, _ := application.NewPrincipal("researcher@example.com", application.ActorTypeUser, application.PermissionOperationsRead)
	authenticator := &stubAuthenticator{principal: principal}
	middleware, err := NewAuthenticationMiddleware(authenticator)
	if err != nil {
		t.Fatalf("NewAuthenticationMiddleware() error = %v", err)
	}
	handler := middleware(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		loaded, principalExists := application.PrincipalFromContext(request.Context())
		audit, auditExists := application.AuditContextFromContext(request.Context())
		if !principalExists || !auditExists || loaded.Subject() != principal.Subject() || audit.RequestedBy() != principal.Subject() || audit.RequestID() != testRequestID {
			t.Fatalf("security context = (%#v, %t, %#v, %t)", loaded, principalExists, audit, auditExists)
		}
		if request.Header.Get(authorizationHeader) != "" {
			t.Fatal("Authorization header reached handler")
		}
		writer.WriteHeader(http.StatusNoContent)
	}))
	request := requestWithID()
	request.Header.Set(authorizationHeader, "Bearer super-secret-token")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusNoContent || authenticator.token != "super-secret-token" || response.Header().Get(RequestIDHeader) != testRequestID {
		t.Fatalf("authentication response = (%d, %q, %q)", response.Code, authenticator.token, response.Header().Get(RequestIDHeader))
	}
}

func TestAuthenticationMiddlewareRejectsMissingMalformedAndInvalidCredentials(t *testing.T) {
	principal, _ := application.NewPrincipal("user", application.ActorTypeUser)
	for _, header := range []string{"", "Basic abc", "Bearer", "Bearer has spaces"} {
		authenticator := &stubAuthenticator{principal: principal}
		middleware, _ := NewAuthenticationMiddleware(authenticator)
		request := requestWithID()
		if header != "" {
			request.Header.Set(authorizationHeader, header)
		}
		response := httptest.NewRecorder()
		middleware(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { t.Fatal("handler ran") })).ServeHTTP(response, request)
		if response.Code != http.StatusUnauthorized || response.Header().Get("WWW-Authenticate") != "Bearer" || strings.Contains(response.Body.String(), "has spaces") {
			t.Fatalf("header %q response = (%d, %s)", header, response.Code, response.Body.String())
		}
		if header != "" && strings.HasPrefix(header, "Bearer ") && strings.Count(header, " ") == 1 && !authenticator.called {
			t.Fatalf("validly shaped token %q did not reach authenticator", header)
		}
	}

	authenticator := &stubAuthenticator{principal: principal, err: errors.New("token super-secret-token rejected")}
	middleware, _ := NewAuthenticationMiddleware(authenticator)
	request := requestWithID()
	request.Header.Set(authorizationHeader, "Bearer super-secret-token")
	response := httptest.NewRecorder()
	middleware(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { t.Fatal("handler ran") })).ServeHTTP(response, request)
	if response.Code != http.StatusUnauthorized || strings.Contains(response.Body.String(), "super-secret-token") {
		t.Fatalf("invalid credential response = (%d, %s)", response.Code, response.Body.String())
	}
}

func TestAuthenticationMiddlewareHandlesUnavailableAndInvalidPrincipal(t *testing.T) {
	tests := []struct {
		name       string
		auth       *stubAuthenticator
		wantStatus int
		wantCode   application.ErrorCode
	}{
		{"unavailable", &stubAuthenticator{err: application.ErrAuthenticationUnavailable}, http.StatusServiceUnavailable, application.ErrorCodeServiceUnavailable},
		{"invalid principal", &stubAuthenticator{principal: application.Principal{}}, http.StatusInternalServerError, application.ErrorCodeInternal},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			middleware, _ := NewAuthenticationMiddleware(test.auth)
			request := requestWithID()
			request.Header.Set(authorizationHeader, "Bearer valid-token")
			response := httptest.NewRecorder()
			middleware(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { t.Fatal("handler ran") })).ServeHTTP(response, request)
			var envelope ErrorEnvelope
			_ = json.Unmarshal(response.Body.Bytes(), &envelope)
			if response.Code != test.wantStatus || envelope.Error.Code != test.wantCode {
				t.Fatalf("response = (%d, %#v)", response.Code, envelope)
			}
		})
	}
	if _, err := NewAuthenticationMiddleware(nil); err == nil {
		t.Fatal("NewAuthenticationMiddleware(nil) error = nil")
	}
}

func TestPermissionMiddleware(t *testing.T) {
	principal, _ := application.NewPrincipal("admin", application.ActorTypeUser, application.PermissionOperationsRead, application.PermissionIngestionManage)
	middleware, err := NewPermissionMiddleware(application.PermissionOperationsRead, application.PermissionIngestionManage)
	if err != nil {
		t.Fatalf("NewPermissionMiddleware() error = %v", err)
	}
	request := requestWithID()
	request = request.WithContext(application.WithPrincipal(request.Context(), principal))
	response := httptest.NewRecorder()
	middleware(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) { writer.WriteHeader(http.StatusNoContent) })).ServeHTTP(response, request)
	if response.Code != http.StatusNoContent {
		t.Fatalf("authorized status = %d", response.Code)
	}

	readOnly, _ := application.NewPrincipal("reader", application.ActorTypeUser, application.PermissionOperationsRead)
	request = requestWithID()
	request = request.WithContext(application.WithPrincipal(request.Context(), readOnly))
	response = httptest.NewRecorder()
	middleware(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { t.Fatal("handler ran") })).ServeHTTP(response, request)
	if response.Code != http.StatusForbidden {
		t.Fatalf("forbidden status = %d", response.Code)
	}

	response = httptest.NewRecorder()
	middleware(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { t.Fatal("handler ran") })).ServeHTTP(response, requestWithID())
	if response.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated status = %d", response.Code)
	}
}

func TestPermissionMiddlewareRejectsInvalidConfigurationAndNilHandler(t *testing.T) {
	if _, err := NewPermissionMiddleware(); err == nil {
		t.Fatal("NewPermissionMiddleware(empty) error = nil")
	}
	if _, err := NewPermissionMiddleware("unknown"); err == nil {
		t.Fatal("NewPermissionMiddleware(unknown) error = nil")
	}
	if _, err := NewPermissionMiddleware(application.PermissionOperationsRead, application.PermissionOperationsRead); err == nil {
		t.Fatal("NewPermissionMiddleware(duplicate) error = nil")
	}
	principal, _ := application.NewPrincipal("reader", application.ActorTypeUser, application.PermissionOperationsRead)
	middleware, _ := NewPermissionMiddleware(application.PermissionOperationsRead)
	request := requestWithID().WithContext(application.WithPrincipal(requestWithID().Context(), principal))
	response := httptest.NewRecorder()
	middleware(nil).ServeHTTP(response, request)
	if response.Code != http.StatusInternalServerError {
		t.Fatalf("nil handler status = %d", response.Code)
	}
}

func TestBearerTokenParserRejectsMultipleAuthorizationHeaders(t *testing.T) {
	request := httptest.NewRequest(http.MethodGet, "/", nil)
	request.Header.Add(authorizationHeader, "Bearer one")
	request.Header.Add(authorizationHeader, "Bearer two")
	if _, err := bearerToken(request); !errors.Is(err, application.ErrInvalidCredentials) {
		t.Fatalf("bearerToken(multiple) error = %v", err)
	}
}
