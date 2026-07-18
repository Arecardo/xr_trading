package postgres

import (
	"database/sql"
	"errors"
	"net"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"

	"xr-trading/market-info-service/internal/domain"
)

func TestMapError(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want error
	}{
		{name: "not found", err: sql.ErrNoRows, want: domain.ErrNotFound},
		{name: "conflict", err: &pgconn.PgError{Code: "23505"}, want: domain.ErrConflict},
		{name: "foreign key", err: &pgconn.PgError{Code: "23503"}, want: domain.ErrReferenceViolation},
		{name: "invalid data", err: &pgconn.PgError{Code: "23514"}, want: domain.ErrInvalidData},
		{name: "serialization", err: &pgconn.PgError{Code: "40001"}, want: domain.ErrRetryable},
		{name: "connection", err: &pgconn.PgError{Code: "08006"}, want: domain.ErrDatabaseUnavailable},
		{name: "network", err: &net.DNSError{Err: "temporary DNS failure", IsTemporary: true}, want: domain.ErrDatabaseUnavailable},
		{name: "driver safe retry", err: retryableDriverError{}, want: domain.ErrRetryable},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got := MapError(test.err)
			if !errors.Is(got, test.want) {
				t.Fatalf("MapError(%v) = %v, want errors.Is(_, %v)", test.err, got, test.want)
			}
			if !errors.Is(got, test.err) {
				t.Fatalf("MapError() must retain original error %v", test.err)
			}
		})
	}
}

type retryableDriverError struct{}

func (retryableDriverError) Error() string     { return "driver retryable" }
func (retryableDriverError) SafeToRetry() bool { return true }

func TestMapErrorPassesThroughUnknownAndNil(t *testing.T) {
	if got := MapError(nil); got != nil {
		t.Fatalf("MapError(nil) = %v, want nil", got)
	}
	want := errors.New("unknown")
	if got := MapError(want); got != want {
		t.Fatalf("MapError(unknown) = %v, want original", got)
	}
}
