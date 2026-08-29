// Command stubs answers the Nintendo endpoints SwitchNet redirects but does not
// implement. See internal/stubs for why answering them at all matters.
package main

import (
	"flag"

	"github.com/n-popescu/switchnet/config"
	"github.com/n-popescu/switchnet/internal/health"
	"github.com/n-popescu/switchnet/internal/service"
	"github.com/n-popescu/switchnet/internal/stubs"
)

func main() {
	configPath := service.ConfigFlag()
	flag.Parse()

	log := service.Logger(config.Default().Log, "stubs")

	cfg, err := config.Load(*configPath)
	if err != nil {
		service.Fatal(log, "load config", err)
	}
	log = service.Logger(cfg.Log, "stubs")

	handler := stubs.New(stubs.Config{
		LogBodies: cfg.Stubs.LogBodies,
		BCATProbe: cfg.Stubs.BCATProbe,
	}, log)
	stubs.LogBCATProbeState(log, cfg.Stubs.BCATProbe)

	ctx, cancel := service.SignalContext()
	defer cancel()

	// No dependency checks: these services hold their signing keys in memory and
	// talk to nothing else, so answering at all IS the health.
	healthz := health.New("stubs")

	if err := service.ServeHTTPWithHealth(ctx, log, cfg.Stubs.Listen, handler.Handler(), healthz); err != nil {
		service.Fatal(log, "serve", err)
	}
}
