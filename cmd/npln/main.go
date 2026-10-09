// Command npln is the NPLN game server for Splatoon 3 (phase 2).
//
// It runs three listeners, and they are not interchangeable:
//
//   - the gRPC gateway, which nginx must reach with grpc_pass over cleartext
//     HTTP/2;
//   - the JSON front end, which nginx must reach with proxy_pass over HTTP/1.1;
//   - gamesync, gRPC again, on its own port because Splatoon 3 reaches it
//     directly rather than through the tenant host.
//
// One process because gamesync verifies tokens the gateway signs -- splitting
// them would mean inventing a protocol between the two -- and because they share
// the title's configuration. Separate listeners because crossing the nginx
// blocks completes the TLS handshake and then fails with nothing in either log.
// See internal/nintendo/hosts.go.
package main

import (
	"context"
	"errors"
	"flag"
	"net"
	"net/http"
	"os"
	"time"

	"github.com/n-popescu/grid0plus/config"
	"github.com/n-popescu/grid0plus/internal/db"
	"github.com/n-popescu/grid0plus/internal/gamesync"
	"github.com/n-popescu/grid0plus/internal/health"
	sjwt "github.com/n-popescu/grid0plus/internal/jwt"
	"github.com/n-popescu/grid0plus/internal/npln"
	"github.com/n-popescu/grid0plus/internal/nplnrest"
	"github.com/n-popescu/grid0plus/internal/presencebus"
	"github.com/n-popescu/grid0plus/internal/service"
	"github.com/n-popescu/grid0plus/internal/toyohr"
	"github.com/n-popescu/grid0plus/internal/ugcstore"
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
	// keep-alive timer rather than the moment a friend's presence changes --
	// and the dashboard's matchmaking gauge honestly reports no data rather
	// than a zero that reads as "nobody is queuing". Same tradeoff
	// internal/dashboard makes for the same reason -- see cmd/dashboard/main.go.
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
			"on their own keep-alive timer rather than the moment a friend's presence changes, " +
			"and the dashboard cannot see the matchmaking queue. The in-process bus does not " +
			"cross a process boundary: set redis.url for both baas and npln to get live pushes.")
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
		PublicIP:           cfg.Network.PublicIP,
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

	// gamesync verifies the matchmaking id tokens the gateway signs. The same
	// key, in the same process: there is nothing to distribute, and the
	// audience is what stops an access token being accepted here.
	var gamesyncProbeSigner func(string, string, time.Time) (string, error)
	if os.Getenv("SWITCHNET_NPLN_DIAGNOSTIC_GAMESYNC_JWT") == "1" {
		gamesyncProbeSigner = srv.IssueGamesyncProbeToken
		log.Info("gamesync: signed access-token probe enabled")
	}
	sync, err := gamesync.New(gamesync.Options{
		Log:              log.With("listener", "gamesync"),
		VerifyIDToken:    srv.VerifyMatchmakingIDToken,
		ResolveSession:   srv.ResolveGamesyncSession,
		LiveParticipants: srv.GamesyncRoomParticipants,
		IssueAccessToken: gamesyncProbeSigner,
		LobbyLogFlag:     lobbyLogFlag(),
		PrepareCoopVerdict: func(ctx context.Context, report gamesync.RefereeReport) error {
			return ugcstore.ApplyCoopGrades(ctx, store, cfg.NPLN.Tenant, report.BattleID,
				report.Mode, report.Verdict, report.SessionUsers, time.Now().UTC())
		},
		OnRefereeReport: func(ctx context.Context, report gamesync.RefereeReport) error {
			now := time.Now().UTC()
			if report.Coop && report.Verdict != nil {
				if err := ugcstore.RecordCoopPointCards(ctx, store, cfg.NPLN.Tenant, report.BattleID,
					report.Mode, report.Verdict, report.SessionUsers, now); err != nil {
					return err
				}
			}
			if report.Coop {
				if err := ugcstore.RecordCoopResults(ctx, store, cfg.NPLN.Tenant, report.BattleID,
					report.Fields, report.Verdict, report.SessionUsers, report.Profiles, now); err != nil {
					return err
				}
			} else {
				if err := ugcstore.RecordVsResults(ctx, store, cfg.NPLN.Tenant, report.BattleID,
					report.Players, report.Fields, report.Verdict, report.Profiles, now); err != nil {
					return err
				}
			}
			return toyohr.RecordFestReport(ctx, store, report, now)
		},
	})
	if err != nil {
		service.Fatal(log, "build gamesync", err)
	}
	srv.SetSessionPhaseReader(sync.SessionPhase)

	lis, err := net.Listen("tcp", cfg.NPLN.Listen)
	if err != nil {
		service.Fatal(log, "listen", err)
	}
	syncLis, err := net.Listen("tcp", cfg.NPLN.GamesyncListen)
	if err != nil {
		service.Fatal(log, "listen gamesync", err)
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

	syncErr := make(chan error, 1)
	go sync.WatchLobbyLogFlag(ctx, 5*time.Second)
	go func() { syncErr <- sync.Serve(syncLis) }()

	go func() {
		<-ctx.Done()
		log.Info("npln: shutting down")
		srv.Stop()
		sync.Stop()
	}()

	log.Info("npln: listening", "addr", cfg.NPLN.Listen, "tenant", cfg.NPLN.Tenant,
		"rest", cfg.NPLN.RestListen, "gamesync", cfg.NPLN.GamesyncListen,
		"frontline", cfg.NPLN.FrontlineHost)
	if err := srv.Serve(lis); err != nil {
		service.Fatal(log, "serve", err)
	}
	if err := <-restErr; err != nil {
		service.Fatal(log, "serve json front end", err)
	}
	if err := <-syncErr; err != nil {
		service.Fatal(log, "serve gamesync", err)
	}
}

// lobbyLogFlag is the file that turns gamesync's lobby-control logging on;
// see gamesync.Options.LobbyLogFlag.
func lobbyLogFlag() string {
	if p := os.Getenv("SWITCHNET_NPLN_LOBBY_LOG_FLAG"); p != "" {
		return p
	}
	return "data/gamesync-lobby-log"
}
