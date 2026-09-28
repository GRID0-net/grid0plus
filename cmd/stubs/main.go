// Command stubs answers the Nintendo endpoints GRID0+ redirects but does not
// implement. See internal/stubs for why answering them at all matters.
package main

import (
	"flag"

	"github.com/n-popescu/grid0plus/config"
	"github.com/n-popescu/grid0plus/internal/health"
	"github.com/n-popescu/grid0plus/internal/service"
	"github.com/n-popescu/grid0plus/internal/stubs"
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

	handler, err := stubs.New(stubs.Config{
		LogBodies:     cfg.Stubs.LogBodies,
		BCATRecordDir: cfg.Stubs.BCATRecordDir,
		BCATSeedDir:   cfg.BCAT.SeedDir,
	}, log)
	if err != nil {
		service.Fatal(log, "build handler", err)
	}

	ctx, cancel := service.SignalContext()
	defer cancel()

	// No dependency checks: these services hold their signing keys in memory and
	// talk to nothing else, so answering at all IS the health.
	healthz := health.New("stubs")

	if err := service.ServeHTTPWithHealth(ctx, log, cfg.Stubs.Listen, handler.Handler(), healthz); err != nil {
		service.Fatal(log, "serve", err)
	}
}
