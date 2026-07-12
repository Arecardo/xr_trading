package config

import (
	"os"
	"strings"
	"testing"
	"time"
)

func TestLoadReadsProcessEnvironment(t *testing.T) {
	for _, name := range []string{
		"MARKET_INFO_HTTP_ADDRESS",
		"MARKET_INFO_HTTP_READ_TIMEOUT",
		"MARKET_INFO_HTTP_WRITE_TIMEOUT",
		"MARKET_INFO_HTTP_IDLE_TIMEOUT",
		"MARKET_INFO_SHUTDOWN_TIMEOUT",
		"MARKET_INFO_READINESS_TIMEOUT",
	} {
		t.Setenv(name, "")
	}
	t.Setenv("MARKET_INFO_HTTP_ADDRESS", "127.0.0.1:8181")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if cfg.HTTPAddress != "127.0.0.1:8181" {
		t.Fatalf("HTTPAddress = %q", cfg.HTTPAddress)
	}
	if _, ok := os.LookupEnv("MARKET_INFO_HTTP_ADDRESS"); !ok {
		t.Fatal("test environment was not set")
	}
}

func TestLoadFromDefaults(t *testing.T) {
	t.Parallel()

	cfg, err := LoadFrom(func(string) (string, bool) { return "", false })
	if err != nil {
		t.Fatalf("LoadFrom() error = %v", err)
	}
	if cfg.HTTPAddress != ":8080" {
		t.Fatalf("HTTPAddress = %q, want :8080", cfg.HTTPAddress)
	}
	if cfg.ReadTimeout != 5*time.Second || cfg.ReadinessTimeout != 2*time.Second {
		t.Fatalf("unexpected defaults: %+v", cfg)
	}
}

func TestLoadFromOverrides(t *testing.T) {
	t.Parallel()

	values := map[string]string{
		"MARKET_INFO_HTTP_ADDRESS":       "127.0.0.1:9090",
		"MARKET_INFO_HTTP_READ_TIMEOUT":  "1s",
		"MARKET_INFO_HTTP_WRITE_TIMEOUT": "2s",
		"MARKET_INFO_HTTP_IDLE_TIMEOUT":  "3s",
		"MARKET_INFO_SHUTDOWN_TIMEOUT":   "4s",
		"MARKET_INFO_READINESS_TIMEOUT":  "500ms",
	}
	cfg, err := LoadFrom(func(key string) (string, bool) {
		value, ok := values[key]
		return value, ok
	})
	if err != nil {
		t.Fatalf("LoadFrom() error = %v", err)
	}
	if cfg.HTTPAddress != "127.0.0.1:9090" || cfg.IdleTimeout != 3*time.Second || cfg.ReadinessTimeout != 500*time.Millisecond {
		t.Fatalf("unexpected overrides: %+v", cfg)
	}
}

func TestLoadFromRejectsInvalidInput(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		values    map[string]string
		wantError string
	}{
		{"bad duration", map[string]string{"MARKET_INFO_HTTP_READ_TIMEOUT": "later"}, "parse MARKET_INFO_HTTP_READ_TIMEOUT"},
		{"zero duration", map[string]string{"MARKET_INFO_SHUTDOWN_TIMEOUT": "0s"}, "must be positive"},
		{"bad address", map[string]string{"MARKET_INFO_HTTP_ADDRESS": "localhost"}, "valid host:port"},
		{"bad port", map[string]string{"MARKET_INFO_HTTP_ADDRESS": "localhost:70000"}, "invalid port"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			_, err := LoadFrom(func(key string) (string, bool) {
				value, ok := test.values[key]
				return value, ok
			})
			if err == nil || !strings.Contains(err.Error(), test.wantError) {
				t.Fatalf("LoadFrom() error = %v, want containing %q", err, test.wantError)
			}
		})
	}
}

func TestLoadFromRejectsNilLookup(t *testing.T) {
	t.Parallel()

	if _, err := LoadFrom(nil); err == nil {
		t.Fatal("LoadFrom(nil) error = nil, want error")
	}
}

func TestValidateRejectsNegativeDuration(t *testing.T) {
	t.Parallel()

	cfg, err := LoadFrom(func(string) (string, bool) { return "", false })
	if err != nil {
		t.Fatalf("LoadFrom() error = %v", err)
	}
	cfg.WriteTimeout = -time.Second
	if err := cfg.Validate(); err == nil {
		t.Fatal("Validate() error = nil, want error")
	}
}
