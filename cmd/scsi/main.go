// Command scsi is GRID0+'s OLSC (Online Save Cloud) server: per-game cloud
// save backup.
//
// It reimplements storage.lp1.scsi.srv.nintendo.net and
// permission.lp1.sata.srv.nintendo.net. See internal/scsi/README.md for the
// endpoints and which parts of the shape are unconfirmed.
package main

import (
	"flag"
	"net/http"

	"github.com/n-popescu/grid0plus/config"
	"github.com/n-popescu/grid0plus/internal/db"
	"github.com/n-popescu/grid0plus/internal/health"
	sjwt "github.com/n-popescu/grid0plus/internal/jwt"
	"github.com/n-popescu/grid0plus/internal/scsi"
	"github.com/n-popescu/grid0plus/internal/service"
)

func main() {
	configPath := service.ConfigFlag()
	flag.Parse()

	log := service.Logger(config.Default().Log, "scsi")

	cfg, err := config.Load(*configPath)
	if err != nil {
		service.Fatal(log, "load config", err)
	}
	log = service.Logger(cfg.Log, "scsi")

	ctx, cancel := service.SignalContext()
	defer cancel()

	store, err := db.OpenPostgres(ctx, cfg.Database.URL, cfg.Database.MaxConns, cfg.Database.AutoMigrate)
	if err != nil {
		service.Fatal(log, "open database", err)
	}
	defer func() { _ = store.Close() }()

	keys := sjwt.NewKeyring(cfg.Keys.Dir, cfg.Keys.GenerateIfMissing)
	// Verifies BAAS's own id tokens, not access tokens: the console's Authorization
	// header carries a 'jku' pointing at /1.0.0/certificates (the id-token JWKS) and a
	// payload 'typ' of "id_token". internal/npln's exchangeIDToken verifies the same
	// credential. Both processes must read the same keys.dir, for the same reason
	// aauth and dauth document where they load their keys.
	idKey, err := keys.Key(sjwt.IssuerBAASID)
	if err != nil {
		service.Fatal(log, "load BAAS id verification key", err)
	}

	blobs, err := scsi.NewLocalDisk(cfg.SCSI.StorageDir, cfg.SCSI.PerUserQuotaBytes)
	if err != nil {
		service.Fatal(log, "open save data storage", err)
	}

	handler := scsi.New(scsi.Config{
		PublicURL: cfg.SCSI.PublicURL,
	}, store, blobs, sjwt.NewVerifier(idKey), log)

	mux := http.NewServeMux()
	handler.Routes(mux)

	healthz := health.New("scsi").Add("database", store.Ping)

	if err := service.ServeHTTPWithHealthUnbounded(ctx, log, cfg.SCSI.Listen, mux, healthz); err != nil {
		service.Fatal(log, "serve", err)
	}
}
