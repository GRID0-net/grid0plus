// Command toolbox answers the public, unauthenticated API the GRID0+
// Toolbox homebrew app (GRID0-net/GRID0plus-cfw, toolbox/) talks to. See
// internal/toolbox for why this is its own service rather than a route on
// stubs or dashboard.
package main

import (
	"flag"
	"time"

	"github.com/n-popescu/grid0plus/config"
	"github.com/n-popescu/grid0plus/internal/health"
	"github.com/n-popescu/grid0plus/internal/service"
	"github.com/n-popescu/grid0plus/internal/toolbox"
)

func main() {
	configPath := service.ConfigFlag()
	flag.Parse()

	log := service.Logger(config.Default().Log, "toolbox")

	cfg, err := config.Load(*configPath)
	if err != nil {
		service.Fatal(log, "load config", err)
	}
	log = service.Logger(cfg.Log, "toolbox")

	handler := toolbox.New(toolbox.Config{
		GitHubOwner:     cfg.Toolbox.GitHubOwner,
		GitHubRepo:      cfg.Toolbox.GitHubRepo,
		GitHubToken:     cfg.Toolbox.GitHubToken,
		PublicBaseURL:   cfg.Toolbox.PublicBaseURL,
		ReleaseCacheTTL: time.Duration(cfg.Toolbox.ReleaseCacheTTLSeconds) * time.Second,
		StatusTargets: map[string]string{
			"dauth":     cfg.DAuth.Listen,
			"aauth":     cfg.AAuth.Listen,
			"baas":      cfg.BAAS.Listen,
			"stubs":     cfg.Stubs.Listen,
			"npln":      cfg.NPLN.AdminListen,
			"dashboard": cfg.Dashboard.Listen,
		},
		Unprobed: map[string]string{
			"natcheck": "UDP, not HTTP: no /healthz to ask",
		},
		PatchesRepo:    cfg.Toolbox.PatchesRepo,
		PatchesDir:     cfg.Toolbox.PatchesDir,
		PatchesRefresh: time.Duration(cfg.Toolbox.PatchesRefreshSeconds) * time.Second,
	}, log)

	ctx, cancel := service.SignalContext()
	defer cancel()

	// No dependency checks of its own: this service's only external call is
	// to GitHub, and that is exactly what /updates/latest already reports as
	// a 502 when it fails -- a second, duplicate signal on /healthz would
	// tell the operator nothing /updates/latest does not.
	healthz := health.New("toolbox")

	go handler.RunPatchMirror(ctx)

	if err := service.ServeHTTPWithHealth(ctx, log, cfg.Toolbox.Listen, handler.Handler(), healthz); err != nil {
		service.Fatal(log, "serve", err)
	}
}
