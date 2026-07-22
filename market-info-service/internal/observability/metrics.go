package observability

import (
	"context"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"xr-trading/market-info-service/internal/application"
	"xr-trading/market-info-service/internal/scheduler"
)

var httpDurationBuckets = []float64{0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1, 2.5, 5, 10}
var taskMetricStatuses = []string{"pending", "running", "retry_wait", "success", "failed", "canceled"}
var providerHealthMetricStatuses = []application.ProviderHealthStatus{
	application.ProviderHealthHealthy,
	application.ProviderHealthDegraded,
	application.ProviderHealthUnhealthy,
	application.ProviderHealthUnknown,
}

// HTTPObserver receives bounded access dimensions after a response completes.
type HTTPObserver interface {
	ObserveHTTPRequest(method, route string, status int, duration time.Duration)
}

// TaskMetricsSnapshot is the task-table truth needed by metrics and alerts.
type TaskMetricsSnapshot struct {
	Counts                 map[string]int64
	OldestBacklogCreatedAt *time.Time
}

// TaskMetricsReader reads task facts without deriving Run state.
type TaskMetricsReader interface {
	ReadTaskMetrics(context.Context, time.Time) (TaskMetricsSnapshot, error)
}

// ProviderStatusLister is the persisted provider status projection.
type ProviderStatusLister interface {
	List(context.Context) ([]application.ProviderStatus, error)
}

// OperationalSnapshot groups one bounded scrape observation.
type OperationalSnapshot struct {
	ObservedAt time.Time
	Tasks      TaskMetricsSnapshot
	Providers  []application.ProviderStatus
}

// OperationalMetricsSource combines task and provider persisted facts.
type OperationalMetricsSource interface {
	Snapshot(context.Context, time.Time) (OperationalSnapshot, error)
}

type operationalMetricsSource struct {
	tasks     TaskMetricsReader
	providers ProviderStatusLister
}

// NewOperationalMetricsSource constructs the scrape-time application bridge.
func NewOperationalMetricsSource(tasks TaskMetricsReader, providers ProviderStatusLister) (OperationalMetricsSource, error) {
	if tasks == nil || providers == nil {
		return nil, errors.New("operational metrics dependencies are required")
	}
	return &operationalMetricsSource{tasks: tasks, providers: providers}, nil
}

func (source *operationalMetricsSource) Snapshot(ctx context.Context, observedAt time.Time) (OperationalSnapshot, error) {
	if ctx == nil || observedAt.IsZero() {
		return OperationalSnapshot{}, errors.New("operational metrics context and observed time are required")
	}
	tasks, err := source.tasks.ReadTaskMetrics(ctx, observedAt.UTC())
	if err != nil {
		return OperationalSnapshot{}, fmt.Errorf("read task metrics: %w", err)
	}
	providers, err := source.providers.List(ctx)
	if err != nil {
		return OperationalSnapshot{}, fmt.Errorf("read provider metrics: %w", err)
	}
	if err := validateOperationalSnapshot(tasks, providers); err != nil {
		return OperationalSnapshot{}, err
	}
	return OperationalSnapshot{ObservedAt: observedAt.UTC(), Tasks: tasks, Providers: providers}, nil
}

// Metrics stores process-local API counters. Operational gauges are rebuilt
// from persistent facts on every scrape.
type Metrics struct {
	mu               sync.Mutex
	http             map[httpMetricKey]*httpHistogram
	snapshotFailures uint64
}

type httpMetricKey struct {
	method, route, statusClass string
}

type httpHistogram struct {
	count   uint64
	sum     float64
	buckets []uint64
}

// NewMetrics constructs an isolated registry.
func NewMetrics() *Metrics {
	return &Metrics{http: make(map[httpMetricKey]*httpHistogram)}
}

// ObserveHTTPRequest implements HTTPObserver with bounded dimensions.
func (metrics *Metrics) ObserveHTTPRequest(method, route string, status int, duration time.Duration) {
	if metrics == nil {
		return
	}
	method = normalizedHTTPMethod(method)
	if route == "" {
		route = "unmatched"
	}
	statusClass := httpStatusClass(status)
	seconds := duration.Seconds()
	if seconds < 0 || math.IsNaN(seconds) || math.IsInf(seconds, 0) {
		seconds = 0
	}
	key := httpMetricKey{method: method, route: route, statusClass: statusClass}
	metrics.mu.Lock()
	defer metrics.mu.Unlock()
	histogram := metrics.http[key]
	if histogram == nil {
		histogram = &httpHistogram{buckets: make([]uint64, len(httpDurationBuckets))}
		metrics.http[key] = histogram
	}
	histogram.count++
	histogram.sum += seconds
	for index, upperBound := range httpDurationBuckets {
		if seconds <= upperBound {
			histogram.buckets[index]++
		}
	}
}

func (metrics *Metrics) recordSnapshotFailure() {
	metrics.mu.Lock()
	defer metrics.mu.Unlock()
	metrics.snapshotFailures++
}

type metricsState struct {
	http             map[httpMetricKey]httpHistogram
	snapshotFailures uint64
}

func (metrics *Metrics) state() metricsState {
	metrics.mu.Lock()
	defer metrics.mu.Unlock()
	state := metricsState{http: make(map[httpMetricKey]httpHistogram, len(metrics.http)), snapshotFailures: metrics.snapshotFailures}
	for key, value := range metrics.http {
		copyValue := *value
		copyValue.buckets = append([]uint64(nil), value.buckets...)
		state.http[key] = copyValue
	}
	return state
}

// MetricsHandler serves Prometheus text exposition without UUID, symbol or
// error-text labels.
type MetricsHandler struct {
	metrics   *Metrics
	source    OperationalMetricsSource
	readiness ReadinessChecker
	timeout   time.Duration
	now       func() time.Time
}

func NewMetricsHandler(metrics *Metrics, source OperationalMetricsSource, readiness ReadinessChecker, timeout time.Duration, now func() time.Time) (*MetricsHandler, error) {
	if metrics == nil || source == nil || readiness == nil || timeout <= 0 || now == nil {
		return nil, errors.New("metrics handler dependencies are required")
	}
	return &MetricsHandler{metrics: metrics, source: source, readiness: readiness, timeout: timeout, now: now}, nil
}

// Register adds the internal scrape endpoint.
func (handler *MetricsHandler) Register(mux *http.ServeMux) error {
	if handler == nil || mux == nil {
		return errors.New("metrics handler and mux are required")
	}
	mux.Handle("GET /metrics", handler)
	return nil
}

func (handler *MetricsHandler) ServeHTTP(writer http.ResponseWriter, request *http.Request) {
	observedAt := handler.now().UTC()
	ctx, cancel := context.WithTimeout(request.Context(), handler.timeout)
	defer cancel()
	ready := handler.readiness.Check(ctx) == nil
	snapshot, err := handler.source.Snapshot(ctx, observedAt)
	snapshotOK := err == nil
	if !snapshotOK {
		handler.metrics.recordSnapshotFailure()
	}
	writer.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
	writer.Header().Set("X-Content-Type-Options", "nosniff")
	writer.WriteHeader(http.StatusOK)
	handler.writeMetrics(writer, ready, snapshotOK, snapshot)
}

func (handler *MetricsHandler) writeMetrics(writer io.Writer, ready, snapshotOK bool, snapshot OperationalSnapshot) {
	state := handler.metrics.state()
	writeHTTPMetrics(writer, state)
	writeGauge(writer, "market_info_readiness_status", "Whether runtime dependencies are ready (1 ready, 0 not ready).", nil, boolFloat(ready))
	writeGauge(writer, "market_info_operational_snapshot_success", "Whether the latest operational metrics snapshot succeeded.", nil, boolFloat(snapshotOK))
	writeCounter(writer, "market_info_operational_snapshot_failures_total", "Total failed operational metrics snapshots.", nil, float64(state.snapshotFailures))
	if snapshotOK {
		writeOperationalMetrics(writer, snapshot)
	}
}

func writeHTTPMetrics(writer io.Writer, state metricsState) {
	keys := make([]httpMetricKey, 0, len(state.http))
	for key := range state.http {
		keys = append(keys, key)
	}
	sort.Slice(keys, func(left, right int) bool {
		if keys[left].method != keys[right].method {
			return keys[left].method < keys[right].method
		}
		if keys[left].route != keys[right].route {
			return keys[left].route < keys[right].route
		}
		return keys[left].statusClass < keys[right].statusClass
	})
	writeHelpType(writer, "market_info_http_requests_total", "Total completed HTTP requests.", "counter")
	for _, key := range keys {
		labels := metricLabels{"method": key.method, "route": key.route, "status_class": key.statusClass}
		writeSample(writer, "market_info_http_requests_total", labels, float64(state.http[key].count))
	}
	writeHelpType(writer, "market_info_http_request_duration_seconds", "HTTP request duration in seconds.", "histogram")
	for _, key := range keys {
		value := state.http[key]
		base := metricLabels{"method": key.method, "route": key.route, "status_class": key.statusClass}
		for index, upperBound := range httpDurationBuckets {
			labels := cloneLabels(base)
			labels["le"] = strconv.FormatFloat(upperBound, 'g', -1, 64)
			writeSample(writer, "market_info_http_request_duration_seconds_bucket", labels, float64(value.buckets[index]))
		}
		labels := cloneLabels(base)
		labels["le"] = "+Inf"
		writeSample(writer, "market_info_http_request_duration_seconds_bucket", labels, float64(value.count))
		writeSample(writer, "market_info_http_request_duration_seconds_sum", base, value.sum)
		writeSample(writer, "market_info_http_request_duration_seconds_count", base, float64(value.count))
	}
}

func writeOperationalMetrics(writer io.Writer, snapshot OperationalSnapshot) {
	writeHelpType(writer, "market_info_ingestion_tasks", "Current ingestion tasks by durable status.", "gauge")
	for _, status := range taskMetricStatuses {
		writeSample(writer, "market_info_ingestion_tasks", metricLabels{"status": status}, float64(snapshot.Tasks.Counts[status]))
	}
	age := float64(0)
	if snapshot.Tasks.OldestBacklogCreatedAt != nil {
		age = max(0, snapshot.ObservedAt.Sub(*snapshot.Tasks.OldestBacklogCreatedAt).Seconds())
	}
	writeGauge(writer, "market_info_ingestion_backlog_oldest_age_seconds", "Age in seconds of the oldest pending or retry-wait task; zero when empty.", nil, age)

	writeHelpType(writer, "market_info_provider_health", "Current provider health as a one-hot gauge.", "gauge")
	writeHelpType(writer, "market_info_provider_consecutive_failures", "Maximum consecutive collection failures for a provider.", "gauge")
	writeHelpType(writer, "market_info_provider_last_success_timestamp_seconds", "Latest successful collection Unix timestamp for a provider.", "gauge")
	writeHelpType(writer, "market_info_provider_data_delay_seconds", "Maximum applicable data delay for a provider scope.", "gauge")
	writeHelpType(writer, "market_info_provider_active_subscriptions", "Active subscriptions in a provider scope.", "gauge")
	writeHelpType(writer, "market_info_provider_delayed_subscriptions", "Delayed subscriptions in a provider scope.", "gauge")
	for _, provider := range snapshot.Providers {
		providerLabel := provider.ProviderCode.String()
		for _, health := range providerHealthMetricStatuses {
			value := float64(0)
			if provider.HealthStatus == health {
				value = 1
			}
			writeSample(writer, "market_info_provider_health", metricLabels{"provider": providerLabel, "status": string(health)}, value)
		}
		writeSample(writer, "market_info_provider_consecutive_failures", metricLabels{"provider": providerLabel}, float64(provider.ConsecutiveFailures))
		if provider.LastSuccessAt != nil {
			writeSample(writer, "market_info_provider_last_success_timestamp_seconds", metricLabels{"provider": providerLabel}, float64(provider.LastSuccessAt.Unix()))
		}
		for _, scope := range provider.Scopes {
			labels := metricLabels{"provider": providerLabel, "market": scope.Market, "session_type": scope.SessionType, "interval": string(scope.Interval)}
			writeSample(writer, "market_info_provider_active_subscriptions", labels, float64(scope.ActiveSubscriptions))
			writeSample(writer, "market_info_provider_delayed_subscriptions", labels, float64(scope.DelayedSubscriptions))
			if scope.MarketState != "closed" && scope.FreshnessStatus != scheduler.FreshnessStatusNotApplicable && scope.DataDelaySeconds != nil {
				writeSample(writer, "market_info_provider_data_delay_seconds", labels, float64(*scope.DataDelaySeconds))
			}
		}
	}
}

func validateOperationalSnapshot(tasks TaskMetricsSnapshot, providers []application.ProviderStatus) error {
	for status, count := range tasks.Counts {
		if !containsString(taskMetricStatuses, status) || count < 0 {
			return errors.New("operational task metrics are invalid")
		}
	}
	if tasks.OldestBacklogCreatedAt != nil && tasks.OldestBacklogCreatedAt.IsZero() {
		return errors.New("operational backlog timestamp is invalid")
	}
	for _, provider := range providers {
		if provider.ProviderCode.IsZero() || !containsProviderHealth(providerHealthMetricStatuses, provider.HealthStatus) || provider.ConsecutiveFailures < 0 {
			return errors.New("operational provider metrics are invalid")
		}
	}
	return nil
}

type metricLabels map[string]string

func writeGauge(writer io.Writer, name, help string, labels metricLabels, value float64) {
	writeHelpType(writer, name, help, "gauge")
	writeSample(writer, name, labels, value)
}

func writeCounter(writer io.Writer, name, help string, labels metricLabels, value float64) {
	writeHelpType(writer, name, help, "counter")
	writeSample(writer, name, labels, value)
}

func writeHelpType(writer io.Writer, name, help, metricType string) {
	_, _ = fmt.Fprintf(writer, "# HELP %s %s\n# TYPE %s %s\n", name, help, name, metricType)
}

func writeSample(writer io.Writer, name string, labels metricLabels, value float64) {
	_, _ = io.WriteString(writer, name)
	if len(labels) > 0 {
		keys := make([]string, 0, len(labels))
		for key := range labels {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		_, _ = io.WriteString(writer, "{")
		for index, key := range keys {
			if index > 0 {
				_, _ = io.WriteString(writer, ",")
			}
			_, _ = fmt.Fprintf(writer, `%s="%s"`, key, escapeMetricLabel(labels[key]))
		}
		_, _ = io.WriteString(writer, "}")
	}
	_, _ = fmt.Fprintf(writer, " %s\n", strconv.FormatFloat(value, 'g', -1, 64))
}

func escapeMetricLabel(value string) string {
	value = strings.ReplaceAll(value, `\`, `\\`)
	value = strings.ReplaceAll(value, "\n", `\n`)
	return strings.ReplaceAll(value, `"`, `\"`)
}

func normalizedHTTPMethod(method string) string {
	switch method {
	case http.MethodGet, http.MethodPost, http.MethodPatch, http.MethodPut, http.MethodDelete, http.MethodHead, http.MethodOptions:
		return method
	default:
		return "OTHER"
	}
}

func httpStatusClass(status int) string {
	if status < 100 || status > 599 {
		return "unknown"
	}
	return strconv.Itoa(status/100) + "xx"
}

func boolFloat(value bool) float64 {
	if value {
		return 1
	}
	return 0
}

func cloneLabels(source metricLabels) metricLabels {
	clone := make(metricLabels, len(source)+1)
	for key, value := range source {
		clone[key] = value
	}
	return clone
}

func containsString(values []string, expected string) bool {
	for _, value := range values {
		if value == expected {
			return true
		}
	}
	return false
}

func containsProviderHealth(values []application.ProviderHealthStatus, expected application.ProviderHealthStatus) bool {
	for _, value := range values {
		if value == expected {
			return true
		}
	}
	return false
}
