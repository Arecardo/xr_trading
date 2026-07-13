package postgres

import (
	"strings"
	"testing"
	"time"
)

func validConfig() Config {
	return Config{
		DatabaseURL:       "postgres://user:pass@localhost:5432/db?sslmode=disable",
		MaxConns:          8,
		MinConns:          1,
		MaxConnLifetime:   30 * time.Minute,
		HealthCheckPeriod: 30 * time.Second,
	}
}

func TestParsePoolConfigMapsSettings(t *testing.T) {
	t.Parallel()

	cfg := validConfig()
	poolConfig, err := ParsePoolConfig(cfg)
	if err != nil {
		t.Fatalf("ParsePoolConfig() error = %v", err)
	}
	if poolConfig.MaxConns != cfg.MaxConns || poolConfig.MinConns != cfg.MinConns {
		t.Fatalf("connection limits not mapped: %+v", poolConfig)
	}
	if poolConfig.MaxConnLifetime != cfg.MaxConnLifetime || poolConfig.HealthCheckPeriod != cfg.HealthCheckPeriod {
		t.Fatalf("durations not mapped: %+v", poolConfig)
	}
	if poolConfig.ConnConfig.Database != "db" {
		t.Fatalf("database = %q, want db", poolConfig.ConnConfig.Database)
	}
}

func TestParsePoolConfigRejectsInvalidInput(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		mutate    func(*Config)
		wantError string
	}{
		{"missing url", func(c *Config) { c.DatabaseURL = "" }, "database URL is required"},
		{"bad url", func(c *Config) { c.DatabaseURL = "://bad" }, "parse database URL"},
		{"zero max", func(c *Config) { c.MaxConns = 0 }, "max connections must be positive"},
		{"negative min", func(c *Config) { c.MinConns = -1 }, "min connections must not be negative"},
		{"min greater than max", func(c *Config) { c.MinConns = 9 }, "min connections must be less"},
		{"zero lifetime", func(c *Config) { c.MaxConnLifetime = 0 }, "max connection lifetime must be positive"},
		{"zero health period", func(c *Config) { c.HealthCheckPeriod = 0 }, "health check period must be positive"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			cfg := validConfig()
			tt.mutate(&cfg)
			_, err := ParsePoolConfig(cfg)
			if err == nil || !strings.Contains(err.Error(), tt.wantError) {
				t.Fatalf("ParsePoolConfig() error = %v, want containing %q", err, tt.wantError)
			}
		})
	}
}
