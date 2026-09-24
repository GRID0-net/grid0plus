package nintendo

import (
	"strings"
	"testing"
)

// The host list generates three artefacts that have to agree exactly: the
// console's DNS-MITM hosts file, the nginx routing map, and the certificate
// SANs. These tests guard the invariants that keep them consistent, and the
// specific mistakes that are expensive to find on real hardware.

func TestEveryHostIsWellFormed(t *testing.T) {
	seen := map[string]bool{}
	for _, h := range Hosts {
		if h.Pattern == "" {
			t.Error("a host has an empty pattern")
			continue
		}
		if seen[h.Pattern] {
			// A duplicate pattern in a hosts file is not an error on the console
			// — the last one wins — but it means two entries disagree about where
			// a host should go, and only one of them takes effect.
			t.Errorf("%s appears twice", h.Pattern)
		}
		seen[h.Pattern] = true

		if h.Service == "" {
			t.Errorf("%s has no service", h.Pattern)
		}
		if h.Comment == "" {
			// The comment is emitted into the generated hosts file, which the
			// operator reads when something does not resolve.
			t.Errorf("%s has no comment", h.Pattern)
		}

		hasWildcard := strings.ContainsAny(h.Pattern, "*%")
		if hasWildcard != h.Wildcard {
			t.Errorf("%s: Wildcard is %v but the pattern %s a wildcard character",
				h.Pattern, h.Wildcard, map[bool]string{true: "contains", false: "does not contain"}[hasWildcard])
		}
	}
}

// TestNATCheckHasTwoHosts guards the single most expensive mistake in this
// stack: Pia needs nncs1 and nncs2 to be two distinct servers, and it silently
// gives up if they are not.
func TestNATCheckHasTwoHosts(t *testing.T) {
	hosts := ServiceHosts(ServiceNATCheck)
	if len(hosts) != 2 {
		t.Fatalf("got %d NAT-check hosts, want nncs1 and nncs2", len(hosts))
	}

	var haveOne, haveTwo bool
	for _, h := range hosts {
		if strings.HasPrefix(h.Pattern, "nncs1") {
			haveOne = true
		}
		if strings.HasPrefix(h.Pattern, "nncs2") {
			haveTwo = true
		}
		// The NAT-check protocol is UDP. Putting these behind the TLS edge would
		// send them somewhere that cannot answer them.
		if h.TLS {
			t.Errorf("%s is marked TLS; NAT check is a UDP protocol", h.Pattern)
		}
	}
	if !haveOne || !haveTwo {
		t.Error("both nncs1 and nncs2 must be listed, or Pia never sends its second probe")
	}
}

// TestSystemUpdateHostsAreNullRouted guards this deployment's specific choice
// for the d4c hosts.
//
// The general-purpose advice — keep these ServicePassthrough — is for a
// console that moves back and forth between SwitchNet and real Nintendo: a
// failed update check sets a persistent flag on the console that survives
// switching back, which is expensive to hit by accident. This host list is
// generated for an emuMMC that is permanently and exclusively SwitchNet's, so
// that cost never applies: there is no "switching back" for the flag to
// survive into, and passthrough would only mean real Nintendo traffic leaving
// the console for no benefit. Null-routed instead, consistent with every
// other host nothing is built for.
//
// If this host list is ever reused for a console that DOES move back and
// forth, put these back to ServicePassthrough first.
//
// The trap this still guards is that `atum` (game content, phase 3) and
// `atumn` (system update content) differ by one letter and share the d4c
// domain, so somebody implementing phase 3 could redirect the wrong one.
func TestSystemUpdateHostsAreNullRouted(t *testing.T) {
	updateHosts := map[string]bool{
		"sun.hac.lp1.d4c.nintendo.net":   false,
		"atumn.hac.lp1.d4c.nintendo.net": false,
		"aqua.hac.lp1.d4c.nintendo.net":  false,
	}

	for _, h := range Hosts {
		if _, isUpdateHost := updateHosts[h.Pattern]; isUpdateHost {
			updateHosts[h.Pattern] = true
			if h.Service != ServiceBlackhole {
				t.Errorf("%s is %s; this deployment's emuMMC never talks to real "+
					"Nintendo, so this should be ServiceBlackhole, not %s",
					h.Pattern, h.Service, h.Service)
			}
		}
	}

	for pattern, listed := range updateHosts {
		if !listed {
			t.Errorf("%s is not listed, so nothing documents that it must not be "+
				"redirected", pattern)
		}
	}
}

// TestPhase3HostsAreNotYetRedirected keeps a console pointed at SwitchNet from
// ever reaching real Nintendo through a host nothing is built for yet.
//
// The hosts are listed so that whoever implements phase 3 finds the list
// rather than assembling their own. They are null-routed rather than
// forwarded: nothing exists behind them, and Phase 1 has no use for them, so
// there is no reason for a console pointed at SwitchNet to ever complete a
// connection to real Nintendo through one of these. A host redirected to a
// SwitchNet service that does not exist would be worse — it breaks the
// console's eShop for no gain — which is why these are ServiceBlackhole,
// not ServiceRedirect: null-routing answers 0.0.0.0 and the console gives up
// immediately, rather than reaching either a broken local endpoint or real
// Nintendo.
func TestPhase3HostsAreNotYetRedirected(t *testing.T) {
	phase3 := []string{
		"bugyo.hac.lp1.eshop.nintendo.net",
		"ecs-lp1.hac.shop.nintendo.net",
		"ias-lp1.hac.shop.nintendo.net",
		"atum.hac.lp1.d4c.nintendo.net",
		"tagaya.hac.lp1.eshop.nintendo.net",
	}

	byPattern := map[string]Host{}
	for _, h := range Hosts {
		byPattern[h.Pattern] = h
	}

	for _, pattern := range phase3 {
		h, ok := byPattern[pattern]
		if !ok {
			t.Errorf("%s is not listed; phase 3 needs it and the list is where "+
				"somebody will look for it", pattern)
			continue
		}
		if h.Service != ServiceBlackhole {
			t.Errorf("%s is %s. If phase 3 now serves it, update this test and "+
				"switch it to ServiceEShop; if not, it must stay ServiceBlackhole, "+
				"so a console pointed at SwitchNet never reaches real Nintendo "+
				"through it.", pattern, h.Service)
		}
	}

	// atum and atumn are one letter apart and both under d4c. Assert they are
	// distinct entries, so a careless edit to one cannot silently cover the other.
	if _, ok := byPattern["atum.hac.lp1.d4c.nintendo.net"]; !ok {
		t.Error("atum (game content) is missing")
	}
	if _, ok := byPattern["atumn.hac.lp1.d4c.nintendo.net"]; !ok {
		t.Error("atumn (system update content) is missing")
	}
}

// TestBlackholedHostsAreAccountedFor guards the null-route list against
// silently growing to cover something that should be redirected instead:
// every blackholed host must be telemetry (never useful to us), a Phase 3
// host nothing is built for yet (covered by TestPhase3HostsAreNotYetRedirected),
// or a d4c system-update host null-routed under this deployment's
// permanently-isolated-emuMMC policy (covered by
// TestSystemUpdateHostsAreNullRouted).
func TestBlackholedHostsAreAccountedFor(t *testing.T) {
	accountedFor := map[string]bool{
		"bugyo.hac.lp1.eshop.nintendo.net":  true,
		"hac.lp1.savanna.srv.nintendo.net":  true,
		"ecs-lp1.hac.shop.nintendo.net":     true,
		"ias-lp1.hac.shop.nintendo.net":     true,
		"atum.hac.lp1.d4c.nintendo.net":     true,
		"tagaya.hac.lp1.eshop.nintendo.net": true,
		"idbe-hac.cdn.nintendo.net":         true,
		"sun.hac.lp1.d4c.nintendo.net":      true,
		"atumn.hac.lp1.d4c.nintendo.net":    true,
		"aqua.hac.lp1.d4c.nintendo.net":     true,
		"cdn.accounts.nintendo.com":         true,
		// All three bcat-* hosts moved out of this map 2026-09-24: none are
		// null-routed any more. See internal/stubs.newsTopics and
		// hosts.go's own "BCAT: answered with 304" section for why.
	}

	hosts := ServiceHosts(ServiceBlackhole)
	if len(hosts) == 0 {
		t.Fatal("no hosts are null-routed")
	}
	for _, h := range hosts {
		isTelemetry := strings.Contains(h.Pattern, ".dg.") || strings.Contains(h.Pattern, ".er.")
		if !isTelemetry && !accountedFor[h.Pattern] {
			t.Errorf("%s is null-routed but is not accounted for by any test — "+
				"is this the right service for it?", h.Pattern)
		}
	}
}

// TestTLSHostnamesAreUsableInACertificate is what keeps the generated
// certificate valid. A wildcard from the hosts file is a DNS-MITM pattern, not a
// DNS name, and putting one in a SAN produces a certificate that matches nothing.
func TestTLSHostnamesAreUsableInACertificate(t *testing.T) {
	names := TLSHostnames()
	if len(names) == 0 {
		t.Fatal("no TLS hostnames")
	}

	seen := map[string]bool{}
	for _, name := range names {
		if strings.ContainsAny(name, "*%") {
			t.Errorf("%q contains a DNS-MITM wildcard character and cannot go in "+
				"a certificate SAN", name)
		}
		if strings.Contains(name, " ") || strings.Contains(name, "/") {
			t.Errorf("%q is not a hostname", name)
		}
		if seen[name] {
			t.Errorf("%q appears twice in the SAN list", name)
		}
		seen[name] = true
	}

	// The hosts a console cannot do without.
	for _, required := range []string{
		"dauth-lp1.ndas.srv.nintendo.net",
		"dcert-lp1.ndas.srv.nintendo.net",
		"aauth-lp1.ndas.srv.nintendo.net",
		"acert-lp1.ndas.srv.nintendo.net",
		"e0d67c509fb203858ebcb2fe3f88c2aa.baas.nintendo.com",
		"m-lp1.baas.nintendo.com",
	} {
		if !seen[required] {
			t.Errorf("%s is missing from the certificate SANs", required)
		}
	}
}

func TestJWKSHostsAreRoutedToTheServerThatIssuesTheTokens(t *testing.T) {
	// A token's 'jku' points at a host the console must be able to fetch the key
	// from. dcert has to reach dauth and acert has to reach aauth, or the
	// console cannot verify a token it was just handed.
	routes := map[string]Service{}
	for _, h := range Hosts {
		routes[h.Pattern] = h.Service
	}

	if routes["dcert-lp1.ndas.srv.nintendo.net"] != ServiceDAuth {
		t.Error("dcert must be routed to the dauth server, which signs device tokens")
	}
	if routes["acert-lp1.ndas.srv.nintendo.net"] != ServiceAAuth {
		t.Error("acert must be routed to the aauth server, which signs application tokens")
	}
}

func TestBAASImageCDNIsRouted(t *testing.T) {
	// Uploaded profile pictures are served from this host. Without it, every
	// friend in the friends applet has a blank avatar and the console retries
	// the fetch on every draw.
	for _, h := range ServiceHosts(ServiceBAAS) {
		if strings.HasPrefix(h.Pattern, "cdn-image-") {
			if !h.TLS {
				t.Error("the image CDN host is not marked TLS")
			}
			return
		}
	}
	t.Error("the BAAS image CDN host is not listed")
}

// TestHostsFoundBySweepingEveryTitleAreStubbedNotJustCaughtByTheFallback
// guards the three hosts found 2026-09-20 by scanning every system title's
// decrypted executable, not just the ones phase 1 already needed. All three
// were already safe (isNintendoOwnedDomain blackholes any unlisted
// *.nintendo.net host), but invisible to this list and to a console that
// gets a refusal instead of an answer -- see the entries' own comments for
// what was actually found in each title.
func TestHostsFoundBySweepingEveryTitleAreStubbedNotJustCaughtByTheFallback(t *testing.T) {
	names := []string{
		"api.sect.srv.nintendo.net",
		"anything.hac.sata.srv.nintendo.net",
		"anything.sprofile.srv.nintendo.net",
	}
	for _, name := range names {
		resolution, host, matched := Resolve(name)
		if !matched {
			t.Errorf("Resolve(%s): matched = false, want true", name)
			continue
		}
		if resolution != ResolveRedirect || host.Service != ServiceStubs {
			t.Errorf("Resolve(%s) = %v, %v, want ResolveRedirect/ServiceStubs", name, resolution, host.Service)
		}
		if !host.TLS {
			t.Errorf("%s: TLS = false, want true", name)
		}
	}
}

// TestAccountsNintendoComIsRoutedToBAAS guards the Nintendo Account link
// stub's routing: without this, a console reaching accounts.nintendo.com
// gets null-routed instead of the login page in internal/baas/nalink.go.
func TestAccountsNintendoComIsRoutedToBAAS(t *testing.T) {
	for _, h := range ServiceHosts(ServiceBAAS) {
		if h.Pattern == "accounts.nintendo.com" {
			if !h.TLS {
				t.Error("accounts.nintendo.com is not marked TLS")
			}
			return
		}
	}
	t.Error("accounts.nintendo.com is not routed to ServiceBAAS")
}

func TestNPLNTenantForSplatoon3IsListed(t *testing.T) {
	var haveTenant bool
	for _, h := range ServiceHosts(ServiceNPLN) {
		if strings.HasPrefix(h.Pattern, "t-dce9377b-lp1.") {
			haveTenant = true
		}
	}
	if !haveTenant {
		t.Error("the Splatoon 3 NPLN tenant host is missing")
	}
}

// TestGamesyncIsItsOwnServiceOnItsOwnPort.
//
// gamesync is reached by the console on PORT 7575, not the edge port, and it is
// a different backend from the tenant gateway. Grouping it with ServiceNPLN
// emitted one nginx block on 443 that grpc_passed both to the gateway -- which
// answers, wrongly, with a service that implements none of gamesync's RPCs. That
// failure looks like the game disliking our gamesync implementation rather than
// like a routing mistake, which is why it is worth a test rather than a comment.
//
// It also does not end in srv.nintendo.net, so no broad wildcard covers it.
func TestGamesyncIsItsOwnServiceOnItsOwnPort(t *testing.T) {
	hosts := ServiceHosts(ServiceGamesync)
	if len(hosts) != 1 {
		t.Fatalf("ServiceGamesync has %d hosts, want 1", len(hosts))
	}
	if hosts[0].Pattern != "gamesync.npln.nintendo.net" {
		t.Errorf("gamesync host = %q", hosts[0].Pattern)
	}
	if hosts[0].TLSPort != 7575 {
		t.Errorf("TLSPort = %d, want 7575 — a block on the edge port never matches", hosts[0].TLSPort)
	}
	if !hosts[0].TLS {
		t.Error("gamesync is TLS, so it needs a certificate SAN and a server block")
	}

	for _, h := range ServiceHosts(ServiceNPLN) {
		if strings.HasPrefix(h.Pattern, "gamesync.") {
			t.Error("gamesync is still grouped with the tenant gateway")
		}
	}
}

func TestProxiedServicesAllHaveHosts(t *testing.T) {
	for _, svc := range ProxiedServices {
		if len(ServiceHosts(svc)) == 0 {
			t.Errorf("%s is in ProxiedServices but has no hosts", svc)
		}
	}
}

func TestServiceHostsReturnsOnlyThatService(t *testing.T) {
	for _, svc := range []Service{
		ServiceDAuth, ServiceAAuth, ServiceBAAS, ServiceStubs,
		ServiceNATCheck, ServiceNPLN, ServiceBlackhole, ServicePassthrough,
	} {
		for _, h := range ServiceHosts(svc) {
			if h.Service != svc {
				t.Errorf("ServiceHosts(%s) returned %s, which is %s", svc, h.Pattern, h.Service)
			}
		}
	}

	if got := ServiceHosts("nonexistent"); got != nil {
		t.Errorf("an unknown service returned %d hosts", len(got))
	}
}

// TestUnlistedNintendoHostsAreBlackholed guards the fallback that exists
// specifically because a live capture found two Nintendo hosts
// (cdn.accounts.nintendo.com, fro-1.hac.lp1.penne.srv.nintendo.net) that
// nobody had added to Hosts. Without this fallback, an unlisted Nintendo host
// is forwarded to real Nintendo — fine for a deployment that tolerates that,
// wrong for one that must never let its console reach real Nintendo at all.
// This is not exhaustive: the fallback covers domains in
// nintendoOwnedDomains, not literally every Nintendo domain that could ever
// exist.
func TestUnlistedNintendoHostsAreBlackholed(t *testing.T) {
	// None of these are in Hosts. If one of them ever is added explicitly,
	// swap it for a different unlisted example rather than deleting the case.
	unlisted := []string{
		"something-nobody-added-yet.hac.lp1.nintendo.net",
		"totally-new.nintendo.com",
		"some-service.nintendowifi.net",
	}
	for _, name := range unlisted {
		resolution, _, matched := Resolve(name)
		if !matched {
			t.Errorf("Resolve(%s): matched = false, want true (should be caught by the Nintendo-domain fallback)", name)
			continue
		}
		if resolution != ResolveBlackhole {
			t.Errorf("Resolve(%s) = %v, want ResolveBlackhole", name, resolution)
		}
	}
}

// TestUnrelatedHostsAreStillForwarded is the other half of the fallback: it
// must not become broad enough to swallow ordinary internet traffic.
func TestUnrelatedHostsAreStillForwarded(t *testing.T) {
	unrelated := []string{
		"example.com",
		"www.google.com",
		"nintendo.example.com", // "nintendo" as a subdomain of someone else's domain
		"notnintendo.com",
	}
	for _, name := range unrelated {
		resolution, _, matched := Resolve(name)
		if matched {
			t.Errorf("Resolve(%s): matched = true, want false — this is not a Nintendo host", name)
			continue
		}
		if resolution != ResolveForward {
			t.Errorf("Resolve(%s) = %v, want ResolveForward", name, resolution)
		}
	}
}

func TestNPLNRestHostsAreNotRoutedToTheGRPCGateway(t *testing.T) {
	// Splatoon 3 talks JSON to vermillion and penne before it opens a gRPC
	// channel, so these must be proxy_pass/HTTP-1.1 hosts, not grpc_pass ones.
	// Putting them under ServiceNPLN would complete the TLS handshake and then
	// answer with something no gRPC client understands, which neither side
	// logs -- the exact failure cmd/switchnetctl's NPLN block warns about.
	wantRest := map[string]bool{
		"gw.hac.lp1.vermillion.srv.nintendo.net": true,
		"val.hac.lp1.penne.srv.nintendo.net":     true,
		"fro-*.hac.lp1.penne.srv.nintendo.net":   true,
	}
	for _, h := range ServiceHosts(ServiceNPLNRest) {
		delete(wantRest, h.Pattern)
	}
	for missing := range wantRest {
		t.Errorf("%s is not routed to ServiceNPLNRest", missing)
	}

	for _, h := range ServiceHosts(ServiceNPLN) {
		if strings.Contains(h.Pattern, "vermillion") || strings.Contains(h.Pattern, "penne") {
			t.Errorf("%s is a JSON host but is routed to the gRPC gateway", h.Pattern)
		}
	}
}

func TestTheFrontlineHostFamilyIsCoveredNotJustFroOne(t *testing.T) {
	// penne tells the console which fro-N to open its persistent connection
	// to. SwitchNet writes that response, so it can name fro-1 -- but a console
	// holding a cached FQDN from real Nintendo may ask for another, and a
	// console pointed at real Nintendo for its frontline is a console partly
	// off SwitchNet. The wildcard covers the family.
	for _, name := range []string{
		"fro-1.hac.lp1.penne.srv.nintendo.net",
		"fro-3.hac.lp1.penne.srv.nintendo.net",
		"fro-9.hac.lp1.penne.srv.nintendo.net",
	} {
		res, h, ok := Resolve(name)
		if !ok || res != ResolveRedirect || h.Service != ServiceNPLNRest {
			t.Errorf("%s resolved to (%v, %s, %v), want a redirect to %s",
				name, res, h.Service, ok, ServiceNPLNRest)
		}
	}

	// The certificate has to name them individually: a DNS wildcard SAN matches
	// exactly one label, and these are multi-label.
	var covered int
	for _, n := range TLSHostnames() {
		if strings.HasPrefix(n, "fro-") {
			covered++
		}
	}
	if covered == 0 {
		t.Error("no fro-N hostname is in the certificate SANs, so TLS to the " +
			"frontline host will fail no matter what DNS says")
	}
}

func TestAMoreSpecificPenneHostStillBeatsTheFrontlineWildcard(t *testing.T) {
	// The wildcard's DNS suffix is everything after the '*', which is the whole
	// penne domain rather than just fro-. Longest-suffix-wins is what keeps the
	// individually listed penne hosts on their own services; without it the
	// wildcard would swallow them.
	for name, want := range map[string]Service{
		"god.hac.lp1.penne.srv.nintendo.net": ServiceStubs,
		"val.hac.lp1.penne.srv.nintendo.net": ServiceNPLNRest,
	} {
		_, h, ok := Resolve(name)
		if !ok || h.Service != want {
			t.Errorf("%s resolved to %s, want %s", name, h.Service, want)
		}
	}

	// The consequence worth stating: an unlisted penne host is now redirected
	// rather than null-routed. That is the intended trade -- it reaches a server
	// that answers 404 instead of a black hole -- but it is a behaviour change,
	// so it is pinned here rather than left to be rediscovered.
	res, h, ok := Resolve("unlisted.hac.lp1.penne.srv.nintendo.net")
	if !ok || res != ResolveRedirect || h.Service != ServiceNPLNRest {
		t.Errorf("an unlisted penne host resolved to (%v, %s), want a redirect to %s",
			res, h.Service, ServiceNPLNRest)
	}
}

func TestOLSCPermissionHostBeatsTheSataWildcard(t *testing.T) {
	// permission.lp1.sata.srv.nintendo.net is phase 4's confirmed-shape OLSC
	// endpoint; everything else under *.sata.srv.nintendo.net (including the
	// privacy-policy pp.<region>.sata... pages) stays stubbed. Longest-suffix
	// -wins in Resolve keeps this true regardless of declaration order in
	// Hosts, but the generated Atmosphere hosts file needs the same outcome
	// from physical line order instead (see switchnetctl's "phase 4: OLSC
	// cloud save" section, which must be emitted after "stubs" for exactly
	// this reason) -- this test only pins Resolve's own answer.
	for name, want := range map[string]Service{
		"permission.lp1.sata.srv.nintendo.net": ServiceSCSI,
		"pp.lp1.sata.srv.nintendo.net":         ServiceStubs,
		"hac.lp1.sata.srv.nintendo.net":        ServiceStubs,
	} {
		_, h, ok := Resolve(name)
		if !ok || h.Service != want {
			t.Errorf("%s resolved to %s, want %s", name, h.Service, want)
		}
	}
}
