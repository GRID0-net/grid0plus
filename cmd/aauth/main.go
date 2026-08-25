// Command aauth is SwitchNet's application authentication server.
//
// It reimplements https://aauth-lp1.ndas.srv.nintendo.net and serves the
// application token JWK set that the acert host publishes. It does not check
// game ownership; see internal/aauth for why.
package main

import (
	"flag"
	"net/http"
	"time"

	"github.com/n-popescu/switchnet/config"
	"github.com/n-popescu/switchnet/internal/aauth"
	sjwt "github.com/n-popescu/switchnet/internal/jwt"
	"github.com/n-popescu/switchnet/internal/service"
)

func main() {
	configPath := service.ConfigFlag()
	flag.Parse()

	log := service.Logger(config.Default().Log, "aauth")

	cfg, err := config.Load(*configPath)
	if err != nil {
		service.Fatal(log, "load config", err)
	}
	log = service.Logger(cfg.Log, "aauth")

	keys := sjwt.NewKeyring(cfg.Keys.Dir, cfg.Keys.GenerateIfMissing)

	appKey, err := keys.Key(sjwt.IssuerApp)
	if err != nil {
		service.Fatal(log, "load application signing key", err)
	}
	// The device token this server verifies was signed by the dauth service, so
	// both processes must read the same key directory. Running them with
	// different keys.dir values means every application token request is
	// rejected with "device token expired", which is a confusing way to find out.
	deviceKey, err := keys.Key(sjwt.IssuerDevice)
	if err != nil {
		service.Fatal(log, "load device verification key", err)
	}

	handler := aauth.New(aauth.Config{
		Issuer:         cfg.AAuth.Issuer,
		JWKSURL:        cfg.AAuth.JWKSURL,
		TokenTTL:       time.Duration(cfg.AAuth.TokenTTL) * time.Second,
		PolicyFor:      cfg.AAuth.PolicyFor,
		DeviceIDSecret: cfg.Keys.DeviceIDSecret,
	}, sjwt.NewIssuer(appKey, cfg.AAuth.JWKSURL, true), sjwt.NewVerifier(deviceKey), log)

	mux := http.NewServeMux()
	handler.Routes(mux)
	mux.HandleFunc("/", aauth.NotFound)

	ctx, cancel := service.SignalContext()
	defer cancel()

	if err := service.ServeHTTP(ctx, log, cfg.AAuth.Listen, mux); err != nil {
		service.Fatal(log, "serve", err)
	}
}
