// Package auth contains replaceable authentication implementations.
package auth

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"errors"

	"xr-trading/market-info-service/internal/application"
)

// StaticCredential configures the first-phase static bearer implementation.
// The constructor hashes Token immediately and does not retain its plaintext.
type StaticCredential struct {
	Token     string
	Principal application.Principal
}

type staticEntry struct {
	digest    [sha256.Size]byte
	principal application.Principal
}

// StaticBearerAuthenticator is suitable for a small first-phase deployment.
// A JWT/OIDC or gateway authenticator can replace it through the same interface.
type StaticBearerAuthenticator struct {
	entries []staticEntry
}

// NewStaticBearerAuthenticator validates and hashes all configured tokens.
func NewStaticBearerAuthenticator(credentials []StaticCredential) (*StaticBearerAuthenticator, error) {
	if len(credentials) == 0 {
		return nil, errors.New("at least one static credential is required")
	}
	entries := make([]staticEntry, 0, len(credentials))
	seen := make(map[[sha256.Size]byte]struct{}, len(credentials))
	for _, credential := range credentials {
		token, err := application.NewBearerToken(credential.Token)
		if err != nil {
			return nil, errors.New("invalid static bearer token")
		}
		if err := credential.Principal.Validate(); err != nil {
			return nil, errors.New("invalid static bearer principal")
		}
		digest := sha256.Sum256([]byte(token.Value()))
		if _, exists := seen[digest]; exists {
			return nil, errors.New("duplicate static bearer token")
		}
		seen[digest] = struct{}{}
		entries = append(entries, staticEntry{digest: digest, principal: credential.Principal})
	}
	return &StaticBearerAuthenticator{entries: entries}, nil
}

// Authenticate compares fixed-size token digests and never returns token data.
func (authenticator *StaticBearerAuthenticator) Authenticate(_ context.Context, token application.BearerToken) (application.Principal, error) {
	if authenticator == nil || len(authenticator.entries) == 0 {
		return application.Principal{}, application.ErrAuthenticationUnavailable
	}
	digest := sha256.Sum256([]byte(token.Value()))
	for _, entry := range authenticator.entries {
		if subtle.ConstantTimeCompare(digest[:], entry.digest[:]) == 1 {
			return entry.principal, nil
		}
	}
	return application.Principal{}, application.ErrInvalidCredentials
}

var _ application.Authenticator = (*StaticBearerAuthenticator)(nil)
