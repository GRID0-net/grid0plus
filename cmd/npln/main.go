// Command npln is the NPLN game server for Splatoon 3 (phase 2).
//
// It runs two listeners, and they are not interchangeable:
//
//   - the gRPC gateway, which nginx must reach with grpc_pass over cleartext
//     HTTP/2;
//   - the JSON front end, which nginx must reach with proxy_pass over HTTP/1.1.
//
// One process because they share the title's configuration and nothing else
// about them is separable; two listeners because crossing the nginx blocks
// completes the TLS handshake and then fails with nothing in either log. See
// internal/nintendo/hosts.go.
package main

import (
	"flag"
	"net"
	"net/http"
	"time"

	"github.com/n-popescu/switchnet/config"
	"github.com/n-popescu/switchnet/internal/db"
	sjwt "github.com/n-popescu/switchnet/internal/jwt"
	"github.com/n-popescu/switchnet/internal/npln"
	"github.com/n-popescu/switchnet/internal/nplnrest"
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

	rest, err := nplnrest.New(nplnrest.Options{
		Log:           log.With("listener", "rest"),
		FrontlineHost: cfg.NPLN.FrontlineHost,
		RecordDir:     cfg.NPLN.RestRecordDir,
	})
	if err != nil {
		service.Fatal(log, "build json front end", err)
	}
	if cfg.NPLN.RestRecordDir != "" {
		log.Warn("npln: recording every JSON front-end request",
			"dir", cfg.NPLN.RestRecordDir)
	}

	lis, err := net.Listen("tcp", cfg.NPLN.Listen)
	if err != nil {
		service.Fatal(log, "listen", err)
	}

	// The JSON front end runs alongside, on its own listener, with NO write or
	// read timeout: the frontline connection is held open by the console for as
	// long as it wants it, and a timeout here severs it. Only the header
	// timeout is kept, which bounds a client that connects and says nothing.
	restErr := make(chan error, 1)
	go func() {
		restErr <- service.Serve(ctx, log.With("listener", "rest"), &http.Server{
			Addr:              cfg.NPLN.RestListen,
			Handler:           rest.Handler(),
			ReadHeaderTimeout: 10 * time.Second,
			IdleTimeout:       120 * time.Second,
		})
	}()

	go func() {
		<-ctx.Done()
		log.Info("npln: shutting down")
		srv.Stop()
	}()

	log.Info("npln: listening", "addr", cfg.NPLN.Listen, "tenant", cfg.NPLN.Tenant,
		"rest", cfg.NPLN.RestListen, "frontline", cfg.NPLN.FrontlineHost)
	if err := srv.Serve(lis); err != nil {
		service.Fatal(log, "serve", err)
	}
	if err := <-restErr; err != nil {
		service.Fatal(log, "serve json front end", err)
	}
}
