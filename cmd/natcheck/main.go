// Command natcheck is GRID0+'s NAT-check (NCS) UDP responder.
//
// It answers the nncs1 and nncs2 hosts, which the console uses to learn its
// external address and classify its router's NAT before attempting
// peer-to-peer. See internal/natcheck — in particular the note about needing
// two distinct IP addresses.
//
// It also answers STUN (RFC 5389) Binding requests, when npln.stun_listen is
// set: a different protocol, but the same kind of lightweight UDP
// connectivity responder Splatoon 3 turns out to need before matchmaking
// even starts. See internal/stunresponder for why.
package main

import (
	"context"
	"flag"
	"net"
	"time"

	"github.com/n-popescu/grid0plus/config"
	"github.com/n-popescu/grid0plus/internal/natcheck"
	"github.com/n-popescu/grid0plus/internal/natobs"
	"github.com/n-popescu/grid0plus/internal/service"
	"github.com/n-popescu/grid0plus/internal/stunresponder"
)

func main() {
	configPath := service.ConfigFlag()
	flag.Parse()

	log := service.Logger(config.Default().Log, "natcheck")

	cfg, err := config.Load(*configPath)
	if err != nil {
		service.Fatal(log, "load config", err)
	}
	log = service.Logger(cfg.Log, "natcheck")

	ctx, cancel := service.SignalContext()
	defer cancel()

	// What the responder observes goes to the NEX game server through Redis,
	// when there is one. Written off the read loop: a slow Redis must never
	// delay a NAT-check reply, whose timing is part of what it measures.
	var observe func(net.IP, int)
	if cfg.Redis.URL != "" {
		obs, err := natobs.NewRedis(ctx, cfg.Redis.URL)
		if err != nil {
			service.Fatal(log, "connect redis", err)
		}
		defer obs.Close()
		type seen struct {
			ip   string
			port int
		}
		queue := make(chan seen, 256)
		go func() {
			for o := range queue {
				wctx, wcancel := context.WithTimeout(ctx, 2*time.Second)
				if err := obs.Record(wctx, o.ip, o.port); err != nil {
					log.Debug("natcheck: observation not shared", "error", err)
				}
				wcancel()
			}
		}()
		observe = func(ip net.IP, port int) {
			select {
			case queue <- seen{ip.String(), port}:
			default: // full: drop it, the console re-checks
			}
		}
	} else {
		log.Info("natcheck: redis.url is not set, so observed NAT mappings are not shared " +
			"with the NEX game server; its stations keep the ports consoles report")
	}

	srv, err := natcheck.Listen(natcheck.Config{
		PrimaryListen:         cfg.NATCheck.PrimaryListen,
		SecondaryListen:       cfg.NATCheck.SecondaryListen,
		AlternateListen:       cfg.NATCheck.AlternateListen,
		AdvertisedIP:          cfg.Network.PublicIP,
		AlternateAdvertisedIP: cfg.Network.NATCheckSecondaryIP,
		Type102SamePort:       cfg.NATCheck.Type102SamePort,
		Observe:               observe,
	}, log)
	if err != nil {
		service.Fatal(log, "listen", err)
	}
	defer func() { _ = srv.Close() }()

	log.Info("listening", "addrs", srv.Addrs())

	errCh := make(chan error, 2)

	if cfg.NATCheck.StunListen != "" {
		stunConn, err := stunresponder.Listen(cfg.NATCheck.StunListen)
		if err != nil {
			service.Fatal(log, "stun listen", err)
		}
		defer stunConn.Close()
		log.Info("stun listening", "addr", cfg.NATCheck.StunListen)
		go func() { errCh <- stunresponder.Serve(ctx, stunConn, log) }()
	}

	go func() { errCh <- srv.Serve(ctx) }()

	if err := <-errCh; err != nil {
		service.Fatal(log, "serve", err)
	}
}
