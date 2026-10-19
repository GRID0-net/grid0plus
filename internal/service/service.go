// Package service is shared startup code for every GRID0+ service.
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
	"net"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/n-popescu/grid0plus/config"
	"github.com/n-popescu/grid0plus/internal/health"
)

// ConfigFlag registers the -config flag and returns a pointer to its value.
//
// An empty path is valid and means "defaults plus environment overrides" --
// every service run through switchnetctl (`run`, or the systemd units under
// deploy/systemd/) passes an explicit path instead, but a bare SWITCHNET_*
// environment with no config file at all still works for anything invoking
// a binary directly.
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

// heldRequestGrace is how long after shutdown starts the requests still open
// are cancelled. Shutdown waits for connections to go idle, and some never do
// on their own: the frontline a console holds, a dashboard live view. Without
// this every restart sat out shutdownGrace and exited 1 ("shutdown ...
// context deadline exceeded"). Ordinary requests finish well inside it.
const heldRequestGrace = 2 * time.Second

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
	return serveHTTP(ctx, log, addr, handler, healthz, 30*time.Second)
}

// ServeHTTPWithHealthUnbounded is ServeHTTPWithHealth without a whole-request
// read or write timeout, for services whose requests are legitimately long:
// a save-data upload over a home uplink (scsi), a 30 MB BCAT pack download
// or the live event stream (dashboard). The thirty-second limit cut each of
// those off mid-transfer. Headers must still arrive promptly, and idle
// connections are still closed.
func ServeHTTPWithHealthUnbounded(
	ctx context.Context, log *slog.Logger, addr string,
	handler http.Handler, healthz *health.Handler,
) error {
	return serveHTTP(ctx, log, addr, handler, healthz, 0)
}

func serveHTTP(
	ctx context.Context, log *slog.Logger, addr string,
	handler http.Handler, healthz *health.Handler, bodyTimeout time.Duration,
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
	return Serve(ctx, log, &http.Server{
		Addr:              addr,
		Handler:           handler,
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       bodyTimeout,
		WriteTimeout:      bodyTimeout,
		// Long enough that a console reuses its connection across the several
		// requests a friend-list refresh makes, short enough that a console that
		// went to sleep does not hold a slot for minutes.
		IdleTimeout: 120 * time.Second,
	})
}

// Serve runs a caller-built HTTP server until ctx is cancelled, then drains it.
//
// The caller owns the timeouts, which is the whole reason this is separate from
// ServeHTTP: NPLN's JSON front end holds a connection open indefinitely, and
// ServeHTTP's thirty-second write timeout would sever it. The console reads that
// close as the game losing its connection (2307-2103 on Splatoon 2, 2321-* on
// Splatoon 3), so a shared timeout that suits every other service is exactly
// wrong for that one.
func Serve(ctx context.Context, log *slog.Logger, srv *http.Server) error {
	addr := srv.Addr
	reqCtx, cancelRequests := context.WithCancel(context.Background())
	defer cancelRequests()
	if srv.BaseContext == nil {
		srv.BaseContext = func(net.Listener) context.Context { return reqCtx }
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
		held := time.AfterFunc(heldRequestGrace, cancelRequests)
		defer held.Stop()
		if err := srv.Shutdown(shutdownCtx); err != nil {
			// Something ignored its cancelled context; close it rather than
			// fail the stop over it.
			log.Warn("shutdown grace exceeded; closing remaining connections", "addr", addr)
			_ = srv.Close()
		}
		return <-errCh
	}
}

// Fatal logs an error and exits non-zero.
func Fatal(log *slog.Logger, msg string, err error) {
	log.Error(msg, "error", err)
	os.Exit(1)
}

// ClientIP returns the console's address as seen from outside the nginx edge.
//
// X-Forwarded-For is only trusted for its last entry, which is the one the
// immediately preceding proxy wrote. Trusting the first entry would let a
// console put whatever it liked in the header and have the server repeat it.
func ClientIP(r *http.Request) string {
	if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
		parts := strings.Split(xff, ",")
		if ip := strings.TrimSpace(parts[len(parts)-1]); ip != "" {
			return ip
		}
	}
	if host, _, err := net.SplitHostPort(r.RemoteAddr); err == nil {
		return host
	}
	return r.RemoteAddr
}
