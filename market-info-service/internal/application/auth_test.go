package application

import (
	"context"
	"strings"
	"testing"
)

func TestPrincipalPermissionsAndAuditContext(t *testing.T) {
	principal, err := NewPrincipal("researcher@example.com", ActorTypeUser, PermissionOperationsRead, PermissionIngestionManage)
	if err != nil {
		t.Fatalf("NewPrincipal() error = %v", err)
	}
	if principal.Subject() != "researcher@example.com" || principal.ActorType() != ActorTypeUser || !principal.HasPermission(PermissionOperationsRead) || principal.HasPermission(PermissionSubscriptionsManage) {
		t.Fatalf("principal = %#v", principal)
	}
	permissions := principal.Permissions()
	permissions[0] = PermissionSubscriptionsManage
	if !principal.HasPermission(PermissionOperationsRead) {
		t.Fatal("Permissions returned shared state")
	}
	audit, err := NewAuditContext(principal, "req_019f1452-90f7-7992-a87a-ca272789160f")
	if err != nil || audit.RequestedBy() != principal.Subject() || audit.ActorType() != ActorTypeUser {
		t.Fatalf("NewAuditContext() = (%#v, %v)", audit, err)
	}
	ctx := WithAuditContext(WithPrincipal(context.Background(), principal), audit)
	loadedPrincipal, principalExists := PrincipalFromContext(ctx)
	loadedAudit, auditExists := AuditContextFromContext(ctx)
	if !principalExists || !auditExists || loadedPrincipal.Subject() != principal.Subject() || loadedAudit.RequestID() != audit.RequestID() {
		t.Fatalf("context values = (%#v, %t, %#v, %t)", loadedPrincipal, principalExists, loadedAudit, auditExists)
	}
}

func TestPrincipalAndAuditValidation(t *testing.T) {
	tests := []struct {
		subject     string
		actor       ActorType
		permissions []Permission
	}{
		{"", ActorTypeUser, nil},
		{" user ", ActorTypeUser, nil},
		{"user\nadmin", ActorTypeUser, nil},
		{strings.Repeat("a", maximumSubjectLength+1), ActorTypeUser, nil},
		{"user", "robot", nil},
		{"user", ActorTypeUser, []Permission{"unknown"}},
		{"user", ActorTypeUser, []Permission{PermissionOperationsRead, PermissionOperationsRead}},
	}
	for _, test := range tests {
		if _, err := NewPrincipal(test.subject, test.actor, test.permissions...); err == nil {
			t.Fatalf("NewPrincipal(%q, %q, %#v) error = nil", test.subject, test.actor, test.permissions)
		}
	}
	if Permission("unknown").Valid() {
		t.Fatal("unknown permission is valid")
	}
	if (Principal{}).Validate() == nil {
		t.Fatal("zero Principal.Validate() error = nil")
	}
	principal, _ := NewPrincipal("user", ActorTypeUser)
	if _, err := NewAuditContext(principal, "bad\nrequest"); err == nil {
		t.Fatal("NewAuditContext(invalid request ID) error = nil")
	}
	if _, ok := PrincipalFromContext(nil); ok {
		t.Fatal("PrincipalFromContext(nil) exists")
	}
	if _, ok := AuditContextFromContext(nil); ok {
		t.Fatal("AuditContextFromContext(nil) exists")
	}
}

func TestBearerTokenIsValidatedAndRedacted(t *testing.T) {
	token, err := NewBearerToken("test-token_123")
	if err != nil || token.Value() != "test-token_123" || token.String() != "[REDACTED]" {
		t.Fatalf("NewBearerToken() = (%q, %v)", token, err)
	}
	for _, invalid := range []string{"", "has space", "line\nbreak", strings.Repeat("a", maximumBearerTokenLength+1)} {
		if _, err := NewBearerToken(invalid); err == nil {
			t.Fatalf("NewBearerToken(%q) error = nil", invalid)
		}
	}
}
