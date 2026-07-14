package application

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"unicode"
	"unicode/utf8"
)

const maximumSubjectLength = 128
const maximumRequestIDLength = 128
const maximumBearerTokenLength = 4096

var ErrInvalidCredentials = errors.New("invalid credentials")
var ErrAuthenticationUnavailable = errors.New("authentication unavailable")

// ActorType identifies the kind of caller recorded in audit context.
type ActorType string

const (
	ActorTypeUser    ActorType = "user"
	ActorTypeService ActorType = "service"
)

// Permission is an explicit market-info operation capability.
type Permission string

const (
	PermissionOperationsRead      Permission = "operations.read"
	PermissionSubscriptionsManage Permission = "subscriptions.manage"
	PermissionIngestionManage     Permission = "ingestion.manage"
)

var knownPermissions = map[Permission]struct{}{
	PermissionOperationsRead: {}, PermissionSubscriptionsManage: {}, PermissionIngestionManage: {},
}

// Valid reports whether a permission belongs to the frozen contract.
func (permission Permission) Valid() bool {
	_, exists := knownPermissions[permission]
	return exists
}

// BearerToken wraps a secret so accidental formatting remains redacted.
type BearerToken struct {
	value string
}

// NewBearerToken validates an HTTP bearer credential.
func NewBearerToken(value string) (BearerToken, error) {
	if value == "" || len(value) > maximumBearerTokenLength {
		return BearerToken{}, ErrInvalidCredentials
	}
	for _, character := range value {
		if character < 0x21 || character > 0x7e {
			return BearerToken{}, ErrInvalidCredentials
		}
	}
	return BearerToken{value: value}, nil
}

// Value returns the raw token only to an Authenticator implementation.
func (token BearerToken) Value() string {
	return token.value
}

// String deliberately redacts credential material.
func (token BearerToken) String() string {
	return "[REDACTED]"
}

// Principal is a validated caller identity with explicit permissions.
type Principal struct {
	subject     string
	actorType   ActorType
	permissions map[Permission]struct{}
}

// NewPrincipal constructs an immutable caller identity.
func NewPrincipal(subject string, actorType ActorType, permissions ...Permission) (Principal, error) {
	if !validAuditText(subject, maximumSubjectLength) {
		return Principal{}, fmt.Errorf("invalid principal subject")
	}
	if actorType != ActorTypeUser && actorType != ActorTypeService {
		return Principal{}, fmt.Errorf("invalid principal actor type")
	}
	permissionSet := make(map[Permission]struct{}, len(permissions))
	for _, permission := range permissions {
		if !permission.Valid() {
			return Principal{}, fmt.Errorf("invalid principal permission %q", permission)
		}
		if _, exists := permissionSet[permission]; exists {
			return Principal{}, fmt.Errorf("duplicate principal permission %q", permission)
		}
		permissionSet[permission] = struct{}{}
	}
	return Principal{subject: subject, actorType: actorType, permissions: permissionSet}, nil
}

// Validate checks a Principal returned by a replaceable Authenticator.
func (principal Principal) Validate() error {
	permissions := principal.Permissions()
	_, err := NewPrincipal(principal.subject, principal.actorType, permissions...)
	return err
}

// Subject returns the stable value stored as requested_by.
func (principal Principal) Subject() string {
	return principal.subject
}

// ActorType returns whether the caller is a user or service.
func (principal Principal) ActorType() ActorType {
	return principal.actorType
}

// HasPermission checks one explicit capability.
func (principal Principal) HasPermission(permission Permission) bool {
	_, exists := principal.permissions[permission]
	return permission.Valid() && exists
}

// Permissions returns a copy of the caller's permissions.
func (principal Principal) Permissions() []Permission {
	permissions := make([]Permission, 0, len(principal.permissions))
	for permission := range principal.permissions {
		permissions = append(permissions, permission)
	}
	return permissions
}

// Authenticator validates an opaque bearer credential. Implementations must
// never include token material in returned errors.
type Authenticator interface {
	Authenticate(context.Context, BearerToken) (Principal, error)
}

// AuditContext contains only safe attribution fields propagated to use cases.
type AuditContext struct {
	requestedBy string
	actorType   ActorType
	requestID   string
}

// NewAuditContext binds a validated Principal to the current Request ID.
func NewAuditContext(principal Principal, requestID string) (AuditContext, error) {
	if err := principal.Validate(); err != nil {
		return AuditContext{}, err
	}
	if !validAuditText(requestID, maximumRequestIDLength) {
		return AuditContext{}, fmt.Errorf("invalid audit request ID")
	}
	return AuditContext{requestedBy: principal.Subject(), actorType: principal.ActorType(), requestID: requestID}, nil
}

// RequestedBy returns the database-safe actor identity.
func (audit AuditContext) RequestedBy() string { return audit.requestedBy }

// ActorType returns the audit actor kind.
func (audit AuditContext) ActorType() ActorType { return audit.actorType }

// RequestID returns the correlation identity for this operation.
func (audit AuditContext) RequestID() string { return audit.requestID }

type principalContextKey struct{}
type auditContextKey struct{}

// WithPrincipal stores an authenticated Principal in a context.
func WithPrincipal(ctx context.Context, principal Principal) context.Context {
	return context.WithValue(ctx, principalContextKey{}, principal)
}

// PrincipalFromContext reads an authenticated Principal.
func PrincipalFromContext(ctx context.Context) (Principal, bool) {
	if ctx == nil {
		return Principal{}, false
	}
	principal, exists := ctx.Value(principalContextKey{}).(Principal)
	return principal, exists && principal.Validate() == nil
}

// WithAuditContext stores safe operation attribution in a context.
func WithAuditContext(ctx context.Context, audit AuditContext) context.Context {
	return context.WithValue(ctx, auditContextKey{}, audit)
}

// AuditContextFromContext reads operation attribution.
func AuditContextFromContext(ctx context.Context) (AuditContext, bool) {
	if ctx == nil {
		return AuditContext{}, false
	}
	audit, exists := ctx.Value(auditContextKey{}).(AuditContext)
	validActor := audit.actorType == ActorTypeUser || audit.actorType == ActorTypeService
	return audit, exists && validActor && validAuditText(audit.requestedBy, maximumSubjectLength) && validAuditText(audit.requestID, maximumRequestIDLength)
}

func validAuditText(value string, maximumLength int) bool {
	if value == "" || value != strings.TrimSpace(value) || utf8.RuneCountInString(value) > maximumLength {
		return false
	}
	for _, character := range value {
		if unicode.IsControl(character) {
			return false
		}
	}
	return true
}
