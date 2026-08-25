// Command natcheck is SwitchNet's NAT-check (NCS) UDP responder.
//
// It answers the nncs1 and nncs2 hosts, which the console uses to learn its
// external address and classify its router's NAT before attempting
// peer-to-peer. See internal/natcheck — in particular the note about needing
// two distinct IP addresses.
package main

import (
	"flag"

	"github.com/n-popescu/switchnet/config"
	"github.com/n-popescu/switchnet/internal/natcheck"
	"github.com/n-popescu/switchnet/internal/service"
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

	if err := srv.Serve(ctx); err != nil {
		service.Fatal(log, "serve", err)
	}
}
