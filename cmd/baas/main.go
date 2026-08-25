// Command baas is SwitchNet's account and friends server.
//
// It reimplements the BAAS API the stock Nintendo friends applet talks to. See
// internal/baas for the endpoints and for why identity resolution here is
// fail-closed.
package main

import (
	"context"
	"flag"
	"log/slog"
	"time"

	"github.com/n-popescu/switchnet/config"
	"github.com/n-popescu/switchnet/internal/baas"
	"github.com/n-popescu/switchnet/internal/db"
	sjwt "github.com/n-popescu/switchnet/internal/jwt"
	"github.com/n-popescu/switchnet/internal/presencebus"
	"github.com/n-popescu/switchnet/internal/service"
)

func main() {
	configPath := service.ConfigFlag()
	flag.Parse()

	log := service.Logger(config.Default().Log, "baas")

	cfg, err := config.Load(*configPath)
	if err != nil {
		service.Fatal(log, "load config", err)
	}
	log = service.Logger(cfg.Log, "baas")

	ctx, cancel := service.SignalContext()
	defer cancel()

	store, err := db.OpenPostgres(ctx, cfg.Database.URL, cfg.Database.MaxConns, cfg.Database.AutoMigrate)
	if err != nil {
		service.Fatal(log, "open database", err)
	}
	defer func() { _ = store.Close() }()

	bus, err := openBus(ctx, cfg, log)
	if err != nil {
		service.Fatal(log, "open presence bus", err)
	}
	defer func() { _ = bus.Close() }()

	keys := sjwt.NewKeyring(cfg.Keys.Dir, cfg.Keys.GenerateIfMissing)

	// Four keys, one per issuer. The two BAAS keys are published at separate
	// JWK set URLs and read by different consumers, and the dauth and aauth keys
	// are only ever verified here — sharing any of them would mean a token
	// minted for one purpose verifying as another.
	accessKey, err := keys.Key(sjwt.IssuerBAASAccess)
	if err != nil {
		service.Fatal(log, "load BAAS access signing key", err)
	}
	idKey, err := keys.Key(sjwt.IssuerBAASID)
	if err != nil {
		service.Fatal(log, "load BAAS id signing key", err)
	}
	deviceKey, err := keys.Key(sjwt.IssuerDevice)
	if err != nil {
		service.Fatal(log, "load device verification key", err)
	}
	appKey, err := keys.Key(sjwt.IssuerApp)
	if err != nil {
		service.Fatal(log, "load application verification key", err)
	}

	// BAAS tokens carry no 'typ' header, unlike device and application tokens.
	accessIssuer := sjwt.NewIssuer(accessKey, cfg.BAAS.AccessTokenJWKSURL, false)
	idIssuer := sjwt.NewIssuer(idKey, cfg.BAAS.IDTokenJWKSURL, false)

	srv := baas.New(baas.Config{
		Issuer:             cfg.BAAS.Issuer,
		Audience:           cfg.BAAS.Audience,
		AccessTokenJWKSURL: cfg.BAAS.AccessTokenJWKSURL,
		IDTokenJWKSURL:     cfg.BAAS.IDTokenJWKSURL,
		AccessTokenTTL:     time.Duration(cfg.BAAS.AccessTokenTTL) * time.Second,
		IDTokenTTL:         time.Duration(cfg.BAAS.IDTokenTTL) * time.Second,
		FriendCodeCooldown: time.Duration(cfg.BAAS.FriendCodeCooldown) * time.Second,
		PresenceTTL:        time.Duration(cfg.BAAS.PresenceTTL) * time.Second,
		ConsolePresenceTTL: time.Duration(cfg.BAAS.ConsolePresenceTTL) * time.Second,
		ImageDir:           cfg.BAAS.ImageDir,
		ImageBaseURL:       cfg.BAAS.ImageBaseURL,
		MaxImageBytes:      int64(cfg.BAAS.MaxImageBytes),
		AllowRegistration:  cfg.BAAS.AllowRegistration,
	}, baas.Deps{
		Store:          store,
		Bus:            bus,
		Log:            log,
		AccessIssuer:   accessIssuer,
		IDIssuer:       idIssuer,
		AccessVerifier: sjwt.NewVerifier(accessKey),
		DeviceVerifier: sjwt.NewVerifier(deviceKey),
		AppVerifier:    sjwt.NewVerifier(appKey),
	})

	if !cfg.BAAS.AllowRegistration {
		log.Info("registration is disabled; an unknown console cannot create an account")
	}

	if err := service.ServeHTTP(ctx, log, cfg.BAAS.Listen, srv.Handler()); err != nil {
		service.Fatal(log, "serve", err)
	}
}

// openBus returns the presence bus: Redis when configured, in-process otherwise.
func openBus(ctx context.Context, cfg config.Config, log *slog.Logger) (presencebus.Bus, error) {
	if cfg.Redis.URL == "" {
		log.Info("presence bus is in-process; configure redis.url before running " +
			"a second process that needs presence events")
		return presencebus.NewLocal(), nil
	}
	return presencebus.NewRedis(ctx, cfg.Redis.URL, cfg.Redis.Channel, log)
}
