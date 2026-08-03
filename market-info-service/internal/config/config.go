// Package config loads and validates market information service configuration.
package config

import (
	"errors"
	"fmt"
	"net"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"
)

const (
	defaultHTTPAddress       = ":8080"
	defaultReadTimeout       = 5 * time.Second
	defaultWriteTimeout      = 10 * time.Second
	defaultIdleTimeout       = 60 * time.Second
	defaultShutdownTimeout   = 10 * time.Second
	defaultReadinessTimeout  = 2 * time.Second
	defaultDBMaxConns        = int32(8)
	defaultDBMinConns        = int32(1)
	defaultDBMaxConnLife     = 30 * time.Minute
	defaultDBHealthPeriod    = 30 * time.Second
	defaultAdminSubject      = "market-info-admin"
	defaultWorkerConcurrency = 4
	defaultWorkerLease       = 15 * time.Minute
	defaultWorkerPoll        = time.Second
	defaultWorkerBackoff     = 5 * time.Second
	defaultIngestionPageSize = 500
	defaultIngestionMaxPages = 20
	defaultSchedulerInterval = time.Minute
)

// RuntimeMode determines which process capabilities require configuration.
type RuntimeMode string

const (
	RuntimeModeServe  RuntimeMode = "serve"
	RuntimeModeWorker RuntimeMode = "worker"
	RuntimeModeAll    RuntimeMode = "all"
)

// Config contains runtime configuration for the HTTP and ingestion processes.
type Config struct {
	HTTPAddress             string
	ReadTimeout             time.Duration
	WriteTimeout            time.Duration
	IdleTimeout             time.Duration
	ShutdownTimeout         time.Duration
	ReadinessTimeout        time.Duration
	DatabaseURL             string
	DBMaxConns              int32
	DBMinConns              int32
	DBMaxConnLife           time.Duration
	DBHealthPeriod          time.Duration
	AdminBearerToken        string
	AdminSubject            string
	EnabledProviders        []string
	BybitBaseURL            string
	WorkerID                string
	WorkerConcurrency       int
	WorkerLeaseDuration     time.Duration
	WorkerPollInterval      time.Duration
	WorkerClaimErrorBackoff time.Duration
	IngestionBarsPerPage    int
	IngestionMaximumPages   int
	SchedulerInterval       time.Duration
}

// LookupEnv provides environment values to the configuration loader.
type LookupEnv func(string) (string, bool)

// Load reads configuration from the process environment.
func Load() (Config, error) {
	return LoadForMode(RuntimeModeServe)
}

// LoadForMode reads configuration and validates only the dependencies needed
// by the selected process mode.
func LoadForMode(mode RuntimeMode) (Config, error) {
	return LoadFromForMode(os.LookupEnv, mode)
}

// LoadFrom reads configuration through lookup and validates the result.
func LoadFrom(lookup LookupEnv) (Config, error) {
	return LoadFromForMode(lookup, RuntimeModeServe)
}

// LoadFromForMode reads configuration through lookup for one process mode.
func LoadFromForMode(lookup LookupEnv, mode RuntimeMode) (Config, error) {
	if lookup == nil {
		return Config{}, errors.New("environment lookup is required")
	}

	cfg := Config{
		HTTPAddress:             valueOrDefault(lookup, "MARKET_INFO_HTTP_ADDRESS", defaultHTTPAddress),
		ReadTimeout:             defaultReadTimeout,
		WriteTimeout:            defaultWriteTimeout,
		IdleTimeout:             defaultIdleTimeout,
		ShutdownTimeout:         defaultShutdownTimeout,
		ReadinessTimeout:        defaultReadinessTimeout,
		DatabaseURL:             valueOrDefault(lookup, "MARKET_INFO_DATABASE_URL", ""),
		DBMaxConns:              defaultDBMaxConns,
		DBMinConns:              defaultDBMinConns,
		DBMaxConnLife:           defaultDBMaxConnLife,
		DBHealthPeriod:          defaultDBHealthPeriod,
		AdminBearerToken:        valueOrDefault(lookup, "MARKET_INFO_ADMIN_BEARER_TOKEN", ""),
		AdminSubject:            valueOrDefault(lookup, "MARKET_INFO_ADMIN_SUBJECT", defaultAdminSubject),
		BybitBaseURL:            valueOrDefault(lookup, "BYBIT_BASE_URL", ""),
		WorkerID:                valueOrDefault(lookup, "MARKET_INFO_WORKER_ID", ""),
		WorkerConcurrency:       defaultWorkerConcurrency,
		WorkerLeaseDuration:     defaultWorkerLease,
		WorkerPollInterval:      defaultWorkerPoll,
		WorkerClaimErrorBackoff: defaultWorkerBackoff,
		IngestionBarsPerPage:    defaultIngestionPageSize,
		IngestionMaximumPages:   defaultIngestionMaxPages,
		SchedulerInterval:       defaultSchedulerInterval,
	}
	if value, ok := lookup("MARKET_INFO_ENABLED_PROVIDERS"); ok && value != "" {
		providers, err := parseEnabledProviders(value)
		if err != nil {
			return Config{}, err
		}
		cfg.EnabledProviders = providers
	}

	durations := []struct {
		name   string
		target *time.Duration
	}{
		{"MARKET_INFO_HTTP_READ_TIMEOUT", &cfg.ReadTimeout},
		{"MARKET_INFO_HTTP_WRITE_TIMEOUT", &cfg.WriteTimeout},
		{"MARKET_INFO_HTTP_IDLE_TIMEOUT", &cfg.IdleTimeout},
		{"MARKET_INFO_SHUTDOWN_TIMEOUT", &cfg.ShutdownTimeout},
		{"MARKET_INFO_READINESS_TIMEOUT", &cfg.ReadinessTimeout},
		{"MARKET_INFO_DB_MAX_CONN_LIFETIME", &cfg.DBMaxConnLife},
		{"MARKET_INFO_DB_HEALTH_CHECK_PERIOD", &cfg.DBHealthPeriod},
		{"MARKET_INFO_WORKER_LEASE_DURATION", &cfg.WorkerLeaseDuration},
		{"MARKET_INFO_WORKER_POLL_INTERVAL", &cfg.WorkerPollInterval},
		{"MARKET_INFO_WORKER_CLAIM_ERROR_BACKOFF", &cfg.WorkerClaimErrorBackoff},
		{"MARKET_INFO_SCHEDULER_INTERVAL", &cfg.SchedulerInterval},
	}
	for _, item := range durations {
		value, ok := lookup(item.name)
		if !ok || value == "" {
			continue
		}
		parsed, err := time.ParseDuration(value)
		if err != nil {
			return Config{}, fmt.Errorf("parse %s: %w", item.name, err)
		}
		*item.target = parsed
	}

	if value, ok := lookup("MARKET_INFO_DB_MAX_CONNS"); ok && value != "" {
		parsed, err := strconv.ParseInt(value, 10, 32)
		if err != nil {
			return Config{}, fmt.Errorf("parse MARKET_INFO_DB_MAX_CONNS: %w", err)
		}
		cfg.DBMaxConns = int32(parsed)
	}
	if value, ok := lookup("MARKET_INFO_DB_MIN_CONNS"); ok && value != "" {
		parsed, err := strconv.ParseInt(value, 10, 32)
		if err != nil {
			return Config{}, fmt.Errorf("parse MARKET_INFO_DB_MIN_CONNS: %w", err)
		}
		cfg.DBMinConns = int32(parsed)
	}
	integers := []struct {
		name   string
		target *int
	}{
		{"MARKET_INFO_WORKER_CONCURRENCY", &cfg.WorkerConcurrency},
		{"MARKET_INFO_INGESTION_BARS_PER_PAGE", &cfg.IngestionBarsPerPage},
		{"MARKET_INFO_INGESTION_MAXIMUM_PAGES", &cfg.IngestionMaximumPages},
	}
	for _, item := range integers {
		value, ok := lookup(item.name)
		if !ok || value == "" {
			continue
		}
		parsed, err := strconv.Atoi(value)
		if err != nil {
			return Config{}, fmt.Errorf("parse %s: %w", item.name, err)
		}
		*item.target = parsed
	}

	if err := cfg.ValidateForMode(mode); err != nil {
		return Config{}, err
	}
	return cfg, nil
}

// Validate checks configuration invariants.
func (c Config) Validate() error {
	return c.ValidateForMode(RuntimeModeServe)
}

// ValidateForMode checks common settings and the least-privilege dependencies
// needed by a serve, worker, or all process.
func (c Config) ValidateForMode(mode RuntimeMode) error {
	if mode != RuntimeModeServe && mode != RuntimeModeWorker && mode != RuntimeModeAll {
		return fmt.Errorf("unsupported runtime mode %q", mode)
	}
	if c.DatabaseURL == "" {
		return errors.New("MARKET_INFO_DATABASE_URL is required")
	}
	if c.DBMaxConns <= 0 {
		return errors.New("MARKET_INFO_DB_MAX_CONNS must be positive")
	}
	if c.DBMinConns < 0 {
		return errors.New("MARKET_INFO_DB_MIN_CONNS must not be negative")
	}
	if c.DBMinConns > c.DBMaxConns {
		return errors.New("MARKET_INFO_DB_MIN_CONNS must be less than or equal to MARKET_INFO_DB_MAX_CONNS")
	}
	if c.DBMaxConnLife <= 0 || c.DBHealthPeriod <= 0 {
		return errors.New("database connection lifetime and health check period must be positive")
	}
	if mode != RuntimeModeWorker {
		if _, port, err := net.SplitHostPort(c.HTTPAddress); err != nil || port == "" {
			return fmt.Errorf("MARKET_INFO_HTTP_ADDRESS must be a valid host:port: %q", c.HTTPAddress)
		} else if number, parseErr := strconv.Atoi(port); parseErr != nil || number < 1 || number > 65535 {
			return fmt.Errorf("MARKET_INFO_HTTP_ADDRESS contains an invalid port: %q", port)
		}
		durations := []struct {
			name  string
			value time.Duration
		}{
			{"MARKET_INFO_HTTP_READ_TIMEOUT", c.ReadTimeout},
			{"MARKET_INFO_HTTP_WRITE_TIMEOUT", c.WriteTimeout},
			{"MARKET_INFO_HTTP_IDLE_TIMEOUT", c.IdleTimeout},
			{"MARKET_INFO_SHUTDOWN_TIMEOUT", c.ShutdownTimeout},
			{"MARKET_INFO_READINESS_TIMEOUT", c.ReadinessTimeout},
		}
		for _, item := range durations {
			if item.value <= 0 {
				return fmt.Errorf("%s must be positive", item.name)
			}
		}
		if !validCredentialText(c.AdminBearerToken, 4096) {
			return errors.New("MARKET_INFO_ADMIN_BEARER_TOKEN is required and must contain 1 to 4096 printable ASCII characters")
		}
		if c.AdminSubject == "" || c.AdminSubject != strings.TrimSpace(c.AdminSubject) || len([]rune(c.AdminSubject)) > 128 {
			return errors.New("MARKET_INFO_ADMIN_SUBJECT must be non-empty, trimmed and at most 128 characters")
		}
	}
	if mode == RuntimeModeServe {
		return nil
	}
	if len(c.EnabledProviders) == 0 {
		return errors.New("MARKET_INFO_ENABLED_PROVIDERS is required in worker and all modes")
	}
	if _, err := parseEnabledProviders(strings.Join(c.EnabledProviders, ",")); err != nil {
		return err
	}
	if strings.TrimSpace(c.WorkerID) != c.WorkerID || len(c.WorkerID) > 128 {
		return errors.New("MARKET_INFO_WORKER_ID must be trimmed and at most 128 characters")
	}
	if c.WorkerConcurrency <= 0 {
		return errors.New("MARKET_INFO_WORKER_CONCURRENCY must be positive")
	}
	if c.WorkerLeaseDuration <= 0 || c.WorkerPollInterval <= 0 || c.WorkerClaimErrorBackoff <= 0 {
		return errors.New("worker lease, poll interval, and claim error backoff must be positive")
	}
	if c.IngestionBarsPerPage < 1 || c.IngestionBarsPerPage > 1000 {
		return errors.New("MARKET_INFO_INGESTION_BARS_PER_PAGE must be between 1 and 1000")
	}
	if c.IngestionMaximumPages < 1 || c.IngestionMaximumPages > 100 {
		return errors.New("MARKET_INFO_INGESTION_MAXIMUM_PAGES must be between 1 and 100")
	}
	if c.SchedulerInterval <= 0 {
		return errors.New("MARKET_INFO_SCHEDULER_INTERVAL must be positive")
	}
	return nil
}

func parseEnabledProviders(value string) ([]string, error) {
	parts := strings.Split(value, ",")
	providers := make([]string, 0, len(parts))
	seen := make(map[string]struct{}, len(parts))
	for _, part := range parts {
		provider := strings.TrimSpace(part)
		if provider != part || (provider != "bybit" && provider != "longbridge") {
			return nil, errors.New("MARKET_INFO_ENABLED_PROVIDERS accepts only comma-separated bybit and longbridge")
		}
		if _, exists := seen[provider]; exists {
			return nil, errors.New("MARKET_INFO_ENABLED_PROVIDERS must not contain duplicates")
		}
		seen[provider] = struct{}{}
		providers = append(providers, provider)
	}
	sort.Strings(providers)
	return providers, nil
}

func validCredentialText(value string, maximum int) bool {
	if value == "" || len(value) > maximum {
		return false
	}
	for _, character := range value {
		if character < 0x21 || character > 0x7e {
			return false
		}
	}
	return true
}

func valueOrDefault(lookup LookupEnv, name, fallback string) string {
	if value, ok := lookup(name); ok && value != "" {
		return value
	}
	return fallback
}
