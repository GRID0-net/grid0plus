// Package nintendo holds the list of Nintendo hostnames that SwitchNet
// replaces, together with which SwitchNet service serves each one.
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

// Service identifies which SwitchNet backend serves a host.
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
	// ServiceEShop is the eShop: the storefront, the commerce API, the eLicense
	// server and the content server (phase 3).
	ServiceEShop Service = "eshop"
	// ServiceBlackhole means the host should be null-routed on the console
	// rather than answered. Used for telemetry.
	ServiceBlackhole Service = "blackhole"
	// ServicePassthrough means the host must keep resolving to real Nintendo.
	// Redirecting it causes more harm than good.
	ServicePassthrough Service = "passthrough"
)

// Host is one Nintendo hostname (or DNS-MITM wildcard pattern) and the
// SwitchNet service that answers it.
type Host struct {
	// Pattern is the hostname, or an Atmosphère DNS-MITM wildcard pattern.
	// In Atmosphère's hosts file, '*' matches zero or more characters and '%'
	// expands to the environment ("lp1"). Wildcards are only valid in the
	// console-side hosts file, never in a TLS SAN or an nginx server_name.
	Pattern string

	// Service is the SwitchNet backend that answers this host.
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
	// CertificateRequest to. Captured 2026-08-25: with ssl_verify_client
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
		Pattern: "e0d67c509fb203858ebcb2fe3f88c2aa.baas.nintendo.com",
		Service: ServiceBAAS, TLS: true,
		Comment: "BAAS lp1 — accounts and friends",
	},
	{
		Pattern: "m-lp1.baas.nintendo.com",
		Service: ServiceBAAS, TLS: true, NoHTTP2: true,
		Comment: "BAAS accounts (21.0.0 and later)",
	},
	{
		Pattern: "cdn-image-e0d67c509fb203858ebcb2fe3f88c2aa.baas.nintendo.com",
		Service: ServiceBAAS, TLS: true, NoHTTP2: true,
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
		Pattern: "storage.lp1.scsi.srv.nintendo.net",
		Service: ServiceStubs, TLS: true,
		Comment: "online save storage",
	},
	{
		Pattern: "god.hac.lp1.penne.srv.nintendo.net",
		Service: ServiceStubs, TLS: true,
		Comment: "push notifications (Penne) discovery",
	},
	{
		Pattern: "consumer.lp1.npns.srv.nintendo.net",
		Service: ServiceStubs, TLS: true,
		Comment: "push notifications (NPNS)",
	},
	{
		Pattern: "beach.hac.lp1.eshop.nintendo.net",
		Service: ServiceStubs, TLS: true,
		Comment: "eshop recommendations/metadata, purpose unconfirmed — not previously " +
			"listed at all, so it fell through to the domain-suffix blackhole " +
			"fallback. A 2026-08-25 capture showed the Nintendo Account link flow " +
			"reaches this host before accounts.nintendo.com on a real working " +
			"private server; that server passes it through to real Nintendo, which " +
			"this deployment's policy rules out, so it is stubbed instead. " +
			"Untested; see docs/CAPTURES.",
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
			"phone app -- likely the SplatNet 3 web view integration. Request " +
			"shape unconfirmed.",
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
		Comment: "OLSC's own host -- title 010000000000003E, decompiled 2026-09-20, " +
			"implements nn::olsc (\"Online Save Cloud\", the OS-level per-game save " +
			"backup tied to a Nintendo Switch Online subscription -- system-wide, " +
			"not the same thing as any one game's own cloud-save RPCs, and not " +
			"currently documented or planned anywhere in this project). No scsi " +
			"string anywhere in this binary, so this -- not " +
			"storage.lp1.scsi.srv.nintendo.net above -- looks like OLSC's actual " +
			"backend. Referenced as \"...hac.%.sata.srv.nintendo.net\"; the wildcard " +
			"is Nintendo's own environment placeholder (compare " +
			"receive-%.dg.srv.nintendo.net above), but there may be another " +
			"variable segment before \"hac\" this pattern does not capture -- the " +
			"DNS suffix match only needs the fixed tail, so a narrower prefix guess " +
			"was not worth making. Implementing real save backup is out of scope " +
			"the same way CloudSave.GetSaveRecord's neighbours are (ROADMAP.md " +
			"phase 2.4): it would mean storing real player save data this server " +
			"has no legitimate way to have. Stubbed, not designed against.",
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

	// ---- Telemetry: null-route ------------------------------------------
	// Redirecting these to our own server would still tell Nintendo nothing,
	// but it would fill our logs with crash and play reports we have no use
	// for. Null-routing them is what Prelude-Nro does.
	{
		Pattern: "receive-%.dg.srv.nintendo.net",
		Service: ServiceBlackhole, Wildcard: true,
		Comment: "play reports",
	},
	{
		Pattern: "receive-%.er.srv.nintendo.net",
		Service: ServiceBlackhole, Wildcard: true,
		Comment: "error reports",
	},

	// ---- BCAT: null-route, not passthrough -------------------------------
	// Observed 2026-08-26 reaching a real Nintendo IP (88.221.83.x)
	// during a real-hardware capture: it was not in this list at all, so with
	// add_defaults_to_dns_hosts off it fell through to ordinary upstream DNS
	// instead of being redirected or null-routed. That is a violation of this
	// deployment's isolation policy (see CLAUDE.md and the
	// emuMMC-never-talks-to-real-Nintendo rule) -- not a feature gap, since it
	// is not needed for Phase 1: background content-delivery discovery.
	//
	// Null-routed rather than stubbed, and this is CONTINGENT rather than
	// settled. Null-routing fails immediately, which is the right answer while
	// SwitchNet cannot answer these usefully -- redirecting to a server that
	// responds without delivering anything risks turning an immediate failure
	// into a wait.
	//
	// TODO.md §2.4's experiment needs them flipped to ServiceStubs, where
	// internal/bcat.Probe is already waiting: it logs every request and answers
	// 200 with an empty body, which is how anybody first learns what the console
	// asks BCAT for. Flipping them means amending
	// TestBlackholedHostsAreAccountedFor too -- deliberately, in a commit that
	// says why. It needs a console, so it is the operator's to run.
	{
		Pattern: "bcat-list-lp1.cdn.nintendo.net",
		Service: ServiceBlackhole,
		Comment: "BCAT background content delivery, list discovery",
	},
	{
		Pattern: "bcat-data-lp1.cdn.nintendo.net",
		Service: ServiceBlackhole,
		Comment: "BCAT background content delivery, data",
	},
	{
		Pattern: "bcat-topics-lp1.cdn.nintendo.net",
		Service: ServiceBlackhole,
		Comment: "BCAT background content delivery, topics",
	},

	// ---- eShop (phase 3) ------------------------------------------------
	// Null-routed on purpose. Nothing exists behind these yet, and Phase 1 has
	// no use for them at all, so there is no reason for a console pointed at
	// SwitchNet to ever reach real Nintendo through them. Phase 3 flips these to
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
	// not SwitchNet.
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
	// advice for a console that moves back and forth between SwitchNet and
	// real Nintendo is to leave these passthrough: the nim sysmodule stores a
	// persistent flag when the update check fails, and that flag survives
	// switching DNS back to Nintendo's servers, which is a genuinely
	// expensive mistake on that kind of setup.
	//
	// It does not apply here. This host list is generated for an emuMMC that
	// is permanently and exclusively SwitchNet's — it never points at real
	// Nintendo again, so there is no "switching back" for the flag to
	// survive into. Given that, passthrough only means one thing: real
	// Nintendo traffic leaving this console for no benefit. Null-routed
	// instead, consistent with every other host nothing is built for.
	//
	// If you ever reuse this host list for a console that DOES move back and
	// forth between SwitchNet and real Nintendo, put these back to
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
	ServiceDAuth, ServiceAAuth, ServiceBAAS,
	ServiceNPLNRest, ServiceNPLN, ServiceGamesync, ServiceStubs,
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
		// Not something SwitchNet explicitly handles. The default is to forward
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
