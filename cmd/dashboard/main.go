// Command dashboard serves the operator's view of SwitchNet.
package main

import (
	"flag"
	"time"

	"github.com/n-popescu/switchnet/config"
	"github.com/n-popescu/switchnet/internal/dashboard"
	"github.com/n-popescu/switchnet/internal/db"
	"github.com/n-popescu/switchnet/internal/health"
	"github.com/n-popescu/switchnet/internal/presencebus"
	"github.com/n-popescu/switchnet/internal/service"
)

func main() {
	configPath := service.ConfigFlag()
	flag.Parse()

	log := service.Logger(config.Default().Log, "dashboard")

	cfg, err := config.Load(*configPath)
	if err != nil {
		service.Fatal(log, "load config", err)
	}
	log = service.Logger(cfg.Log, "dashboard")

	ctx, cancel := service.SignalContext()
	defer cancel()

	store, err := db.OpenPostgres(ctx, cfg.Database.URL, cfg.Database.MaxConns, cfg.Database.AutoMigrate)
	if err != nil {
		service.Fatal(log, "open database", err)
	}
	defer store.Close()

	// The dashboard is a SEPARATE PROCESS from baas, so the in-process bus
	// delivers nothing to it -- silently. Without redis.url the live view stays
	// empty forever and nothing says why, which is why this warns loudly rather
	// than starting quietly.
	var bus presencebus.Bus
	if cfg.Redis.URL != "" {
		r, err := presencebus.NewRedis(ctx, cfg.Redis.URL, cfg.Redis.Channel, log)
		if err != nil {
			service.Fatal(log, "connect redis", err)
		}
		defer r.Close()
		bus = r
	} else {
		log.Warn("dashboard: redis.url is not set, so there is no live presence view. " +
			"The in-process bus does not cross a process boundary: set redis.url " +
			"for both baas and the dashboard, or the page will never update.")
	}

	srv, err := dashboard.New(dashboard.Options{
		Store:       store,
		Bus:         bus,
		Log:         log,
		Tenant:      cfg.NPLN.Tenant,
		PresenceTTL: time.Duration(cfg.BAAS.PresenceTTL) * time.Second,
		Token:       cfg.Dashboard.Token,

		NPLNAdminURL:   cfg.Dashboard.NPLNAdminURL,
		NPLNAdminToken: cfg.Dashboard.NPLNAdminToken,
		Addresses:      dashboard.AddressesFrom(cfg),
	})
	if err != nil {
		service.Fatal(log, "build dashboard", err)
	}

	if cfg.Dashboard.Token == "" {
		log.Warn("dashboard: no dashboard.token is set, so anyone who can reach " +
			"this port can schedule and cancel Splatfests")
	}

	healthz := health.New("dashboard").Add("database", store.Ping)

	log.Info("dashboard: listening", "addr", cfg.Dashboard.Listen)
	if err := service.ServeHTTPWithHealth(ctx, log, cfg.Dashboard.Listen, srv.Handler(), healthz); err != nil {
		service.Fatal(log, "serve", err)
	}
}
