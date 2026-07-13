// Package postgres owns PostgreSQL pool construction and lifecycle.
package postgres

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// Config contains pgx pool settings.
type Config struct {
	DatabaseURL       string
	MaxConns          int32
	MinConns          int32
	MaxConnLifetime   time.Duration
	HealthCheckPeriod time.Duration
}

// OpenPool constructs a pgx pool and verifies that PostgreSQL is reachable.
func OpenPool(ctx context.Context, cfg Config) (*pgxpool.Pool, error) {
	poolConfig, err := ParsePoolConfig(cfg)
	if err != nil {
		return nil, err
	}
	pool, err := pgxpool.NewWithConfig(ctx, poolConfig)
	if err != nil {
		return nil, fmt.Errorf("open PostgreSQL pool: %w", err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("ping PostgreSQL: %w", err)
	}
	return pool, nil
}

// ParsePoolConfig validates Config and maps it to pgxpool.Config.
func ParsePoolConfig(cfg Config) (*pgxpool.Config, error) {
	if cfg.DatabaseURL == "" {
		return nil, errors.New("database URL is required")
	}
	if cfg.MaxConns <= 0 {
		return nil, errors.New("max connections must be positive")
	}
	if cfg.MinConns < 0 {
		return nil, errors.New("min connections must not be negative")
	}
	if cfg.MinConns > cfg.MaxConns {
		return nil, errors.New("min connections must be less than or equal to max connections")
	}
	if cfg.MaxConnLifetime <= 0 {
		return nil, errors.New("max connection lifetime must be positive")
	}
	if cfg.HealthCheckPeriod <= 0 {
		return nil, errors.New("health check period must be positive")
	}

	poolConfig, err := pgxpool.ParseConfig(cfg.DatabaseURL)
	if err != nil {
		return nil, fmt.Errorf("parse database URL: %w", err)
	}
	poolConfig.MaxConns = cfg.MaxConns
	poolConfig.MinConns = cfg.MinConns
	poolConfig.MaxConnLifetime = cfg.MaxConnLifetime
	poolConfig.HealthCheckPeriod = cfg.HealthCheckPeriod
	return poolConfig, nil
}
