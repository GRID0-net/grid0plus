// Package config loads and validates SwitchNet's configuration.
//
// Configuration comes from a single YAML file, with environment variable
// overrides for everything an operator is likely to want to change per
// deployment (and for everything secret). Nothing is hardcoded: the operator's
// own domain, IP addresses, ports, database URL and key paths all live here.
package config

import (
	"errors"
	"fmt"
	"net"
	"os"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"
)

// Config is the whole SwitchNet configuration.
type Config struct {
	// Network describes where the operator's servers live, from the console's
	// point of view.
	Network Network `yaml:"network"`

	Database Database `yaml:"database"`
	Redis    Redis    `yaml:"redis"`

	Keys Keys `yaml:"keys"`

	DNS       DNS       `yaml:"dns"`
	DAuth     DAuth     `yaml:"dauth"`
	AAuth     AAuth     `yaml:"aauth"`
	BAAS      BAAS      `yaml:"baas"`
	Stubs     Stubs     `yaml:"stubs"`
	NPLN      NPLN      `yaml:"npln"`
	Dashboard Dashboard `yaml:"dashboard"`
	NATCheck  NATCheck  `yaml:"natcheck"`

	Log Log `yaml:"log"`
}

// Network is the operator's own addressing.
type Network struct {
	// PublicIP is the address the consoles should be pointed at. It goes into
	// the generated hosts file and into the NAT-check responses.
	PublicIP string `yaml:"public_ip"`

	// NATCheckSecondaryIP is a SECOND address, different from PublicIP, that
	// answers the nncs2 NAT-check host.
	//
	// Pia sends its second NAT probe only if nncs1 and nncs2 resolve to
	// different addresses. If they are the same it de-duplicates the probe,
	// never completes NAT detection, and peer-to-peer matchmaking fails. If
	// you genuinely only have one address, leave this empty and accept that
	// the console will report a stricter NAT type than it really has.
	NATCheckSecondaryIP string `yaml:"natcheck_secondary_ip"`

	// EdgeTLSPort is the port the nginx TLS edge listens on. The console always
	// connects to 443, so this only differs from 443 if something else in front
	// of nginx is doing the port mapping.
	EdgeTLSPort int `yaml:"edge_tls_port"`
}

// DNS configures the DNS server, which is how a console is pointed at SwitchNet
// without modifying the console.
type DNS struct {
	// Listen is the address to serve on. A console cannot be told to use a port
	// other than 53, so this is ":53" in any real deployment.
	Listen string `yaml:"listen"`

	// Upstream resolvers for every name SwitchNet does not own, tried in order.
	//
	// Do NOT point these at a resolver that itself points back here, and do not
	// use the router's own address if the router forwards to this server: that
	// is a loop, and the symptom is every non-Nintendo lookup timing out while
	// Nintendo's work fine.
	Upstream []string `yaml:"upstream"`

	// TTL of the answers SwitchNet synthesises, in seconds. Short on purpose:
	// switching a console back to Nintendo should take effect in a minute, not
	// after an hour of cached records.
	TTL int `yaml:"ttl"`

	// UpstreamTimeout bounds a forwarded query, in seconds.
	UpstreamTimeout int `yaml:"upstream_timeout"`

	// LogForwarded logs every name that was forwarded, not just the redirected
	// ones. Useful once, to find a Nintendo host missing from the list; far too
	// chatty otherwise, because a console resolves a lot of names.
	LogForwarded bool `yaml:"log_forwarded"`
}

// Database is the PostgreSQL connection.
type Database struct {
	// URL is a libpq connection string or postgres:// URL.
	URL string `yaml:"url"`
	// MaxConns caps the pool. Zero means pgx's default.
	MaxConns int32 `yaml:"max_conns"`
	// AutoMigrate runs pending migrations on startup.
	AutoMigrate bool `yaml:"auto_migrate"`
}

// Redis is the optional Redis connection used for the presence bus.
type Redis struct {
	// URL is a redis:// URL. Leave empty to use a single-process in-memory bus,
	// which is correct only if exactly one process publishes presence.
	URL string `yaml:"url"`
	// Channel is the pub/sub channel presence events are published on.
	Channel string `yaml:"channel"`
}

// Keys points at the signing keys. Generate them with scripts/gen-certs.sh;
// they are created on first use if missing and generate_if_missing is set.
type Keys struct {
	// Dir holds the PEM-encoded RSA private keys, one per token issuer.
	Dir string `yaml:"dir"`
	// GenerateIfMissing creates a 2048-bit RSA key when one is not on disk.
	// Convenient for a first run; set it to false once the keys are backed up,
	// so that a lost volume fails loudly instead of silently invalidating every
	// token the consoles are holding.
	GenerateIfMissing bool `yaml:"generate_if_missing"`

	// DeviceIDSecret salts the derivation of device ids from a console's TLS
	// client certificate. Any long random string; it only has to stay stable.
	DeviceIDSecret string `yaml:"device_id_secret"`
}

// DAuth configures the device authentication server.
type DAuth struct {
	Listen string `yaml:"listen"`
	// Issuer is the 'iss' claim of device tokens. Keep Nintendo's value: the
	// console does not check it, but tools and captures compare against it.
	Issuer string `yaml:"issuer"`
	// JWKSURL is the 'jku' header of device tokens. It must be a URL the
	// console can reach, which means a host in the redirect list.
	JWKSURL string `yaml:"jwks_url"`
	// TokenTTL is 'expires_in', in seconds.
	TokenTTL int `yaml:"token_ttl"`
	// ChallengeTTL is how long a challenge stays usable, in seconds.
	ChallengeTTL int `yaml:"challenge_ttl"`
	// ClientCertHeader is the request header nginx puts the console's TLS
	// client certificate fingerprint in. When present, each console gets its
	// own stable device id. When absent, every console shares one, which is
	// harmless because accounts are keyed on the BAAS device account, not on
	// the device id — but it does make the dashboard's device list useless.
	ClientCertHeader string `yaml:"client_cert_header"`
	// ClientCertPEMHeader is the request header nginx puts the console's full
	// TLS client certificate in ($ssl_client_escaped_cert), used only to
	// compute the 'x5t#S256' claim on issued tokens. When absent, tokens omit
	// the claim rather than guessing at it. See jwt.DeviceInfo.X5tS256.
	ClientCertPEMHeader string `yaml:"client_cert_pem_header"`
}

// AAuth configures the application authentication server.
type AAuth struct {
	Listen   string `yaml:"listen"`
	Issuer   string `yaml:"issuer"`
	JWKSURL  string `yaml:"jwks_url"`
	TokenTTL int    `yaml:"token_ttl"`

	// OnlinePlayPolicy is the default policy reported for every title:
	// "FREE" or "MEMBERSHIP_REQUIRED".
	//
	// "FREE" is the right default for a private network: the console never asks
	// for a Nintendo Switch Online membership. Some titles behave differently
	// when the policy does not match what they saw on retail servers, so
	// per-title overrides exist below.
	OnlinePlayPolicy string `yaml:"online_play_policy"`
	// PolicyHandler is "SYSTEM" or "GAME_SERVER", and is only sent when the
	// policy is MEMBERSHIP_REQUIRED.
	PolicyHandler string `yaml:"policy_handler"`

	// TitleOverrides maps a 16-hex-digit title id to a policy override.
	TitleOverrides map[string]TitlePolicy `yaml:"title_overrides"`
}

// TitlePolicy overrides the online play policy for one title.
type TitlePolicy struct {
	OnlinePlayPolicy string `yaml:"online_play_policy"`
	PolicyHandler    string `yaml:"policy_handler"`
}

// BAAS configures the account and friends server.
type BAAS struct {
	Listen string `yaml:"listen"`
	// Issuer is the 'iss' claim of access and id tokens. Nintendo uses the
	// https:// URL of the BAAS host.
	Issuer string `yaml:"issuer"`
	// Audience is the 'aud' claim. Nintendo uses a fixed opaque id.
	Audience string `yaml:"audience"`
	// AccessTokenJWKSURL is the 'jku' of access and user tokens
	// (/1.0.0/internal_certificates).
	AccessTokenJWKSURL string `yaml:"access_token_jwks_url"`
	// IDTokenJWKSURL is the 'jku' of id tokens (/1.0.0/certificates).
	IDTokenJWKSURL string `yaml:"id_token_jwks_url"`

	// AccessTokenTTL is 'expiresIn' for access tokens, in seconds.
	AccessTokenTTL int `yaml:"access_token_ttl"`
	// IDTokenTTL is the lifetime of id tokens handed to game servers, in seconds.
	IDTokenTTL int `yaml:"id_token_ttl"`

	// FriendCodeCooldown is how long a user must wait before regenerating
	// their friend code, in seconds. Nintendo uses 24 hours.
	FriendCodeCooldown int `yaml:"friend_code_cooldown"`

	// PresenceTTL is how long an explicitly reported presence stays valid
	// without a refresh, in seconds. After this the user reads as OFFLINE.
	PresenceTTL int `yaml:"presence_ttl"`
	// ConsolePresenceTTL is how long a console counts as ONLINE after its last
	// authenticated request, in seconds.
	//
	// This exists because a console sitting in the Home Menu does not report
	// "playing" anything, and without a liveness path every friend reads as
	// permanently offline — the exact failure documented for this stack.
	ConsolePresenceTTL int `yaml:"console_presence_ttl"`

	// ImageDir is where uploaded profile pictures are written.
	ImageDir string `yaml:"image_dir"`
	// ImageBaseURL is the public URL prefix uploaded images are served from.
	// It has to be a host in the redirect list, or the console cannot fetch it.
	ImageBaseURL string `yaml:"image_base_url"`
	// MaxImageBytes caps a single profile picture upload.
	MaxImageBytes int `yaml:"max_image_bytes"`

	// AllowRegistration lets an unknown console create an account. Turn it off
	// once every console in the group is registered, so a stranger who finds
	// the server cannot add themselves.
	AllowRegistration bool `yaml:"allow_registration"`
}

// NPLN configures the Splatoon 3 game server (phase 2).
//
// Two listeners, not one, and they are not interchangeable. Splatoon 3 opens
// with ordinary JSON over HTTPS to the vermillion and penne hosts, and only
// then opens a gRPC channel to the tenant host. nginx must send each to the
// right one -- grpc_pass for Listen, proxy_pass for RestListen -- because
// crossing them completes the TLS handshake and then fails with nothing in
// either log.
type NPLN struct {
	// Listen is the gRPC gateway (HTTP/2, protobuf).
	Listen string `yaml:"listen"`
	// RestListen is the JSON front end: vermillion, penne, and the persistent
	// frontline connection.
	RestListen string `yaml:"rest_listen"`
	// GamesyncListen is the document/watch service. Splatoon 3 reaches it on
	// its own port rather than through the tenant host.
	GamesyncListen string `yaml:"gamesync_listen"`

	// Tenant is the NPLN tenant id. Splatoon 3's is dce9377b.
	Tenant string `yaml:"tenant"`
	// Issuer is the 'iss' of the ES256 access tokens this server signs.
	Issuer string `yaml:"issuer"`
	// JWKSURL is where those tokens' 'jku' points.
	JWKSURL string `yaml:"jwks_url"`
	// AccessTokenTTL is the lifetime of an NPLN access token, in seconds.
	AccessTokenTTL int `yaml:"access_token_ttl"`

	// ICEServers are the STUN/TURN servers handed to a console for peer-to-peer
	// connectivity. Empty means none are offered.
	//
	// Splatoon 3 uses ICE rather than the Pia NAT-check pair, so internal/natcheck
	// is not involved in this title and these addresses are what let two consoles
	// behind different NATs reach each other. They must be reachable FROM THE
	// CONSOLE, not merely from the server -- a server-side address here produces
	// "the lobby fills but the match never starts", which is indistinguishable
	// from a matchmaking bug when read from this side.
	//
	// Peers on one LAN find each other without any of this, which is why an empty
	// list is a warning rather than an error.
	ICEServers []ICEServer `yaml:"ice_servers"`
}

// ICEServer is one STUN or TURN server offered to a console.
type ICEServer struct {
	// Kind is "stun" or "turn".
	Kind string `yaml:"kind"`
	// Host is the address the CONSOLE must be able to reach.
	Host string `yaml:"host"`
	Port int    `yaml:"port"`
	// Protocol is "udp", "tcp" or "tls". Defaults to udp.
	Protocol string `yaml:"protocol"`

	// Username and Password are TURN's long-term credentials. Ignored for stun.
	//
	// A static credential rather than coturn's time-limited HMAC scheme: this is
	// one operator's server for consoles they own, and the shared-secret variant
	// would need the console to derive the same username, which nothing here can
	// make it do.
	Username string `yaml:"username"`
	Password string `yaml:"password"`
}

// Dashboard configures the operator's web view.
type Dashboard struct {
	Listen string `yaml:"listen"`

	// Token is a shared secret required on every request. Empty means no
	// authentication at all.
	//
	// Nothing in phase 1 authenticates a PERSON -- every credential in the
	// stack belongs to a console -- so there is no user account to check
	// against. A shared token is the honest minimum: it keeps the Splatfest
	// controls off the open network without pretending to be an identity
	// system. See TODO.md for what a real answer would look like.
	Token string `yaml:"token"`
}

// Stubs configures the catch-all server.
type Stubs struct {
	Listen string `yaml:"listen"`
	// LogBodies logs request bodies. Useful exactly once, while working out
	// what an unimplemented endpoint is being asked for.
	LogBodies bool `yaml:"log_bodies"`

	// BCATProbe answers the bcat-* hosts, logging every request in full, rather
	// than leaving them null-routed on the console.
	//
	// An EXPERIMENT, not a service. SwitchNet cannot serve BCAT: the `list`
	// response body is undocumented, and whether the console verifies the
	// container's RSA signature is unknown. This establishes only whether the
	// console reaches us at all and what it asks for, which is the first record
	// anyone would have of either. See TODO.md 2.4.
	//
	// Off by default and it should stay that way outside the experiment:
	// answering 200-empty to a console expecting content turns an immediate,
	// legible failure into a hang.
	BCATProbe bool `yaml:"bcat_probe"`
}

// NATCheck configures the UDP NAT-check responder.
type NATCheck struct {
	// PrimaryListen is the main socket. Nintendo uses UDP 10025.
	PrimaryListen string `yaml:"primary_listen"`
	// SecondaryListen is the "different port, same address" socket, used for
	// NAT filtering detection. Nintendo uses UDP 10125.
	SecondaryListen string `yaml:"secondary_listen"`
	// AlternateListen is a socket on a DIFFERENT ADDRESS, used for message
	// type 2. Leave empty if you only have one address.
	AlternateListen string `yaml:"alternate_listen"`
}

// Log configures logging.
type Log struct {
	// Level is one of debug, info, warn, error.
	Level string `yaml:"level"`
	// Format is "text" or "json".
	Format string `yaml:"format"`
}

// Default returns the configuration SwitchNet uses when nothing is set. The
// Nintendo-facing values (issuers, audiences, JWKS URLs) match what a real
// console sees, because captures are compared against them.
func Default() Config {
	return Config{
		Network: Network{
			EdgeTLSPort: 443,
		},
		DNS: DNS{
			Listen: ":53",
			// Quad9 and Cloudflare. Neither is special; they are simply not the
			// operator's router, which would be a loop if the router forwards
			// here.
			Upstream:        []string{"9.9.9.9:53", "1.1.1.1:53"},
			TTL:             60,
			UpstreamTimeout: 3,
		},
		Database: Database{
			URL:         "postgres://switchnet:switchnet@localhost:5432/switchnet?sslmode=disable",
			MaxConns:    10,
			AutoMigrate: true,
		},
		Redis: Redis{
			Channel: "switchnet:presence",
		},
		Keys: Keys{
			Dir:               "certs/keys",
			GenerateIfMissing: true,
		},
		DAuth: DAuth{
			Listen:              ":8081",
			Issuer:              "dauth-lp1.ndas.srv.nintendo.net",
			JWKSURL:             "https://dcert-lp1.ndas.srv.nintendo.net/keys",
			TokenTTL:            86400,
			ChallengeTTL:        60,
			ClientCertHeader:    "X-SwitchNet-Client-Cert",
			ClientCertPEMHeader: "X-SwitchNet-Client-Cert-PEM",
		},
		AAuth: AAuth{
			Listen:           ":8082",
			Issuer:           "aauth-lp1.ndas.srv.nintendo.net",
			JWKSURL:          "https://acert-lp1.ndas.srv.nintendo.net/keys",
			TokenTTL:         86400,
			OnlinePlayPolicy: "FREE",
			PolicyHandler:    "GAME_SERVER",
		},
		BAAS: BAAS{
			Listen:             ":8083",
			Issuer:             "https://e0d67c509fb203858ebcb2fe3f88c2aa.baas.nintendo.com",
			Audience:           "ed9e2f05d286f7b8",
			AccessTokenJWKSURL: "https://e0d67c509fb203858ebcb2fe3f88c2aa.baas.nintendo.com/1.0.0/internal_certificates",
			IDTokenJWKSURL:     "https://e0d67c509fb203858ebcb2fe3f88c2aa.baas.nintendo.com/1.0.0/certificates",
			AccessTokenTTL:     10800,
			IDTokenTTL:         10800,
			FriendCodeCooldown: 86400,
			PresenceTTL:        300,
			ConsolePresenceTTL: 300,
			ImageDir:           "data/images",
			ImageBaseURL:       "https://cdn-image-e0d67c509fb203858ebcb2fe3f88c2aa.baas.nintendo.com/images",
			MaxImageBytes:      1 << 20,
			AllowRegistration:  true,
		},
		Stubs: Stubs{
			Listen: ":8084",
		},
		Dashboard: Dashboard{
			Listen: ":8087",
		},
		NPLN: NPLN{
			Listen:         ":8085",
			RestListen:     ":8086",
			GamesyncListen: ":7575",
			Tenant:         "dce9377b",
			Issuer:         "https://t-dce9377b-lp1.lp1.t.npln.srv.nintendo.net",
			JWKSURL:        "https://t-dce9377b-lp1.lp1.t.npln.srv.nintendo.net/1.0.0/certificates",
			AccessTokenTTL: 10800,
		},
		NATCheck: NATCheck{
			PrimaryListen:   ":10025",
			SecondaryListen: ":10125",
		},
		Log: Log{
			Level:  "info",
			Format: "text",
		},
	}
}

// Load reads the YAML file at path, applies environment overrides, and
// validates the result. An empty path skips the file and uses defaults plus
// environment overrides, which is how the containers in docker-compose run.
func Load(path string) (Config, error) {
	cfg := Default()

	if path != "" {
		data, err := os.ReadFile(path)
		if err != nil {
			return cfg, fmt.Errorf("read config %s: %w", path, err)
		}
		dec := yaml.NewDecoder(strings.NewReader(string(data)))
		dec.KnownFields(true)
		if err := dec.Decode(&cfg); err != nil {
			return cfg, fmt.Errorf("parse config %s: %w", path, err)
		}
	}

	applyEnv(&cfg)

	if err := cfg.Validate(); err != nil {
		return cfg, err
	}
	return cfg, nil
}

// applyEnv overrides configuration from the environment. Every override is
// named SWITCHNET_* so it cannot collide with anything else in a container.
func applyEnv(c *Config) {
	envStr("SWITCHNET_PUBLIC_IP", &c.Network.PublicIP)
	envStr("SWITCHNET_NATCHECK_SECONDARY_IP", &c.Network.NATCheckSecondaryIP)
	envInt("SWITCHNET_EDGE_TLS_PORT", &c.Network.EdgeTLSPort)

	envStr("SWITCHNET_DNS_LISTEN", &c.DNS.Listen)
	envStrSlice("SWITCHNET_DNS_UPSTREAM", &c.DNS.Upstream)
	envInt("SWITCHNET_DNS_TTL", &c.DNS.TTL)
	envInt("SWITCHNET_DNS_UPSTREAM_TIMEOUT", &c.DNS.UpstreamTimeout)
	envBool("SWITCHNET_DNS_LOG_FORWARDED", &c.DNS.LogForwarded)

	envStr("SWITCHNET_DATABASE_URL", &c.Database.URL)
	envBool("SWITCHNET_DATABASE_AUTO_MIGRATE", &c.Database.AutoMigrate)

	envStr("SWITCHNET_REDIS_URL", &c.Redis.URL)
	envStr("SWITCHNET_REDIS_CHANNEL", &c.Redis.Channel)

	envStr("SWITCHNET_KEYS_DIR", &c.Keys.Dir)
	envBool("SWITCHNET_KEYS_GENERATE_IF_MISSING", &c.Keys.GenerateIfMissing)
	envStr("SWITCHNET_DEVICE_ID_SECRET", &c.Keys.DeviceIDSecret)

	envStr("SWITCHNET_DAUTH_LISTEN", &c.DAuth.Listen)
	envStr("SWITCHNET_DAUTH_ISSUER", &c.DAuth.Issuer)
	envStr("SWITCHNET_DAUTH_JWKS_URL", &c.DAuth.JWKSURL)

	envStr("SWITCHNET_AAUTH_LISTEN", &c.AAuth.Listen)
	envStr("SWITCHNET_AAUTH_ISSUER", &c.AAuth.Issuer)
	envStr("SWITCHNET_AAUTH_JWKS_URL", &c.AAuth.JWKSURL)
	envStr("SWITCHNET_AAUTH_ONLINE_PLAY_POLICY", &c.AAuth.OnlinePlayPolicy)
	envStr("SWITCHNET_AAUTH_POLICY_HANDLER", &c.AAuth.PolicyHandler)

	envStr("SWITCHNET_BAAS_LISTEN", &c.BAAS.Listen)
	envStr("SWITCHNET_BAAS_ISSUER", &c.BAAS.Issuer)
	envStr("SWITCHNET_BAAS_AUDIENCE", &c.BAAS.Audience)
	envStr("SWITCHNET_BAAS_IMAGE_DIR", &c.BAAS.ImageDir)
	envStr("SWITCHNET_BAAS_IMAGE_BASE_URL", &c.BAAS.ImageBaseURL)
	envInt("SWITCHNET_BAAS_PRESENCE_TTL", &c.BAAS.PresenceTTL)
	envInt("SWITCHNET_BAAS_CONSOLE_PRESENCE_TTL", &c.BAAS.ConsolePresenceTTL)
	envInt("SWITCHNET_BAAS_FRIEND_CODE_COOLDOWN", &c.BAAS.FriendCodeCooldown)
	envBool("SWITCHNET_BAAS_ALLOW_REGISTRATION", &c.BAAS.AllowRegistration)

	envStr("SWITCHNET_STUBS_LISTEN", &c.Stubs.Listen)

	envStr("SWITCHNET_DASHBOARD_LISTEN", &c.Dashboard.Listen)
	envStr("SWITCHNET_DASHBOARD_TOKEN", &c.Dashboard.Token)

	envStr("SWITCHNET_NPLN_LISTEN", &c.NPLN.Listen)
	envStr("SWITCHNET_NPLN_REST_LISTEN", &c.NPLN.RestListen)
	envStr("SWITCHNET_NPLN_GAMESYNC_LISTEN", &c.NPLN.GamesyncListen)
	envStr("SWITCHNET_NPLN_TENANT", &c.NPLN.Tenant)
	envStr("SWITCHNET_NPLN_ISSUER", &c.NPLN.Issuer)
	envStr("SWITCHNET_NPLN_JWKS_URL", &c.NPLN.JWKSURL)
	envBool("SWITCHNET_STUBS_LOG_BODIES", &c.Stubs.LogBodies)
	envBool("SWITCHNET_STUBS_BCAT_PROBE", &c.Stubs.BCATProbe)

	envStr("SWITCHNET_NATCHECK_PRIMARY_LISTEN", &c.NATCheck.PrimaryListen)
	envStr("SWITCHNET_NATCHECK_SECONDARY_LISTEN", &c.NATCheck.SecondaryListen)
	envStr("SWITCHNET_NATCHECK_ALTERNATE_LISTEN", &c.NATCheck.AlternateListen)

	envStr("SWITCHNET_LOG_LEVEL", &c.Log.Level)
	envStr("SWITCHNET_LOG_FORMAT", &c.Log.Format)
}

// Validate checks the configuration is usable. It rejects the mistakes that
// would otherwise fail much later, on a console, with an unhelpful error.
func (c Config) Validate() error {
	var errs []error

	if c.Network.PublicIP != "" && net.ParseIP(c.Network.PublicIP) == nil {
		errs = append(errs, fmt.Errorf("network.public_ip: %q is not an IP address", c.Network.PublicIP))
	}
	if ip := c.Network.NATCheckSecondaryIP; ip != "" {
		if net.ParseIP(ip) == nil {
			errs = append(errs, fmt.Errorf("network.natcheck_secondary_ip: %q is not an IP address", ip))
		} else if ip == c.Network.PublicIP {
			errs = append(errs, errors.New(
				"network.natcheck_secondary_ip is the same as network.public_ip: "+
					"Pia will de-duplicate the second NAT probe and NAT detection will never "+
					"complete. Use a second address, or leave it empty."))
		}
	}
	if c.Database.URL == "" {
		errs = append(errs, errors.New("database.url is required"))
	}
	if len(c.DNS.Upstream) == 0 {
		errs = append(errs, errors.New(
			"dns.upstream must list at least one resolver, or every name that is "+
				"not Nintendo's stops resolving for the consoles"))
	}
	for _, upstream := range c.DNS.Upstream {
		host, port, err := net.SplitHostPort(upstream)
		if err != nil {
			errs = append(errs, fmt.Errorf(
				"dns.upstream: %q must be host:port, e.g. 9.9.9.9:53", upstream))
			continue
		}
		if net.ParseIP(host) == nil {
			// A hostname here cannot be resolved: this IS the resolver.
			errs = append(errs, fmt.Errorf(
				"dns.upstream: %q must be an IP address, not a hostname — this "+
					"server is the resolver, so it cannot look one up", upstream))
		}
		if port == "" {
			errs = append(errs, fmt.Errorf("dns.upstream: %q has no port", upstream))
		}
		if c.Network.PublicIP != "" && host == c.Network.PublicIP {
			errs = append(errs, fmt.Errorf(
				"dns.upstream: %q is this server's own address, which is a loop. "+
					"Every non-Nintendo lookup would time out.", upstream))
		}
	}
	if c.DNS.TTL < 0 {
		errs = append(errs, errors.New("dns.ttl must not be negative"))
	}
	if c.Keys.Dir == "" {
		errs = append(errs, errors.New("keys.dir is required"))
	}
	if c.BAAS.PresenceTTL <= 0 {
		errs = append(errs, errors.New("baas.presence_ttl must be positive"))
	}
	if c.BAAS.ConsolePresenceTTL <= 0 {
		errs = append(errs, errors.New("baas.console_presence_ttl must be positive"))
	}
	if c.BAAS.AccessTokenTTL <= 0 {
		errs = append(errs, errors.New("baas.access_token_ttl must be positive"))
	}
	switch c.AAuth.OnlinePlayPolicy {
	case "FREE", "MEMBERSHIP_REQUIRED":
	default:
		errs = append(errs, fmt.Errorf(
			"aauth.online_play_policy: %q must be FREE or MEMBERSHIP_REQUIRED", c.AAuth.OnlinePlayPolicy))
	}
	switch c.AAuth.PolicyHandler {
	case "SYSTEM", "GAME_SERVER":
	default:
		errs = append(errs, fmt.Errorf(
			"aauth.policy_handler: %q must be SYSTEM or GAME_SERVER", c.AAuth.PolicyHandler))
	}
	for id, o := range c.AAuth.TitleOverrides {
		if len(id) != 16 {
			errs = append(errs, fmt.Errorf(
				"aauth.title_overrides: %q is not a 16-hex-digit title id", id))
		}
		switch o.OnlinePlayPolicy {
		case "", "FREE", "MEMBERSHIP_REQUIRED":
		default:
			errs = append(errs, fmt.Errorf(
				"aauth.title_overrides[%s].online_play_policy: %q is invalid", id, o.OnlinePlayPolicy))
		}
	}
	for i, s := range c.NPLN.ICEServers {
		switch s.Kind {
		case "stun", "turn":
		default:
			errs = append(errs, fmt.Errorf(
				"npln.ice_servers[%d].kind: %q must be stun or turn", i, s.Kind))
		}
		if s.Host == "" {
			errs = append(errs, fmt.Errorf("npln.ice_servers[%d].host is required", i))
		}
		if s.Port <= 0 || s.Port > 65535 {
			errs = append(errs, fmt.Errorf(
				"npln.ice_servers[%d].port: %d is not a port", i, s.Port))
		}
		switch s.Protocol {
		case "", "udp", "tcp", "tls":
		default:
			errs = append(errs, fmt.Errorf(
				"npln.ice_servers[%d].protocol: %q must be udp, tcp or tls", i, s.Protocol))
		}
	}

	return errors.Join(errs...)
}

// PolicyFor returns the online play policy and handler for a title id.
func (a AAuth) PolicyFor(titleID string) (policy, handler string) {
	policy, handler = a.OnlinePlayPolicy, a.PolicyHandler
	if o, ok := a.TitleOverrides[strings.ToLower(titleID)]; ok {
		if o.OnlinePlayPolicy != "" {
			policy = o.OnlinePlayPolicy
		}
		if o.PolicyHandler != "" {
			handler = o.PolicyHandler
		}
	}
	return policy, handler
}

func envStr(key string, dst *string) {
	if v, ok := os.LookupEnv(key); ok {
		*dst = v
	}
}

func envInt(key string, dst *int) {
	v, ok := os.LookupEnv(key)
	if !ok {
		return
	}
	if n, err := strconv.Atoi(v); err == nil {
		*dst = n
	}
}

// envStrSlice reads a comma-separated list, for settings that are a list in YAML
// but have to be one environment variable.
func envStrSlice(key string, dst *[]string) {
	v, ok := os.LookupEnv(key)
	if !ok {
		return
	}
	var out []string
	for _, part := range strings.Split(v, ",") {
		if part = strings.TrimSpace(part); part != "" {
			out = append(out, part)
		}
	}
	*dst = out
}

func envBool(key string, dst *bool) {
	v, ok := os.LookupEnv(key)
	if !ok {
		return
	}
	if b, err := strconv.ParseBool(v); err == nil {
		*dst = b
	}
}
