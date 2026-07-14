package auth

import (
	"context"
	"errors"
	"strings"
	"testing"

	"xr-trading/market-info-service/internal/application"
)

func TestStaticBearerAuthenticator(t *testing.T) {
	principal, _ := application.NewPrincipal("admin@example.com", application.ActorTypeUser, application.PermissionSubscriptionsManage)
	authenticator, err := NewStaticBearerAuthenticator([]StaticCredential{{Token: "admin-token", Principal: principal}})
	if err != nil {
		t.Fatalf("NewStaticBearerAuthenticator() error = %v", err)
	}
	token, _ := application.NewBearerToken("admin-token")
	loaded, err := authenticator.Authenticate(context.Background(), token)
	if err != nil || loaded.Subject() != principal.Subject() {
		t.Fatalf("Authenticate(valid) = (%#v, %v)", loaded, err)
	}
	invalid, _ := application.NewBearerToken("wrong-token")
	if _, err := authenticator.Authenticate(context.Background(), invalid); !errors.Is(err, application.ErrInvalidCredentials) || strings.Contains(err.Error(), invalid.Value()) {
		t.Fatalf("Authenticate(invalid) error = %v", err)
	}
	if _, err := (*StaticBearerAuthenticator)(nil).Authenticate(context.Background(), token); !errors.Is(err, application.ErrAuthenticationUnavailable) {
		t.Fatalf("nil Authenticate() error = %v", err)
	}
}

func TestStaticBearerAuthenticatorRejectsInvalidConfiguration(t *testing.T) {
	principal, _ := application.NewPrincipal("admin", application.ActorTypeUser)
	tests := [][]StaticCredential{
		nil,
		{{Token: "", Principal: principal}},
		{{Token: "token", Principal: application.Principal{}}},
		{{Token: "token", Principal: principal}, {Token: "token", Principal: principal}},
	}
	for _, credentials := range tests {
		if _, err := NewStaticBearerAuthenticator(credentials); err == nil {
			t.Fatalf("NewStaticBearerAuthenticator(%#v) error = nil", credentials)
		}
	}
}
