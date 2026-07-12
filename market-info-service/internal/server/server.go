// Package server owns the market information HTTP server lifecycle.
package server

import (
	"context"
	"errors"
	"net/http"
	"time"
)

// Config contains HTTP server lifecycle settings.
type Config struct {
	Address         string
	ReadTimeout     time.Duration
	WriteTimeout    time.Duration
	IdleTimeout     time.Duration
	ShutdownTimeout time.Duration
}

// Server wraps http.Server with graceful shutdown behavior.
type Server struct {
	httpServer      *http.Server
	shutdownTimeout time.Duration
	serve           func() error
	shutdown        func(context.Context) error
}

// New constructs a configured HTTP server.
func New(cfg Config, handler http.Handler) (*Server, error) {
	if handler == nil {
		return nil, errors.New("HTTP handler is required")
	}
	if cfg.Address == "" || cfg.ReadTimeout <= 0 || cfg.WriteTimeout <= 0 || cfg.IdleTimeout <= 0 || cfg.ShutdownTimeout <= 0 {
		return nil, errors.New("invalid HTTP server configuration")
	}
	httpServer := &http.Server{
		Addr:         cfg.Address,
		Handler:      handler,
		ReadTimeout:  cfg.ReadTimeout,
		WriteTimeout: cfg.WriteTimeout,
		IdleTimeout:  cfg.IdleTimeout,
	}
	return &Server{
		httpServer:      httpServer,
		shutdownTimeout: cfg.ShutdownTimeout,
		serve:           httpServer.ListenAndServe,
		shutdown:        httpServer.Shutdown,
	}, nil
}

// Run serves requests until ctx is canceled or the server fails.
func (s *Server) Run(ctx context.Context) error {
	errCh := make(chan error, 1)
	go func() {
		errCh <- s.serve()
	}()

	select {
	case err := <-errCh:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), s.shutdownTimeout)
		defer cancel()
		return s.shutdown(shutdownCtx)
	}
}
