// Command dauth is GRID0+'s device authentication server.
//
// It reimplements https://dauth-lp1.ndas.srv.nintendo.net and serves the device
// token JWK set that the dcert host publishes. See internal/dauth for what it
// does and does not verify.
package main

import (
	"flag"
	"net/http"
	"time"

	"github.com/n-popescu/grid0plus/config"
	"github.com/n-popescu/grid0plus/internal/dauth"
	"github.com/n-popescu/grid0plus/internal/health"
	sjwt "github.com/n-popescu/grid0plus/internal/jwt"
	"github.com/n-popescu/grid0plus/internal/service"
)

func main() {
	configPath := service.ConfigFlag()
	flag.Parse()

	log := service.Logger(config.Default().Log, "dauth")

	cfg, err := config.Load(*configPath)
	if err != nil {
		service.Fatal(log, "load config", err)
	}
	log = service.Logger(cfg.Log, "dauth")

	keys := sjwt.NewKeyring(cfg.Keys.Dir, cfg.Keys.GenerateIfMissing)
	key, err := keys.Key(sjwt.IssuerDevice)
	if err != nil {
		service.Fatal(log, "load device signing key", err)
	}
	// Device tokens carry 'typ: JWT'.
	issuer := sjwt.NewIssuer(key, cfg.DAuth.JWKSURL, true)

	handler := dauth.New(dauth.Config{
		Issuer:              cfg.DAuth.Issuer,
		JWKSURL:             cfg.DAuth.JWKSURL,
		TokenTTL:            time.Duration(cfg.DAuth.TokenTTL) * time.Second,
		ChallengeTTL:        time.Duration(cfg.DAuth.ChallengeTTL) * time.Second,
		ClientCertHeader:    cfg.DAuth.ClientCertHeader,
		ClientCertPEMHeader: cfg.DAuth.ClientCertPEMHeader,
		DeviceIDSecret:      cfg.Keys.DeviceIDSecret,
	}, issuer, log, func(id, serial, systemVersion string, ist bool) {
		// Phase 1 only logs the console. Recording it in the devices table needs
		// a database connection, which dauth otherwise does not want: it is the
		// one service that has to answer before any account exists, so keeping
		// it free of a database dependency means a console can always get far
		// enough to see a useful error.
		log.Info("device authenticated",
			"device", id, "serial", serial, "system_version", systemVersion, "ist", ist)
	})

	mux := http.NewServeMux()
	handler.Routes(mux)
	mux.HandleFunc("/", dauth.NotFound)

	ctx, cancel := service.SignalContext()
	defer cancel()

	// No dependency checks: these services hold their signing keys in memory and
	// talk to nothing else, so answering at all IS the health.
	healthz := health.New("dauth")

	if err := service.ServeHTTPWithHealth(ctx, log, cfg.DAuth.Listen, mux, healthz); err != nil {
		service.Fatal(log, "serve", err)
	}
}
