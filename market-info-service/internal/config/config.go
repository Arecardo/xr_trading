// Package config loads and validates market information service configuration.
package config

import (
	"errors"
	"fmt"
	"net"
	"os"
	"strconv"
	"strings"
	"time"
)

const (
	defaultHTTPAddress      = ":8080"
	defaultReadTimeout      = 5 * time.Second
	defaultWriteTimeout     = 10 * time.Second
	defaultIdleTimeout      = 60 * time.Second
	defaultShutdownTimeout  = 10 * time.Second
	defaultReadinessTimeout = 2 * time.Second
	defaultDBMaxConns       = int32(8)
	defaultDBMinConns       = int32(1)
	defaultDBMaxConnLife    = 30 * time.Minute
	defaultDBHealthPeriod   = 30 * time.Second
	defaultAdminSubject     = "market-info-admin"
)

// Config contains runtime configuration for the HTTP process.
type Config struct {
	HTTPAddress      string
	ReadTimeout      time.Duration
	WriteTimeout     time.Duration
	IdleTimeout      time.Duration
	ShutdownTimeout  time.Duration
	ReadinessTimeout time.Duration
	DatabaseURL      string
	DBMaxConns       int32
	DBMinConns       int32
	DBMaxConnLife    time.Duration
	DBHealthPeriod   time.Duration
	AdminBearerToken string
	AdminSubject     string
}

// LookupEnv provides environment values to the configuration loader.
type LookupEnv func(string) (string, bool)

// Load reads configuration from the process environment.
func Load() (Config, error) {
	return LoadFrom(os.LookupEnv)
}

// LoadFrom reads configuration through lookup and validates the result.
func LoadFrom(lookup LookupEnv) (Config, error) {
	if lookup == nil {
		return Config{}, errors.New("environment lookup is required")
	}

	cfg := Config{
		HTTPAddress:      valueOrDefault(lookup, "MARKET_INFO_HTTP_ADDRESS", defaultHTTPAddress),
		ReadTimeout:      defaultReadTimeout,
		WriteTimeout:     defaultWriteTimeout,
		IdleTimeout:      defaultIdleTimeout,
		ShutdownTimeout:  defaultShutdownTimeout,
		ReadinessTimeout: defaultReadinessTimeout,
		DatabaseURL:      valueOrDefault(lookup, "MARKET_INFO_DATABASE_URL", ""),
		DBMaxConns:       defaultDBMaxConns,
		DBMinConns:       defaultDBMinConns,
		DBMaxConnLife:    defaultDBMaxConnLife,
		DBHealthPeriod:   defaultDBHealthPeriod,
		AdminBearerToken: valueOrDefault(lookup, "MARKET_INFO_ADMIN_BEARER_TOKEN", ""),
		AdminSubject:     valueOrDefault(lookup, "MARKET_INFO_ADMIN_SUBJECT", defaultAdminSubject),
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

	if err := cfg.Validate(); err != nil {
		return Config{}, err
	}
	return cfg, nil
}

// Validate checks configuration invariants.
func (c Config) Validate() error {
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
	if c.DatabaseURL == "" {
		return errors.New("MARKET_INFO_DATABASE_URL is required")
	}
	if !validCredentialText(c.AdminBearerToken, 4096) {
		return errors.New("MARKET_INFO_ADMIN_BEARER_TOKEN is required and must contain 1 to 4096 printable ASCII characters")
	}
	if c.AdminSubject == "" || c.AdminSubject != strings.TrimSpace(c.AdminSubject) || len([]rune(c.AdminSubject)) > 128 {
		return errors.New("MARKET_INFO_ADMIN_SUBJECT must be non-empty, trimmed and at most 128 characters")
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
	return nil
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
