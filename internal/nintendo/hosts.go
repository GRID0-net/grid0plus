// Package nintendo holds the list of Nintendo hostnames that GRID0+
// replaces, together with which GRID0+ service serves each one.
//
// This is the single source of truth for the domain list. Three artefacts are
// generated from it, so they cannot drift apart:
//
//   - switch/hosts.txt      the Atmosphère DNS-MITM redirect list
//   - deploy/nginx.conf     the SNI/vhost routing map
//   - the TLS certificate SANs used by scripts/gen-certs.sh
//
// See docs at https://github.com/kinnay/NintendoClients/wiki/Server-List
package nintendo

import "strings"

// Service identifies which GRID0+ backend serves a host.
type Service string

const (
	// ServiceDAuth is the device authentication server (and the dcert JWKS host).
	ServiceDAuth Service = "dauth"
	// ServiceAAuth is the application authentication server (and the acert JWKS host).
	ServiceAAuth Service = "aauth"
	// ServiceBAAS is the account and friends server.
	ServiceBAAS Service = "baas"
	// ServiceStubs is the catch-all server for endpoints we answer but do not implement.
	ServiceStubs Service = "stubs"
	// ServiceNATCheck is the UDP NAT-check responder. It is not proxied by nginx.
	ServiceNATCheck Service = "natcheck"
	// ServiceNPLN is the NPLN gRPC gateway.
	ServiceNPLN Service = "npln"
	// ServiceGamesync is NPLN's document/watch service.
	//
	// Separate from ServiceNPLN for one reason that matters: the console reaches
	// it on PORT 7575, not 443. Grouping it with the tenant host would emit one
	// nginx block on the edge port that grpc_passes both to the gateway, which
	// answers, wrongly, with a service that does not implement gamesync's RPCs.
	// That failure looks like the game being unhappy with our gamesync
	// implementation rather than like a routing mistake.
	ServiceGamesync Service = "gamesync"
	// ServiceNPLNRest is NPLN's JSON front end: the hosts Splatoon 3 talks to over
	// ordinary HTTPS *before* it opens a gRPC channel.
	//
	// Separate from ServiceNPLN because the two need different nginx: gRPC
	// needs grpc_pass and HTTP/2, this needs proxy_pass and HTTP/1.1. Routing
	// either through the other's block completes the TLS handshake and then
	// fails in a way neither side logs.
	ServiceNPLNRest Service = "npln_rest"
	// ServiceNEX is the NEX game servers (Splatoon 2): a WebSocket per
	// console, carrying PRUDP-Lite. Its own service because nginx must
	// proxy it as a WebSocket upgrade over HTTP/1.1 -- neither grpc_pass nor
	// a plain proxy_pass block completes the upgrade -- and must not send
	// the console a TLS CertificateRequest.
	ServiceNEX Service = "nex"
	// ServiceEShop is the eShop: the storefront, the commerce API, the eLicense
	// server and the content server. GRID0+ doesn't implement it.
	ServiceEShop Service = "eshop"
	// ServiceSCSI is the OLSC (Online Save Cloud) cloud-save server:
	// storage.hac.lp1.scsi.srv.nintendo.net's save data archives and
	// permission.lp1.sata.srv.nintendo.net's save data sets and key package.
	ServiceSCSI Service = "scsi"
	// ServiceBlackhole means the host should be null-routed on the console
	// rather than answered. Used for telemetry.
	ServiceBlackhole Service = "blackhole"
	// ServicePassthrough means the host must keep resolving to real Nintendo.
	// Redirecting it causes more harm than good.
	ServicePassthrough Service = "passthrough"
)

// Host is one Nintendo hostname (or DNS-MITM wildcard pattern) and the
// GRID0+ service that answers it.
type Host struct {
	// Pattern is the hostname, or an Atmosphère DNS-MITM wildcard pattern.
	// In Atmosphère's hosts file, '*' matches zero or more characters and '%'
	// expands to the environment ("lp1"). Wildcards are only valid in the
	// console-side hosts file, never in a TLS SAN or an nginx server_name.
	Pattern string

	// Service is the GRID0+ backend that answers this host.
	Service Service

	// Comment explains what the host is for. It is emitted into the generated
	// hosts file, so keep it short and in English.
	Comment string

	// TLS is true if the console speaks HTTPS to this host, which means nginx
	// needs a server block and the certificate needs the name in its SANs.
	TLS bool

	// TLSPort is the port the console connects to, when it is not the edge's.
	// Zero means network.edge_tls_port.
	//
	// Only gamesync needs this today, and it needs it badly: the console reaches
	// it on 7575, and a server block on 443 would never match.
	TLSPort int

	// Wildcard is true if Pattern is a DNS-MITM pattern rather than a literal
	// hostname. Wildcard entries are skipped when generating certificate SANs
	// and nginx server names unless an explicit TLSNames list is given.
	Wildcard bool

	// TLSNames are the concrete hostnames to put in the certificate and in
	// nginx for a wildcard entry. A DNS wildcard SAN such as "*.baas.nintendo.com"
	// only matches one label, so multi-label hosts have to be listed out.
	TLSNames []string

	// NoClientCertRequest is true for a host nginx must not send a TLS
	// CertificateRequest to. Two hosts need it, for two different clients that fail
	// on one: accounts.nintendo.com (the browser applet, below) and the friends host
	// (the friends sysmodule, see its entry). With ssl_verify_client optional_no_ca
	// (needed by dauth/aauth/baas to read the console's device certificate), the TLS
	// handshake for accounts.nintendo.com negotiates fine up through the server's
	// certificate flight, then the console's browser-applet client goes silent and
	// closes the connection after ~600ms without ever sending its Finished message --
	// the "loads forever" symptom on the Nintendo Account link page. The curl-based
	// device-auth clients (dauth, aauth, the rest of baas) handle the same
	// CertificateRequest fine by answering with an empty certificate; the system
	// browser's separate TLS stack does not. accounts.nintendo.com is only ever reached
	// by that browser, never by a device-auth client, so it gets its own nginx server
	// block with no client-cert request at all.
	NoClientCertRequest bool

	// NoHTTP2 is true for a host nginx must not offer HTTP/2 to, independent of
	// NoClientCertRequest -- m-lp1.baas.nintendo.com needs the client-cert request (it
	// is real device-auth traffic, "system-level account state synchronization", not
	// the browser applet) but shows the same signature that led to disabling HTTP/2
	// for accounts.nintendo.com: a real, sizeable, bidirectional TLS exchange that
	// nginx never logs as a completed HTTP request. It shows only against real nginx
	// (http2 on by default), never through a mitmproxy stand-in that does not offer
	// HTTP/2 at all -- the same HTTP/2 interop failure, on a different host.
	NoHTTP2 bool
}

// Hosts is the redirect list, in the order it should be written to the
// console's hosts file.
//
// Ordering matters: Atmosphère's DNS-MITM resolver applies the LAST matching
// line, so broad wildcards come first and specific overrides come last.
var Hosts = []Host{
	// ---- Device and application authentication --------------------------
	{
		Pattern: "dauth-lp1.ndas.srv.nintendo.net",
		Service: ServiceDAuth, TLS: true,
		Comment: "device auth (up to 19.0.1)",
	},
	{
		Pattern: "dauth.hac.lp1.ndas.srv.nintendo.net",
		Service: ServiceDAuth, TLS: true,
		Comment: "device auth (20.0.0 and later)",
	},
	{
		Pattern: "dcert-lp1.ndas.srv.nintendo.net",
		Service: ServiceDAuth, TLS: true,
		Comment: "device token JWK set (the 'jku' of every device token)",
	},
	{
		Pattern: "aauth-lp1.ndas.srv.nintendo.net",
		Service: ServiceAAuth, TLS: true,
		Comment: "application auth (up to 19.0.1)",
	},
	{
		Pattern: "aauth.hac.lp1.ndas.srv.nintendo.net",
		Service: ServiceAAuth, TLS: true,
		Comment: "application auth (20.0.0 and later)",
	},
	{
		Pattern: "acert-lp1.ndas.srv.nintendo.net",
		Service: ServiceAAuth, TLS: true,
		Comment: "application token JWK set",
	},

	// ---- Accounts and friends -------------------------------------------
	{
		// No client-cert request: this is the friends sysmodule's host, and friends does
		// not cope with one the way nnAccount does. A friend-list refresh shows a clean
		// TLS 1.3 handshake to this SNI, then the console aborting in its own final
		// flight -- nginx: "client closed connection while waiting for request" -- and
		// the applet showing 2121-1058, which friends maps to CURLE_SSL_CERTPROBLEM: asked
		// for a certificate, friends tries to present one and cannot. Nothing on this host
		// reads a client certificate (only dauth does), so there is nothing to lose by not
		// asking. Implies HTTP/1.1, see NoHTTP2.
		Pattern: "e0d67c509fb203858ebcb2fe3f88c2aa.baas.nintendo.com",
		Service: ServiceBAAS, TLS: true, NoClientCertRequest: true,
		Comment: "BAAS lp1 — accounts and friends",
	},
	{
		Pattern: "m-lp1.baas.nintendo.com",
		Service: ServiceBAAS, TLS: true, NoHTTP2: true,
		Comment: "BAAS accounts (21.0.0 and later)",
	},
	{
		Pattern: "cdn-image-e0d67c509fb203858ebcb2fe3f88c2aa.baas.nintendo.com",
		Service: ServiceBAAS, TLS: true, NoHTTP2: true, NoClientCertRequest: true,
		// No client-cert request for the same reason as the friends host above: the
		// friends sysmodule downloads the thumbnails, tries to answer the
		// CertificateRequest and aborts its own handshake. Emulators ignore the request
		// and fetch every image; a console never reaches nginx with one. Nothing here
		// reads a client certificate.
		//
		// HTTP/2 is off too: the console's image fetch is system-level account-data
		// traffic, not the browser applet, so it gets m-lp1.baas.nintendo.com's
		// treatment (NoHTTP2), not accounts.nintendo.com's.
		Comment: "BAAS profile image CDN",
	},
	{
		Pattern: "accounts.nintendo.com",
		Service: ServiceBAAS, TLS: true, NoClientCertRequest: true,
		Comment: "Nintendo Account link stub — a custom login page, not a copy of " +
			"Nintendo's OAuth flow. See internal/baas/nalink.go",
	},
	{
		Pattern: "api.accounts.nintendo.com",
		Service: ServiceBAAS, TLS: true,
		Comment: "Nintendo Account profile API (GET /2.0.0/users/me), called right after the link token exchange. See internal/baas/nalink.go",
	},

	// ---- NAT check ------------------------------------------------------
	// Pia requires nncs1 and nncs2 to be two DISTINCT addresses. If they
	// resolve to the same IP the console de-duplicates the probe, never sends
	// the second one, and NAT detection never completes. It shows up as Mario
	// Kart 8 error 2618-201.
	{
		Pattern: "nncs1-lp1.n.n.srv.nintendo.net",
		Service: ServiceNATCheck,
		Comment: "NAT check, primary (UDP 10025/10125)",
	},
	{
		Pattern: "nncs2-lp1.n.n.srv.nintendo.net",
		Service: ServiceNATCheck,
		Comment: "NAT check, secondary — MUST be a different IP from nncs1",
	},

	// ---- The Pia game-session host ----------------------------------------
	// A second host family exists besides nncs1/nncs2:
	// "g2122d301.<region>.p.srv.nintendo.net", built from Splatoon 3's own Pia game
	// code ("g2122d301", the literal fallback in the function that constructs this
	// string) rather than a shared prefix. isNintendoOwnedDomain's catch-all would
	// blackhole it like any unlisted *.nintendo.net host, but then a console reaching
	// it would leave no log line to find it by, the same invisibility problem the
	// sect/sata/sprofile hosts had before their own entries were added.
	//
	// Gameplay itself is ICE-based, not Pia-hosted (see AllocateIceServerSet's doc
	// comment), so this is unlikely to be on the connection-critical path -- but
	// that is a reading of one other function's behavior, not confirmation this host
	// is dead. Stubbed rather than blackholed specifically, so if a console does
	// reach it, what it asks for gets recorded instead of silently discarded.
	{
		Pattern: "%.p.srv.nintendo.net",
		Service: ServiceStubs, Wildcard: true, TLS: true,
		Comment: "Splatoon 3's own Pia game-session host (game code " +
			"g2122d301) — purpose and whether it is ever actually reached " +
			"are both UNCONFIRMED",
	},

	// ---- Parental controls ------------------------------------------------
	// The system's network server discovery resolver references exactly two hosts
	// in its own code: this one and moon.nintendo.com below. Parental controls are
	// out of scope for a private network (nobody enforces restrictions against
	// themselves), but both are stubbed so they never reach real Nintendo.
	{
		Pattern: "%.pctl.srv.nintendo.net",
		Service: ServiceStubs, Wildcard: true, TLS: true,
		Comment: "parental controls sync API",
	},
	{
		Pattern: "moon.nintendo.com",
		Service: ServiceStubs, TLS: true,
		Comment: "\"Nintendo Switch Parental Controls\" companion app API " +
			"(public codename \"moon\")",
	},

	// ---- A second, unrelated "vermillion" -------------------------------
	// The system update/install module uses "vermillionDeviceId" throughout a local
	// nearby-console-discovery mechanism (console-to-console data transfer during
	// setup), which has nothing to do with NPLN. Nintendo reused the "vermillion"
	// codename for two unrelated systems: this one, and internal/nplnrest's
	// gw.hac.lp1.vermillion.srv.nintendo.net device-init host. Don't conflate them.
	//
	// That module reports failures from this mechanism to a host nothing else here
	// named ("vermillion-app-call-failed" is one of its error strings). It is out of
	// scope (console-to-console transfer, not accounts or Splatoon 3), so it is
	// stubbed instead of left on the generic catch-all.
	{
		Pattern: "problem-details.ennovate.srv.nintendo.net",
		Service: ServiceStubs, TLS: true,
		Comment: "error reporting for nearby-device transfer (path seen: /vermillion-gw/); unrelated to NPLN despite the shared codename",
	},

	// ---- Connection test ------------------------------------------------
	// The console (and the browser applet) refuse to consider themselves
	// online if the connection test fails, which surfaces as "this feature is
	// not available" rather than as a network error.
	{
		Pattern: "ctest.cdn.nintendo.net",
		Service: ServiceStubs,
		Comment: "connection test",
	},
	{
		Pattern: "api.hac.lp1.ctest.srv.nintendo.net",
		Service: ServiceStubs, TLS: true,
		Comment: "connection test API",
	},
	{
		Pattern: "conntest.nintendowifi.net",
		Service: ServiceStubs,
		Comment: "browser connectivity check",
	},

	// ---- Stubs ----------------------------------------------------------
	{
		Pattern: "api-lp1.frs.srv.nintendo.net",
		Service: ServiceStubs, TLS: true,
		Comment: "friend recommendations — answered with empty results",
	},
	{
		Pattern: "app.lp1.five.nintendo.net",
		Service: ServiceStubs, TLS: true,
		Comment: "online play invitations — answered with empty results",
	},
	{
		Pattern: "capi.lp1.op2.nintendo.net",
		Service: ServiceStubs, TLS: true,
		Comment: "NSO membership verification — always reports a membership",
	},
	{
		Pattern: "dragons.hac.lp1.dragons.nintendo.net",
		Service: ServiceStubs, TLS: true,
		Comment: "eLicense / contents authorization token for digital titles",
	},
	{
		Pattern: "pubkey.lp1.dragons.nintendo.net",
		Service: ServiceStubs, TLS: true,
		Comment: "eLicense JWK set",
	},
	{
		// OLSC's save-data-archive API. The field-level shape is documented at
		// https://github.com/kinnay/NintendoClients/wiki/OLSC-Servers ("GET
		// /api/nx/v2/save_data_archives" and its siblings: start_upload,
		// finish_upload, generate_key_seed_package, extend_upload_timeout). See
		// internal/scsi/README.md for which endpoints are confirmed and which are a
		// defensible reading of an undocumented one.
		Pattern: "storage.hac.lp1.scsi.srv.nintendo.net",
		Service: ServiceSCSI, TLS: true,
		Comment: "OLSC save data archives",
	},
	{
		Pattern: "%.scsi.srv.nintendo.net",
		Service: ServiceStubs, Wildcard: true, TLS: true,
		Comment: "console-to-console account migration (migration.%.scsi...). Stubbed so it never reaches real Nintendo; building real migration is out of scope",
	},
	{
		Pattern: "god.hac.lp1.penne.srv.nintendo.net",
		Service: ServiceNPLNRest, TLS: true,
		Comment: "push notification accounts (Penne): the push service registers here for its penne id and the notification token the Nintendo Account link later PUTs to /1.0.0/push_channels; internal/nplnrest remembers whose token it is, which is what lets a push reach the console's frontline",
	},
	{
		Pattern: "consumer.lp1.npns.srv.nintendo.net",
		Service: ServiceStubs, TLS: true,
		Comment: "push notifications (NPNS)",
	},
	{
		Pattern: "beach.hac.lp1.eshop.nintendo.net",
		Service: ServiceStubs, TLS: true,
		Comment: "eShop device registration. The Nintendo Account link flow reaches it before accounts.nintendo.com, and Splatoon 3's NPLN bootstrap POSTs /v1/devices/<id>/penne_id/register here, which has to succeed before the console tries a penne login. The generic empty-object stub answer is what both expect",
	},
	{
		Pattern: "bvc-hac-lp1.cdn.nintendo.net",
		Service: ServiceStubs, TLS: true,
		Comment: "browser version check: a file at /<major>-<minor>-<micro> means that firmware must update, which blocks Nintendo Account linking, the eShop and Switch Online. The stub answers 404 for every version",
	},
	{
		Pattern: "service-status-lp1.cdn.nintendo.net",
		Service: ServiceStubs, TLS: true,
		Comment: "game server availability — always reports everything up",
	},
	{
		Pattern: "api-lp1.znc.srv.nintendo.net",
		Service: ServiceStubs, TLS: true,
		Comment: "smartphone app API (Coral). Splatoon 3 also builds URLs on this host itself. Request shape unconfirmed",
	},
	{
		Pattern: "%.znc.srv.nintendo.net",
		Service: ServiceStubs, Wildcard: true, TLS: true,
		Comment: "web view backend for the smartphone app (wildcard, since the region code is a placeholder in the binary)",
	},

	// ---- Hosts that were not listed ---------------------------------------
	// Sweeping every system module for *.nintendo.net and *.nintendo.com strings
	// turned up three hosts with no entry in this file. None leaked to real
	// Nintendo regardless, because isNintendoOwnedDomain's catch-all blackholes
	// any unlisted *.nintendo.net host, but they were invisible to anyone reading
	// this list, and the catch-all refuses connections instead of answering them.
	// They are stubbed for the same reason as every other host whose purpose is
	// unconfirmed: an answer risks less than a refusal does.
	{
		Pattern: "api.sect.srv.nintendo.net",
		Service: ServiceStubs, TLS: true,
		Comment: "network interface assignment, not account settings sync as the bare path (/v1/setting) suggests",
	},
	{
		Pattern: "%.sata.srv.nintendo.net",
		Service: ServiceStubs, Wildcard: true, TLS: true,
		Comment: "OLSC's host family (cloud save backup). permission.lp1.sata.srv.nintendo.net just below is served by scsi and MUST stay after this wildcard, because Atmosphere applies the last matching line. Everything else under it is stubbed, including the privacy policy pages (pp.%.sata.srv.nintendo.net)",
	},
	{
		// Same wiki page as storage.hac.lp1.scsi.srv.nintendo.net above: "SATA
		// permission server". Its two endpoints (save_data_sets,
		// save_data_sets/generate_key_package) are listed with no method or
		// field shape at all -- generate_key_package is flagged in
		// internal/scsi/handler.go as a likely hard blocker on equal footing
		// with generate_key_seed_package, since it may carry real
		// cryptographic material a console validates before trusting a
		// restore. MUST stay after the %.sata.srv.nintendo.net wildcard
		// above -- see that entry's own comment for why.
		Pattern: "permission.lp1.sata.srv.nintendo.net",
		Service: ServiceSCSI, TLS: true,
		Comment: "OLSC save data sets / key package",
	},
	{
		Pattern: "%.sprofile.srv.nintendo.net",
		Service: ServiceStubs, Wildcard: true, TLS: true,
		Comment: "device profile metadata (/v1/device/metadata), probably analytics. Stubbed conservatively, like sata and sect",
	},

	// ---- NPLN, the JSON front ---------------------------------------------
	// Splatoon 3 does NOT open with gRPC. It first talks ordinary JSON over
	// HTTPS to vermillion and penne, and only then opens the gRPC channel to
	// the tenant host below.
	//
	// These are ServiceNPLNRest, not ServiceNPLN, because they need proxy_pass
	// and HTTP/1.1 rather than grpc_pass and HTTP/2; see the ServiceNPLNRest
	// doc comment for what happens when the two are swapped.
	{
		Pattern: "gw.hac.lp1.vermillion.srv.nintendo.net",
		Service: ServiceNPLNRest, TLS: true,
		Comment: "NPLN device initialisation and account config. A device-init handshake that expects real answers, so a bare stub isn't enough. See internal/nplnrest",
	},
	{
		Pattern: "val.hac.lp1.penne.srv.nintendo.net",
		Service: ServiceNPLNRest, TLS: true,
		Comment: "NPLN login tickets (POST /v1/login_tickets) and the frontline hostname the console is told to open its persistent connection to",
	},
	{
		// fro-N is a family: the frontline host is whichever one penne names in
		// its response, so listing only fro-1 leaves the console pointed at real
		// Nintendo the moment it is told to use another. The wildcard covers the
		// family; TLSNames lists the ones a certificate has to cover, because a
		// DNS wildcard SAN matches one label and these are multi-label names.
		Pattern: "fro-*.hac.lp1.penne.srv.nintendo.net",
		Service: ServiceNPLNRest, TLS: true, Wildcard: true,
		TLSNames: []string{
			"fro-1.hac.lp1.penne.srv.nintendo.net",
			"fro-2.hac.lp1.penne.srv.nintendo.net",
			"fro-3.hac.lp1.penne.srv.nintendo.net",
			"fro-4.hac.lp1.penne.srv.nintendo.net",
		},
		Comment: "NPLN persistent frontline connection. The console holds it open and treats a close as a failure (2307-2103 on Splatoon 2, 2321-* on Splatoon 3), so it is a long-lived streaming response, not a request/response endpoint",
	},

	// ---- NPLN, the gRPC tenant --------------------------------------------
	// The tenant hostname is <tenant id>.lp1.t.npln.srv.nintendo.net. The
	// Splatoon 3 tenant is t-dce9377b-lp1. gamesync.npln.nintendo.net does not
	// end in srv.nintendo.net, so a *srv.nintendo.net wildcard misses it.
	{
		Pattern: "t-dce9377b-lp1.lp1.t.npln.srv.nintendo.net",
		Service: ServiceNPLN, TLS: true,
		Comment: "NPLN tenant: Splatoon 3",
	},
	{
		// PORT 7575, not 443 -- the console reaches gamesync directly rather
		// than through the tenant host. nginx therefore needs its own server
		// block on that port; see ServiceGamesync.
		Pattern: "gamesync.npln.nintendo.net",
		Service: ServiceGamesync, TLS: true, TLSPort: 7575,
		Comment: "NPLN game session host (Splatoon 3 lobby), on port 7575",
	},

	// ---- NEX game servers ---------------------------------------------
	// A Switch finds a NEX title's game server by DNS alone:
	// g<game server id>-lp1.s.n.srv.nintendo.net, port 443, a WebSocket (see the
	// NEX overview on Kinnay's wiki). Both halves of the game server, login and
	// play, are on this one name; see internal/rendezvous.
	//
	// NoClientCertRequest and NoHTTP2 follow what Splatoon 2's server needs: it
	// never sends a CertificateRequest and offers only http/1.1 in ALPN, because
	// a WebSocket upgrade is an HTTP/1.1 exchange.
	{
		Pattern: "g2df33d01-lp1.s.n.srv.nintendo.net",
		Service: ServiceNEX, TLS: true, NoClientCertRequest: true, NoHTTP2: true,
		Comment: "NEX game server: Splatoon 2 (game server 2DF33D01)",
	},

	// ---- Telemetry: redirected to a sink, not null-routed -----------------
	// These used to be null-routed, on the theory that it tells Nintendo nothing
	// either way and keeps our logs quiet. But the Switch Online/friends applet
	// (0100000000000816) waits SYNCHRONOUSLY for an HTTP answer from these two
	// hosts before it loads its own main module, so a null route makes the connection
	// hang until it times out and the applet gives up before it initialises. So both
	// hosts are redirected to a server that reads the body, discards it and
	// answers 200 immediately. Telemetry still never reaches real Nintendo; only
	// the wait the applet blocks on is satisfied. See internal/stubs.telemetrySink.
	// Neither host uses a client certificate, so nginx must not request one.
	{
		Pattern: "receive-%.dg.srv.nintendo.net",
		Service: ServiceStubs, Wildcard: true, TLS: true, NoClientCertRequest: true,
		TLSNames: []string{"receive-lp1.dg.srv.nintendo.net"},
		Comment:  "play reports -- read and discarded, answered 200 so the NSO applet does not hang",
	},
	{
		Pattern: "receive-%.er.srv.nintendo.net",
		Service: ServiceStubs, Wildcard: true, TLS: true, NoClientCertRequest: true,
		TLSNames: []string{"receive-lp1.er.srv.nintendo.net"},
		Comment:  "error reports -- read and discarded, answered 200 so the NSO applet does not hang",
	},

	// ---- BCAT: answered with 304, never left unreachable -----------------
	// These three hosts must stay reachable. Null-routing them looks safe but
	// isn't: Splatoon 3 raises 2122-2403 (module 122 is bcat) when bcat-list-lp1
	// is unreachable, and a null-routed topics host fails the console's very
	// first boot request. A generic 304 Not Modified on every request tells the
	// console "your cache is current" and it falls back to whatever it already
	// has locally. That sends no data; it only stops the absence from being a hard
	// error. A host missing from this list entirely would fall through to ordinary
	// upstream DNS and reach real Nintendo, which this deployment's isolation
	// policy rules out.
	//
	// When bcat.seed_dir holds files for a title, list and data serve that
	// content natively; internal/bcat.Probe is only a diagnostic an operator
	// reaches for deliberately, not what these hosts answer with day to day.
	//
	// The CDN hosts must not request a client certificate: a TLS connection that
	// is closed before any HTTP was sent is what a CertificateRequest causes here.
	{
		Pattern: "bcat-list-lp1.cdn.nintendo.net",
		Service: ServiceStubs, TLS: true, NoClientCertRequest: true,
		Comment: "BCAT background content delivery, list discovery -- 304, never null-routed",
	},
	{
		Pattern: "bcat-data-lp1.cdn.nintendo.net",
		Service: ServiceStubs, TLS: true, NoClientCertRequest: true,
		Comment: "BCAT background content delivery, data -- 304, never null-routed",
	},
	{
		Pattern: "bcat-topics-lp1.cdn.nintendo.net",
		Service: ServiceStubs, TLS: true, NoClientCertRequest: true,
		Comment: "System news/topics feed -- confirmed real shape, 304, never null-routed",
	},

	// ---- eShop ------------------------------------------------------------
	// Null-routed on purpose. GRID0+ doesn't implement the eShop, so there is no
	// reason for a console pointed at it to reach real Nintendo through these
	// hosts. They are listed so the next person who builds it finds them, and so
	// `atumn` or `aqua` aren't added to this group by mistake: see the d4c note
	// below, which covers system updates and is a different group.
	//
	// If you want this console's eShop to keep working normally against real
	// Nintendo, switch these back to ServicePassthrough. That means this console's
	// traffic for these hosts, including the Nintendo Account sign-in flow, goes to
	// real Nintendo, not GRID0+.
	//
	// The storefront is a WEBSITE: the applet loads bugyo/ashigaru and renders it,
	// exposing system functions through a `window.nx` bridge, so building it means a
	// web app plus a commerce API, not a native UI.
	// https://github.com/kinnay/NintendoClients/wiki/eShop-Applet-(Switch)
	{
		Pattern: "bugyo.hac.lp1.eshop.nintendo.net",
		Service: ServiceBlackhole,
		Comment: "eShop: the eShop storefront (a website). Not served by GRID0+; null-routed rather than reaching real Nintendo",
	},
	{
		Pattern: "hac.lp1.savanna.srv.nintendo.net",
		Service: ServiceBlackhole,
		Comment: "eShop: the newer eShop applet. Not served by GRID0+; null-routed rather than reaching real Nintendo",
	},
	{
		Pattern: "ecs-lp1.hac.shop.nintendo.net",
		Service: ServiceBlackhole,
		Comment: "eShop: commerce (ECS). Not served by GRID0+; null-routed rather than reaching real Nintendo",
	},
	{
		Pattern: "ias-lp1.hac.shop.nintendo.net",
		Service: ServiceBlackhole,
		Comment: "eShop: commerce (IAS). Not served by GRID0+; null-routed rather than reaching real Nintendo",
	},
	{
		Pattern: "atum.hac.lp1.d4c.nintendo.net",
		Service: ServiceBlackhole,
		Comment: "eShop: GAME content, addressed by content id. NOT a system update host. Not served by GRID0+; null-routed rather than reaching real Nintendo",
	},
	{
		Pattern: "tagaya.hac.lp1.eshop.nintendo.net",
		Service: ServiceBlackhole,
		Comment: "eShop: latest version per title. Not served by GRID0+; null-routed rather than reaching real Nintendo",
	},
	{
		Pattern: "idbe-hac.cdn.nintendo.net",
		Service: ServiceBlackhole,
		Comment: "eShop: title icons. Not served by GRID0+; null-routed rather than reaching real Nintendo",
	},
	{
		Pattern: "cdn.accounts.nintendo.com",
		Service: ServiceBlackhole,
		Comment: "static assets for the Nintendo Account sign-in page. Null-routed so it never reaches real Nintendo",
	},

	// ---- System updates: null-routed on this deployment -----------------
	// The d4c hosts serve SYSTEM UPDATES: sun, atumn and aqua. The general advice
	// for a console that moves back and forth between GRID0+ and real Nintendo is to
	// leave these passthrough: the nim sysmodule stores a persistent flag when the
	// update check fails, and that flag survives switching DNS back to Nintendo's
	// servers, which is an expensive mistake on that kind of setup.
	//
	// It does not apply here. This host list is generated for an emuMMC that is
	// permanently and exclusively GRID0+'s, so there is no "switching back" for the
	// flag to survive into. Given that, passthrough would only mean real Nintendo
	// traffic leaving this console for no benefit, so they are null-routed like every
	// other host nothing is built for.
	//
	// If you ever reuse this host list for a console that DOES move back and forth
	// between GRID0+ and real Nintendo, put these back to ServicePassthrough first;
	// TestSystemUpdateHostsAreNullRouted records the reasoning.
	//
	// Note the trap: `atum` (game content, listed above) and `atumn` (system update
	// content) differ by one letter and share the d4c domain.
	{
		Pattern: "sun.hac.lp1.d4c.nintendo.net",
		Service: ServiceBlackhole,
		Comment: "system update metadata. Null-routed: this emuMMC never talks to real Nintendo",
	},
	{
		Pattern: "atumn.hac.lp1.d4c.nintendo.net",
		Service: ServiceBlackhole,
		Comment: "system update content. NOT atum. Null-routed: this emuMMC never talks to real Nintendo",
	},
	{
		Pattern: "aqua.hac.lp1.d4c.nintendo.net",
		Service: ServiceBlackhole,
		Comment: "system update requirements. Null-routed: this emuMMC never talks to real Nintendo",
	},

	// ---- Not previously listed at all -----------------------------------
	{
		Pattern: "fw-api.lp1.nso.nintendo.net",
		Service: ServiceBlackhole,
		Comment: "the Switch Online app's own backend. Null-routed, because this console never talks to real Nintendo",
	},
}

// ServiceHosts returns the hosts served by one service, in declaration order.
func ServiceHosts(s Service) []Host {
	var out []Host
	for _, h := range Hosts {
		if h.Service == s {
			out = append(out, h)
		}
	}
	return out
}

// TLSHostnames returns every concrete hostname that needs to appear in the
// edge certificate's subject alternative names and in an nginx server_name.
// Wildcard patterns are expanded through their TLSNames list, or skipped if
// they have none.
func TLSHostnames() []string {
	seen := map[string]bool{}
	var out []string
	add := func(name string) {
		if name == "" || seen[name] {
			return
		}
		seen[name] = true
		out = append(out, name)
	}
	for _, h := range Hosts {
		if !h.TLS {
			continue
		}
		if h.Wildcard {
			for _, n := range h.TLSNames {
				add(n)
			}
			continue
		}
		add(h.Pattern)
	}
	return out
}

// ProxiedServices are the services that sit behind the nginx TLS edge, in the
// order their server blocks should be emitted.
var ProxiedServices = []Service{
	ServiceDAuth, ServiceAAuth, ServiceBAAS, ServiceSCSI,
	ServiceNPLNRest, ServiceNPLN, ServiceGamesync, ServiceNEX, ServiceStubs,
}

// Resolution says what a DNS server should do with a query.
type Resolution int

const (
	// ResolveForward means answer from upstream: this host is not ours.
	ResolveForward Resolution = iota
	// ResolveRedirect means answer with the operator's address.
	ResolveRedirect
	// ResolveNATCheckSecondary means answer with the operator's SECOND address.
	// Only nncs2 gets this, and it has to be a different address from nncs1 or
	// the console never sends its second NAT probe.
	ResolveNATCheckSecondary
	// ResolveBlackhole means answer with 0.0.0.0, so the console gives up
	// immediately instead of waiting for a timeout.
	ResolveBlackhole
)

// Resolve decides what to do with a DNS query for name.
//
// This is the DNS counterpart of the hosts file, driven by the same list, so a
// console pointed at a resolver using the generated dnsmasq/unbound
// configuration and a console using an Atmosphère hosts file see the same
// redirects. Two mechanisms that disagree about which hosts are ours would be
// very hard to debug from the console side.
//
// Matching is longest-suffix-wins, which is the DNS analogue of Atmosphère's
// last-line-wins: a specific host beats a wildcard covering it.
func Resolve(name string) (Resolution, Host, bool) {
	name = normaliseDNSName(name)

	var best Host
	var bestLen int
	found := false

	for _, h := range Hosts {
		suffix, ok := h.dnsSuffix()
		if !ok {
			continue
		}
		if !matchesSuffix(name, suffix, h.Wildcard) {
			continue
		}
		// A longer suffix is more specific. On a tie, later declaration wins,
		// which matches the hosts file's last-line-wins rule.
		if len(suffix) >= bestLen {
			best, bestLen, found = h, len(suffix), true
		}
	}

	if !found {
		// Not something GRID0+ explicitly handles. The default is to forward
		// it — this is not a Nintendo host, so it is not ours to have an opinion
		// about — with one exception: a Nintendo-owned domain nobody has added to
		// Hosts yet. On a deployment where the console must never reach real
		// Nintendo, forwarding it there just because it has not been individually
		// catalogued is exactly the gap that this whole list exists to close.
		// See isNintendoOwnedDomain and TestUnlistedNintendoHostsAreBlackholed.
		if isNintendoOwnedDomain(name) {
			return ResolveBlackhole, Host{}, true
		}
		return ResolveForward, Host{}, false
	}

	switch best.Service {
	case ServiceBlackhole:
		return ResolveBlackhole, best, true
	case ServicePassthrough:
		return ResolveForward, best, true
	case ServiceNATCheck:
		if strings.HasPrefix(best.Pattern, "nncs2") {
			return ResolveNATCheckSecondary, best, true
		}
		return ResolveRedirect, best, true
	default:
		return ResolveRedirect, best, true
	}
}

// dnsSuffix turns a host entry into a DNS suffix to match against.
//
// Atmosphère's patterns are not DNS patterns: '*' matches zero or more
// characters anywhere, and '%' expands to the environment. For DNS, what matters
// is the fixed tail, so 'receive-%.dg.srv.nintendo.net' becomes
// '.dg.srv.nintendo.net' and matches any host under it.
func (h Host) dnsSuffix() (string, bool) {
	if !h.Wildcard {
		return h.Pattern, true
	}
	// Take everything after the last wildcard character.
	idx := strings.LastIndexAny(h.Pattern, "*%")
	if idx < 0 {
		return h.Pattern, true
	}
	suffix := h.Pattern[idx+1:]
	if suffix == "" || suffix == "." {
		// A pattern with nothing fixed after the wildcard would match
		// everything, which is never what is wanted here.
		return "", false
	}
	return strings.TrimPrefix(suffix, "."), true
}

// nintendoOwnedDomains are domains Nintendo is known to operate, beyond the
// specific hostnames explicitly listed in Hosts.
//
// It comes from the community-maintained "block everything Nintendo" hosts lists,
// not from guesswork: those lists have had far more consoles' worth of traffic to
// find gaps in than this one has. A regional Nintendo domain not on this list is
// still possible; the two hosts that prompted this fallback (cdn.accounts.nintendo.com,
// fro-1.hac.lp1.penne.srv.nintendo.net) were found by watching real traffic, not by
// reasoning about the domain in advance, and more probably exist.
var nintendoOwnedDomains = []string{
	"nintendo.com",
	"nintendo.net",
	"nintendo.jp",
	"nintendo.co.jp",
	"nintendo.co.uk",
	"nintendo-europe.com",
	"nintendowifi.net",
	"nintendo.es",
	"nintendo.co.kr",
	"nintendo.tw",
	"nintendo.com.hk",
	"nintendo.com.au",
	"nintendo.co.nz",
	"nintendo.at",
	"nintendo.be",
	"nintendods.cz",
	"nintendo.dk",
	"nintendo.de",
	"nintendo.fi",
	"nintendo.fr",
	"nintendo.gr",
	"nintendo.hu",
	"nintendo.it",
	"nintendo.nl",
	"nintendo.no",
	"nintendo.pt",
	"nintendo.ru",
	"nintendo.co.za",
	"nintendo.se",
	"nintendo.ch",
	"nintendoswitch.com",
	"nintendoswitch.com.cn",
	"nintendoswitch.cn",
}

// isNintendoOwnedDomain reports whether name is under a domain in
// nintendoOwnedDomains, whether or not the specific host is one Hosts lists.
func isNintendoOwnedDomain(name string) bool {
	for _, d := range nintendoOwnedDomains {
		if name == d || strings.HasSuffix(name, "."+d) {
			return true
		}
	}
	return false
}

func matchesSuffix(name, suffix string, wildcard bool) bool {
	if !wildcard {
		return name == suffix
	}
	return name == suffix || strings.HasSuffix(name, "."+suffix)
}

// normaliseDNSName lower-cases a query name and strips the trailing dot.
func normaliseDNSName(name string) string {
	return strings.ToLower(strings.TrimSuffix(name, "."))
}

// RedirectedNames returns every concrete hostname the DNS server answers with
// the operator's address. Wildcard entries are excluded because there is no
// finite list of names under them.
func RedirectedNames() []string {
	var out []string
	for _, h := range Hosts {
		if h.Wildcard {
			continue
		}
		switch h.Service {
		case ServiceBlackhole, ServicePassthrough:
			continue
		}
		out = append(out, h.Pattern)
	}
	return out
}
