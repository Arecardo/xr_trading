package httpapi

import (
	"context"
	"errors"
	"net/http"
	"strings"

	"xr-trading/market-info-service/internal/application"
)

const authorizationHeader = "Authorization"

// NewAuthenticationMiddleware authenticates a strict Bearer header, removes
// the credential before invoking handlers and propagates Principal/AuditContext.
func NewAuthenticationMiddleware(authenticator application.Authenticator) (func(http.Handler) http.Handler, error) {
	if authenticator == nil {
		return nil, errors.New("authenticator is required")
	}
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
			token, err := bearerToken(request)
			if err != nil {
				writeUnauthenticated(writer, request)
				return
			}
			principal, err := authenticator.Authenticate(request.Context(), token)
			if err != nil {
				if errors.Is(err, application.ErrAuthenticationUnavailable) {
					WriteError(writer, request, application.NewError(application.ErrorCodeServiceUnavailable, "authentication service unavailable", true, nil))
					return
				}
				writeUnauthenticated(writer, request)
				return
			}
			if err := principal.Validate(); err != nil {
				WriteError(writer, request, errors.New("authenticator returned invalid principal"))
				return
			}
			requestID := requestIDForResponse(request)
			audit, err := application.NewAuditContext(principal, requestID)
			if err != nil {
				WriteError(writer, request, errors.New("construct audit context"))
				return
			}
			ctx := context.WithValue(request.Context(), requestIDContextKey{}, requestID)
			ctx = application.WithPrincipal(ctx, principal)
			ctx = application.WithAuditContext(ctx, audit)
			writer.Header().Set(RequestIDHeader, requestID)
			request.Header.Del(authorizationHeader)
			if next == nil {
				WriteError(writer, request.WithContext(ctx), errors.New("HTTP handler is required"))
				return
			}
			next.ServeHTTP(writer, request.WithContext(ctx))
		})
	}, nil
}

// NewPermissionMiddleware requires every listed permission. It is placed after
// authentication and can be composed per route group.
func NewPermissionMiddleware(permissions ...application.Permission) (func(http.Handler) http.Handler, error) {
	if len(permissions) == 0 {
		return nil, errors.New("at least one permission is required")
	}
	seen := make(map[application.Permission]struct{}, len(permissions))
	for _, permission := range permissions {
		if !permission.Valid() {
			return nil, errors.New("invalid required permission")
		}
		if _, exists := seen[permission]; exists {
			return nil, errors.New("duplicate required permission")
		}
		seen[permission] = struct{}{}
	}
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
			principal, exists := application.PrincipalFromContext(request.Context())
			if !exists {
				writeUnauthenticated(writer, request)
				return
			}
			for _, permission := range permissions {
				if !principal.HasPermission(permission) {
					WriteError(writer, request, application.NewError(application.ErrorCodePermissionDenied, "permission denied", false, nil))
					return
				}
			}
			if next == nil {
				WriteError(writer, request, errors.New("HTTP handler is required"))
				return
			}
			next.ServeHTTP(writer, request)
		})
	}, nil
}

func bearerToken(request *http.Request) (application.BearerToken, error) {
	if request == nil {
		return application.BearerToken{}, application.ErrInvalidCredentials
	}
	values := request.Header.Values(authorizationHeader)
	if len(values) != 1 {
		return application.BearerToken{}, application.ErrInvalidCredentials
	}
	parts := strings.Fields(values[0])
	if len(parts) != 2 || !strings.EqualFold(parts[0], "Bearer") {
		return application.BearerToken{}, application.ErrInvalidCredentials
	}
	return application.NewBearerToken(parts[1])
}

func writeUnauthenticated(writer http.ResponseWriter, request *http.Request) {
	writer.Header().Set("WWW-Authenticate", "Bearer")
	WriteError(writer, request, application.NewError(application.ErrorCodeUnauthenticated, "authentication required", false, nil))
}
