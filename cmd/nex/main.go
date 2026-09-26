// Command nex serves SwitchNet's NEX game servers: Splatoon 2.
//
// NEX is what Nintendo's games used before NPLN. A Switch reaches a NEX
// title's game server as a WebSocket on g<game server id>-lp1.s.n.srv.nintendo.net;
// nginx terminates the TLS and proxies the upgrade here, and the Host header
// it passes along says which title the console asked for. One process, one
// listener, every title -- a second title is a second entry in titles below,
// not a second service to deploy.
package main

import (
	"errors"
	"flag"
	"fmt"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/n-popescu/switchnet/config"
	"github.com/n-popescu/switchnet/internal/db"
	"github.com/n-popescu/switchnet/internal/health"
	"github.com/n-popescu/switchnet/internal/identity"
	sjwt "github.com/n-popescu/switchnet/internal/jwt"
	"github.com/n-popescu/switchnet/internal/natobs"
	"github.com/n-popescu/switchnet/internal/rendezvous"
	"github.com/n-popescu/switchnet/internal/service"
	"github.com/n-popescu/switchnet/internal/splatoon2"
)

func main() {
	configPath := service.ConfigFlag()
	flag.Parse()

	log := service.Logger(config.Default().Log, "nex")

	cfg, err := config.Load(*configPath)
	if err != nil {
		service.Fatal(log, "load config", err)
	}
	log = service.Logger(cfg.Log, "nex")

	ctx, cancel := service.SignalContext()
	defer cancel()

	store, err := db.OpenPostgres(ctx, cfg.Database.URL, cfg.Database.MaxConns, cfg.Database.AutoMigrate)
	if err != nil {
		service.Fatal(log, "open database", err)
	}
	defer store.Close()

	// The same key directory every other service reads: a NEX login is a
	// BAAS id token, verified with the key BAAS signs it with. A different
	// directory does not error -- it refuses every login.
	keys := sjwt.NewKeyring(cfg.Keys.Dir, cfg.Keys.GenerateIfMissing)
	baasIDKey, err := keys.Key(sjwt.IssuerBAASID)
	if err != nil {
		service.Fatal(log, "load baas id key", err)
	}
	idTokens := identity.NewIDTokens(baasIDKey, store, log)

	// What cmd/natcheck observed of each console's NAT, shared through Redis:
	// without it, a console behind a router that does not preserve ports is
	// handed to its peers at a port nothing listens on. See internal/natobs.
	var natObs rendezvous.NATObservations
	if cfg.Redis.URL != "" {
		obs, err := natobs.NewRedis(ctx, cfg.Redis.URL)
		if err != nil {
			service.Fatal(log, "connect redis", err)
		}
		defer obs.Close()
		natObs = obs
	} else {
		log.Warn("nex: redis.url is not set, so the NAT-check responder's observations do not " +
			"reach this server. Consoles behind routers that remap ports will be handed to their " +
			"peers at the wrong port, and their matches will not start. Set redis.url for both " +
			"natcheck and nex.")
	}

	titles := map[string]http.Handler{}
	var s2 *splatoon2.Server
	if cfg.NEX.Splatoon2.Enabled {
		s2 = splatoon2.New(splatoon2.Options{
			Config:          cfg.NEX.Splatoon2,
			Store:           store,
			IDTokens:        idTokens,
			NATObservations: natObs,
			Log:             log.With("title", "splatoon2"),
			IdleTimeout:     time.Duration(cfg.NEX.IdleTimeout) * time.Second,
		})
		titles[splatoon2.Host] = s2
		if r := cfg.NEX.Relay; r.Enabled {
			ip := r.AdvertiseIP
			if ip == "" {
				ip = cfg.Network.PublicIP
			}
			relay, err := rendezvous.NewRelay(s2.Server, rendezvous.RelayConfig{
				AdvertiseIP: ip, ListenIP: r.ListenIP, PortBase: r.PortBase, PortSpan: r.PortSpan,
				Allow: r.Allow, AllowAll: r.AllowAll,
			})
			if err != nil {
				service.Fatal(log, "start relay", err)
			}
			defer relay.Close()
			s2.SetRelay(relay)
			log.Info("nex: relay on", "advertise", ip, "ports", fmt.Sprintf("%d-%d", r.PortBase, r.PortBase+r.PortSpan-1),
				"allowed", len(r.Allow), "allow_all", r.AllowAll)
		}
	}
	if len(titles) == 0 {
		log.Warn("nex: no title is enabled; every connection will be refused")
	}

	healthz := health.New("nex").Add("database", store.Ping)
	router := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == health.Path {
			healthz.ServeHTTP(w, r)
			return
		}
		host := r.Host
		if h, _, err := net.SplitHostPort(host); err == nil {
			host = h
		}
		if t := titles[strings.ToLower(host)]; t != nil {
			t.ServeHTTP(w, r)
			return
		}
		// A NEX hostname nginx routed here that no title answers: most
		// likely a game this server does not serve yet. Logged, because a
		// console that gets this shows only a generic connection error.
		log.Warn("nex: request for a host no title serves", "host", r.Host, "remote", service.ClientIP(r))
		http.NotFound(w, r)
	})

	// No read or write timeout: a WebSocket is held for the whole play
	// session, and a server-wide deadline would sever it mid-match. Idle
	// consoles are dropped by nex.idle_timeout instead, per connection.
	srv := &http.Server{
		Addr:              cfg.NEX.Listen,
		Handler:           router,
		ReadHeaderTimeout: 10 * time.Second,
	}

	if cfg.NEX.AdminToken == "" && cfg.NEX.AdminListen != "" {
		log.Info("nex: nex.admin_token is not set, so the dashboard's Splatoon 2 panel is not served")
	}
	if cfg.NEX.AdminToken != "" && cfg.NEX.AdminListen == "" {
		service.Fatal(log, "serve admin api", errors.New(
			"nex.admin_token is set but nex.admin_listen is empty, so the admin API would not be served anywhere"))
	}
	if s2 != nil && cfg.NEX.AdminToken != "" {
		admin := s2.AdminHandler(cfg.NEX.AdminToken)
		go func() {
			log.Info("nex: admin api listening", "addr", cfg.NEX.AdminListen)
			if err := service.ServeHTTPWithHealth(ctx, log, cfg.NEX.AdminListen, admin, healthz); err != nil {
				log.Error("nex: admin api stopped", "error", err)
			}
		}()
	}

	log.Info("nex: serving", "addr", cfg.NEX.Listen, "splatoon2", cfg.NEX.Splatoon2.Enabled)
	if err := service.Serve(ctx, log, srv); err != nil {
		service.Fatal(log, "serve", err)
	}
}
