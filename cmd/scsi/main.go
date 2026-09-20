// Command scsi is SwitchNet's OLSC (Online Save Cloud) server -- phase 4,
// per-game cloud save backup.
//
// It reimplements storage.lp1.scsi.srv.nintendo.net and
// permission.lp1.sata.srv.nintendo.net. See internal/scsi for the endpoints
// and for which parts of the shape are confirmed versus this project's own
// defensible reading of an undocumented one.
package main

import (
	"flag"
	"net/http"

	"github.com/n-popescu/switchnet/config"
	"github.com/n-popescu/switchnet/internal/db"
	"github.com/n-popescu/switchnet/internal/health"
	sjwt "github.com/n-popescu/switchnet/internal/jwt"
	"github.com/n-popescu/switchnet/internal/scsi"
	"github.com/n-popescu/switchnet/internal/service"
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
	// Verifies BAAS's own access tokens: this service reads identity off the
	// token BAAS already issued, rather than issuing or verifying one of its
	// own. Both processes must read the same keys.dir, the same reason
	// aauth/dauth already document at their own key-loading call sites.
	accessKey, err := keys.Key(sjwt.IssuerBAASAccess)
	if err != nil {
		service.Fatal(log, "load BAAS access verification key", err)
	}

	blobs, err := scsi.NewLocalDisk(cfg.SCSI.StorageDir, cfg.SCSI.PerUserQuotaBytes)
	if err != nil {
		service.Fatal(log, "open save data storage", err)
	}

	handler := scsi.New(scsi.Config{
		PublicURL: cfg.SCSI.PublicURL,
	}, store, blobs, sjwt.NewVerifier(accessKey), log)

	mux := http.NewServeMux()
	handler.Routes(mux)

	healthz := health.New("scsi").Add("database", store.Ping)

	if err := service.ServeHTTPWithHealth(ctx, log, cfg.SCSI.Listen, mux, healthz); err != nil {
		service.Fatal(log, "serve", err)
	}
}
