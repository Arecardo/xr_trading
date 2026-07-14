package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"xr-trading/market-info-service/internal/application"
	"xr-trading/market-info-service/internal/domain"
)

func TestWriteErrorMapsApplicationErrors(t *testing.T) {
	tests := []struct {
		code   application.ErrorCode
		status int
	}{
		{application.ErrorCodeInvalidTimeRange, http.StatusBadRequest},
		{application.ErrorCodeUnauthenticated, http.StatusUnauthorized},
		{application.ErrorCodePermissionDenied, http.StatusForbidden},
		{application.ErrorCodeInstrumentNotFound, http.StatusNotFound},
		{application.ErrorCodeTaskStateConflict, http.StatusConflict},
		{application.ErrorCodeRateLimited, http.StatusTooManyRequests},
		{application.ErrorCodeDatabaseUnavailable, http.StatusServiceUnavailable},
		{application.ErrorCodeInternal, http.StatusInternalServerError},
	}
	for _, test := range tests {
		t.Run(string(test.code), func(t *testing.T) {
			request := requestWithID()
			response := httptest.NewRecorder()
			WriteError(response, request, application.NewError(test.code, "safe message", test.status >= 500, map[string]any{"safe": true}))
			if response.Code != test.status || response.Header().Get(RequestIDHeader) != testRequestID {
				t.Fatalf("WriteError() response = (%d, %q)", response.Code, response.Header().Get(RequestIDHeader))
			}
			var envelope ErrorEnvelope
			if err := json.Unmarshal(response.Body.Bytes(), &envelope); err != nil {
				t.Fatalf("decode envelope: %v", err)
			}
			if envelope.Error.Code != test.code || envelope.Error.RequestID != testRequestID || envelope.Error.Details["safe"] != true {
				t.Fatalf("envelope = %#v", envelope)
			}
		})
	}
}

func TestWriteErrorMapsDomainErrorsWithoutLeakingCause(t *testing.T) {
	tests := []struct {
		err    error
		status int
		code   application.ErrorCode
	}{
		{domain.ErrInvalidData, http.StatusBadRequest, application.ErrorCodeInvalidArgument},
		{domain.ErrNotFound, http.StatusNotFound, application.ErrorCodeNotFound},
		{domain.ErrConflict, http.StatusConflict, application.ErrorCodeConflict},
		{domain.ErrDatabaseUnavailable, http.StatusServiceUnavailable, application.ErrorCodeDatabaseUnavailable},
		{domain.ErrRetryable, http.StatusServiceUnavailable, application.ErrorCodeServiceUnavailable},
		{errors.New("secret database URL"), http.StatusInternalServerError, application.ErrorCodeInternal},
	}
	for _, test := range tests {
		response := httptest.NewRecorder()
		WriteError(response, requestWithID(), test.err)
		if response.Code != test.status {
			t.Fatalf("WriteError(%v) status = %d", test.err, response.Code)
		}
		var envelope ErrorEnvelope
		_ = json.Unmarshal(response.Body.Bytes(), &envelope)
		if envelope.Error.Code != test.code || envelope.Error.Message == "secret database URL" {
			t.Fatalf("WriteError(%v) envelope = %#v", test.err, envelope)
		}
	}
}

func TestWriteErrorFallsBackWhenDetailsCannotEncode(t *testing.T) {
	response := httptest.NewRecorder()
	badDetails := map[string]any{"unsupported": make(chan int)}
	WriteError(response, requestWithID(), application.NewError(application.ErrorCodeInvalidArgument, "bad", false, badDetails))
	if response.Code != http.StatusInternalServerError {
		t.Fatalf("WriteError() status = %d", response.Code)
	}
	var envelope ErrorEnvelope
	_ = json.Unmarshal(response.Body.Bytes(), &envelope)
	if envelope.Error.Code != application.ErrorCodeInternal || envelope.Error.Details != nil {
		t.Fatalf("fallback envelope = %#v", envelope)
	}
}

func TestWriteErrorSanitizesInvalidApplicationError(t *testing.T) {
	response := httptest.NewRecorder()
	WriteError(response, requestWithID(), application.WrapError(domain.ErrInvalidData, "UNKNOWN", "do not expose", false, nil))
	var envelope ErrorEnvelope
	_ = json.Unmarshal(response.Body.Bytes(), &envelope)
	if response.Code != http.StatusInternalServerError || envelope.Error.Code != application.ErrorCodeInternal {
		t.Fatalf("invalid application error response = (%d, %#v)", response.Code, envelope)
	}
}

func TestWriteJSONDoesNotCommitOnMarshalFailure(t *testing.T) {
	response := httptest.NewRecorder()
	if err := WriteJSON(response, http.StatusOK, make(chan int)); err == nil {
		t.Fatal("WriteJSON(channel) error = nil")
	}
	if response.Header().Get("Content-Type") != "" || response.Body.Len() != 0 {
		t.Fatalf("WriteJSON marshal failure committed response: %#v", response.Result())
	}
}

func requestWithID() *http.Request {
	request := httptest.NewRequest(http.MethodGet, "/", nil)
	ctx := context.WithValue(request.Context(), requestIDContextKey{}, testRequestID)
	return request.WithContext(ctx)
}
