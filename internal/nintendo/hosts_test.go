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

// TestSystemUpdateHostsArePassthrough guards a mistake that is not recoverable
// by changing the server: the nim sysmodule stores a persistent flag when the
// system update check fails, and that flag survives switching back to
// Nintendo's servers.
func TestSystemUpdateHostsArePassthrough(t *testing.T) {
	var found bool
	for _, h := range Hosts {
		if !strings.Contains(h.Pattern, "d4c") {
			continue
		}
		found = true
		if h.Service != ServicePassthrough {
			t.Errorf("%s is %s; the d4c hosts must reach real Nintendo, because "+
				"a failed system update check sets a persistent flag on the console",
				h.Pattern, h.Service)
		}
	}
	if !found {
		t.Error("the d4c hosts are not listed at all, so nothing documents that they " +
			"must not be redirected")
	}
}

func TestTelemetryIsBlackholedNotRedirected(t *testing.T) {
	hosts := ServiceHosts(ServiceBlackhole)
	if len(hosts) == 0 {
		t.Fatal("no telemetry hosts are null-routed")
	}
	for _, h := range hosts {
		if !strings.Contains(h.Pattern, ".dg.") && !strings.Contains(h.Pattern, ".er.") {
			t.Errorf("%s is null-routed but is not a telemetry host", h.Pattern)
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

func TestNPLNTenantForSplatoon3IsListed(t *testing.T) {
	// Phase 2 needs this, and it is listed now so the hosts file an operator
	// installs today does not have to be regenerated then. gamesync does not end
	// in srv.nintendo.net, which is why a broad wildcard would miss it.
	var haveTenant, haveGamesync bool
	for _, h := range ServiceHosts(ServiceNPLN) {
		if strings.HasPrefix(h.Pattern, "t-dce9377b-lp1.") {
			haveTenant = true
		}
		if strings.HasPrefix(h.Pattern, "gamesync.") {
			haveGamesync = true
		}
	}
	if !haveTenant {
		t.Error("the Splatoon 3 NPLN tenant host is missing")
	}
	if !haveGamesync {
		t.Error("gamesync.npln.nintendo.net is missing; it does not end in " +
			"srv.nintendo.net, so no wildcard covers it")
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
