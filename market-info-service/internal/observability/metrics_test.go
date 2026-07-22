package observability

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"xr-trading/market-info-service/internal/application"
	"xr-trading/market-info-service/internal/domain"
	"xr-trading/market-info-service/internal/scheduler"
)

type taskMetricsReaderStub struct {
	snapshot TaskMetricsSnapshot
	err      error
	at       time.Time
}

func (stub *taskMetricsReaderStub) ReadTaskMetrics(_ context.Context, at time.Time) (TaskMetricsSnapshot, error) {
	stub.at = at
	return stub.snapshot, stub.err
}

type providerMetricsListerStub struct {
	providers []application.ProviderStatus
	err       error
}

func (stub *providerMetricsListerStub) List(context.Context) ([]application.ProviderStatus, error) {
	return stub.providers, stub.err
}

type operationalSourceStub struct {
	snapshot OperationalSnapshot
	err      error
}

func (stub operationalSourceStub) Snapshot(context.Context, time.Time) (OperationalSnapshot, error) {
	return stub.snapshot, stub.err
}

func TestOperationalMetricsSourceCombinesAndValidatesFacts(t *testing.T) {
	t.Parallel()

	observedAt := time.Date(2026, 7, 23, 12, 0, 0, 0, time.FixedZone("east", 8*60*60))
	tasks := &taskMetricsReaderStub{snapshot: TaskMetricsSnapshot{Counts: map[string]int64{"pending": 2}}}
	providers := &providerMetricsListerStub{providers: []application.ProviderStatus{metricProvider("bybit", application.ProviderHealthHealthy)}}
	source, err := NewOperationalMetricsSource(tasks, providers)
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := source.Snapshot(context.Background(), observedAt)
	if err != nil || !snapshot.ObservedAt.Equal(observedAt) || snapshot.ObservedAt.Location() != time.UTC || tasks.at.Location() != time.UTC || len(snapshot.Providers) != 1 {
		t.Fatalf("Snapshot() = (%#v, %v), taskAt=%v", snapshot, err, tasks.at)
	}
	if _, err := source.Snapshot(nil, observedAt); err == nil {
		t.Fatal("Snapshot(nil) error = nil")
	}
	if _, err := NewOperationalMetricsSource(nil, nil); err == nil {
		t.Fatal("NewOperationalMetricsSource(nil) error = nil")
	}

	tasks.err = errors.New("tasks unavailable")
	if _, err := source.Snapshot(context.Background(), observedAt); err == nil || !strings.Contains(err.Error(), "task metrics") {
		t.Fatalf("Snapshot(task failure) error = %v", err)
	}
	tasks.err = nil
	providers.err = errors.New("providers unavailable")
	if _, err := source.Snapshot(context.Background(), observedAt); err == nil || !strings.Contains(err.Error(), "provider metrics") {
		t.Fatalf("Snapshot(provider failure) error = %v", err)
	}
	providers.err = nil
	tasks.snapshot.Counts = map[string]int64{"invented": 1}
	if _, err := source.Snapshot(context.Background(), observedAt); err == nil {
		t.Fatal("Snapshot(invalid status) error = nil")
	}
}

func TestMetricsHandlerExposesBoundedAPITaskAndProviderMetrics(t *testing.T) {
	t.Parallel()

	observedAt := time.Date(2026, 7, 23, 12, 0, 0, 0, time.UTC)
	oldest := observedAt.Add(-2 * time.Hour)
	delay := int64(14400)
	closedDelay := int64(999999)
	provider := metricProvider("bybit", application.ProviderHealthUnhealthy)
	provider.ConsecutiveFailures = 3
	provider.LastSuccessAt = timePointer(observedAt.Add(-time.Hour))
	provider.Scopes = []application.ProviderScopeStatus{
		{Market: "crypto_spot", SessionType: "continuous", Interval: domain.BarInterval1Hour, MarketState: "open", FreshnessStatus: scheduler.FreshnessStatusDelayed, DataDelaySeconds: &delay, ActiveSubscriptions: 2, DelayedSubscriptions: 1},
		{Market: "us_equity", SessionType: "regular", Interval: domain.BarInterval1Day, MarketState: "closed", FreshnessStatus: scheduler.FreshnessStatusNotApplicable, DataDelaySeconds: &closedDelay, ActiveSubscriptions: 1},
	}
	snapshot := OperationalSnapshot{ObservedAt: observedAt, Tasks: TaskMetricsSnapshot{
		Counts:                 map[string]int64{"pending": 2, "running": 1, "retry_wait": 1, "success": 7, "failed": 3, "canceled": 1},
		OldestBacklogCreatedAt: &oldest,
	}, Providers: []application.ProviderStatus{provider}}
	metrics := NewMetrics()
	metrics.ObserveHTTPRequest(http.MethodGet, "/api/market-info/v1/bars", http.StatusOK, 750*time.Millisecond)
	metrics.ObserveHTTPRequest("TRACE", "unmatched", 700, -time.Second)
	handler, err := NewMetricsHandler(metrics, operationalSourceStub{snapshot: snapshot}, checkerFunc(func(context.Context) error { return nil }), time.Second, func() time.Time { return observedAt })
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	if err := handler.Register(mux); err != nil {
		t.Fatal(err)
	}
	response := httptest.NewRecorder()
	mux.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	text := response.Body.String()
	if response.Code != http.StatusOK || response.Header().Get("Content-Type") != "text/plain; version=0.0.4; charset=utf-8" {
		t.Fatalf("metrics response = %d %q", response.Code, response.Header().Get("Content-Type"))
	}
	for _, expected := range []string{
		`market_info_http_requests_total{method="GET",route="/api/market-info/v1/bars",status_class="2xx"} 1`,
		`market_info_http_request_duration_seconds_bucket{le="1",method="GET",route="/api/market-info/v1/bars",status_class="2xx"} 1`,
		`market_info_http_requests_total{method="OTHER",route="unmatched",status_class="unknown"} 1`,
		`market_info_readiness_status 1`, `market_info_operational_snapshot_success 1`,
		`market_info_ingestion_tasks{status="pending"} 2`, `market_info_ingestion_backlog_oldest_age_seconds 7200`,
		`market_info_provider_health{provider="bybit",status="unhealthy"} 1`,
		`market_info_provider_consecutive_failures{provider="bybit"} 3`,
		`market_info_provider_data_delay_seconds{interval="1h",market="crypto_spot",provider="bybit",session_type="continuous"} 14400`,
	} {
		if !strings.Contains(text, expected) {
			t.Fatalf("metrics output does not contain %q:\n%s", expected, text)
		}
	}
	if strings.Contains(text, "999999") || strings.Contains(text, "019f1452") || strings.Contains(text, "BTCUSDT") || strings.Contains(text, "error_message") {
		t.Fatalf("metrics exposed closed/high-cardinality/sensitive values:\n%s", text)
	}
}

func TestMetricsHandlerReportsDependencyFailuresAndConcurrentObservations(t *testing.T) {
	t.Parallel()

	metrics := NewMetrics()
	var group sync.WaitGroup
	for index := 0; index < 20; index++ {
		group.Add(1)
		go func() {
			defer group.Done()
			metrics.ObserveHTTPRequest(http.MethodPost, "/work", http.StatusServiceUnavailable, 15*time.Millisecond)
		}()
	}
	group.Wait()
	handler, _ := NewMetricsHandler(metrics, operationalSourceStub{err: errors.New("database URL must not leak")}, checkerFunc(func(context.Context) error { return ErrDatabaseUnavailable }), time.Second, time.Now)
	for requestNumber := 1; requestNumber <= 2; requestNumber++ {
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/metrics", nil))
		text := response.Body.String()
		if response.Code != http.StatusOK || !strings.Contains(text, `market_info_readiness_status 0`) || !strings.Contains(text, `market_info_operational_snapshot_success 0`) || !strings.Contains(text, `market_info_http_requests_total{method="POST",route="/work",status_class="5xx"} 20`) || strings.Contains(text, "database URL") {
			t.Fatalf("failure metrics response %d:\n%s", requestNumber, text)
		}
		if !strings.Contains(text, `market_info_operational_snapshot_failures_total `+string(rune('0'+requestNumber))) {
			t.Fatalf("snapshot failure count missing on response %d:\n%s", requestNumber, text)
		}
	}
	if _, err := NewMetricsHandler(nil, nil, nil, 0, nil); err == nil {
		t.Fatal("NewMetricsHandler(nil) error = nil")
	}
	var nilHandler *MetricsHandler
	if err := nilHandler.Register(http.NewServeMux()); err == nil {
		t.Fatal("nil MetricsHandler.Register error = nil")
	}
}

func metricProvider(code string, health application.ProviderHealthStatus) application.ProviderStatus {
	parsed, _ := domain.ParseCode(code)
	return application.ProviderStatus{ProviderCode: parsed, HealthStatus: health}
}

func timePointer(value time.Time) *time.Time { return &value }
