package observability

import (
	"context"
	"io"
	"log/slog"
	"strings"

	"xr-trading/market-info-service/internal/domain"
)

const redactedValue = "[REDACTED]"

type loggerContextKey struct{}
type correlationContextKey struct{}

// CorrelationFields are the bounded identities shared by API, scheduler and
// ingestion logs. Human-readable codes complement UUID identities; they do
// not replace database relationships.
type CorrelationFields struct {
	RequestID      string
	RunID          domain.ID
	TaskID         domain.ID
	Provider       domain.Code
	InstrumentID   domain.ID
	InstrumentCode domain.Code
}

// NewJSONLogger creates the process logger. The redacting handler is a final
// safety net; callers must still avoid logging request bodies and raw errors.
func NewJSONLogger(writer io.Writer, minimumLevel slog.Leveler) *slog.Logger {
	if writer == nil {
		writer = io.Discard
	}
	if minimumLevel == nil {
		minimumLevel = slog.LevelInfo
	}
	handler := slog.NewJSONHandler(writer, &slog.HandlerOptions{Level: minimumLevel})
	return slog.New(redactingHandler{next: handler})
}

// WithLogger installs the process logger at an execution boundary.
func WithLogger(ctx context.Context, logger *slog.Logger) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	if logger == nil {
		logger = NewJSONLogger(io.Discard, slog.LevelInfo)
	}
	return context.WithValue(ctx, loggerContextKey{}, logger)
}

// WithCorrelation merges non-zero identities into the current context.
func WithCorrelation(ctx context.Context, fields CorrelationFields) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	merged := CorrelationFromContext(ctx)
	if fields.RequestID != "" {
		merged.RequestID = fields.RequestID
	}
	if !fields.RunID.IsZero() {
		merged.RunID = fields.RunID
	}
	if !fields.TaskID.IsZero() {
		merged.TaskID = fields.TaskID
	}
	if !fields.Provider.IsZero() {
		merged.Provider = fields.Provider
	}
	if !fields.InstrumentID.IsZero() {
		merged.InstrumentID = fields.InstrumentID
	}
	if !fields.InstrumentCode.IsZero() {
		merged.InstrumentCode = fields.InstrumentCode
	}
	return context.WithValue(ctx, correlationContextKey{}, merged)
}

// CorrelationFromContext returns the current immutable correlation snapshot.
func CorrelationFromContext(ctx context.Context) CorrelationFields {
	if ctx == nil {
		return CorrelationFields{}
	}
	fields, _ := ctx.Value(correlationContextKey{}).(CorrelationFields)
	return fields
}

// LoggerFromContext returns a logger enriched with all available correlation
// fields. A missing logger is intentionally silent in unit-only boundaries.
func LoggerFromContext(ctx context.Context) *slog.Logger {
	logger := NewJSONLogger(io.Discard, slog.LevelInfo)
	if ctx != nil {
		if value, ok := ctx.Value(loggerContextKey{}).(*slog.Logger); ok && value != nil {
			logger = value
		}
	}
	return logger.With(correlationAttributes(CorrelationFromContext(ctx))...)
}

func correlationAttributes(fields CorrelationFields) []any {
	attributes := make([]any, 0, 12)
	if fields.RequestID != "" {
		attributes = append(attributes, slog.String("request_id", fields.RequestID))
	}
	if !fields.RunID.IsZero() {
		attributes = append(attributes, slog.String("run_id", fields.RunID.String()))
	}
	if !fields.TaskID.IsZero() {
		attributes = append(attributes, slog.String("task_id", fields.TaskID.String()))
	}
	if !fields.Provider.IsZero() {
		attributes = append(attributes, slog.String("provider", fields.Provider.String()))
	}
	if !fields.InstrumentID.IsZero() {
		attributes = append(attributes, slog.String("instrument_id", fields.InstrumentID.String()))
	}
	if !fields.InstrumentCode.IsZero() {
		attributes = append(attributes, slog.String("instrument_code", fields.InstrumentCode.String()))
	}
	return attributes
}

type redactingHandler struct {
	next slog.Handler
}

func (handler redactingHandler) Enabled(ctx context.Context, level slog.Level) bool {
	return handler.next.Enabled(ctx, level)
}

func (handler redactingHandler) Handle(ctx context.Context, record slog.Record) error {
	sanitized := slog.NewRecord(record.Time, record.Level, record.Message, record.PC)
	record.Attrs(func(attribute slog.Attr) bool {
		sanitized.AddAttrs(redactAttribute(attribute))
		return true
	})
	return handler.next.Handle(ctx, sanitized)
}

func (handler redactingHandler) WithAttrs(attributes []slog.Attr) slog.Handler {
	sanitized := make([]slog.Attr, 0, len(attributes))
	for _, attribute := range attributes {
		sanitized = append(sanitized, redactAttribute(attribute))
	}
	return redactingHandler{next: handler.next.WithAttrs(sanitized)}
}

func (handler redactingHandler) WithGroup(name string) slog.Handler {
	return redactingHandler{next: handler.next.WithGroup(name)}
}

func redactAttribute(attribute slog.Attr) slog.Attr {
	attribute.Value = attribute.Value.Resolve()
	if sensitiveLogKey(attribute.Key) {
		return slog.String(attribute.Key, redactedValue)
	}
	if attribute.Value.Kind() == slog.KindGroup {
		group := attribute.Value.Group()
		for index := range group {
			group[index] = redactAttribute(group[index])
		}
		return slog.Group(attribute.Key, attrsToAny(group)...)
	}
	return attribute
}

func attrsToAny(attributes []slog.Attr) []any {
	values := make([]any, len(attributes))
	for index := range attributes {
		values[index] = attributes[index]
	}
	return values
}

func sensitiveLogKey(key string) bool {
	normalized := strings.ToLower(strings.TrimSpace(key))
	if normalized == "error" || normalized == "err" || normalized == "cause" {
		return true
	}
	for _, fragment := range []string{"authorization", "credential", "password", "secret", "signature", "token", "cookie", "database_url", "dsn"} {
		if strings.Contains(normalized, fragment) {
			return true
		}
	}
	return false
}
