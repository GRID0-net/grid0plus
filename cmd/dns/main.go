// Command dns is SwitchNet's DNS server.
//
// It answers Nintendo's hostnames with the operator's address and forwards
// everything else upstream, so a console reaches SwitchNet with no file on its SD
// card and no modification to the console at all.
//
// Point a console at it either by setting DNS manually in the console's own
// network settings, or — better — by handing this server's address out over DHCP
// from the router, in which case the console needs no configuration whatsoever.
//
// Port 53 is privileged. Either grant the capability rather than running as root:
//
//	sudo setcap 'cap_net_bind_service=+ep' ./bin/dns
//
// or let systemd do it: deploy/systemd/switchnet-dns.service sets
// AmbientCapabilities=CAP_NET_BIND_SERVICE, which needs no setcap step and
// nothing to reapply after every rebuild (unlike setcap, a file attribute
// go build does not preserve).
package main

import (
	"flag"
	"net"
	"time"

	"github.com/n-popescu/switchnet/config"
	"github.com/n-popescu/switchnet/internal/dns"
	"github.com/n-popescu/switchnet/internal/service"
)

func main() {
	configPath := service.ConfigFlag()
	flag.Parse()

	log := service.Logger(config.Default().Log, "dns")

	cfg, err := config.Load(*configPath)
	if err != nil {
		service.Fatal(log, "load config", err)
	}
	log = service.Logger(cfg.Log, "dns")

	if cfg.Network.PublicIP == "" {
		service.Fatal(log, "network.public_ip is not set, so there is no address "+
			"to point the consoles at", errMissingPublicIP)
	}

	srv, err := dns.Listen(dns.Config{
		Listen:            cfg.DNS.Listen,
		RedirectTo:        net.ParseIP(cfg.Network.PublicIP),
		NATCheckSecondary: parseOptionalIP(cfg.Network.NATCheckSecondaryIP),
		Upstream:          cfg.DNS.Upstream,
		TTL:               uint32(cfg.DNS.TTL),
		UpstreamTimeout:   time.Duration(cfg.DNS.UpstreamTimeout) * time.Second,
		LogForwarded:      cfg.DNS.LogForwarded,
	}, log)
	if err != nil {
		service.Fatal(log, "listen", err)
	}
	defer func() { _ = srv.Close() }()

	log.Info("listening",
		"addr", srv.Addr(),
		"redirect_to", cfg.Network.PublicIP,
		"natcheck_secondary", cfg.Network.NATCheckSecondaryIP,
		"upstream", cfg.DNS.Upstream)

	ctx, cancel := service.SignalContext()
	defer cancel()

	if err := srv.Serve(ctx); err != nil {
		service.Fatal(log, "serve", err)
	}
}

func parseOptionalIP(s string) net.IP {
	if s == "" {
		return nil
	}
	return net.ParseIP(s)
}

// errMissingPublicIP gives service.Fatal something to log. The message above
// carries the explanation.
var errMissingPublicIP = errConfig("network.public_ip is empty")

type errConfig string

func (e errConfig) Error() string { return string(e) }
