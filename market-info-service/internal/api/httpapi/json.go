package httpapi

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"

	"xr-trading/market-info-service/internal/application"
)

const DefaultMaximumRequestBodyBytes int64 = 1 << 20

// DecodeJSON limits request size, rejects unknown fields and requires exactly
// one JSON value.
func DecodeJSON(writer http.ResponseWriter, request *http.Request, destination any, maximumBytes int64) error {
	if writer == nil || request == nil || request.Body == nil || destination == nil {
		return errors.New("JSON decoder dependencies are required")
	}
	if maximumBytes <= 0 {
		return errors.New("maximum JSON request size must be positive")
	}
	request.Body = http.MaxBytesReader(writer, request.Body, maximumBytes)
	decoder := json.NewDecoder(request.Body)
	decoder.DisallowUnknownFields()

	if err := decoder.Decode(destination); err != nil {
		return classifyJSONDecodeError(err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		return application.NewError(application.ErrorCodeInvalidArgument, "request body must contain exactly one JSON value", false, nil)
	}
	return nil
}

func classifyJSONDecodeError(err error) error {
	var maximumError *http.MaxBytesError
	if errors.As(err, &maximumError) {
		return application.NewError(application.ErrorCodeInvalidArgument, "request body exceeds the size limit", false, nil)
	}
	if errors.Is(err, io.EOF) {
		return application.NewError(application.ErrorCodeInvalidArgument, "request body is required", false, nil)
	}
	var typeError *json.UnmarshalTypeError
	if errors.As(err, &typeError) && typeError.Field != "" {
		return application.ValidationError([]application.FieldViolation{{Field: typeError.Field, Reason: "has an invalid JSON type"}})
	}
	return application.WrapError(fmt.Errorf("decode JSON: %w", err), application.ErrorCodeInvalidArgument, "request body contains invalid JSON", false, nil)
}
