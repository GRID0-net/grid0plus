// Command npln is the NPLN game server for Splatoon 3 (phase 2).
//
// It serves gRPC. nginx must reach it with grpc_pass over cleartext HTTP/2, not
// proxy_pass — see internal/nintendo/hosts.go for what happens otherwise.
package main

import (
	"errors"
	"flag"
	"net"
	"time"

	"github.com/n-popescu/switchnet/config"
	"github.com/n-popescu/switchnet/internal/db"
	"github.com/n-popescu/switchnet/internal/health"
	sjwt "github.com/n-popescu/switchnet/internal/jwt"
	"github.com/n-popescu/switchnet/internal/npln"
	"github.com/n-popescu/switchnet/internal/nplnrest"
	"github.com/n-popescu/switchnet/internal/presencebus"
	"github.com/n-popescu/switchnet/internal/service"
)

func main() {
	configPath := service.ConfigFlag()
	flag.Parse()

	log := service.Logger(config.Default().Log, "npln")

	cfg, err := config.Load(*configPath)
	if err != nil {
		service.Fatal(log, "load config", err)
	}
	log = service.Logger(cfg.Log, "npln")

	ctx, cancel := service.SignalContext()
	defer cancel()

	store, err := db.OpenPostgres(ctx, cfg.Database.URL, cfg.Database.MaxConns, cfg.Database.AutoMigrate)
	if err != nil {
		service.Fatal(log, "open database", err)
	}
	defer store.Close()

	// The same key directory every other service reads. NPLN verifies BAAS's id
	// tokens, so pointing it somewhere else does not error -- it rejects every
	// session with an unhelpful message, which is the failure CLAUDE.md warns
	// about for the rest of the stack and is no different here.
	keys := sjwt.NewKeyring(cfg.Keys.Dir, cfg.Keys.GenerateIfMissing)

	accessKey, err := keys.ECKey(sjwt.IssuerNPLN)
	if err != nil {
		service.Fatal(log, "load npln signing key", err)
	}
	baasIDKey, err := keys.Key(sjwt.IssuerBAASID)
	if err != nil {
		service.Fatal(log, "load baas id key", err)
	}

	// This is a SEPARATE PROCESS from baas, so the in-process bus delivers
	// nothing to it -- silently. Without redis.url, Friends and
	// PresenceService's streams still work, just refreshed only on their own
	// keep-alive timer rather than the moment a friend's presence changes.
	// Same tradeoff internal/dashboard makes for the same reason -- see
	// cmd/dashboard/main.go.
	var bus presencebus.Bus
	if cfg.Redis.URL != "" {
		r, err := presencebus.NewRedis(ctx, cfg.Redis.URL, cfg.Redis.Channel, log)
		if err != nil {
			service.Fatal(log, "connect redis", err)
		}
		defer r.Close()
		bus = r
	} else {
		log.Warn("npln: redis.url is not set, so Friends and PresenceService only refresh " +
			"on their own keep-alive timer rather than the moment a friend's presence changes. " +
			"The in-process bus does not cross a process boundary: set redis.url for both baas " +
			"and npln to get live pushes.")
	}

	srv, err := npln.New(npln.Options{
		Store:              store,
		Log:                log,
		Config:             cfg.NPLN,
		AccessKey:          accessKey,
		BAASIDKey:          baasIDKey,
		PresenceTTL:        time.Duration(cfg.BAAS.PresenceTTL) * time.Second,
		ConsolePresenceTTL: time.Duration(cfg.BAAS.ConsolePresenceTTL) * time.Second,
		Bus:                bus,
	})
	if err != nil {
		service.Fatal(log, "build server", err)
	}

	// The matchmaking controls the dashboard drives. A separate listener from
	// the gRPC gateway, not on a Nintendo hostname, and served only when a token
	// is configured -- see internal/npln/admin.go.
	if admin := srv.AdminHandler(); admin != nil {
		if cfg.NPLN.AdminListen == "" {
			service.Fatal(log, "serve admin api",
				errors.New("npln.admin_token is set but npln.admin_listen is empty, "+
					"so the matchmaking controls would not be served anywhere"))
		}
		// Health rides on the admin listener rather than the gRPC port: gRPC
		// does not answer plain HTTP, and this is the port the dashboard
		// already reaches npln on.
		healthz := health.New("npln").Add("database", store.Ping)

		go func() {
			log.Info("npln: matchmaking controls listening", "addr", cfg.NPLN.AdminListen)
			if err := service.ServeHTTPWithHealth(ctx, log, cfg.NPLN.AdminListen, admin.Handler(), healthz); err != nil {
				log.Error("npln: matchmaking controls stopped", "error", err)
			}
		}()
	} else if cfg.NPLN.AdminListen != "" {
		log.Warn("npln: npln.admin_listen is set but npln.admin_token is not, so the " +
			"matchmaking controls are NOT served. They can start matches and change " +
			"how many players one takes, so they are not served without a token.")
	}

	// The JSON front end: vermillion and penne, which Splatoon 3 talks to
	// BEFORE it opens the gRPC channel above. A separate listener, plain
	// HTTP/1.1, because nginx reaches it with proxy_pass not grpc_pass --
	// see internal/nplnrest's package comment and internal/nintendo/hosts.go.
	if cfg.NPLN.RestListen == "" {
		service.Fatal(log, "serve npln rest",
			errors.New("npln.rest_listen is empty, so vermillion/penne/frontline "+
				"would not be served anywhere -- Splatoon 3 cannot reach the gRPC "+
				"gateway above without them"))
	}
	rest := nplnrest.New(nplnrest.Options{Log: log})
	go func() {
		log.Info("npln: rest front end listening", "addr", cfg.NPLN.RestListen)
		if err := service.ServeHTTP(ctx, log, cfg.NPLN.RestListen, rest.Handler()); err != nil {
			log.Error("npln: rest front end stopped", "error", err)
		}
	}()

	lis, err := net.Listen("tcp", cfg.NPLN.Listen)
	if err != nil {
		service.Fatal(log, "listen", err)
	}

	go func() {
		<-ctx.Done()
		log.Info("npln: shutting down")
		srv.Stop()
	}()

	log.Info("npln: listening", "addr", cfg.NPLN.Listen, "tenant", cfg.NPLN.Tenant)
	if err := srv.Serve(lis); err != nil {
		service.Fatal(log, "serve", err)
	}
}
