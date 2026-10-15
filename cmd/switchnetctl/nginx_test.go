package main

import (
	"regexp"
	"strings"
	"testing"

	"github.com/n-popescu/grid0plus/config"
	"github.com/n-popescu/grid0plus/internal/nintendo"
)

func testConfig() config.Config {
	cfg := config.Default()
	cfg.Network.PublicIP = "192.168.1.50"
	cfg.Network.NATCheckSecondaryIP = "192.168.1.51"
	return cfg
}

// TestNoGeneratedServerBlockHasAnEmptyServerName guards a failure that is
// invisible in review and silent at runtime.
//
// The blocks are built by grouping hosts into (client-cert, http2) variants,
// and a block that reads its names from a variant no host lands in renders as
// `server_name ;`. nginx accepts that: it just never matches, so every request
// for those hosts falls through to the default block. On the console that is a
// game which cannot reach a server that is running and configured.
func TestNoGeneratedServerBlockHasAnEmptyServerName(t *testing.T) {
	conf := nginxConf(testConfig())

	// Both live and commented-out blocks: a commented block is uncommented one
	// day, and it should not be broken when it is.
	empty := regexp.MustCompile(`(?m)^#?\s*server_name\s*;`)
	if loc := empty.FindStringIndex(conf); loc != nil {
		t.Errorf("a generated server block has an empty server_name:\n%s",
			contextAround(conf, loc[0]))
	}
}

// The console's BCAT/topics and telemetry connections reached nginx but
// closed during TLS before sending HTTP when nginx requested a client cert.
func TestBCATAndTelemetryDoNotRequestClientCertificates(t *testing.T) {
	conf := nginxConf(testConfig())
	for _, host := range []string{
		"bcat-list-lp1.cdn.nintendo.net",
		"bcat-data-lp1.cdn.nintendo.net",
		"bcat-topics-lp1.cdn.nintendo.net",
		"receive-lp1.dg.srv.nintendo.net",
		"receive-lp1.er.srv.nintendo.net",
	} {
		found := false
		for _, block := range strings.Split(conf, "server {")[1:] {
			if !strings.Contains(block, host) || !strings.Contains(block, "server_name") {
				continue
			}
			found = true
			if strings.Contains(block, "ssl_verify_client optional_no_ca;") {
				t.Errorf("%s requests a client certificate", host)
			}
			break
		}
		if !found {
			t.Errorf("%s has no nginx server block", host)
		}
	}
}

// TestEveryProxiedServiceAppearsInTheGeneratedConfig catches a service that was
// added to ProxiedServices but never given a name or a backend: it would render
// as "# ---- ----" with an upstream called switchnet_ and no hosts.
func TestEveryProxiedServiceAppearsInTheGeneratedConfig(t *testing.T) {
	conf := nginxConf(testConfig())

	for _, svc := range nintendo.ProxiedServices {
		if len(nintendo.ServiceHosts(svc)) == 0 {
			continue
		}
		if !strings.Contains(conf, "# ---- "+string(svc)+" ----") &&
			!strings.Contains(conf, "# ---- "+nginxNameOf(svc)+" ----") {
			t.Errorf("service %q has hosts but no section in the generated config", svc)
		}
	}

	if strings.Contains(conf, "# ----  ----") {
		t.Error("a section header has no name, so a service is missing from the names map")
	}
	if strings.Contains(conf, "upstream switchnet_ ") {
		t.Error("an upstream has no name, so a service is missing from the names map")
	}
}

// TestTheNPLNJSONHostsUseProxyPassAndTheTenantUsesGRPCPass is the routing
// mistake ROADMAP.md and hosts.go both warn about, asserted rather than trusted.
//
// Splatoon 3 talks JSON to vermillion and penne before it opens a gRPC channel.
// Sending the JSON hosts through grpc_pass, or the tenant through proxy_pass,
// completes the TLS handshake and then fails with nothing in either log.
func TestTheNPLNJSONHostsUseProxyPassAndTheTenantUsesGRPCPass(t *testing.T) {
	conf := nginxConf(testConfig())

	restBlock := sectionOf(t, conf, "npln_rest")
	if !hasDirective(restBlock, "proxy_pass http://switchnet_npln_rest") {
		t.Error("the NPLN JSON hosts are not proxy_pass'd")
	}
	// Checked as a DIRECTIVE, not a substring: the block's own comment says
	// "-- NOT grpc_pass", and a substring match reads that prose as config.
	if hasDirective(restBlock, "grpc_pass") {
		t.Error("the NPLN JSON hosts are routed through grpc_pass, which fails silently")
	}
	// The frontline connection is a stream the console holds open.
	for _, required := range []string{"proxy_buffering off", "proxy_read_timeout"} {
		if !hasDirective(restBlock, required) {
			t.Errorf("the frontline block is missing %q, without which the "+
				"connection is buffered or idle-timed out and the game reports "+
				"a communication error", required)
		}
	}

	// npns streams its frames on an HTTP/2 request body; over HTTP/1.1 curl
	// sends an empty body and the console never leaves state 7.
	for _, server := range strings.Split(restBlock, "server {")[1:] {
		fro := strings.Contains(server, "fro-1.hac.lp1.penne.srv.nintendo.net")
		if fro != hasDirective(server, "http2 on") {
			t.Errorf("http2 on must be set exactly for the frontline hosts; this block has fro=%v:\n%s", fro, server)
		}
	}

	grpcBlock := sectionOf(t, conf, "npln")
	if !hasDirective(grpcBlock, "grpc_pass grpc://switchnet_npln") {
		t.Error("the NPLN tenant is not grpc_pass'd")
	}
	if !hasDirective(grpcBlock, "http2 on") {
		t.Error("the NPLN tenant block does not enable HTTP/2, which gRPC requires")
	}
}

func TestTheFrontlineHostsAreInTheCertificateSANs(t *testing.T) {
	// A DNS wildcard SAN matches one label, so fro-N has to be listed out. If
	// it is not, DNS points the console at GRID0+ and TLS then fails.
	var found bool
	for _, n := range nintendo.TLSHostnames() {
		if strings.HasPrefix(n, "fro-") {
			found = true
			break
		}
	}
	if !found {
		t.Error("no fro-N hostname is in TLSHostnames(), so the generated " +
			"certificate will not cover the frontline connection")
	}
}

func nginxNameOf(svc nintendo.Service) string { return string(svc) }

// sectionOf returns the "# ---- name ----" section of the generated config.
func sectionOf(t *testing.T, conf, name string) string {
	t.Helper()
	start := strings.Index(conf, "# ---- "+name+" ----")
	if start < 0 {
		t.Fatalf("no %q section in the generated config", name)
	}
	rest := conf[start+len("# ---- "+name+" ----"):]
	if end := strings.Index(rest, "# ---- "); end >= 0 {
		return rest[:end]
	}
	return rest
}

func contextAround(s string, at int) string {
	start := at - 200
	if start < 0 {
		start = 0
	}
	end := at + 200
	if end > len(s) {
		end = len(s)
	}
	return s[start:end]
}

// hasDirective reports whether block contains prefix as an nginx directive
// rather than as prose in a comment.
//
// The generated phase-2 blocks are emitted commented out, so "#" is stripped
// before the line is considered -- but a line that is *explanatory* text rather
// than commented-out configuration must not count. The distinction is that a
// directive line, once de-commented, starts with the directive itself.
func hasDirective(block, prefix string) bool {
	for _, line := range strings.Split(block, "\n") {
		line = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(line), "#"))
		if strings.HasPrefix(line, prefix) {
			return true
		}
	}
	return false
}

// TestEveryHostReachesTheGeneratedHostsFile is the guard for a drift CLAUDE.md
// warns about and this generator was quietly capable of.
//
// The host list is meant to be the single source of truth. But this generator
// walks it SERVICE BY SERVICE, in named sections — so adding a new Service and
// forgetting a section here drops its hosts from the file with no error
// anywhere. That happened: giving gamesync its own service (it is reached on
// port 7575, not 443) removed gamesync.npln.nintendo.net from the hosts file,
// and the only symptom would have been a console reaching real Nintendo for it.
func TestEveryHostReachesTheGeneratedHostsFile(t *testing.T) {
	file := hostsFile(testConfig())

	for _, h := range nintendo.Hosts {
		if h.Service == nintendo.ServicePassthrough {
			// Passthrough hosts are deliberately absent: they must keep
			// resolving to real Nintendo.
			continue
		}
		if !strings.Contains(file, h.Pattern) {
			t.Errorf("%s (%s) is in the host list but not in the generated hosts "+
				"file — a section is missing from hostsFile", h.Pattern, h.Service)
		}
	}
}

// TestEveryProxiedHostReachesTheGeneratedNginxConfig is the same guard for the
// edge: a TLS host with no server block is a console whose connection reaches
// the default block and gets nothing useful.
func TestEveryProxiedHostReachesTheGeneratedNginxConfig(t *testing.T) {
	conf := nginxConf(testConfig())

	for _, svc := range nintendo.ProxiedServices {
		for _, h := range nintendo.ServiceHosts(svc) {
			if !h.TLS {
				continue
			}
			names := []string{h.Pattern}
			if h.Wildcard {
				names = h.TLSNames
			}
			for _, name := range names {
				if !strings.Contains(conf, name) {
					t.Errorf("%s (%s) needs a server block and has none", name, svc)
				}
			}
		}
	}
}

func TestTLSResumptionCachesSeparateClientCertificatePolicies(t *testing.T) {
	cfg := testConfig()
	withCert := httpServerBlock(cfg, []string{"a.example"}, "baas", true, true, "1m")
	withoutCert := httpServerBlock(cfg, []string{"b.example"}, "baas", false, true, "1m")
	for _, tc := range []struct{ body, zone string }{{withCert, "GRID0_http_baas_true"}, {withoutCert, "GRID0_http_baas_false"}, {grpcServerBlock(cfg, "npln", 443, []string{"c.example"}), "GRID0_grpc_npln_false"}} {
		if !strings.Contains(tc.body, "ssl_session_cache shared:"+tc.zone+":1m;") || !strings.Contains(tc.body, "ssl_session_timeout 10m;") {
			t.Fatalf("missing bounded TLS session cache: %s", tc.body)
		}
	}
}
