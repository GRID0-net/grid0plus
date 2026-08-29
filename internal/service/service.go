// Package service is shared startup code for every SwitchNet service.
//
// It keeps the cmd/* entrypoints down to "load config, build one thing, serve",
// so that behaviour that must be identical everywhere — logging setup, graceful
// shutdown, signal handling — is written once.
package service

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/n-popescu/switchnet/config"
	"github.com/n-popescu/switchnet/internal/health"
)

// ConfigFlag registers the -config flag and returns a pointer to its value.
//
// An empty path is valid and means "defaults plus environment overrides", which
// is how the containers in docker-compose.yml run.
func ConfigFlag() *string {
	return flag.String("config", os.Getenv("SWITCHNET_CONFIG"),
		"path to config.yaml (empty: defaults plus SWITCHNET_* environment overrides)")
}

// Logger builds the logger from configuration.
func Logger(cfg config.Log, component string) *slog.Logger {
	var level slog.Level
	switch strings.ToLower(cfg.Level) {
	case "debug":
		level = slog.LevelDebug
	case "warn", "warning":
		level = slog.LevelWarn
	case "error":
		level = slog.LevelError
	default:
		level = slog.LevelInfo
	}

	opts := &slog.HandlerOptions{Level: level}
	var handler slog.Handler
	if strings.EqualFold(cfg.Format, "json") {
		handler = slog.NewJSONHandler(os.Stderr, opts)
	} else {
		handler = slog.NewTextHandler(os.Stderr, opts)
	}
	return slog.New(handler).With("service", component)
}

// SignalContext returns a context cancelled on SIGINT or SIGTERM.
func SignalContext() (context.Context, context.CancelFunc) {
	return signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
}

// shutdownGrace is how long an in-flight request has to finish once shutdown
// starts. A console request is a few milliseconds; ten seconds is enough for
// anything legitimate and short enough that a container restart is not held up.
const shutdownGrace = 10 * time.Second

// ServeHTTP runs an HTTP server until ctx is cancelled, then drains it.
//
// Read and write timeouts are set because these servers face consoles on a home
// network: a console that drops off mid-request must not hold a connection open
// indefinitely.
func ServeHTTP(ctx context.Context, log *slog.Logger, addr string, handler http.Handler) error {
	return ServeHTTPWithHealth(ctx, log, addr, handler, nil)
}

// ServeHTTPWithHealth is ServeHTTP with a /healthz endpoint in front.
//
// In front of the service's own router, not inside it: every service has its
// own mux that answers a Nintendo API, and threading health through each of
// them is how one gets missed. A nil health handler serves nothing extra, so
// this is also just ServeHTTP.
func ServeHTTPWithHealth(
	ctx context.Context, log *slog.Logger, addr string,
	handler http.Handler, healthz *health.Handler,
) error {
	if healthz != nil {
		inner := handler
		handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == health.Path {
				healthz.ServeHTTP(w, r)
				return
			}
			inner.ServeHTTP(w, r)
		})
	}

	srv := &http.Server{
		Addr:              addr,
		Handler:           handler,
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      30 * time.Second,
		// Long enough that a console reuses its connection across the several
		// requests a friend-list refresh makes, short enough that a console that
		// went to sleep does not hold a slot for minutes.
		IdleTimeout: 120 * time.Second,
	}

	errCh := make(chan error, 1)
	go func() {
		log.Info("listening", "addr", addr)
		err := srv.ListenAndServe()
		if errors.Is(err, http.ErrServerClosed) {
			err = nil
		}
		errCh <- err
	}()

	select {
	case err := <-errCh:
		if err != nil {
			return fmt.Errorf("serve %s: %w", addr, err)
		}
		return nil
	case <-ctx.Done():
		log.Info("shutting down")
		shutdownCtx, cancel := context.WithTimeout(context.Background(), shutdownGrace)
		defer cancel()
		if err := srv.Shutdown(shutdownCtx); err != nil {
			return fmt.Errorf("shutdown %s: %w", addr, err)
		}
		return <-errCh
	}
}

// Fatal logs an error and exits non-zero.
func Fatal(log *slog.Logger, msg string, err error) {
	log.Error(msg, "error", err)
	os.Exit(1)
}
