// Command switchnetctl generates the Switch-side and edge configuration from
// config.yaml and the host list in internal/nintendo.
//
// Several artefacts have to agree exactly or the console fails in ways that give
// no useful error: the DNS redirects (however they are served), the nginx routing
// map, and the TLS certificate's subject alternative names. Generating all of
// them from one list is what stops them drifting.
//
//	switchnetctl dnsmasq  > deploy/switchnet.dnsmasq.conf
//	switchnetctl unbound  > deploy/switchnet.unbound.conf
//	switchnetctl hosts    > switch/hosts.txt
//	switchnetctl nginx    > deploy/nginx.generated.conf
//	switchnetctl sans             # one hostname per line, for gen-certs.sh
//	switchnetctl check            # validate config without writing anything
//	switchnetctl run [service...] # build and run the Go services natively
package main

import (
	"flag"
	"fmt"
	"os"
	"strings"

	"github.com/n-popescu/switchnet/config"
	"github.com/n-popescu/switchnet/internal/nintendo"
)

func main() {
	configPath := flag.String("config", os.Getenv("SWITCHNET_CONFIG"),
		"path to config.yaml")
	bcatProbe := flag.Bool("bcat-probe", false,
		"hosts: point the bcat-* hosts at SwitchNet instead of null-routing them, "+
			"for the TODO.md 2.4 experiment.")
	flag.Usage = usage
	flag.Parse()

	// "account <sub>", "fest <sub>" and "coop <sub>" take further arguments
	// of their own; "run" takes zero or more service names; every other
	// command takes none.
	if flag.NArg() == 0 ||
		(flag.Arg(0) != "account" && flag.Arg(0) != "fest" && flag.Arg(0) != "coop" &&
			flag.Arg(0) != "run" && flag.NArg() != 1) {
		usage()
		os.Exit(2)
	}

	cfg, err := config.Load(*configPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "switchnetctl: %v\n", err)
		os.Exit(1)
	}

	switch flag.Arg(0) {
	case "hosts":
		requirePublicIP(cfg)
		fmt.Print(hostsFile(cfg, *bcatProbe))

	case "dnsmasq":
		requirePublicIP(cfg)
		fmt.Print(dnsmasqConf(cfg))

	case "unbound":
		requirePublicIP(cfg)
		fmt.Print(unboundConf(cfg))

	case "nginx":
		fmt.Print(nginxConf(cfg))

	case "sans":
		for _, name := range nintendo.TLSHostnames() {
			fmt.Println(name)
		}

	case "check":
		fmt.Fprintln(os.Stderr, "switchnetctl: configuration is valid")
		warn(cfg)

	case "run":
		runCmd(cfg, *configPath, flag.Args()[1:])

	case "account":
		if flag.NArg() < 2 {
			fmt.Fprint(os.Stderr, "switchnetctl: usage: switchnetctl account <create|revoke-tokens|disable|enable|delete-user|restore-user> ...\n")
			os.Exit(2)
		}
		switch flag.Arg(1) {
		case "create":
			accountCreate(cfg, flag.Args()[2:])
		case "revoke-tokens":
			accountRevokeTokens(cfg, flag.Args()[2:])
		case "disable":
			accountDisable(cfg, flag.Args()[2:])
		case "enable":
			accountEnable(cfg, flag.Args()[2:])
		case "delete-user":
			accountDeleteUser(cfg, flag.Args()[2:])
		case "restore-user":
			accountRestoreUser(cfg, flag.Args()[2:])
		default:
			fmt.Fprint(os.Stderr, "switchnetctl: usage: switchnetctl account <create|revoke-tokens|disable|enable|delete-user|restore-user> ...\n")
			os.Exit(2)
		}

	case "coop":
		if flag.NArg() < 2 {
			fmt.Fprint(os.Stderr, "switchnetctl: usage: switchnetctl coop <schedule|generate|list|delete|stages|weapons|bosses> ...\n")
			os.Exit(2)
		}
		switch flag.Arg(1) {
		case "schedule":
			coopSchedule(cfg, flag.Args()[2:])
		case "generate":
			coopGenerate(cfg, flag.Args()[2:])
		case "list":
			coopList(cfg, flag.Args()[2:])
		case "delete":
			coopDelete(cfg, flag.Args()[2:])
		case "stages":
			coopStages()
		case "weapons":
			coopWeapons(flag.Args()[2:])
		case "bosses":
			coopBosses()
		default:
			fmt.Fprint(os.Stderr, "switchnetctl: usage: switchnetctl coop <schedule|generate|list|delete|stages|weapons|bosses> ...\n")
			os.Exit(2)
		}

	case "fest":
		if flag.NArg() < 2 || flag.Arg(1) != "build" {
			fmt.Fprint(os.Stderr, "switchnetctl: usage: switchnetctl fest build ...\n\n")
			fmt.Fprint(os.Stderr, festBuildUsage)
			os.Exit(2)
		}
		festBuild(cfg, flag.Args()[2:])

	default:
		usage()
		os.Exit(2)
	}
}

func usage() {
	fmt.Fprint(os.Stderr, `switchnetctl generates SwitchNet's console-side and edge configuration.

Usage:
  switchnetctl [-config path] <command>

Commands:
  hosts     Atmosphère DNS-MITM hosts file, for /atmosphere/hosts/{sysmmc,emummc}.txt
            (the fallback: prefer network-side DNS, which touches no console)
  dnsmasq   dnsmasq / Pi-hole / OpenWrt configuration
  unbound   unbound configuration
  nginx     nginx server blocks routing each Nintendo hostname to its backend
  sans      the hostnames the edge certificate needs, one per line
  check     validate the configuration and report anything risky
  run       build and run the Go services natively, in this terminal --
            'switchnetctl run' for all of them, or name a subset, e.g.
            'switchnetctl run baas dauth'. Deliberately does not restart a
            service that exits unexpectedly; see deploy/systemd for that.
  account   create a user from a device account id and password you already
            have, rather than one the server generates; revoke a device
            account's live sessions; disable/re-enable a single device
            account; or delete/restore a user entirely -- every device
            account under them at once, existing sessions included -- all
            without touching the database by hand. See
            'switchnetctl account create -h', '... revoke-tokens -h',
            '... disable -h', '... enable -h', '... delete-user -h' and
            '... restore-user -h'.
  fest      build a Splatfest's BCAT packs, write the seed folder and register
            the fest, in one step so the four values that must agree cannot
            drift apart. See 'switchnetctl fest build -h'.
  coop      schedule a Salmon Run or Big Run rotation shift by hand, or
            auto-fill a stretch of the normal rotation with 'coop generate';
            list or delete scheduled shifts, or print the real
            stage/weapon/boss id tables a shift is built from. See
            'switchnetctl coop schedule -h' and '... generate -h'.

Redirection is best done off the console: add the generated dnsmasq or unbound
configuration to a resolver you already have. The hosts file is only needed
when you cannot control DNS on the network.

`)
	flag.PrintDefaults()
}

// warn reports configuration that is valid but likely to cause trouble.
func warn(cfg config.Config) {
	if cfg.Network.PublicIP == "" {
		fmt.Fprintln(os.Stderr,
			"warning: network.public_ip is empty; 'switchnetctl hosts' cannot run")
	}
	if cfg.Network.NATCheckSecondaryIP == "" {
		fmt.Fprintln(os.Stderr,
			"warning: network.natcheck_secondary_ip is empty. nncs1 and nncs2 will "+
				"resolve to the same address, the console will not send its second NAT "+
				"probe, and NAT detection will not complete. Peer-to-peer matchmaking "+
				"needs two distinct addresses.")
	}
	if cfg.Keys.DeviceIDSecret == "" {
		fmt.Fprintln(os.Stderr,
			"warning: keys.device_id_secret is empty. Device ids and the 'edi' claim "+
				"will be derived from an empty secret, so they are predictable.")
	}
	if len(cfg.NPLN.IceServers) == 0 {
		fmt.Fprintln(os.Stderr,
			"warning: npln.ice_servers is empty. Splatoon 3 uses ICE rather than the "+
				"NAT-check pair, so consoles behind different NATs will match and then "+
				"fail to connect -- the lobby fills and the match never starts, which "+
				"looks like a matchmaking bug from the server side. Consoles on one LAN "+
				"are unaffected.")
	}
	for i, s := range cfg.NPLN.IceServers {
		if s.Kind == "turn" && s.Username == "" {
			fmt.Fprintf(os.Stderr,
				"warning: npln.ice_servers[%d] is a TURN server with no username. "+
					"TURN needs a credential; the console will be handed one it cannot "+
					"use, and will fall back to a direct connection that may not work.\n", i)
		}
	}
	if cfg.Keys.GenerateIfMissing {
		fmt.Fprintln(os.Stderr,
			"note: keys.generate_if_missing is on. Once "+cfg.Keys.Dir+" is backed up, "+
				"turn it off so a lost volume fails loudly instead of silently "+
				"invalidating every token the consoles hold.")
	}
	if cfg.Redis.URL == "" {
		fmt.Fprintln(os.Stderr,
			"warning: redis.url is empty. baas, npln and dashboard each get their own "+
				"in-process presence bus, which delivers nothing across a process "+
				"boundary -- the dashboard's 'online now' view will silently stay empty "+
				"and match/presence updates from npln will never reach it. Fine when "+
				"everything runs as one process; set this before running dashboard or "+
				"npln separately.")
	}
	if (cfg.Dashboard.NPLNAdminToken == "") != (cfg.NPLN.AdminToken == "") {
		fmt.Fprintln(os.Stderr,
			"warning: exactly one of dashboard.npln_admin_token and npln.admin_token is "+
				"set. They must agree -- the dashboard authenticates to npln's matchmaking "+
				"controls with this token, and a mismatch reaches the operator as a plain "+
				"401 with nothing connecting it back to these two settings.")
	} else if cfg.Dashboard.NPLNAdminToken != "" && cfg.NPLN.AdminToken != "" &&
		cfg.Dashboard.NPLNAdminToken != cfg.NPLN.AdminToken {
		fmt.Fprintln(os.Stderr,
			"warning: dashboard.npln_admin_token does not match npln.admin_token. "+
				"They must be the same value, or the dashboard's matchmaking controls "+
				"will fail every request with a plain 401.")
	}
	if base := strings.TrimPrefix(cfg.BAAS.ImageBaseURL, "https://"); strings.ContainsRune(base, '/') {
		fmt.Fprintln(os.Stderr,
			"warning: baas.image_base_url has a path after the host ('"+cfg.BAAS.ImageBaseURL+"'). "+
				"A 2026-09-19 capture showed the console requests profile pictures at the root "+
				"of this host, as /1/<name> and /2/<name> -- see internal/baas/images.go's "+
				"imageURLs doc comment. Any path here makes the console's own URL validation "+
				"reject the field before it ever requests the image, which fails closed and "+
				"silent: no request reaches this server at all, so nothing here will show it.")
	}
}

// ---- hosts file ----------------------------------------------------------

// hostsFile renders the Atmosphère DNS-MITM hosts file.
//
// Atmosphère applies the LAST matching line, so order is significant: broad
// entries first, specific overrides last. In the hosts file '*' matches zero or
// more characters and '%' expands to the environment ("lp1").
func hostsFile(cfg config.Config, bcatProbe bool) string {
	ip := cfg.Network.PublicIP
	nncs2 := cfg.Network.NATCheckSecondaryIP

	var b strings.Builder
	b.WriteString("# ==========================================================\n")
	b.WriteString("#  SwitchNet - Atmosphere DNS-MITM hosts file\n")
	b.WriteString("#  GENERATED by 'switchnetctl hosts'. Do not edit by hand:\n")
	b.WriteString("#  regenerate it instead, or the next regeneration loses the edit.\n")
	b.WriteString("#\n")
	b.WriteString("#  Install to BOTH of these, or it works from one boot\n")
	b.WriteString("#  configuration and not the other:\n")
	b.WriteString("#    /atmosphere/hosts/sysmmc.txt\n")
	b.WriteString("#    /atmosphere/hosts/emummc.txt\n")
	b.WriteString("#\n")
	b.WriteString("#  And in /atmosphere/config/system_settings.ini:\n")
	b.WriteString("#    [atmosphere]\n")
	b.WriteString("#    enable_dns_mitm = u8!0x1\n")
	b.WriteString("#    add_defaults_to_dns_hosts = u8!0x0\n")
	b.WriteString("#\n")
	b.WriteString("#  Atmosphere applies the LAST matching line, so the order below\n")
	b.WriteString("#  matters. '*' matches zero or more characters; '%' means lp1.\n")
	b.WriteString("# ==========================================================\n\n")

	section := func(title string) {
		b.WriteString("\n# --- " + title + " ---\n")
	}

	emit := func(addr string, h nintendo.Host) {
		fmt.Fprintf(&b, "%-16s %-58s # %s\n", addr, h.Pattern, h.Comment)
	}

	section("accounts, friends and authentication")
	for _, svc := range []nintendo.Service{
		nintendo.ServiceDAuth, nintendo.ServiceAAuth, nintendo.ServiceBAAS,
	} {
		for _, h := range nintendo.ServiceHosts(svc) {
			emit(ip, h)
		}
	}

	section("stubs: answered, not implemented")
	for _, h := range nintendo.ServiceHosts(nintendo.ServiceStubs) {
		emit(ip, h)
	}

	// MUST come after the stubs section above: permission.lp1.sata.srv.
	// nintendo.net is a literal host that overrides the broader
	// %.sata.srv.nintendo.net wildcard emitted there, and Atmosphere applies
	// the LAST matching line. internal/nintendo.Resolve gets this right on
	// its own via longest-suffix-wins regardless of declaration order; this
	// generated file has to get the same answer by physical line order
	// instead, since Atmosphere's own DNS-MITM resolver has no such
	// preference.
	section("phase 4: OLSC cloud save")
	for _, h := range nintendo.ServiceHosts(nintendo.ServiceSCSI) {
		emit(ip, h)
	}

	// Every service with hosts must appear in some section here, or its hosts
	// vanish from the file with no error -- see
	// TestEveryHostReachesTheGeneratedHostsFile.
	section("NPLN game servers (phase 2)")
	for _, svc := range []nintendo.Service{
		nintendo.ServiceNPLNRest, nintendo.ServiceNPLN, nintendo.ServiceGamesync,
	} {
		for _, h := range nintendo.ServiceHosts(svc) {
			emit(ip, h)
		}
	}

	section("NAT check")
	for _, h := range nintendo.ServiceHosts(nintendo.ServiceNATCheck) {
		addr := ip
		if strings.HasPrefix(h.Pattern, "nncs2") {
			if nncs2 == "" {
				b.WriteString("# WARNING: network.natcheck_secondary_ip is not set, so nncs2 points\n")
				b.WriteString("# at the same address as nncs1. The console de-duplicates the probe,\n")
				b.WriteString("# never sends the second one, and NAT detection does not complete.\n")
			} else {
				addr = nncs2
			}
		}
		emit(addr, h)
	}

	section("telemetry: null-routed")
	b.WriteString("# Not redirected to SwitchNet: there is nothing useful to do with a\n")
	b.WriteString("# console's crash and play reports, and answering them only fills the log.\n")
	if bcatProbe {
		b.WriteString("#\n")
		b.WriteString("# -bcat-probe is ON: the three bcat-* hosts below point at SwitchNet\n")
		b.WriteString("# instead of 0.0.0.0. This is the TODO.md 2.4 experiment, NOT a working\n")
		b.WriteString("# BCAT service -- SwitchNet logs what the console asks for and answers\n")
		b.WriteString("# 200 with an empty body via internal/bcat.Probe, which is always\n")
		b.WriteString("# registered -- this flag only decides which hosts route to it.\n")
		b.WriteString("# Regenerate WITHOUT the flag when the experiment is over: answering a\n")
		b.WriteString("# console that expected content turns a clean failure into a hang.\n")
	}
	for _, h := range nintendo.ServiceHosts(nintendo.ServiceBlackhole) {
		addr := "0.0.0.0"
		if bcatProbe && strings.HasPrefix(h.Pattern, "bcat-") {
			addr = ip
		}
		emit(addr, h)
	}

	// Passthrough hosts get their own section only when there are any: on a
	// deployment where every unused host is null-routed instead (see the d4c
	// entries above), there is nothing here to explain.
	if passthrough := nintendo.ServiceHosts(nintendo.ServicePassthrough); len(passthrough) > 0 {
		section("deliberately NOT redirected: must keep reaching real Nintendo")
		for _, h := range passthrough {
			fmt.Fprintf(&b, "# %-56s # %s\n", h.Pattern, h.Comment)
		}
	}

	return b.String()
}

// ---- nginx --------------------------------------------------------------

// nginxConf renders the TLS edge configuration.
//
// One server block per hostname, routed by SNI to the right backend. A default
// block catches anything not listed, so a hostname that was redirected but not
// configured shows up as a log line here instead of as a silent hang on the
// console.
func nginxConf(cfg config.Config) string {
	backends := map[nintendo.Service]string{
		nintendo.ServiceDAuth: cfg.DAuth.Listen,
		nintendo.ServiceAAuth: cfg.AAuth.Listen,
		nintendo.ServiceBAAS:  cfg.BAAS.Listen,
		nintendo.ServiceSCSI:  cfg.SCSI.Listen,
		nintendo.ServiceStubs: cfg.Stubs.Listen,

		// Two NPLN backends, deliberately. See config.NPLN: the JSON front and
		// the gRPC gateway need different nginx, and crossing them fails
		// silently.
		nintendo.ServiceNPLN:     cfg.NPLN.Listen,
		nintendo.ServiceNPLNRest: cfg.NPLN.RestListen,
		nintendo.ServiceGamesync: cfg.NPLN.GamesyncListen,
	}
	names := map[nintendo.Service]string{
		nintendo.ServiceDAuth:    "dauth",
		nintendo.ServiceAAuth:    "aauth",
		nintendo.ServiceBAAS:     "baas",
		nintendo.ServiceSCSI:     "scsi",
		nintendo.ServiceStubs:    "stubs",
		nintendo.ServiceNPLN:     "npln",
		nintendo.ServiceNPLNRest: "npln_rest",
		nintendo.ServiceGamesync: "gamesync",
	}

	var b strings.Builder
	b.WriteString("# ==========================================================\n")
	b.WriteString("#  SwitchNet - nginx TLS edge\n")
	b.WriteString("#  GENERATED by 'switchnetctl nginx'. Regenerate rather than edit.\n")
	b.WriteString("#\n")
	b.WriteString("#  Certificates come from scripts/gen-certs.sh. A retail console does\n")
	b.WriteString("#  not let you add a CA to its ssl sysmodule trust store, so cert trust\n")
	b.WriteString("#  is handled on the console with an Atmosphere exefs patch instead --\n")
	b.WriteString("#  see switch/README.md. The SANs still matter for curl, for tooling and\n")
	b.WriteString("#  for emulators.\n")
	b.WriteString("# ==========================================================\n\n")

	// A deterministic order: the generated file is committed, and a map range
	// would reshuffle the upstream blocks on every run and churn the diff.
	for _, svc := range []nintendo.Service{
		nintendo.ServiceDAuth, nintendo.ServiceAAuth,
		nintendo.ServiceBAAS, nintendo.ServiceSCSI, nintendo.ServiceStubs,
	} {
		fmt.Fprintf(&b, "upstream switchnet_%s { server %s; }\n",
			names[svc], upstreamAddr(backends[svc]))
	}
	// Phase 2's upstreams. All three are served by cmd/npln, which exists now:
	// the gRPC gateway on npln.listen, the JSON front end on npln.rest_listen,
	// and gamesync on its own port (npln.gamesync_listen).
	for _, svc := range []nintendo.Service{
		nintendo.ServiceNPLNRest, nintendo.ServiceNPLN, nintendo.ServiceGamesync,
	} {
		fmt.Fprintf(&b, "upstream switchnet_%s { server %s; }\n",
			names[svc], upstreamAddr(backends[svc]))
	}
	b.WriteString("\n")

	for _, svc := range nintendo.ProxiedServices {
		hosts := nintendo.ServiceHosts(svc)
		if len(hosts) == 0 {
			continue
		}

		// Hosts within one service can still need different nginx treatment,
		// independent of each other: NoClientCertRequest and NoHTTP2 are two
		// separate observed HTTP/2 and mTLS interop failures, not always on
		// the same hosts (accounts.nintendo.com needs both off;
		// m-lp1.baas.nintendo.com needs the client-cert request kept but
		// HTTP/2 off). Grouped by (verifyClient, http2) rather than one flag
		// each, so a host needing any combination gets the right block
		// without a new variable per flag.
		type variant struct{ verifyClient, http2 bool }
		grouped := map[variant][]string{}
		for _, h := range hosts {
			if !h.TLS {
				continue
			}
			v := variant{verifyClient: !h.NoClientCertRequest, http2: !h.NoClientCertRequest && !h.NoHTTP2}
			if h.Wildcard {
				grouped[v] = append(grouped[v], h.TLSNames...)
				continue
			}
			grouped[v] = append(grouped[v], h.Pattern)
		}
		if len(grouped) == 0 {
			continue
		}

		fmt.Fprintf(&b, "# ---- %s ----\n", names[svc])
		if svc == nintendo.ServiceNPLN || svc == nintendo.ServiceGamesync {
			b.WriteString("# NPLN is gRPC over HTTP/2, so it needs grpc_pass and http2, not\n")
			b.WriteString("# proxy_pass. Routing it to an HTTP/1.1 backend completes the TLS\n")
			b.WriteString("# handshake and then says nothing a gRPC client understands, which\n")
			b.WriteString("# is the worst kind of failure: DNS resolves, the port is open, TLS\n")
			b.WriteString("# succeeds, the game fails, and neither side logs anything.\n")
			if svc == nintendo.ServiceGamesync {
				b.WriteString("#\n")
				b.WriteString("# On its OWN PORT: the console reaches gamesync directly on 7575\n")
				b.WriteString("# rather than through the tenant host, so a block on the edge port\n")
				b.WriteString("# would never match it.\n")
			}
			b.WriteString(grpcServerBlock(cfg, names[svc],
				listenPort(cfg, hosts), grouped[variant{verifyClient: true, http2: true}]))
			b.WriteString("\n")
			continue
		}
		if svc == nintendo.ServiceNPLNRest {
			b.WriteString("# These are the hosts Splatoon 3 talks to BEFORE it opens its gRPC\n")
			b.WriteString("# channel: vermillion, penne (login tickets), and the fro-N frontline\n")
			b.WriteString("# connection -- served by internal/nplnrest on NPLN.RestListen. They\n")
			b.WriteString("# are ordinary JSON, so proxy_pass -- NOT grpc_pass. The frontline\n")
			b.WriteString("# connection is held open by the console and must not be closed or\n")
			b.WriteString("# buffered, hence the long read timeout and proxy_buffering off below.\n")
			// One block for the whole service: unlike baas, no host here has a
			// per-host interop flag, so grouping by variant would only ever
			// produce one bucket -- and picking the wrong one silently emits a
			// server block with an empty server_name.
			b.WriteString(frontlineServerBlock(cfg, tlsNamesOf(hosts)))
			b.WriteString("\n")
			continue
		}

		// Deterministic order: a map range would reshuffle the generated
		// file's blocks on every run and churn the diff. {false, true} is
		// not listed: the construction above never produces it -- a host
		// with no client-cert request also always has HTTP/2 off.
		for _, v := range []variant{
			{verifyClient: true, http2: true},
			{verifyClient: true, http2: false},
			{verifyClient: false, http2: false},
		} {
			serverNames := grouped[v]
			if len(serverNames) == 0 {
				continue
			}
			switch {
			case !v.verifyClient:
				b.WriteString("# No ssl_verify_client, and HTTP/2 off: unlike the rest of this service,\n")
				b.WriteString("# these hosts' clients fail on a TLS CertificateRequest instead of\n")
				b.WriteString("# answering it with an empty certificate: the browser applet on\n")
				b.WriteString("# accounts.nintendo.com, and the friends sysmodule on the friends host\n")
				b.WriteString("# (2121-1058, see its entry in internal/nintendo/hosts.go). For the\n")
				b.WriteString("# browser, a 2026-08-25 capture of a\n")
				b.WriteString("# real working Nextendo server showed this exact request made over plain\n")
				b.WriteString("# HTTP/1.1 -- never HTTP/2. With http2 on and the browser offering 'h2'\n")
				b.WriteString("# in ALPN, nginx picked h2 and the connection completed a full TLS\n")
				b.WriteString("# handshake and a short encrypted exchange but nginx never logged an\n")
				b.WriteString("# HTTP request: the browser's HTTP/2 client could not actually complete\n")
				b.WriteString("# one. See Host.NoClientCertRequest.\n")
			case !v.http2:
				b.WriteString("# HTTP/2 off, ssl_verify_client kept: this host's traffic is genuine\n")
				b.WriteString("# device-auth/system sync, not the browser applet, so it still needs the\n")
				b.WriteString("# client-cert request -- but a 2026-08-26 capture showed the exact same\n")
				b.WriteString("# signature that led to disabling HTTP/2 for accounts.nintendo.com: a\n")
				b.WriteString("# full, sizeable, bidirectional TLS exchange nginx never logs as a\n")
				b.WriteString("# completed HTTP request. See Host.NoHTTP2.\n")
			}
			b.WriteString(httpServerBlock(cfg, serverNames, "switchnet_"+names[svc], v.verifyClient, v.http2, maxBodySizeFor(svc)))
		}
		b.WriteString("\n")
	}

	b.WriteString("# ---- anything not listed above ----\n")
	b.WriteString("# A hostname that was redirected on the console but has no block here\n")
	b.WriteString("# would otherwise fall through to the first server block and get an\n")
	b.WriteString("# answer from the wrong service. Sending it to the stub server means it\n")
	b.WriteString("# is logged as an unhandled host, which is the to-do list.\n")
	fmt.Fprintf(&b, `server {
    listen %d ssl default_server;
    http2 on;
    server_name _;

    ssl_certificate     /etc/switchnet/certs/edge.crt;
    ssl_certificate_key /etc/switchnet/certs/edge.key;

    access_log /var/log/nginx/switchnet-unmatched.log;

    location / {
        proxy_pass http://switchnet_stubs;
        proxy_set_header Host              $host;
        proxy_set_header X-Real-IP         $remote_addr;
        proxy_set_header X-Forwarded-For   $proxy_add_x_forwarded_for;
        proxy_set_header X-Forwarded-Proto $scheme;
    }
}
`, cfg.Network.EdgeTLSPort)

	return b.String()
}

// maxBodySizeFor is the nginx client_max_body_size for one proxied service's
// server block. 4m is enough for everything except real save-data uploads:
// scsi's component-file PUT (internal/scsi/handler.go's uploadComponentData)
// carries the actual save bytes, and real Switch saves routinely exceed 4m
// even though they are "almost always well under 100 MiB" (see
// internal/scsi/README.md) -- nginx's own limit has to clear that bar or a
// save large enough to matter fails at the edge before scsi's own quota
// check (internal/scsi.LocalDisk.Put) ever runs.
func maxBodySizeFor(svc nintendo.Service) string {
	if svc == nintendo.ServiceSCSI {
		return "200m"
	}
	return "4m"
}

func httpServerBlock(cfg config.Config, serverNames []string, upstream string, verifyClient, http2 bool, maxBodySize string) string {
	var verifyClientDirective, clientCertHeader string
	if verifyClient {
		verifyClientDirective = `
    # The console presents its own device certificate to dauth and, from 21.0.0,
    # to m-lp1.baas. It is signed by Nintendo, so it cannot be verified here --
    # 'optional_no_ca' asks for it and passes it through without validating it.
    # Its only use is giving each console a stable device id; nothing is
    # authorised on it. Without this, every console shares one device id, which
    # is harmless but makes the dashboard's device list useless.
    ssl_verify_client optional_no_ca;
`
		clientCertHeader = `
        # Read by internal/dauth to derive a per-console device id.
        proxy_set_header X-SwitchNet-Client-Cert $ssl_client_fingerprint;
        # Read by internal/dauth to compute the 'x5t#S256' claim -- a real
        # SHA-256 thumbprint of the cert above, not verification of it. See
        # jwt.DeviceInfo.X5tS256.
        proxy_set_header X-SwitchNet-Client-Cert-PEM $ssl_client_escaped_cert;`
	} else {
		// Captured 2026-08-25, after the http2-off fix: the TLS handshake to
		// accounts.nintendo.com now completes cleanly and matches a plain curl
		// baseline byte-for-byte, including OpenSSL's two default post-handshake
		// NewSessionTicket messages -- but the console's browser closes the
		// connection right there, client-initiated, without ever sending the
		// HTTP request. ssl_session_tickets off did not stop nginx sending them
		// (a known nginx/OpenSSL 3.x TLS 1.3 quirk), so instead force TLS 1.2 for
		// this one host: TLS 1.2 delivers a session ticket synchronously, inside
		// the handshake, never as the asynchronous post-Finished message TLS 1.3
		// uses. Only affects this browser-only block, not the curl-based
		// device-auth clients that already negotiate TLS 1.3 successfully.
		verifyClientDirective = `
    ssl_protocols TLSv1.2;
`
	}

	http2Directive := "off"
	if http2 {
		http2Directive = "on"
	}

	var b strings.Builder
	fmt.Fprintf(&b, `server {
    listen %d ssl;
    http2 %s;
    server_name %s;

    ssl_certificate     /etc/switchnet/certs/edge.crt;
    ssl_certificate_key /etc/switchnet/certs/edge.key;
%s
    client_max_body_size %s;

    location / {
        proxy_pass http://%s;
        proxy_http_version 1.1;
        proxy_set_header Host              $host;
        proxy_set_header X-Real-IP         $remote_addr;
        proxy_set_header X-Forwarded-For   $proxy_add_x_forwarded_for;
        proxy_set_header X-Forwarded-Proto $scheme;%s
    }
}
`, cfg.Network.EdgeTLSPort, http2Directive, strings.Join(serverNames, "\n                "), verifyClientDirective, maxBodySize, upstream, clientCertHeader)
	return b.String()
}

func grpcServerBlock(cfg config.Config, upstream string, port int, serverNames []string) string {
	// The tenant host also answers Gamesync. A match tells each console where
	// to open its gamesync session (GameSession.host/port), and the address
	// that is known to work is the tenant host on the edge port:
	// NextendoNetwork/splatoon-3 measured on hardware that the same service
	// reached by a literal address on :7575 accepts TLS and then never gets a
	// request (2321-4992), while the tenant name on :443 does. gRPC names the
	// service in the path, so the split needs no second listener.
	gamesync := ""
	if upstream == "npln" {
		gamesync = `
    location /nn.npln.gamesync.v1.Gamesync/ {
        grpc_pass grpc://switchnet_gamesync;
    }
`
	}
	return fmt.Sprintf(`server {
    listen %d ssl;
    http2 on;
    server_name %s;

    ssl_certificate     /etc/switchnet/certs/edge.crt;
    ssl_certificate_key /etc/switchnet/certs/edge.key;

    # Splatoon 3 holds gRPC streams open that can stay quiet for minutes
    # (PresenceService/KeepAlive, LobbyMessaging/RecvMessage, ticket tracking,
    # Gamesync/KeepUserSession). nginx's defaults end a stream after 60s of
    # silence in either direction and an idle HTTP/2 connection after 75s; a
    # stream torn down mid-lobby is 2321-4992 on the console, the pattern
    # NextendoNetwork/splatoon-3 measured when its own server cut them.
    grpc_read_timeout   1h;
    grpc_send_timeout   1h;
    client_body_timeout 1h;
    keepalive_timeout   1h;
%s
    location / {
        grpc_pass grpc://switchnet_%s;
    }
}
`, port, strings.Join(serverNames, "\n                "), gamesync, upstream)
}

// listenPort returns the port a service's hosts are reached on.
//
// The edge port unless a host says otherwise. Only gamesync says otherwise, and
// a mismatch here means a server block that never matches -- which on the
// console looks like the game rejecting our implementation rather than like a
// routing mistake.
func listenPort(cfg config.Config, hosts []nintendo.Host) int {
	for _, h := range hosts {
		if h.TLSPort != 0 {
			return h.TLSPort
		}
	}
	return cfg.Network.EdgeTLSPort
}

// tlsNamesOf returns every concrete hostname in hosts that needs a server_name,
// expanding wildcard entries through their TLSNames.
func tlsNamesOf(hosts []nintendo.Host) []string {
	var out []string
	for _, h := range hosts {
		if !h.TLS {
			continue
		}
		if h.Wildcard {
			out = append(out, h.TLSNames...)
			continue
		}
		out = append(out, h.Pattern)
	}
	return out
}

// frontlineServerBlock renders the NPLN JSON front end.
//
// Ordinary proxy_pass over HTTP/1.1, with two settings the frontline
// connection depends on:
//
//   - proxy_buffering off, so a frame the server writes reaches the console
//     when it is written rather than when a buffer happens to fill. The
//     connection is a stream, and a buffered stream is a dead one.
//   - a read timeout far longer than nginx's 60s default. The console holds
//     this connection open and treats a close as a failure -- 2307-2103 on
//     Splatoon 2, 2321-* on Splatoon 3 -- so an idle-timeout disconnect looks
//     to the player like the game losing its connection for no reason.
//
// No "http2 on", so nginx serves these over HTTP/1.1. That is what the console
// is known to be served today rather than a measured requirement: unlike
// accounts.nintendo.com and m-lp1.baas.nintendo.com, no capture shows this
// client failing on HTTP/2, so no NoHTTP2 flag is claimed on the hosts
// themselves. If a capture ever shows HTTP/2 working here, turning it on is a
// one-line change; the reverse -- inventing a flag with no capture behind it --
// is the thing this repository has been bitten by before.
func frontlineServerBlock(cfg config.Config, serverNames []string) string {
	return fmt.Sprintf(`server {
    listen %d ssl;
    server_name %s;

    ssl_certificate     /etc/switchnet/certs/edge.crt;
    ssl_certificate_key /etc/switchnet/certs/edge.key;
    ssl_verify_client   optional_no_ca;

    location / {
        proxy_pass http://switchnet_npln_rest;
        proxy_http_version 1.1;
        proxy_set_header Host $host;
        proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;

        # The frontline connection is a long-lived stream, not a request.
        proxy_buffering off;
        proxy_read_timeout 3600s;
        proxy_send_timeout 3600s;
    }
}
`, cfg.Network.EdgeTLSPort, strings.Join(serverNames, "\n                "))
}

// upstreamAddr turns a Go listen address into one nginx can proxy to.
//
// A bare ":8083" means "every interface" to Go but is not a valid nginx
// upstream, so it becomes 127.0.0.1:8083 — which is also the right answer, since
// the backends should not be reachable from outside the host.
func upstreamAddr(listen string) string {
	if strings.HasPrefix(listen, ":") {
		return "127.0.0.1" + listen
	}
	return listen
}
