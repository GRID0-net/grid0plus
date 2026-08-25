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

	// Wildcard is true if Pattern is a DNS-MITM pattern rather than a literal
	// hostname. Wildcard entries are skipped when generating certificate SANs
	// and nginx server names unless an explicit TLSNames list is given.
	Wildcard bool

	// TLSNames are the concrete hostnames to put in the certificate and in
	// nginx for a wildcard entry. A DNS wildcard SAN such as "*.baas.nintendo.com"
	// only matches one label, so multi-label hosts have to be listed out.
	TLSNames []string
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
		Service: ServiceBAAS, TLS: true,
		Comment: "BAAS accounts (21.0.0 and later)",
	},
	{
		Pattern: "cdn-image-e0d67c509fb203858ebcb2fe3f88c2aa.baas.nintendo.com",
		Service: ServiceBAAS, TLS: true,
		Comment: "BAAS profile image CDN",
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
		Pattern: "val.hac.lp1.penne.srv.nintendo.net",
		Service: ServiceStubs, TLS: true,
		Comment: "push notifications (Penne) validation",
	},
	{
		Pattern: "consumer.lp1.npns.srv.nintendo.net",
		Service: ServiceStubs, TLS: true,
		Comment: "push notifications (NPNS)",
	},
	{
		Pattern: "service-status-lp1.cdn.nintendo.net",
		Service: ServiceStubs, TLS: true,
		Comment: "game server availability — always reports everything up",
	},
	{
		Pattern: "api-lp1.znc.srv.nintendo.net",
		Service: ServiceStubs, TLS: true,
		Comment: "smartphone app API (Coral)",
	},

	// ---- NPLN (phase 2) -------------------------------------------------
	// The tenant hostname is <tenant id>.lp1.t.npln.srv.nintendo.net. The
	// Splatoon 3 tenant is t-dce9377b-lp1. gamesync.npln.nintendo.net does not
	// end in srv.nintendo.net, so a *srv.nintendo.net wildcard misses it.
	{
		Pattern: "t-dce9377b-lp1.lp1.t.npln.srv.nintendo.net",
		Service: ServiceNPLN, TLS: true,
		Comment: "NPLN tenant: Splatoon 3",
	},
	{
		Pattern: "gamesync.npln.nintendo.net",
		Service: ServiceNPLN, TLS: true,
		Comment: "NPLN game session host (Splatoon 3 lobby)",
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

	// ---- Deliberately NOT redirected ------------------------------------
	// The d4c hosts serve system updates. Do not null-route them: the nim
	// sysmodule stores a persistent flag when the update check fails, and that
	// flag survives switching back to Nintendo's servers.
	{
		Pattern: "*.d4c.nintendo.net",
		Service: ServicePassthrough, Wildcard: true,
		Comment: "system updates — must reach real Nintendo, do NOT null-route",
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
var ProxiedServices = []Service{ServiceDAuth, ServiceAAuth, ServiceBAAS, ServiceNPLN, ServiceStubs}
