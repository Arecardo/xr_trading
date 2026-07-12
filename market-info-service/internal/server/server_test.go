package server

import (
	"context"
	"errors"
	"net/http"
	"testing"
	"time"
)

func validConfig() Config {
	return Config{
		Address:         "127.0.0.1:0",
		ReadTimeout:     time.Second,
		WriteTimeout:    time.Second,
		IdleTimeout:     time.Second,
		ShutdownTimeout: time.Second,
	}
}

func TestNewValidation(t *testing.T) {
	t.Parallel()

	if _, err := New(validConfig(), nil); err == nil {
		t.Fatal("New(nil handler) error = nil, want error")
	}
	cfg := validConfig()
	cfg.ReadTimeout = 0
	if _, err := New(cfg, http.NewServeMux()); err == nil {
		t.Fatal("New(invalid config) error = nil, want error")
	}
}

func TestNewMapsConfiguration(t *testing.T) {
	t.Parallel()

	cfg := validConfig()
	server, err := New(cfg, http.NewServeMux())
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	if server.httpServer.Addr != cfg.Address || server.httpServer.ReadTimeout != cfg.ReadTimeout || server.shutdownTimeout != cfg.ShutdownTimeout {
		t.Fatalf("configuration not mapped: %+v", server.httpServer)
	}
}

func TestRunReturnsListenError(t *testing.T) {
	t.Parallel()

	server, _ := New(validConfig(), http.NewServeMux())
	server.serve = func() error { return errors.New("listen failed") }
	if err := server.Run(context.Background()); err == nil || err.Error() != "listen failed" {
		t.Fatal("Run() error = nil, want listen error")
	}
}

func TestRunTreatsServerClosedAsSuccess(t *testing.T) {
	t.Parallel()

	server, _ := New(validConfig(), http.NewServeMux())
	server.serve = func() error { return http.ErrServerClosed }
	if err := server.Run(context.Background()); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
}

func TestRunShutsDownOnCancellation(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	server, _ := New(validConfig(), http.NewServeMux())
	serveDone := make(chan struct{})
	server.serve = func() error {
		<-serveDone
		return http.ErrServerClosed
	}
	shutdownCalled := false
	server.shutdown = func(context.Context) error {
		shutdownCalled = true
		close(serveDone)
		return nil
	}
	if err := server.Run(ctx); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if !shutdownCalled {
		t.Fatal("shutdown was not called")
	}
}

func TestRunReturnsShutdownError(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	wantErr := errors.New("shutdown failed")
	server, _ := New(validConfig(), http.NewServeMux())
	serveDone := make(chan struct{})
	server.serve = func() error {
		<-serveDone
		return http.ErrServerClosed
	}
	server.shutdown = func(context.Context) error {
		close(serveDone)
		return wantErr
	}
	if err := server.Run(ctx); !errors.Is(err, wantErr) {
		t.Fatalf("Run() error = %v, want %v", err, wantErr)
	}
}
