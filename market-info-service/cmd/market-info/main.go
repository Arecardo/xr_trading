package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
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
	"xr-trading/market-info-service/internal/ingestion/ports"
	"xr-trading/market-info-service/internal/ingestion/worker"
	"xr-trading/market-info-service/internal/markettime"
	"xr-trading/market-info-service/internal/observability"
	"xr-trading/market-info-service/internal/providers"
	"xr-trading/market-info-service/internal/providers/bybit"
	"xr-trading/market-info-service/internal/providers/coingecko"
	"xr-trading/market-info-service/internal/providers/longbridge"
	repositorypostgres "xr-trading/market-info-service/internal/repository/postgres"
	"xr-trading/market-info-service/internal/scheduler"
	"xr-trading/market-info-service/internal/server"
	"xr-trading/market-info-service/internal/servicehost"
)

type pooledDB interface {
	readiness.DB
	repositorypostgres.CatalogDatabase
	Begin(context.Context) (pgx.Tx, error)
	Close()
}

type openPool func(context.Context, postgres.Config) (pooledDB, error)
type loadConfig func(config.RuntimeMode) (config.Config, error)
type newServer func(server.Config, http.Handler) (*server.Server, error)

func main() {
	os.Exit(entrypoint())
}

func entrypoint() int {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if err := run(ctx, os.Args[1:], config.LoadForMode, openPostgresPool, server.New); err != nil {
		observability.NewJSONLogger(os.Stderr, slog.LevelInfo).
			With(slog.String("service", "market-info-service")).
			Error("market information service stopped", slog.String("error_code", "service_failed"))
		return 1
	}
	return 0
}

func run(ctx context.Context, args []string, load loadConfig, open openPool, createServer newServer) error {
	if ctx == nil {
		return errors.New("runtime context is required")
	}
	if load == nil || open == nil {
		return errors.New("runtime configuration loader and database opener are required")
	}
	mode, err := parseRuntimeMode(args)
	if err != nil {
		return err
	}
	if (mode == config.RuntimeModeServe || mode == config.RuntimeModeAll) && createServer == nil {
		return errors.New("HTTP server constructor is required")
	}

	cfg, err := load(mode)
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

	logger := observability.NewJSONLogger(os.Stdout, slog.LevelInfo).With(slog.String("service", "market-info-service"))
	components := make([]servicehost.Component, 0, 3)
	if mode == config.RuntimeModeServe || mode == config.RuntimeModeAll {
		httpServer, err := buildHTTPServer(cfg, pool, createServer, logger)
		if err != nil {
			return err
		}
		components = append(components, servicehost.Component{Name: "http", Runner: httpServer})
	}
	var closeAdapters func() error
	if mode == config.RuntimeModeWorker || mode == config.RuntimeModeAll {
		background, close, err := buildBackgroundComponents(ctx, cfg, pool, logger)
		if err != nil {
			return err
		}
		closeAdapters = close
		components = append(components, background...)
	}
	runError := servicehost.Run(ctx, components...)
	var closeError error
	if closeAdapters != nil {
		closeError = closeAdapters()
	}
	if runError != nil {
		return fmt.Errorf("run %s mode: %w", mode, errors.Join(runError, closeError))
	}
	if closeError != nil {
		return fmt.Errorf("close %s mode adapters: %w", mode, closeError)
	}
	return nil
}

func parseRuntimeMode(args []string) (config.RuntimeMode, error) {
	if len(args) > 1 {
		return "", errors.New("expected at most one runtime mode argument")
	}
	mode := config.RuntimeModeServe
	if len(args) == 1 {
		mode = config.RuntimeMode(args[0])
	}
	switch mode {
	case config.RuntimeModeServe, config.RuntimeModeWorker, config.RuntimeModeAll:
		return mode, nil
	default:
		return "", fmt.Errorf("unsupported mode %q", mode)
	}
}

func buildHTTPServer(cfg config.Config, pool pooledDB, createServer newServer, logger *slog.Logger) (*server.Server, error) {
	checker, err := readiness.New(pool, migrations.LatestVersion)
	if err != nil {
		return nil, fmt.Errorf("create readiness checker: %w", err)
	}
	health, err := observability.NewHealthHandler(checker, cfg.ReadinessTimeout)
	if err != nil {
		return nil, fmt.Errorf("create health handler: %w", err)
	}
	catalog, err := repositorypostgres.NewCatalogRepository(pool)
	if err != nil {
		return nil, fmt.Errorf("create catalog repository: %w", err)
	}
	instrumentOptions, err := application.NewInstrumentOptionsService(catalog, catalog, time.Now)
	if err != nil {
		return nil, fmt.Errorf("create instrument options service: %w", err)
	}
	instrumentPrecision, err := application.NewInstrumentPrecisionService(catalog, time.Now)
	if err != nil {
		return nil, fmt.Errorf("create instrument precision service: %w", err)
	}
	latestQuoteReader, err := repositorypostgres.NewLatestQuoteQueryRepository(pool)
	if err != nil {
		return nil, fmt.Errorf("create latest quote query repository: %w", err)
	}
	latestQuotes, err := application.NewLatestQuotesService(catalog, latestQuoteReader, time.Now)
	if err != nil {
		return nil, fmt.Errorf("create latest quote service: %w", err)
	}
	barReader, err := repositorypostgres.NewMarketBarQueryRepository(pool)
	if err != nil {
		return nil, fmt.Errorf("create bar query repository: %w", err)
	}
	bars, err := application.NewBarsService(catalog, barReader, time.Now)
	if err != nil {
		return nil, fmt.Errorf("create bar service: %w", err)
	}
	subscriptions, err := repositorypostgres.NewSubscriptionRepository(pool)
	if err != nil {
		return nil, fmt.Errorf("create subscription repository: %w", err)
	}
	subscriptionService, err := application.NewSubscriptionService(subscriptions, subscriptions, time.Now, domain.NewID)
	if err != nil {
		return nil, fmt.Errorf("create subscription service: %w", err)
	}
	ingestionRepository, err := repositorypostgres.NewIngestionRepository(pool)
	if err != nil {
		return nil, fmt.Errorf("create ingestion repository: %w", err)
	}
	backfills, err := ingestion.NewBackfillService(ingestion.BackfillConfig{}, ingestionRepository, time.Now, domain.NewID)
	if err != nil {
		return nil, fmt.Errorf("create backfill service: %w", err)
	}
	ingestionQueries, err := application.NewIngestionQueryService(ingestionRepository)
	if err != nil {
		return nil, fmt.Errorf("create ingestion query service: %w", err)
	}
	runService, err := ingestion.NewRunService(ingestionRepository)
	if err != nil {
		return nil, fmt.Errorf("create ingestion run service: %w", err)
	}
	taskCommands, err := ingestion.NewManualTaskService(ingestion.ManualTaskConfig{}, ingestionRepository, runService, time.Now, domain.NewID)
	if err != nil {
		return nil, fmt.Errorf("create ingestion task command service: %w", err)
	}
	usCalendar, err := markettime.NewNYSECalendar()
	if err != nil {
		return nil, fmt.Errorf("create US trading calendar: %w", err)
	}
	providerStatuses, err := application.NewProviderStatusService(ingestionRepository, time.Now, usCalendar)
	if err != nil {
		return nil, fmt.Errorf("create provider status service: %w", err)
	}
	operationalMetrics, err := observability.NewOperationalMetricsSource(ingestionRepository, providerStatuses)
	if err != nil {
		return nil, fmt.Errorf("create operational metrics source: %w", err)
	}
	metrics := observability.NewMetrics()
	metricsHandler, err := observability.NewMetricsHandler(metrics, operationalMetrics, checker, cfg.ReadinessTimeout, time.Now)
	if err != nil {
		return nil, fmt.Errorf("create metrics handler: %w", err)
	}
	adminPrincipal, err := application.NewPrincipal(
		cfg.AdminSubject, application.ActorTypeUser,
		application.PermissionOperationsRead,
		application.PermissionSubscriptionsManage,
		application.PermissionIngestionManage,
	)
	if err != nil {
		return nil, fmt.Errorf("create admin principal: %w", err)
	}
	authenticator, err := auth.NewStaticBearerAuthenticator([]auth.StaticCredential{{Token: cfg.AdminBearerToken, Principal: adminPrincipal}})
	if err != nil {
		return nil, fmt.Errorf("create admin authenticator: %w", err)
	}
	mux := http.NewServeMux()
	health.Register(mux)
	if err := metricsHandler.Register(mux); err != nil {
		return nil, fmt.Errorf("register metrics route: %w", err)
	}
	if err := httpapi.RegisterPublicQueryRoutes(mux, httpapi.PublicQueryRoutes{
		InstrumentOptions:   instrumentOptions,
		InstrumentPrecision: instrumentPrecision,
		LatestQuotes:        latestQuotes,
		Bars:                bars,
	}); err != nil {
		return nil, fmt.Errorf("register public query routes: %w", err)
	}
	if err := httpapi.RegisterSubscriptionRoutes(mux, subscriptionService, authenticator); err != nil {
		return nil, fmt.Errorf("register subscription routes: %w", err)
	}
	if err := httpapi.RegisterBackfillRoutes(mux, backfills, authenticator); err != nil {
		return nil, fmt.Errorf("register backfill routes: %w", err)
	}
	if err := httpapi.RegisterIngestionQueryRoutes(mux, ingestionQueries, authenticator); err != nil {
		return nil, fmt.Errorf("register ingestion query routes: %w", err)
	}
	if err := httpapi.RegisterIngestionTaskCommandRoutes(mux, taskCommands, authenticator); err != nil {
		return nil, fmt.Errorf("register ingestion task command routes: %w", err)
	}
	if err := httpapi.RegisterProviderStatusRoutes(mux, providerStatuses, authenticator); err != nil {
		return nil, fmt.Errorf("register provider status routes: %w", err)
	}
	observeHTTP, err := httpapi.NewObservabilityMiddleware(logger, time.Now, metrics)
	if err != nil {
		return nil, fmt.Errorf("create HTTP observability middleware: %w", err)
	}
	// Request ID is outermost so recovery and every access event share it.
	handler := httpapi.WithRequestID(observeHTTP(mux))

	httpServer, err := createServer(server.Config{
		Address:         cfg.HTTPAddress,
		ReadTimeout:     cfg.ReadTimeout,
		WriteTimeout:    cfg.WriteTimeout,
		IdleTimeout:     cfg.IdleTimeout,
		ShutdownTimeout: cfg.ShutdownTimeout,
	}, handler)
	if err != nil {
		return nil, fmt.Errorf("create HTTP server: %w", err)
	}
	return httpServer, nil
}

func buildBackgroundComponents(ctx context.Context, cfg config.Config, pool pooledDB, logger *slog.Logger) ([]servicehost.Component, func() error, error) {
	usCalendar, err := markettime.NewNYSECalendar()
	if err != nil {
		return nil, nil, fmt.Errorf("create worker US trading calendar: %w", err)
	}
	registry, closeAdapters, err := buildAdapterRegistry(ctx, cfg, usCalendar)
	if err != nil {
		return nil, nil, err
	}
	fail := func(err error) ([]servicehost.Component, func() error, error) {
		_ = closeAdapters()
		return nil, nil, err
	}

	repository, err := repositorypostgres.NewIngestionRepository(pool)
	if err != nil {
		return fail(fmt.Errorf("create worker ingestion repository: %w", err))
	}
	ingestionService, err := ingestion.NewService(ingestion.Config{
		BarsPerPage:  cfg.IngestionBarsPerPage,
		MaximumPages: cfg.IngestionMaximumPages,
	}, repository, registry, ingestion.NewStructuralBarQualityValidator(), time.Now)
	if err != nil {
		return fail(fmt.Errorf("create ingestion service: %w", err))
	}
	workerID := cfg.WorkerID
	if workerID == "" {
		id, err := domain.NewID()
		if err != nil {
			return fail(fmt.Errorf("create worker identity: %w", err))
		}
		workerID = "market-info-worker-" + id.String()
	}
	ingestionWorker, err := worker.New(worker.Config{
		WorkerID:          workerID,
		Concurrency:       cfg.WorkerConcurrency,
		LeaseDuration:     cfg.WorkerLeaseDuration,
		PollInterval:      cfg.WorkerPollInterval,
		ClaimErrorBackoff: cfg.WorkerClaimErrorBackoff,
	}, worker.Dependencies{
		Claimer:  repository,
		Executor: ingestionService,
		ReportError: func(error) {
			logger.Error("ingestion worker iteration failed", slog.String("error_code", "worker_iteration_failed"))
		},
	})
	if err != nil {
		return fail(fmt.Errorf("create ingestion worker: %w", err))
	}
	incremental, err := scheduler.NewIncrementalScheduler(
		scheduler.IncrementalConfig{},
		repository,
		scheduler.SystemClock{},
		domain.NewID,
		usCalendar,
	)
	if err != nil {
		return fail(fmt.Errorf("create incremental scheduler: %w", err))
	}
	schedulerLoop, err := servicehost.NewSchedulerLoop(
		servicehost.SchedulerLoopConfig{Interval: cfg.SchedulerInterval},
		incremental,
		slogSchedulerReporter{logger: logger},
	)
	if err != nil {
		return fail(fmt.Errorf("create scheduler loop: %w", err))
	}
	return []servicehost.Component{
		{Name: "scheduler", Runner: schedulerLoop},
		{Name: "worker", Runner: ingestionWorker},
	}, closeAdapters, nil
}

func buildAdapterRegistry(ctx context.Context, cfg config.Config, usCalendar markettime.TradingCalendar) (*providers.Registry, func() error, error) {
	adapters := make([]ports.MarketDataAdapter, 0, len(cfg.EnabledProviders))
	closers := make([]io.Closer, 0, len(cfg.EnabledProviders))
	closeAll := func() error {
		var closeError error
		for index := len(closers) - 1; index >= 0; index-- {
			closeError = errors.Join(closeError, closers[index].Close())
		}
		return closeError
	}
	for _, providerCode := range cfg.EnabledProviders {
		switch providerCode {
		case "bybit":
			adapter, err := bybit.New(bybit.Config{BaseURL: cfg.BybitBaseURL})
			if err != nil {
				_ = closeAll()
				return nil, nil, fmt.Errorf("create Bybit adapter: %w", err)
			}
			adapters = append(adapters, adapter)
		case "coingecko":
			adapter, err := coingecko.New(coingecko.Config{BaseURL: cfg.CoinGeckoBaseURL})
			if err != nil {
				_ = closeAll()
				return nil, nil, fmt.Errorf("create CoinGecko adapter: %w", err)
			}
			adapters = append(adapters, adapter)
		case "longbridge":
			adapter, err := longbridge.NewFromEnvironment(longbridge.Config{MarketCalendar: usCalendar})
			if err != nil {
				_ = closeAll()
				return nil, nil, fmt.Errorf("create Longbridge adapter: %w", err)
			}
			adapters = append(adapters, adapter)
			closers = append(closers, adapter)
		default:
			_ = closeAll()
			return nil, nil, fmt.Errorf("unsupported configured provider %q", providerCode)
		}
	}
	registry, err := providers.NewRegistry(ctx, adapters...)
	if err != nil {
		_ = closeAll()
		return nil, nil, fmt.Errorf("create adapter registry: %w", err)
	}
	return registry, closeAll, nil
}

type slogSchedulerReporter struct{ logger *slog.Logger }

func (reporter slogSchedulerReporter) ReportSchedulerResult(result scheduler.IncrementalResult) {
	reporter.logger.Info(
		"incremental scheduler cycle completed",
		slog.Int("scanned_subscriptions", result.ScannedSubscriptions),
		slog.Int("due_windows", result.DueWindows),
		slog.Int("created_runs", result.CreatedRuns),
		slog.Int("existing_runs", result.ExistingRuns),
		slog.Int64("recovered_tasks", result.RecoveredTasks),
	)
}

func (reporter slogSchedulerReporter) ReportSchedulerError(error) {
	reporter.logger.Error("incremental scheduler cycle failed", slog.String("error_code", "scheduler_cycle_failed"))
}

func openPostgresPool(ctx context.Context, cfg postgres.Config) (pooledDB, error) {
	return postgres.OpenPool(ctx, cfg)
}
