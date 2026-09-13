package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// clearEnv removes every SWITCHNET_* variable for the duration of a test.
//
// Without this the tests are not hermetic: anybody with SWITCHNET_DATABASE_URL
// exported in their shell — which is exactly what running the servers locally
// leaves behind — sees the validation tests fail for a reason that has nothing
// to do with the code.
func clearEnv(t *testing.T) {
	t.Helper()
	for _, entry := range os.Environ() {
		key, _, found := strings.Cut(entry, "=")
		if !found || !strings.HasPrefix(key, "SWITCHNET_") {
			continue
		}
		// t.Setenv restores the previous value at the end of the test, and
		// setting it to empty is not the same as unsetting it, so unset it
		// explicitly and restore by hand.
		previous := os.Getenv(key)
		if err := os.Unsetenv(key); err != nil {
			t.Fatalf("unset %s: %v", key, err)
		}
		t.Cleanup(func() { _ = os.Setenv(key, previous) })
	}
}

func writeConfig(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestDefaultsAreValid(t *testing.T) {
	clearEnv(t)

	// A first run with no config file at all has to work: internal/service's
	// ConfigFlag defaults to an empty path, so anything invoking a binary
	// directly with no -config flag and no SWITCHNET_CONFIG set relies on this.
	cfg, err := Load("")
	if err != nil {
		t.Fatalf("the defaults do not validate: %v", err)
	}

	// The Nintendo-facing values have to match what a real console sees, because
	// captures are compared against them.
	if cfg.DAuth.Issuer != "dauth-lp1.ndas.srv.nintendo.net" {
		t.Errorf("dauth issuer = %q", cfg.DAuth.Issuer)
	}
	if cfg.BAAS.Audience != "ed9e2f05d286f7b8" {
		t.Errorf("baas audience = %q", cfg.BAAS.Audience)
	}
	if !strings.HasSuffix(cfg.BAAS.AccessTokenJWKSURL, "/1.0.0/internal_certificates") {
		t.Errorf("access token JWKS URL = %q", cfg.BAAS.AccessTokenJWKSURL)
	}
	// The default must let friends see each other online.
	if cfg.BAAS.PresenceTTL <= 0 || cfg.BAAS.ConsolePresenceTTL <= 0 {
		t.Errorf("presence TTLs = %d and %d, both must be positive",
			cfg.BAAS.PresenceTTL, cfg.BAAS.ConsolePresenceTTL)
	}
	// FREE is the right default for a private network: the console never asks
	// for a Nintendo Switch Online membership.
	if cfg.AAuth.OnlinePlayPolicy != "FREE" {
		t.Errorf("online play policy = %q, want FREE", cfg.AAuth.OnlinePlayPolicy)
	}
}

func TestExampleConfigIsValid(t *testing.T) {
	clearEnv(t)

	// The file an operator copies has to load. A typo in it is a first-run
	// failure with a stack trace instead of a working server.
	path := filepath.Join("config.example.yaml")
	if _, err := os.Stat(path); err != nil {
		t.Skipf("config.example.yaml is not present: %v", err)
	}
	if _, err := Load(path); err != nil {
		t.Fatalf("config.example.yaml does not load: %v", err)
	}
}

func TestUnknownFieldIsRejected(t *testing.T) {
	clearEnv(t)

	// A silently ignored key is a setting the operator thinks they changed. That
	// is worse than a startup error, especially for something like
	// natcheck_secondary_ip whose absence only shows up as a game not
	// matchmaking.
	path := writeConfig(t, "network:\n  publik_ip: 1.2.3.4\n")
	if _, err := Load(path); err == nil {
		t.Fatal("a misspelled key was accepted")
	}
}

func TestEnvironmentOverridesFile(t *testing.T) {
	clearEnv(t)

	path := writeConfig(t, `
network:
  public_ip: 10.0.0.1
baas:
  listen: ":9999"
  presence_ttl: 60
`)

	t.Setenv("SWITCHNET_PUBLIC_IP", "192.168.1.50")
	t.Setenv("SWITCHNET_BAAS_PRESENCE_TTL", "120")

	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Network.PublicIP != "192.168.1.50" {
		t.Errorf("public_ip = %q, want the environment to win", cfg.Network.PublicIP)
	}
	if cfg.BAAS.PresenceTTL != 120 {
		t.Errorf("presence_ttl = %d, want 120 from the environment", cfg.BAAS.PresenceTTL)
	}
	// A setting with no override keeps the file's value.
	if cfg.BAAS.Listen != ":9999" {
		t.Errorf("baas listen = %q, want the file's value", cfg.BAAS.Listen)
	}
}

// TestNATCheckSecondaryIPMustDiffer is the validation that saves the most
// debugging time.
//
// Pia sends its second NAT probe only if nncs1 and nncs2 resolve to different
// addresses. Set to the same value, matchmaking finds opponents and the match
// then never starts — and nothing in any log says why.
func TestNATCheckSecondaryIPMustDiffer(t *testing.T) {
	clearEnv(t)

	path := writeConfig(t, `
network:
  public_ip: 192.168.1.50
  natcheck_secondary_ip: 192.168.1.50
`)
	_, err := Load(path)
	if err == nil {
		t.Fatal("the same address was accepted for nncs1 and nncs2")
	}
	if !strings.Contains(err.Error(), "de-duplicate") {
		t.Errorf("the error does not explain the consequence: %v", err)
	}

	// Two different addresses are fine, and so is leaving it empty.
	for _, body := range []string{
		"network:\n  public_ip: 192.168.1.50\n  natcheck_secondary_ip: 192.168.1.51\n",
		"network:\n  public_ip: 192.168.1.50\n",
	} {
		if _, err := Load(writeConfig(t, body)); err != nil {
			t.Errorf("rejected a valid configuration: %v", err)
		}
	}
}

// TestNATCheckSecondaryIPMustDifferByAddressNotByText covers the gap fixed
// alongside this test: the same check above compared the two settings as
// plain strings, so two different textual spellings of the identical address
// (here, an IPv4-mapped IPv6 form) passed validation -- the exact
// misconfiguration TestNATCheckSecondaryIPMustDiffer exists to catch, only
// caught later at `make up` when internal/dns.Listen's own net.IP.Equal
// check refuses to start instead.
func TestNATCheckSecondaryIPMustDifferByAddressNotByText(t *testing.T) {
	clearEnv(t)

	path := writeConfig(t, `
network:
  public_ip: 192.168.1.50
  natcheck_secondary_ip: ::ffff:192.168.1.50
`)
	_, err := Load(path)
	if err == nil {
		t.Fatal("an IPv4-mapped-IPv6 spelling of the same address was accepted for nncs1 and nncs2")
	}
	if !strings.Contains(err.Error(), "de-duplicate") {
		t.Errorf("the error does not explain the consequence: %v", err)
	}
}

func TestInvalidAddressesAreRejected(t *testing.T) {
	clearEnv(t)

	for name, body := range map[string]string{
		"public ip is a hostname": "network:\n  public_ip: switchnet.example.com\n",
		"public ip is nonsense":   "network:\n  public_ip: not-an-ip\n",
		"secondary is nonsense": "network:\n  public_ip: 192.168.1.50\n" +
			"  natcheck_secondary_ip: nope\n",
	} {
		if _, err := Load(writeConfig(t, body)); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

func TestPolicyValidation(t *testing.T) {
	clearEnv(t)

	for name, body := range map[string]string{
		"unknown policy":  "aauth:\n  online_play_policy: SOMETIMES\n",
		"unknown handler": "aauth:\n  policy_handler: MAYBE\n",
		"short title id": "aauth:\n  title_overrides:\n" +
			"    \"0100c250\":\n      online_play_policy: FREE\n",
		"bad override policy": "aauth:\n  title_overrides:\n" +
			"    \"0100c2500fc20000\":\n      online_play_policy: NEVER\n",
	} {
		if _, err := Load(writeConfig(t, body)); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

func TestPolicyFor(t *testing.T) {
	cfg := AAuth{
		OnlinePlayPolicy: "FREE",
		PolicyHandler:    "GAME_SERVER",
		TitleOverrides: map[string]TitlePolicy{
			"0100c2500fc20000": {OnlinePlayPolicy: "MEMBERSHIP_REQUIRED"},
			"0100152000022000": {PolicyHandler: "SYSTEM"},
		},
	}

	policy, handler := cfg.PolicyFor("0100abf008968000")
	if policy != "FREE" || handler != "GAME_SERVER" {
		t.Errorf("unlisted title = %q/%q, want the defaults", policy, handler)
	}

	// An override that names only the policy keeps the default handler.
	policy, handler = cfg.PolicyFor("0100c2500fc20000")
	if policy != "MEMBERSHIP_REQUIRED" || handler != "GAME_SERVER" {
		t.Errorf("policy-only override = %q/%q", policy, handler)
	}

	// And one that names only the handler keeps the default policy.
	policy, handler = cfg.PolicyFor("0100152000022000")
	if policy != "FREE" || handler != "SYSTEM" {
		t.Errorf("handler-only override = %q/%q", policy, handler)
	}

	// A title id is 16 hex digits and may arrive in either case.
	policy, _ = cfg.PolicyFor("0100C2500FC20000")
	if policy != "MEMBERSHIP_REQUIRED" {
		t.Errorf("an upper-case title id missed its override: %q", policy)
	}
}

func TestMissingConfigFileIsAnError(t *testing.T) {
	clearEnv(t)

	// An operator who passes -config with a typo should be told, not silently
	// given the defaults and a server pointing at nothing.
	if _, err := Load(filepath.Join(t.TempDir(), "does-not-exist.yaml")); err == nil {
		t.Fatal("a missing config file was accepted")
	}
}

func TestRequiredFieldsAreEnforced(t *testing.T) {
	clearEnv(t)

	for name, body := range map[string]string{
		"no database url":       "database:\n  url: \"\"\n",
		"no keys dir":           "keys:\n  dir: \"\"\n",
		"zero presence ttl":     "baas:\n  presence_ttl: 0\n",
		"negative console ttl":  "baas:\n  console_presence_ttl: -1\n",
		"zero access token ttl": "baas:\n  access_token_ttl: 0\n",
	} {
		if _, err := Load(writeConfig(t, body)); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

// ---- DNS ----------------------------------------------------------------

func TestDNSUpstreamValidation(t *testing.T) {
	clearEnv(t)

	for name, body := range map[string]string{
		"no upstream": "dns:\n  upstream: []\n",
		// This server IS the resolver, so it cannot look up a hostname to find
		// its own upstream.
		"hostname instead of an IP": "dns:\n  upstream: [\"dns.quad9.net:53\"]\n",
		"missing port":              "dns:\n  upstream: [\"9.9.9.9\"]\n",
		"negative ttl":              "dns:\n  ttl: -1\n",
		// Pointing the upstream at ourselves is a loop: every non-Nintendo
		// lookup would time out while Nintendo's kept working, which is a
		// confusing way to find out.
		"upstream is ourselves": "network:\n  public_ip: 192.168.1.50\n" +
			"dns:\n  upstream: [\"192.168.1.50:53\"]\n",
	} {
		if _, err := Load(writeConfig(t, body)); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}

	// A valid list is accepted.
	cfg, err := Load(writeConfig(t, "dns:\n  upstream: [\"9.9.9.9:53\", \"1.1.1.1:53\"]\n"))
	if err != nil {
		t.Fatalf("rejected a valid upstream list: %v", err)
	}
	if len(cfg.DNS.Upstream) != 2 {
		t.Errorf("upstream = %v, want two entries", cfg.DNS.Upstream)
	}
}

func TestDNSUpstreamFromEnvironment(t *testing.T) {
	clearEnv(t)

	// A YAML list has to be expressible as one environment variable, because
	// that is all a container gives us.
	t.Setenv("SWITCHNET_DNS_UPSTREAM", "8.8.8.8:53, 8.8.4.4:53")

	cfg, err := Load("")
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"8.8.8.8:53", "8.8.4.4:53"}
	if len(cfg.DNS.Upstream) != len(want) {
		t.Fatalf("upstream = %v, want %v", cfg.DNS.Upstream, want)
	}
	for i := range want {
		if cfg.DNS.Upstream[i] != want[i] {
			t.Errorf("upstream[%d] = %q, want %q", i, cfg.DNS.Upstream[i], want[i])
		}
	}
}

func TestDNSDefaultsAreUsable(t *testing.T) {
	clearEnv(t)

	cfg, err := Load("")
	if err != nil {
		t.Fatal(err)
	}
	// A console cannot be told to use a port other than 53.
	if cfg.DNS.Listen != ":53" {
		t.Errorf("dns.listen = %q, want :53", cfg.DNS.Listen)
	}
	if len(cfg.DNS.Upstream) == 0 {
		t.Error("no default upstream, so a fresh install resolves nothing")
	}
	// Short, so switching a console back to Nintendo is not blocked by a cached
	// record.
	if cfg.DNS.TTL == 0 || cfg.DNS.TTL > 300 {
		t.Errorf("dns.ttl = %d, want something short and non-zero", cfg.DNS.TTL)
	}
}
