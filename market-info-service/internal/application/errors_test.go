package application

import (
	"errors"
	"testing"
)

func TestApplicationErrorPreservesCauseAndCopiesDetails(t *testing.T) {
	cause := errors.New("database detail")
	details := map[string]any{"resource": "instrument"}
	appError := WrapError(cause, ErrorCodeInstrumentNotFound, "instrument not found", false, details)
	details["resource"] = "changed"
	if !errors.Is(appError, cause) || appError.Details["resource"] != "instrument" {
		t.Fatalf("WrapError() = %#v", appError)
	}
	if err := appError.Validate(); err != nil {
		t.Fatalf("Validate() error = %v", err)
	}
}

func TestApplicationErrorValidation(t *testing.T) {
	if (*Error)(nil).Error() != "<nil>" || (*Error)(nil).Unwrap() != nil {
		t.Fatal("nil Error methods are not safe")
	}
	if err := (*Error)(nil).Validate(); err == nil {
		t.Fatal("nil Validate() error = nil")
	}
	if err := NewError("UNKNOWN", "unknown", false, nil).Validate(); err == nil {
		t.Fatal("unknown code Validate() error = nil")
	}
	if err := NewError(ErrorCodeInvalidArgument, "", false, nil).Validate(); err == nil {
		t.Fatal("empty message Validate() error = nil")
	}
	if ErrorCode("UNKNOWN").Valid() {
		t.Fatal("unknown code is valid")
	}
}

func TestValidationErrorReturnsAllFields(t *testing.T) {
	violations := []FieldViolation{{Field: "start_time", Reason: "must precede end_time"}}
	appError := ValidationError(violations)
	violations[0].Field = "changed"
	fields, ok := appError.Details["fields"].([]FieldViolation)
	if !ok || len(fields) != 1 || fields[0].Field != "start_time" || appError.Retryable {
		t.Fatalf("ValidationError() = %#v", appError)
	}
	if ValidationError(nil).Details != nil {
		t.Fatal("ValidationError(nil) details != nil")
	}
}
