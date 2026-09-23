// Command natcheck is SwitchNet's NAT-check (NCS) UDP responder.
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
	"flag"

	"github.com/n-popescu/switchnet/config"
	"github.com/n-popescu/switchnet/internal/natcheck"
	"github.com/n-popescu/switchnet/internal/service"
	"github.com/n-popescu/switchnet/internal/stunresponder"
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

	srv, err := natcheck.Listen(natcheck.Config{
		PrimaryListen:   cfg.NATCheck.PrimaryListen,
		SecondaryListen: cfg.NATCheck.SecondaryListen,
		AlternateListen: cfg.NATCheck.AlternateListen,
		AdvertisedIP:    cfg.Network.PublicIP,
	}, log)
	if err != nil {
		service.Fatal(log, "listen", err)
	}
	defer func() { _ = srv.Close() }()

	log.Info("listening", "addrs", srv.Addrs())

	ctx, cancel := service.SignalContext()
	defer cancel()

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
