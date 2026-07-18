package main

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/jackc/pgx/v5"

	"xr-trading/market-info-service/internal/api/httpapi"
	"xr-trading/market-info-service/internal/application"
	"xr-trading/market-info-service/internal/auth"
	"xr-trading/market-info-service/internal/config"
	"xr-trading/market-info-service/internal/database/migrations"
	"xr-trading/market-info-service/internal/database/postgres"
	"xr-trading/market-info-service/internal/database/readiness"
	"xr-trading/market-info-service/internal/domain"
	"xr-trading/market-info-service/internal/ingestion"
	"xr-trading/market-info-service/internal/markettime"
	"xr-trading/market-info-service/internal/observability"
	repositorypostgres "xr-trading/market-info-service/internal/repository/postgres"
	"xr-trading/market-info-service/internal/server"
)

type pooledDB interface {
	readiness.DB
	repositorypostgres.CatalogDatabase
	Begin(context.Context) (pgx.Tx, error)
	Close()
}

type openPool func(context.Context, postgres.Config) (pooledDB, error)
type loadConfig func() (config.Config, error)
type newServer func(server.Config, http.Handler) (*server.Server, error)

func main() {
	os.Exit(entrypoint())
}

func entrypoint() int {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if err := run(ctx, os.Args[1:], config.Load, openPostgresPool, server.New); err != nil {
		_, _ = fmt.Fprintln(os.Stderr, err)
		return 1
	}
	return 0
}

func run(ctx context.Context, args []string, load loadConfig, open openPool, createServer newServer) error {
	if load == nil || open == nil || createServer == nil {
		return errors.New("serve dependencies are required")
	}
	mode := "serve"
	if len(args) > 0 {
		mode = args[0]
	}
	if mode != "serve" {
		return fmt.Errorf("unsupported mode %q", mode)
	}

	cfg, err := load()
	if err != nil {
		return fmt.Errorf("load configuration: %w", err)
	}
	pool, err := open(ctx, postgres.Config{
		DatabaseURL:       cfg.DatabaseURL,
		MaxConns:          cfg.DBMaxConns,
		MinConns:          cfg.DBMinConns,
		MaxConnLifetime:   cfg.DBMaxConnLife,
		HealthCheckPeriod: cfg.DBHealthPeriod,
	})
	if err != nil {
		return fmt.Errorf("open database pool: %w", err)
	}
	defer pool.Close()

	checker, err := readiness.New(pool, migrations.LatestVersion)
	if err != nil {
		return fmt.Errorf("create readiness checker: %w", err)
	}
	health, err := observability.NewHealthHandler(checker, cfg.ReadinessTimeout)
	if err != nil {
		return fmt.Errorf("create health handler: %w", err)
	}
	catalog, err := repositorypostgres.NewCatalogRepository(pool)
	if err != nil {
		return fmt.Errorf("create catalog repository: %w", err)
	}
	instrumentOptions, err := application.NewInstrumentOptionsService(catalog, catalog, time.Now)
	if err != nil {
		return fmt.Errorf("create instrument options service: %w", err)
	}
	latestQuoteReader, err := repositorypostgres.NewLatestQuoteQueryRepository(pool)
	if err != nil {
		return fmt.Errorf("create latest quote query repository: %w", err)
	}
	latestQuotes, err := application.NewLatestQuotesService(catalog, latestQuoteReader, time.Now)
	if err != nil {
		return fmt.Errorf("create latest quote service: %w", err)
	}
	barReader, err := repositorypostgres.NewMarketBarQueryRepository(pool)
	if err != nil {
		return fmt.Errorf("create bar query repository: %w", err)
	}
	bars, err := application.NewBarsService(catalog, barReader, time.Now)
	if err != nil {
		return fmt.Errorf("create bar service: %w", err)
	}
	subscriptions, err := repositorypostgres.NewSubscriptionRepository(pool)
	if err != nil {
		return fmt.Errorf("create subscription repository: %w", err)
	}
	subscriptionService, err := application.NewSubscriptionService(subscriptions, subscriptions, time.Now, domain.NewID)
	if err != nil {
		return fmt.Errorf("create subscription service: %w", err)
	}
	ingestionRepository, err := repositorypostgres.NewIngestionRepository(pool)
	if err != nil {
		return fmt.Errorf("create ingestion repository: %w", err)
	}
	backfills, err := ingestion.NewBackfillService(ingestion.BackfillConfig{}, ingestionRepository, time.Now, domain.NewID)
	if err != nil {
		return fmt.Errorf("create backfill service: %w", err)
	}
	ingestionQueries, err := application.NewIngestionQueryService(ingestionRepository)
	if err != nil {
		return fmt.Errorf("create ingestion query service: %w", err)
	}
	runService, err := ingestion.NewRunService(ingestionRepository)
	if err != nil {
		return fmt.Errorf("create ingestion run service: %w", err)
	}
	taskCommands, err := ingestion.NewManualTaskService(ingestion.ManualTaskConfig{}, ingestionRepository, runService, time.Now, domain.NewID)
	if err != nil {
		return fmt.Errorf("create ingestion task command service: %w", err)
	}
	usCalendar, err := markettime.NewNYSECalendar()
	if err != nil {
		return fmt.Errorf("create US trading calendar: %w", err)
	}
	providerStatuses, err := application.NewProviderStatusService(ingestionRepository, time.Now, usCalendar)
	if err != nil {
		return fmt.Errorf("create provider status service: %w", err)
	}
	adminPrincipal, err := application.NewPrincipal(
		cfg.AdminSubject, application.ActorTypeUser,
		application.PermissionOperationsRead,
		application.PermissionSubscriptionsManage,
		application.PermissionIngestionManage,
	)
	if err != nil {
		return fmt.Errorf("create admin principal: %w", err)
	}
	authenticator, err := auth.NewStaticBearerAuthenticator([]auth.StaticCredential{{Token: cfg.AdminBearerToken, Principal: adminPrincipal}})
	if err != nil {
		return fmt.Errorf("create admin authenticator: %w", err)
	}
	mux := http.NewServeMux()
	health.Register(mux)
	if err := httpapi.RegisterPublicQueryRoutes(mux, httpapi.PublicQueryRoutes{
		InstrumentOptions: instrumentOptions,
		LatestQuotes:      latestQuotes,
		Bars:              bars,
	}); err != nil {
		return fmt.Errorf("register public query routes: %w", err)
	}
	if err := httpapi.RegisterSubscriptionRoutes(mux, subscriptionService, authenticator); err != nil {
		return fmt.Errorf("register subscription routes: %w", err)
	}
	if err := httpapi.RegisterBackfillRoutes(mux, backfills, authenticator); err != nil {
		return fmt.Errorf("register backfill routes: %w", err)
	}
	if err := httpapi.RegisterIngestionQueryRoutes(mux, ingestionQueries, authenticator); err != nil {
		return fmt.Errorf("register ingestion query routes: %w", err)
	}
	if err := httpapi.RegisterIngestionTaskCommandRoutes(mux, taskCommands, authenticator); err != nil {
		return fmt.Errorf("register ingestion task command routes: %w", err)
	}
	if err := httpapi.RegisterProviderStatusRoutes(mux, providerStatuses, authenticator); err != nil {
		return fmt.Errorf("register provider status routes: %w", err)
	}
	handler := httpapi.WithRequestID(mux)

	httpServer, err := createServer(server.Config{
		Address:         cfg.HTTPAddress,
		ReadTimeout:     cfg.ReadTimeout,
		WriteTimeout:    cfg.WriteTimeout,
		IdleTimeout:     cfg.IdleTimeout,
		ShutdownTimeout: cfg.ShutdownTimeout,
	}, handler)
	if err != nil {
		return fmt.Errorf("create HTTP server: %w", err)
	}
	if err := httpServer.Run(ctx); err != nil {
		return fmt.Errorf("run HTTP server: %w", err)
	}
	return nil
}

func openPostgresPool(ctx context.Context, cfg postgres.Config) (pooledDB, error) {
	return postgres.OpenPool(ctx, cfg)
}
