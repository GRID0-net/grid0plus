// Command npln is the NPLN game server for Splatoon 3 (phase 2).
//
// It serves gRPC. nginx must reach it with grpc_pass over cleartext HTTP/2, not
// proxy_pass — see internal/nintendo/hosts.go for what happens otherwise.
package main

import (
	"errors"
	"flag"
	"net"

	"github.com/n-popescu/switchnet/config"
	"github.com/n-popescu/switchnet/internal/db"
	sjwt "github.com/n-popescu/switchnet/internal/jwt"
	"github.com/n-popescu/switchnet/internal/npln"
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

	srv, err := npln.New(npln.Options{
		Store:     store,
		Log:       log,
		Config:    cfg.NPLN,
		AccessKey: accessKey,
		BAASIDKey: baasIDKey,
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
		go func() {
			log.Info("npln: matchmaking controls listening", "addr", cfg.NPLN.AdminListen)
			if err := service.ServeHTTP(ctx, log, cfg.NPLN.AdminListen, admin.Handler()); err != nil {
				log.Error("npln: matchmaking controls stopped", "error", err)
			}
		}()
	} else if cfg.NPLN.AdminListen != "" {
		log.Warn("npln: npln.admin_listen is set but npln.admin_token is not, so the " +
			"matchmaking controls are NOT served. They can start matches and change " +
			"how many players one takes, so they are not served without a token.")
	}

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
