package observability

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"testing"

	"github.com/google/uuid"

	"xr-trading/market-info-service/internal/domain"
)

func TestJSONLoggerPropagatesCorrelationAndRedactsSensitiveFields(t *testing.T) {
	t.Parallel()

	var output bytes.Buffer
	logger := NewJSONLogger(&output, slog.LevelDebug)
	runID := logTestID("019f1452-90f7-7992-a87a-ca2727899201")
	taskID := logTestID("019f1452-90f7-7992-a87a-ca2727899202")
	instrumentID := logTestID("019f1452-90f7-7992-a87a-ca2727899203")
	provider, _ := domain.ParseCode("bybit")
	instrumentCode, _ := domain.ParseCode("instrument.bybit.spot.btc-usdt")

	ctx := WithLogger(context.Background(), logger)
	ctx = WithCorrelation(ctx, CorrelationFields{RequestID: "req_test", RunID: runID, TaskID: taskID})
	ctx = WithCorrelation(ctx, CorrelationFields{Provider: provider, InstrumentID: instrumentID, InstrumentCode: instrumentCode})
	LoggerFromContext(ctx).InfoContext(ctx, "task completed",
		slog.String("access_token", "token-secret-value"),
		slog.Any("error", errors.New("postgres://user:password@database")),
		slog.Group("provider_response", slog.String("signature", "signature-secret-value"), slog.String("code", "ok")),
	)

	text := output.String()
	for _, expected := range []string{`"msg":"task completed"`, `"request_id":"req_test"`, `"run_id":"` + runID.String() + `"`, `"task_id":"` + taskID.String() + `"`, `"provider":"bybit"`, `"instrument_id":"` + instrumentID.String() + `"`, `"instrument_code":"instrument.bybit.spot.btc-usdt"`, `"code":"ok"`, redactedValue} {
		if !strings.Contains(text, expected) {
			t.Fatalf("log output %q does not contain %q", text, expected)
		}
	}
	for _, secret := range []string{"token-secret-value", "signature-secret-value", "postgres://", "password@database"} {
		if strings.Contains(text, secret) {
			t.Fatalf("log output leaked %q: %s", secret, text)
		}
	}
}

func TestJSONLoggerRedactsPreboundAndGroupedAttributes(t *testing.T) {
	t.Parallel()

	var output bytes.Buffer
	logger := NewJSONLogger(&output, slog.LevelInfo).
		With("credential", "credential-secret").
		WithGroup("auth").
		With("password", "password-secret", "result", "denied")
	logger.Warn("authentication rejected")

	text := output.String()
	if strings.Contains(text, "credential-secret") || strings.Contains(text, "password-secret") {
		t.Fatalf("prebound attributes leaked: %s", text)
	}
	if !strings.Contains(text, redactedValue) || !strings.Contains(text, `"result":"denied"`) {
		t.Fatalf("redaction removed safe fields: %s", text)
	}
}

func TestLoggingContextHandlesNilAndMergesNonZeroFields(t *testing.T) {
	t.Parallel()

	runID := logTestID("019f1452-90f7-7992-a87a-ca2727899211")
	provider, _ := domain.ParseCode("longbridge")
	ctx := WithCorrelation(nil, CorrelationFields{RequestID: "first", RunID: runID})
	ctx = WithCorrelation(ctx, CorrelationFields{Provider: provider})
	fields := CorrelationFromContext(ctx)
	if fields.RequestID != "first" || fields.RunID != runID || fields.Provider != provider {
		t.Fatalf("merged fields = %+v", fields)
	}
	if got := CorrelationFromContext(nil); got != (CorrelationFields{}) {
		t.Fatalf("nil correlation = %+v", got)
	}
	LoggerFromContext(nil).Info("discarded")
	_ = WithLogger(nil, nil)
}

func logTestID(value string) domain.ID {
	return domain.IDFromUUID(uuid.MustParse(value))
}
