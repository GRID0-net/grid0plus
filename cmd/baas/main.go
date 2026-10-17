// Command baas is GRID0+'s account and friends server.
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

	"github.com/n-popescu/grid0plus/config"
	"github.com/n-popescu/grid0plus/internal/baas"
	"github.com/n-popescu/grid0plus/internal/dashboard"
	"github.com/n-popescu/grid0plus/internal/db"
	"github.com/n-popescu/grid0plus/internal/health"
	sjwt "github.com/n-popescu/grid0plus/internal/jwt"
	"github.com/n-popescu/grid0plus/internal/presencebus"
	"github.com/n-popescu/grid0plus/internal/service"
	"github.com/n-popescu/grid0plus/internal/splat3save"
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

	// Push notifications for consoles (friend requests and removals), read
	// by npln's frontline. Only over Redis: in-process, nothing would read
	// them.
	var notifyBus presencebus.Bus
	if cfg.Redis.URL != "" {
		nb, err := presencebus.NewRedis(ctx, cfg.Redis.URL, cfg.Redis.Channel+".notify", log)
		if err != nil {
			service.Fatal(log, "open notification bus", err)
		}
		defer func() { _ = nb.Close() }()
		notifyBus = nb
	}

	// New fest entries from npln, streamed to the Discord bot so a player who
	// picks a team in game gets its role at once. Only over Redis, for the
	// same reason as notifyBus.
	var festBus presencebus.Bus
	if cfg.Redis.URL != "" {
		fb, err := presencebus.NewRedis(ctx, cfg.Redis.URL, cfg.Redis.Channel+presencebus.FestChannelSuffix, log)
		if err != nil {
			service.Fatal(log, "open fest entry bus", err)
		}
		defer func() { _ = fb.Close() }()
		festBus = fb
	}

	keys := sjwt.NewKeyring(cfg.Keys.Dir, cfg.Keys.GenerateIfMissing)

	// Five keys, one per issuer. The BAAS keys are published at JWK set URLs
	// and read by different consumers, and the dauth and aauth keys are only
	// ever verified here — sharing any of them would mean a token minted for
	// one purpose verifying as another. IssuerNAID and IssuerBAASID publish at
	// the *same* URL (both are id tokens) but must still be different keys —
	// see IssuerNAID's doc comment.
	accessKey, err := keys.Key(sjwt.IssuerBAASAccess)
	if err != nil {
		service.Fatal(log, "load BAAS access signing key", err)
	}
	idKey, err := keys.Key(sjwt.IssuerBAASID)
	if err != nil {
		service.Fatal(log, "load BAAS id signing key", err)
	}
	naIDKey, err := keys.Key(sjwt.IssuerNAID)
	if err != nil {
		service.Fatal(log, "load Nintendo Account link id signing key", err)
	}
	deviceKey, err := keys.Key(sjwt.IssuerDevice)
	if err != nil {
		service.Fatal(log, "load device verification key", err)
	}
	appKey, err := keys.Key(sjwt.IssuerApp)
	if err != nil {
		service.Fatal(log, "load application verification key", err)
	}

	// All three carry 'typ: JWT', like dauth's and aauth's. These three used to
	// pass false, on the belief that BAAS tokens have no 'typ' header; every
	// token in a real working server's capture has one. See Issuer.includeTyp.
	accessIssuer := sjwt.NewIssuer(accessKey, cfg.BAAS.AccessTokenJWKSURL, true)
	idIssuer := sjwt.NewIssuer(idKey, cfg.BAAS.IDTokenJWKSURL, true)
	// naIDIssuer's own JWK set URL, not the BAAS host's -- see naIDTokenJWKSURL's
	// doc comment. Both handlers are the same one (the mux does not route on
	// Host), so this needs no new routing, only a different 'jku' in the token.
	naIDIssuer := sjwt.NewIssuer(naIDKey, baas.NAIDTokenJWKSURL, true)

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

		LoginAttemptsPerWindow: cfg.BAAS.LoginAttemptsPerWindow,
		LoginWindow:            time.Duration(cfg.BAAS.LoginWindowSeconds) * time.Second,

		LogBodies:   cfg.BAAS.LogBodies,
		BCATSeedDir: cfg.BCAT.SeedDir,
	}, baas.Deps{
		Store:          store,
		Bus:            bus,
		Notify:         notifyBus,
		Log:            log,
		AccessIssuer:   accessIssuer,
		IDIssuer:       idIssuer,
		NAIDIssuer:     naIDIssuer,
		AccessVerifier: sjwt.NewVerifier(accessKey),
		// federation verifies the console's relayed Nintendo Account id_token
		// against naIDKey, the key nalink.go actually signed it with -- not
		// idKey, which is a different issuer's key. See IssuerNAID.
		IDVerifier:     sjwt.NewVerifier(naIDKey),
		DeviceVerifier: sjwt.NewVerifier(deviceKey),
		AppVerifier:    sjwt.NewVerifier(appKey),
	})

	if !cfg.BAAS.AllowRegistration {
		log.Info("registration is disabled; an unknown console cannot create an account")
	}

	healthz := health.New("baas").Add("database", store.Ping)

	// The Discord bot's account API, on its own loopback listener. Config
	// validation has already refused a listen address without a real token.
	if cfg.BAAS.BotAPI.Listen != "" {
		var saveTable *splat3save.Table
		if p := cfg.BAAS.BotAPI.Splatoon3SaveTable; p != "" {
			// Not fatal: an optional feature's bad path must not stop
			// sign-in. switchnetctl check reports it too.
			if saveTable, err = splat3save.LoadTable(p); err != nil {
				log.Error("Splatoon 3 save import is off: cannot load splatoon3_save_table", "error", err)
			}
		}
		bot := srv.BotHandler(baas.BotAPIOptions{
			Token: cfg.BAAS.BotAPI.Token,
			// Splatoon 3's cloud save records, as toyohr's CloudSave names them.
			SaveRecordPrefix: "tenants/" + cfg.NPLN.Tenant + "/saveRecords/",
			// Its own folder: scsi.storage_dir holds OLSC console saves.
			SaveBackupDir: "data/splat3-save-backups",
			SaveTable:     saveTable,
			FestBus:       festBus,
			Health: func(ctx context.Context) []baas.ServiceStatus {
				probed := dashboard.ProbeServices(ctx, cfg)
				out := make([]baas.ServiceStatus, 0, len(probed))
				for _, p := range probed {
					out = append(out, baas.ServiceStatus{Name: p.Name, Status: p.Status, Detail: p.Detail})
				}
				return out
			},
		})
		go func() {
			if err := service.ServeHTTP(ctx, log.With("listener", "bot_api"), cfg.BAAS.BotAPI.Listen, bot); err != nil {
				log.Error("bot API stopped", "error", err)
			}
		}()
		log.Info("bot API listening", "addr", cfg.BAAS.BotAPI.Listen)
	}

	if err := service.ServeHTTPWithHealth(ctx, log, cfg.BAAS.Listen, srv.Handler(), healthz); err != nil {
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
