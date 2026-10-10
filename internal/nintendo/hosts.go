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
	// ServiceNPLN is the NPLN gRPC gateway (phase 2).
	ServiceNPLN Service = "npln"
	// ServiceGamesync is NPLN's document/watch service (phase 2).
	//
	// Separate from ServiceNPLN for one reason that matters: the console reaches
	// it on PORT 7575, not 443. Grouping it with the tenant host would emit one
	// nginx block on the edge port that grpc_passes both to the gateway --
	// which answers, wrongly, with a service that does not implement gamesync's
	// RPCs. That failure looks like the game being unhappy with our gamesync
	// implementation rather than like a routing mistake.
	ServiceGamesync Service = "gamesync"
	// ServiceNPLNRest is NPLN's JSON front end (phase 2): the hosts Splatoon 3
	// talks to over ordinary HTTPS *before* it opens a gRPC channel.
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
	// server and the content server (phase 3).
	ServiceEShop Service = "eshop"
	// ServiceSCSI is the OLSC (Online Save Cloud) cloud-save server (phase 4):
	// storage.hac.lp1.scsi.srv.nintendo.net's save data archives and
	// permission.lp1.sata.srv.nintendo.net's save data sets / key package.
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
	// CertificateRequest to. Two hosts need it, for two different clients
	// that fail on one: accounts.nintendo.com (the browser applet, below) and
	// the friends host (the friends sysmodule, see its entry). Captured 2026-08-25: with ssl_verify_client
	// optional_no_ca (needed by dauth/aauth/baas to read the console's device
	// certificate), the TLS handshake for accounts.nintendo.com negotiated
	// fine up through the server's certificate flight, then the console's
	// browser-applet client went silent and closed the connection after
	// ~600ms without ever sending its Finished message — the "loads forever"
	// symptom on the Nintendo Account link page. The curl-based device-auth
	// clients (dauth, aauth, the rest of baas) handle the same
	// CertificateRequest fine by answering with an empty certificate; the
	// system browser's separate TLS stack does not. accounts.nintendo.com is
	// only ever reached by that browser, never by a device-auth client, so it
	// gets its own nginx server block with no client-cert request at all.
	NoClientCertRequest bool

	// NoHTTP2 is true for a host nginx must not offer HTTP/2 to, independent
	// of NoClientCertRequest — m-lp1.baas.nintendo.com needs the client-cert
	// request (it is real device-auth traffic, "system-level account state
	// synchronization", not the browser applet) but shows the exact same
	// signature that led to disabling HTTP/2 for accounts.nintendo.com:
	// a real, sizeable, bidirectional TLS exchange that nginx never logs as
	// a completed HTTP request. Observed 2026-08-26 only against real nginx
	// (http2 on by default here), never once across three separate decrypted
	// captures taken through a hand-rolled mitmproxy stand-in that never
	// offered HTTP/2 at all — the strongest evidence yet that this
	// connection is the same HTTP/2 interop failure, on a different host.
	NoHTTP2 bool
}

// Hosts is the redirect list, in the order it should be written to the
// console's hosts file.
//
// Ordering matters: Atmosphère's DNS-MITM resolver applies the LAST matching
// line, so broad wildcards come first and specific overrides come last. This
// ordering rule is the one Prelude-Nro relies on in production.
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
		// No client-cert request: this is the friends sysmodule's host, and
		// friends does not cope with one the way nnAccount does. A 2026-09-23
		// tcpdump of a friend-list refresh showed a clean TLS 1.3 handshake
		// to this SNI, then the console aborting in its own final flight --
		// nginx: "client closed connection while waiting for request" -- and
		// the applet showing 2121-1058. The friends binary's curl-error table
		// (0x121608) maps 2121-1058 to CURLE_SSL_CERTPROBLEM: asked for a
		// certificate, friends tries to present one and cannot. Nothing on
		// this host reads a client certificate (only dauth does), so there is
		// nothing to lose by not asking. Implies HTTP/1.1, see NoHTTP2.
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
		// No client-cert request for the same reason as the friends host
		// above: the friends sysmodule downloads the thumbnails, tries to
		// answer the CertificateRequest and aborts its own handshake. No
		// console fetch of /1/<image> ever reached nginx while emulators,
		// which ignore the request, fetched every one. Nothing here reads
		// a client certificate.
		//
		// Never individually verified against a real capture until
		// 2026-08-28: it was running nginx's default HTTP/2-on variant, the
		// exact configuration m-lp1.baas.nintendo.com needed NoHTTP2 for. The
		// console's image fetch is system-level account-data traffic, not
		// the browser applet, so it gets that host's treatment, not
		// accounts.nintendo.com's.
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
		Comment: "GET /2.0.0/users/me -- called by nnAccount (libcurl) right after the " +
			"NA-link token exchange, not by the browser, so it keeps the usual " +
			"optional_no_ca client-cert request. Missing entirely until a " +
			"2026-08-26 mitmproxy capture of a real first-time link against " +
			"Nextendo showed the console calling it and expecting a real profile " +
			"body back. See internal/baas/nalink.go",
	},

	// ---- NAT check ------------------------------------------------------
	// Pia requires nncs1 and nncs2 to be two DISTINCT addresses. If they
	// resolve to the same IP the console de-duplicates the probe, never sends
	// the second one, and NAT detection never completes. Prelude-Nro hit this
	// as Mario Kart 8 error 2618-201.
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

	// ---- Found 2026-09-20: Pia game-session host, previously invisible --
	// A decompile of Splatoon 3 v11.3.0 found a second host family neither
	// this file nor any capture had named: "g2122d301.<region>.p.srv."
	// nintendo.net", built from Splatoon 3's own Pia game code
	// ("g2122d301", the literal fallback in the function that constructs
	// this string) rather than a shared prefix like nncs1/nncs2's. It was
	// not leaking anywhere -- isNintendoOwnedDomain's catch-all already
	// blackholed it like any unlisted *.nintendo.net host -- but it had no
	// name here, so a real console reaching it would have produced no log
	// line to find it by, the same invisibility problem the sect/sata/
	// sprofile hosts had before their own entries were added.
	//
	// AllocateIceServerSet's own doc comment already establishes gameplay
	// itself is ICE-based, not Pia-hosted, so this is unlikely to be on the
	// connection-critical path -- but "unlikely" is a reading of one other
	// function's behavior, not confirmation this host is dead. Stubbed
	// rather than blackholed specifically, so if a console does reach it,
	// what it asks for gets recorded instead of silently discarded.
	{
		Pattern: "%.p.srv.nintendo.net",
		Service: ServiceStubs, Wildcard: true, TLS: true,
		Comment: "Splatoon 3's own Pia game-session host (game code " +
			"g2122d301) — purpose and whether it is ever actually reached " +
			"are both TODO(capture-needed)",
	},

	// ---- Found 2026-09-20: parental controls (nsd's own decompile) --------
	// nsd (title 010000000000002E, the OS's own Network Server Discovery
	// resolver) references exactly two hosts in its own code: this one and
	// moon.nintendo.com below. Out of scope for a personal/private-network
	// deployment the same way Phase 3's eShop is -- nobody is enforcing
	// parental restrictions against themselves -- but both were previously
	// invisible on the generic catch-all, same reasoning as the Pia entry
	// above.
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

	// ---- Found 2026-09-20: a second, unrelated "vermillion" ---------------
	// A decompile of nim.nso (the system update/install module) found
	// "vermillionDeviceId" used throughout a local nearby-console-discovery
	// mechanism -- $.candidates[*].vermillionDeviceIds[*], $.nearnessChecks[*]
	// .srcDevice.vermillionDeviceId, /v1/devices/vermillion-device-id -- for
	// what reads as console-to-console data transfer during setup, not
	// anything NPLN-related. Nintendo reused the "vermillion" codename for
	// two unrelated systems: this one, and internal/nplnrest's
	// gw.hac.lp1.vermillion.srv.nintendo.net device-init host. They are NOT
	// the same thing, and this decompile settles nothing about nplnrest's
	// own vermillion TODO -- flagging the collision here specifically so a
	// future reader does not conflate the two.
	//
	// The one concretely new fact: nim.nso reports failures from this
	// mechanism to a host neither this file nor any capture had named
	// ("vermillion-app-call-failed" is one of the literal error strings
	// alongside it). Out of Phase 1/2 scope (console-to-console transfer,
	// not accounts or Splatoon 3), stubbed for visibility rather than left
	// invisible on the generic catch-all like the Pia host above.
	{
		Pattern: "problem-details.ennovate.srv.nintendo.net",
		Service: ServiceStubs, TLS: true,
		Comment: "nim's nearby-device-transfer error reporting (path seen: " +
			"/vermillion-gw/) — unrelated to NPLN despite the shared codename; " +
			"see the comment above this entry",
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
		// Phase 4: OLSC's real save-data-archive API. Full field-level shape
		// confirmed at https://github.com/kinnay/NintendoClients/wiki/
		// OLSC-Servers -- "GET /api/nx/v2/save_data_archives" and its
		// siblings (start_upload, finish_upload, generate_key_seed_package,
		// extend_upload_timeout) are all documented there, the same wiki
		// this project already leans on for BAAS/dAuth/aAuth. See
		// internal/scsi/README.md for exactly which endpoints are that
		// confirmed versus this project's own defensible reading of an
		// undocumented one.
		Pattern: "storage.hac.lp1.scsi.srv.nintendo.net",
		Service: ServiceSCSI, TLS: true,
		Comment: "OLSC save data archives (phase 4)",
	},
	{
		Pattern: "%.scsi.srv.nintendo.net",
		Service: ServiceStubs, Wildcard: true, TLS: true,
		Comment: "found 2026-09-20, decompiling title 010000000000003A (nifm, " +
			"\"nn::nifm::\" -- Network Interface Manager, not anything scsi/" +
			"storage-related despite the host name): " +
			"\"migration.%.scsi.srv.nintendo.net/api/nx/v1/account_migrations/" +
			"{gen_key,get_key}\" -- Nintendo's console-to-console account " +
			"migration flow. Genuinely account-adjacent (unlike Pia/pctl/moon " +
			"above), but implementing real migration is a different, much " +
			"larger feature than anything Phase 1 currently attempts; stubbed " +
			"for visibility only. Overlaps storage.lp1.scsi... above only in " +
			"domain, not host -- covered by the wildcard rather than folded into " +
			"that entry since the subdomain differs.",
	},
	{
		Pattern: "god.hac.lp1.penne.srv.nintendo.net",
		Service: ServiceNPLNRest, TLS: true,
		Comment: "push notifications (Penne) accounts. npns registers here for " +
			"its penne id and the notification token the Nintendo Account link " +
			"then PUTs to /1.0.0/push_channels; internal/nplnrest remembers whose " +
			"token it is, which is what lets a push reach the console's frontline",
	},
	{
		Pattern: "consumer.lp1.npns.srv.nintendo.net",
		Service: ServiceStubs, TLS: true,
		Comment: "push notifications (NPNS)",
	},
	{
		Pattern: "beach.hac.lp1.eshop.nintendo.net",
		Service: ServiceStubs, TLS: true,
		Comment: "A 2026-08-25 capture showed the Nintendo Account link flow reaches " +
			"this host before accounts.nintendo.com; a real working private server " +
			"passes it through to real Nintendo, which this deployment's policy " +
			"rules out, so it is stubbed instead. A second, unrelated purpose: " +
			"NextendoNetwork's own public Splatoon 3 NPLN server (facts taken from " +
			"their published findings, never their code -- see " +
			"internal/nplnrest.go's package doc comment) documents this host also " +
			"receiving \"POST /v1/devices/<id>/penne_id/register\" during NPLN's " +
			"vermillion/penne bootstrap, which their real-console capture found " +
			"had to succeed before the console would ever attempt a penne login. " +
			"The generic empty-object answer every unmatched host on this server " +
			"already gives (internal/stubs's default case) happens to match their " +
			"own -- independently arrived at -- answer for this exact path, so no " +
			"code change was needed, only this note that the path is real and " +
			"already handled correctly.",
	},
	{
		Pattern: "service-status-lp1.cdn.nintendo.net",
		Service: ServiceStubs, TLS: true,
		Comment: "game server availability — always reports everything up",
	},
	{
		Pattern: "api-lp1.znc.srv.nintendo.net",
		Service: ServiceStubs, TLS: true,
		Comment: "smartphone app API (Coral). A 2026-09-20 decompile of " +
			"Splatoon 3 v11.3.0 confirms the game itself constructs " +
			"api-<region>.znc.srv.nintendo.net URLs directly, not just the " +
			"phone app. A separate decompile of a system NSO-companion applet " +
			"found a distinct \"web-%.znc.srv.nintendo.net\" host (below) -- " +
			"likely the actual SplatNet 3 web-view backend, with this api- " +
			"host doing structured API calls instead. Request shape for both " +
			"is unconfirmed.",
	},
	{
		Pattern: "%.znc.srv.nintendo.net",
		Service: ServiceStubs, Wildcard: true, TLS: true,
		Comment: "web- prefix confirmed by decompile (see api-lp1.znc above); " +
			"wildcarded rather than a literal web-lp1 entry since the region " +
			"code was seen as \"%\" (unsubstituted) in the binary, not a " +
			"specific literal",
	},

	// ---- Found 2026-09-20: not previously listed at all -----------------
	// A full sweep of every system title's decrypted executable (not just
	// the account/friends/npns modules phase 1 already needed) for
	// *.nintendo.net/*.nintendo.com strings turned up three hosts with no
	// entry anywhere in this file. None leaked to real Nintendo regardless
	// -- isNintendoOwnedDomain's catch-all already blackholes any unlisted
	// *.nintendo.net host -- but they were invisible to anyone reading this
	// list, and the catch-all doesn't answer requests, only refuses
	// connections. Stubbed rather than left to the catch-all for the same
	// reason as every other purpose-unconfirmed host here: an answer risks
	// less than a refusal does. Purpose unconfirmed for all three; no
	// capture.
	{
		Pattern: "api.sect.srv.nintendo.net",
		Service: ServiceStubs, TLS: true,
		Comment: "title 0100000000000012, decompiled 2026-09-20: implements " +
			"nn::anif (\"Assigned Network Interface\" -- Assign/GetUserInfo/" +
			"GetStateChangedEvent/AddSession), connected to dns:priv, nsd and " +
			"ifcfg. Network interface/connection-profile assignment, not account " +
			"settings sync as the bare path (/v1/setting) first suggested -- " +
			"corrected after actually decompiling it rather than guessing from " +
			"the endpoint name alone.",
	},
	{
		Pattern: "%.sata.srv.nintendo.net",
		Service: ServiceStubs, Wildcard: true, TLS: true,
		Comment: "OLSC's own host family -- title 010000000000003E, decompiled " +
			"2026-09-20, implements nn::olsc (\"Online Save Cloud\", the OS-level " +
			"per-game save backup tied to a Nintendo Switch Online subscription -- " +
			"system-wide, not the same thing as any one game's own cloud-save " +
			"RPCs). Phase 4 (docs/ROADMAP.md) now implements the two specific " +
			"hosts under this family with a confirmed shape -- see the literal " +
			"permission.lp1.sata.srv.nintendo.net override just below, which MUST " +
			"stay after this wildcard: Atmosphere applies the last matching line, " +
			"and this broad entry would otherwise win back over it. Everything " +
			"else under this wildcard remains stubbed, including a second, " +
			"unrelated purpose found the same night in qlaunch's own decompile: " +
			"\"pp.%.sata.srv.nintendo.net/noa/<locale>.html\" (\"pp\" almost " +
			"certainly privacy policy; en_US/fr_CA/es_LA/pt_BR/de_DE/en_GB locale " +
			"pages all referenced) -- matched generically by this same wildcard " +
			"(matchesSuffix matches any subdomain depth), so no pattern change was " +
			"needed for it, just this note that the family serves more than one " +
			"purpose. Referenced as \"...hac.%.sata.srv.nintendo.net\"; the " +
			"wildcard is Nintendo's own environment placeholder (compare " +
			"receive-%.dg.srv.nintendo.net above), but there may be another " +
			"variable segment before \"hac\" this pattern does not capture -- the " +
			"DNS suffix match only needs the fixed tail, so a narrower prefix guess " +
			"was not worth making.",
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
		Comment: "OLSC save data sets / key package (phase 4)",
	},
	{
		Pattern: "%.sprofile.srv.nintendo.net",
		Service: ServiceStubs, Wildcard: true, TLS: true,
		Comment: "same title (010000000000003E) as sata above, which also implements " +
			"nn::sprofile (ISProfileBgAgentForSystemProcess, IBgAgentContoller) " +
			"alongside olsc. Full path referenced: /v1/device/metadata -- sounds " +
			"like device-level analytics/telemetry given the adjacency to erpt's " +
			"own sprofile interfaces (see erpt's own title), which would argue for " +
			"null-routing like the telemetry hosts below instead of stubbing -- but " +
			"that is still a guess, not a confirmed read, so it gets the same " +
			"conservative default as sata and sect until a capture says otherwise.",
	},

	// ---- NPLN, the JSON front (phase 2) ---------------------------------
	// Splatoon 3 does NOT open with gRPC. It first talks ordinary JSON over
	// HTTPS to vermillion and penne, and only then opens the gRPC channel to
	// the tenant host below. Establishing that reclassified three hosts that
	// were listed here as push notifications and telemetry.
	//
	// These are ServiceNPLNRest, not ServiceNPLN, because they need proxy_pass
	// and HTTP/1.1 rather than grpc_pass and HTTP/2 -- see the ServiceNPLNRest
	// doc comment for what happens when the two are swapped.
	{
		Pattern: "gw.hac.lp1.vermillion.srv.nintendo.net",
		Service: ServiceNPLNRest, TLS: true,
		Comment: "NPLN device initialisation and account config. Was listed as " +
			"'background game registration/telemetry, purpose unconfirmed' and " +
			"stubbed. That also explains the open question recorded against it: a " +
			"2026-08-27 tcpdump of a real, successful Nintendo Account import " +
			"showed 16.5KB exchanged here mid-flow, 'not the negligible amount a " +
			"background-only contact would suggest'. It is a device-init handshake " +
			"that expects real answers, which is why a stub did not satisfy it. " +
			"Request and response bodies are still TODO(capture-needed) -- see " +
			"internal/nplnrest",
	},
	{
		Pattern: "val.hac.lp1.penne.srv.nintendo.net",
		Service: ServiceNPLNRest, TLS: true,
		Comment: "NPLN login tickets, and the frontline FQDN the console is told " +
			"to open its persistent connection to. Was listed as 'push " +
			"notifications (Penne) validation'. The path is very likely " +
			"POST /v1/login_tickets: found 2026-09-20 in a decompile of the " +
			"operator's own console's npns sysmodule, which shares this exact " +
			"ticket-then-frontline mechanism (confirmed by the shared field names " +
			"-- see internal/nplnrest.go's penne() for the full citation) but is " +
			"not itself an NPLN host, so this is strong circumstantial evidence " +
			"for the path here rather than a direct capture of it.",
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
		Comment: "NPLN persistent frontline connection. The console holds this " +
			"open and treats a close as a failure (2307-2103 on Splatoon 2, " +
			"2321-* on Splatoon 3), so it is a long-lived streaming response, not " +
			"a request/response endpoint. Was listed as 'push notifications " +
			"(Penne), purpose unconfirmed'",
	},

	// ---- NPLN, the gRPC tenant (phase 2) --------------------------------
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
	// g<game server id>-lp1.s.n.srv.nintendo.net, port 443, a WebSocket
	// (Kinnay, NEX Overview). Both halves of the game server -- login and
	// play -- are on this one name; see internal/rendezvous.
	//
	// NoClientCertRequest and NoHTTP2 follow the only server Splatoon 2 is
	// known to work against: NextendoNetwork's (facts only) never sends a
	// CertificateRequest and offers only http/1.1 in ALPN, because a
	// WebSocket upgrade is an HTTP/1.1 exchange.
	{
		Pattern: "g2df33d01-lp1.s.n.srv.nintendo.net",
		Service: ServiceNEX, TLS: true, NoClientCertRequest: true, NoHTTP2: true,
		Comment: "NEX game server: Splatoon 2 (game server 2DF33D01)",
	},

	// ---- Telemetry: redirected to a sink, not null-routed -----------------
	// Used to be null-routed, reasoned as "tells Nintendo nothing either
	// way, and this way it does not fill our logs" -- and reasoned, wrongly,
	// that null-routing them is what Prelude-Nro does too. Checked against
	// Prelude's actual current source rather than assumed: it does not.
	// Prelude's own commit history (facts only, not its code) documents a
	// real hardware measurement: the NSO/friends applet
	// (0100000000000816) waits SYNCHRONOUSLY for an HTTP response from
	// these two hosts before it loads its own main module (lp1.nso).
	// Null-routed, the connection hangs until timeout and the applet gives
	// up without ever reaching lp1.nso -- no profile icon handling, and
	// plausibly more of the applet than that, never initialises. Their fix
	// is a sink: redirect both hosts to a server that reads the body,
	// discards it, and answers 200 immediately. Telemetry still never
	// reaches real Nintendo -- only the synchronous wait the applet blocks
	// on is satisfied. See internal/stubs.telemetrySink.
	// A 2026-09-24 Ethernet trace showed receive-lp1.dg complete TLS then
	// close before sending HTTP, matching bcat-topics below. Neither service
	// uses a client certificate here, so nginx must not request one.
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
	// Observed 2026-08-26 reaching a real Nintendo IP (88.221.83.x)
	// during a real-hardware capture: it was not in this list at all, so with
	// add_defaults_to_dns_hosts off it fell through to ordinary upstream DNS
	// instead of being redirected or null-routed. That is a violation of this
	// deployment's isolation policy (see CLAUDE.md and the
	// emuMMC-never-talks-to-real-Nintendo rule).
	//
	// These three used to be null-routed, reasoned as the safe choice while
	// GRID0+ cannot answer BCAT usefully. That reasoning turned out to be
	// wrong: NextendoNetwork/Prelude's own commit history (facts only, not
	// its code) documents a real, hard failure from exactly this choice --
	// Splatoon 3 raised 2122-2403 (module 122 = bcat) when bcat-list-lp1 was
	// unreachable, not silence. Their fix, applied to all three hosts, is a
	// generic 304 Not Modified on every request: the console reads "your
	// cache is current" and falls back to whatever it already has locally.
	// This sends no data -- a player with nothing in their BCAT cache still
	// gets nothing -- it only stops that absence from being a hard error.
	// Reproduced independently on GRID0+'s own hardware on 2026-09-24 for
	// bcat-topics-lp1 specifically (see internal/stubs.newsTopics): a real
	// capture against Nextendo showed the identical 304-always shape, and
	// null-routing it made the console's very first boot request fail
	// outright. list/data get the same answer for the same reason, not
	// because their own failure mode has been separately reproduced here.
	//
	// internal/bcat.Probe (TODO.md §2.4's actual open question -- whether
	// GRID0+ could serve real delivery-cache content, not just "nothing
	// changed") is unaffected: it is a diagnostic tool for an operator to
	// reach for deliberately, not what these hosts answer with day to day.
	// The 2026-09-24 Ethernet capture proved the topics host was reached but
	// its TLS connection closed before HTTP; nginx had asked for a client
	// certificate. Do not issue CertificateRequest on these CDN hosts.
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

	// ---- eShop (phase 3) ------------------------------------------------
	// Null-routed on purpose. Nothing exists behind these yet, and Phase 1 has
	// no use for them at all, so there is no reason for a console pointed at
	// GRID0+ to ever reach real Nintendo through them. Phase 3 flips these to
	// ServiceEShop once the servers behind them exist. Having them here means
	// the next person to work on phase 3 finds the list, and does not add
	// `atumn` or `aqua` to it by mistake -- see the d4c note below, which is the
	// one group in this file that stays different: those are used by background
	// update checks today and must keep reaching real Nintendo, or the console
	// ends up with a persistent broken-update flag that outlives switching back.
	//
	// If you want this console's eShop to keep working normally against real
	// Nintendo while only Phase 1 is in use, switch these back to
	// ServicePassthrough -- but that means this console's traffic for these
	// hosts, including the Nintendo Account sign-in flow, goes to real Nintendo,
	// not GRID0+.
	//
	// The storefront is a WEBSITE: the applet loads bugyo/ashigaru and renders
	// it, exposing system functions through a `window.nx` bridge. So phase 3 is
	// a web app plus a commerce API, not a native UI.
	// https://github.com/kinnay/NintendoClients/wiki/eShop-Applet-(Switch)
	{
		Pattern: "bugyo.hac.lp1.eshop.nintendo.net",
		Service: ServiceBlackhole,
		Comment: "phase 3: the eShop storefront (a website). Not built; null-routed rather than reaching real Nintendo",
	},
	{
		Pattern: "hac.lp1.savanna.srv.nintendo.net",
		Service: ServiceBlackhole,
		Comment: "phase 3: the newer eShop applet. Not built; null-routed rather than reaching real Nintendo",
	},
	{
		Pattern: "ecs-lp1.hac.shop.nintendo.net",
		Service: ServiceBlackhole,
		Comment: "phase 3: commerce (ECS). Not built; null-routed rather than reaching real Nintendo",
	},
	{
		Pattern: "ias-lp1.hac.shop.nintendo.net",
		Service: ServiceBlackhole,
		Comment: "phase 3: commerce (IAS). Not built; null-routed rather than reaching real Nintendo",
	},
	{
		Pattern: "atum.hac.lp1.d4c.nintendo.net",
		Service: ServiceBlackhole,
		Comment: "phase 3: GAME content, addressed by content id. NOT a system update host. Not built; null-routed rather than reaching real Nintendo",
	},
	{
		Pattern: "tagaya.hac.lp1.eshop.nintendo.net",
		Service: ServiceBlackhole,
		Comment: "phase 3: latest version per title. Not built; null-routed rather than reaching real Nintendo",
	},
	{
		Pattern: "idbe-hac.cdn.nintendo.net",
		Service: ServiceBlackhole,
		Comment: "phase 3: title icons. Not built; null-routed rather than reaching real Nintendo",
	},
	{
		Pattern: "cdn.accounts.nintendo.com",
		Service: ServiceBlackhole,
		Comment: "static assets for the Nintendo Account sign-in page — observed in a " +
			"2026-08-25 SNI capture, not previously listed. Not built; null-routed rather than reaching real Nintendo",
	},

	// ---- System updates: null-routed on this deployment -----------------
	// The d4c hosts serve SYSTEM UPDATES: sun, atumn and aqua. The general
	// advice for a console that moves back and forth between GRID0+ and
	// real Nintendo is to leave these passthrough: the nim sysmodule stores a
	// persistent flag when the update check fails, and that flag survives
	// switching DNS back to Nintendo's servers, which is a genuinely
	// expensive mistake on that kind of setup.
	//
	// It does not apply here. This host list is generated for an emuMMC that
	// is permanently and exclusively GRID0+'s — it never points at real
	// Nintendo again, so there is no "switching back" for the flag to
	// survive into. Given that, passthrough only means one thing: real
	// Nintendo traffic leaving this console for no benefit. Null-routed
	// instead, consistent with every other host nothing is built for.
	//
	// If you ever reuse this host list for a console that DOES move back and
	// forth between GRID0+ and real Nintendo, put these back to
	// ServicePassthrough first — check TestSystemUpdateHostsAreNullRouted for
	// the reasoning captured at the time this was changed.
	//
	// Note the trap: `atum` (game content, phase 3, listed above) and `atumn`
	// (system update content) differ by one letter and share the d4c domain.
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
		Comment: "the Nintendo Switch Online app's own backend (a friend/parental-control " +
			"whitelist-style API, by name). Found missing entirely -- not passthrough, " +
			"not blackholed, just absent -- in a 2026-09-24 Ethernet capture: the console " +
			"resolved it via real DNS and completed a full real TLS handshake with real " +
			"Nintendo's own infrastructure, exchanging application data, then closing " +
			"cleanly. That closes cleanly and is unrelated to any other issue investigated " +
			"that day (it completes well before the point where a separate, still-open " +
			"Splatoon 3 hall issue stalls) -- but it is still a real breach of this " +
			"deployment's own isolation rule: this emuMMC never talks to real Nintendo, " +
			"for anything, ever. Null-routed like every other host nothing is built for.",
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
// Sourced from the community-maintained "block everything Nintendo" hosts
// list (the kind Prelude/Nextendo-style tooling ships), not invented for this
// project — that list has had many more consoles' worth of traffic to find
// gaps in than this one has. A regional Nintendo domain not on this list is
// still possible; the two hosts that prompted this fallback
// (cdn.accounts.nintendo.com, fro-1.hac.lp1.penne.srv.nintendo.net) were
// found by a live capture, not by reasoning about the domain in advance, and
// more probably exist.
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
