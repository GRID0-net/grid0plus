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
// misconfiguration TestNATCheckSecondaryIPMustDiffer exists to catch. There is
// no second line of defense once this passes: redirection now happens off the
// console, in a resolver GRID0+ does not run, so this check is the only
// place the mistake gets caught.
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

// ---- toolbox --------------------------------------------------------------

func TestToolboxSettingsAreAllOrNothing(t *testing.T) {
	clearEnv(t)

	for name, body := range map[string]string{
		"owner only": "toolbox:\n  github_owner: n-popescu\n",
		"missing public_base_url": "toolbox:\n" +
			"  github_owner: n-popescu\n  github_repo: grid0plus-toolbox\n  github_token: t\n",
		"public_base_url only": "toolbox:\n  public_base_url: https://203.0.113.5:8443\n",
	} {
		if _, err := Load(writeConfig(t, body)); err == nil {
			t.Errorf("%s: accepted a partially-configured toolbox section", name)
		}
	}

	// All four together, or none at all, are both fine.
	for name, body := range map[string]string{
		"all four set": "toolbox:\n" +
			"  github_owner: n-popescu\n  github_repo: grid0plus-toolbox\n" +
			"  github_token: t\n  public_base_url: https://203.0.113.5:8443\n",
		"none set": "toolbox:\n  listen: \"127.0.0.1:8090\"\n",
	} {
		if _, err := Load(writeConfig(t, body)); err != nil {
			t.Errorf("%s: rejected: %v", name, err)
		}
	}
}

// TestToolboxPublicBaseURLMustBeAnAbsoluteHTTPSURL guards the fix for a real
// bug: this value used to be inferred from the incoming request instead of
// configured, which behind the real deploy/nginx.toolbox.conf (TLS
// terminated at nginx, proxied to this service over plain HTTP, Host header
// forwarded without its port) always produced a browser_download_url with
// the wrong scheme and a missing port. Requiring an absolute https:// URL
// here is what makes that class of mistake a config-load error instead of a
// response the console silently cannot use.
func TestToolboxPublicBaseURLMustBeAnAbsoluteHTTPSURL(t *testing.T) {
	clearEnv(t)

	base := "toolbox:\n  github_owner: n-popescu\n  github_repo: grid0plus-toolbox\n  github_token: t\n"
	for name, url := range map[string]string{
		"plain http, not https": "http://203.0.113.5:8443",
		"no scheme at all":      "203.0.113.5:8443",
		"has a path":            "https://203.0.113.5:8443/updates",
	} {
		body := base + "  public_base_url: " + url + "\n"
		if _, err := Load(writeConfig(t, body)); err == nil {
			t.Errorf("%s (%q): accepted", name, url)
		}
	}

	valid := base + "  public_base_url: https://203.0.113.5:8443\n"
	if _, err := Load(writeConfig(t, valid)); err != nil {
		t.Errorf("a valid https public_base_url was rejected: %v", err)
	}
}

// ---- DNS ----------------------------------------------------------------

func TestDNSTTLValidation(t *testing.T) {
	clearEnv(t)

	if _, err := Load(writeConfig(t, "dns:\n  ttl: -1\n")); err == nil {
		t.Error("negative ttl: accepted")
	}
}

func TestDNSDefaultsAreUsable(t *testing.T) {
	clearEnv(t)

	cfg, err := Load("")
	if err != nil {
		t.Fatal(err)
	}
	// Short, so switching a console back to Nintendo is not blocked by a cached
	// record.
	if cfg.DNS.TTL == 0 || cfg.DNS.TTL > 300 {
		t.Errorf("dns.ttl = %d, want something short and non-zero", cfg.DNS.TTL)
	}
}

// TestSalmonRunIsFourPlayersWithoutBeingConfigured: a config that never
// mentions Salmon Run must still form it as one crew of four, and a size an
// operator sets for another mode must not drop it.
func TestSalmonRunIsFourPlayersWithoutBeingConfigured(t *testing.T) {
	clearEnv(t)
	path := writeConfig(t, "npln:\n  mode_players:\n    \"tenants/current/matchmakingConfigs/regular_match_config\": 4\n")
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	coop := salmonRunConfig("regular")
	if cfg.NPLN.ModePlayers[coop] != 4 || len(cfg.NPLN.CoopModes) != 3 {
		t.Errorf("Salmon Run size %d, coop modes %v; want 4 and the three Salmon Run configs",
			cfg.NPLN.ModePlayers[coop], cfg.NPLN.CoopModes)
	}
	if cfg.NPLN.ModePlayers["tenants/current/matchmakingConfigs/regular_match_config"] != 4 {
		t.Error("the operator's own entry was lost")
	}
}
