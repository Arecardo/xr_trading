package httpapi

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"regexp"
	"strconv"

	"xr-trading/market-info-service/internal/application"
)

const cursorVersion = 1
const maximumEncodedCursorLength = 2048
const maximumCursorValues = 8
const maximumCursorValueLength = 256

var cursorScopePattern = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,127}$`)

// PageLimits defines endpoint-specific defaults and a hard public maximum.
type PageLimits struct {
	Default int
	Maximum int
}

// Validate checks server-owned pagination configuration.
func (limits PageLimits) Validate() error {
	if limits.Default <= 0 || limits.Maximum <= 0 || limits.Default > limits.Maximum {
		return errors.New("invalid page limits")
	}
	return nil
}

// ParsePageSize parses a client limit while enforcing the endpoint maximum.
func ParsePageSize(raw string, limits PageLimits) (int, error) {
	if err := limits.Validate(); err != nil {
		return 0, err
	}
	if raw == "" {
		return limits.Default, nil
	}
	value, err := strconv.Atoi(raw)
	if err != nil || value <= 0 || value > limits.Maximum {
		return 0, application.ValidationError([]application.FieldViolation{{
			Field: "limit", Reason: fmt.Sprintf("must be an integer between 1 and %d", limits.Maximum),
		}})
	}
	return value, nil
}

type cursorPayload struct {
	Version int      `json:"v"`
	Scope   string   `json:"scope"`
	Values  []string `json:"values"`
}

// EncodeCursor creates a versioned URL-safe opaque cursor. Scope binds a
// cursor to one endpoint and filter/order contract.
func EncodeCursor(scope string, values ...string) (string, error) {
	payload := cursorPayload{Version: cursorVersion, Scope: scope, Values: append([]string(nil), values...)}
	if err := validateCursorPayload(payload, scope); err != nil {
		return "", err
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		return "", fmt.Errorf("encode cursor payload: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(encoded), nil
}

// DecodeCursor validates version, endpoint scope and value count before
// returning a copy of the opaque position values.
func DecodeCursor(encoded, expectedScope string, expectedValues int) ([]string, error) {
	if encoded == "" || len(encoded) > maximumEncodedCursorLength || expectedValues <= 0 || expectedValues > maximumCursorValues {
		return nil, invalidCursor()
	}
	raw, err := base64.RawURLEncoding.DecodeString(encoded)
	if err != nil {
		return nil, invalidCursor()
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	var payload cursorPayload
	if err := decoder.Decode(&payload); err != nil {
		return nil, invalidCursor()
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		return nil, invalidCursor()
	}
	if err := validateCursorPayload(payload, expectedScope); err != nil || len(payload.Values) != expectedValues {
		return nil, invalidCursor()
	}
	return append([]string(nil), payload.Values...), nil
}

func validateCursorPayload(payload cursorPayload, expectedScope string) error {
	if payload.Version != cursorVersion || !cursorScopePattern.MatchString(payload.Scope) || payload.Scope != expectedScope {
		return errors.New("invalid cursor version or scope")
	}
	if len(payload.Values) == 0 || len(payload.Values) > maximumCursorValues {
		return errors.New("invalid cursor value count")
	}
	for _, value := range payload.Values {
		if value == "" || len(value) > maximumCursorValueLength {
			return errors.New("invalid cursor value")
		}
	}
	return nil
}

func invalidCursor() *application.Error {
	return application.ValidationError([]application.FieldViolation{{Field: "cursor", Reason: "is invalid or does not match this query"}})
}
