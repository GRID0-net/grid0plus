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
	"net/url"
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
	SCSI      SCSI      `yaml:"scsi"`
	Stubs     Stubs     `yaml:"stubs"`
	NPLN      NPLN      `yaml:"npln"`
	BCAT      BCAT      `yaml:"bcat"`
	Dashboard Dashboard `yaml:"dashboard"`
	Toolbox   Toolbox   `yaml:"toolbox"`
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

// DNS configures the dnsmasq/unbound configuration generators
// (`switchnetctl dnsmasq`/`unbound`). SwitchNet does not run its own DNS
// server; redirection is done by a resolver the operator already runs.
type DNS struct {
	// TTL of the answers SwitchNet synthesises, in seconds. Short on purpose:
	// switching a console back to Nintendo should take effect in a minute, not
	// after an hour of cached records.
	TTL int `yaml:"ttl"`
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

	// LoginAttemptsPerWindow caps how many /1.0.0/login (and federation) login
	// attempts one client address gets per LoginWindowSeconds. 0 disables the
	// limit entirely.
	//
	// A device account's password is generated, high-entropy and never
	// user-chosen, so unbounded guessing is a much smaller practical risk here
	// than for a password a person picked -- but "much smaller" is not "none",
	// and there was previously no limit of any kind on this endpoint. The
	// default is generous enough that a household sharing one NAT address
	// during ordinary, repeated wrong-password mistakes will not be affected.
	LoginAttemptsPerWindow int `yaml:"login_attempts_per_window"`
	// LoginWindowSeconds is the sliding window LoginAttemptsPerWindow counts
	// over.
	LoginWindowSeconds int `yaml:"login_window_seconds"`

	// LogBodies logs request and response bodies on the access log line.
	// Useful exactly once, to see the real bytes a console sent or received
	// without a console-side capture -- carries real tokens and device
	// account passwords, so far too chatty and sensitive to leave on. See
	// internal/stubs.Config.LogBodies for the same pattern elsewhere.
	LogBodies bool `yaml:"log_bodies"`
}

// SCSI configures the OLSC (Online Save Cloud) cloud-save server (phase 4).
//
// Reimplements storage.lp1.scsi.srv.nintendo.net and
// permission.lp1.sata.srv.nintendo.net. See internal/scsi for the endpoints.
type SCSI struct {
	Listen string `yaml:"listen"`

	// PublicURL is this service's own externally-reachable base URL, used to
	// build the signed_uri a component-file upload/download is reached
	// through. Must be a host in the redirect list (storage.lp1.scsi.srv.
	// nintendo.net), the same requirement ImageBaseURL documents for BAAS's
	// own profile images.
	PublicURL string `yaml:"public_url"`

	// StorageDir is where save data blobs are written, on the same box
	// already running SwitchNet -- the default ROADMAP.md's phase 4 section
	// picks: no new recurring cost, bounded by whatever disk the box has.
	StorageDir string `yaml:"storage_dir"`

	// PerUserQuotaBytes caps one BAAS user's total stored save data across
	// every title and every archive. Zero means unlimited: Validate permits
	// it rather than requiring a number, since a small private deployment
	// the operator already trusts every console on may reasonably accept
	// that risk -- but ROADMAP.md §4.2 is explicit that a hard cap should
	// normally exist, so one console cannot fill the disk and take the rest
	// of SwitchNet down with it. Set this in any real deployment.
	PerUserQuotaBytes int64 `yaml:"per_user_quota_bytes"`
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
	// GamesyncListen is where the document/watch service listens INTERNALLY.
	//
	// Not 7575. The console reaches gamesync on 7575, and that port belongs to
	// the nginx edge -- which terminates TLS there and grpc_passes to this
	// address. Binding the backend to 7575 as well means nginx cannot start,
	// and the two listeners racing for it is a much worse first symptom than a
	// port that reads oddly in config.
	GamesyncListen string `yaml:"gamesync_listen"`

	// Tenant is the NPLN tenant id. Splatoon 3's is t-dce9377b-lp1: the full
	// form a real Nintendo access token carries in its tid, and the one
	// NextendoNetwork/splatoon-3 records the console sending as
	// npln-tenant-id. It names this tenant's resources ("tenants/<id>/...")
	// and goes into every token's tid. The short "dce9377b" is still accepted
	// on incoming calls -- see npln.sameTenant.
	Tenant string `yaml:"tenant"`
	// Issuer is the 'iss' of the ES256 access tokens this server signs.
	Issuer string `yaml:"issuer"`
	// JWKSURL is where those tokens' 'jku' points.
	JWKSURL string `yaml:"jwks_url"`
	// AccessTokenTTL is the lifetime of an NPLN access token, in seconds.
	AccessTokenTTL int `yaml:"access_token_ttl"`

	// PlayersPerMatch is how many players a match takes for any mode
	// mode_players does not name.
	//
	// Two, not eight. This is a server for a handful of consoles, and waiting
	// for a faithful 4v4 means never playing.
	PlayersPerMatch int `yaml:"players_per_match"`

	// ModePlayers overrides players_per_match for one matchmaking mode.
	//
	// Keyed on the mode name the game sends in the ticket's matchmaking_config,
	// which nothing public documents — so this is empty by default and every
	// unknown mode is logged, and that log is how an operator learns the names
	// to put here. Turf War and Anarchy are 4v4 (eight), Salmon Run is four.
	ModePlayers map[string]int `yaml:"mode_players"`

	// TicketTTL is how long a matchmaking ticket waits before giving up, in
	// seconds. A ticket that never resolves shows the player a spinner with no
	// way out.
	TicketTTL int `yaml:"ticket_ttl"`

	// IceServers are the STUN and TURN servers handed to a console for NAT
	// traversal. Splatoon 3 uses ICE, not the Pia NAT-check pair, so
	// internal/natcheck is not involved in this title.
	//
	// **The addresses must be reachable FROM THE CONSOLE**, not merely from
	// this server. Wrong ones produce "the lobby fills but the match never
	// starts", which is indistinguishable from a matchmaking bug when read from
	// the server side. Empty is legitimate: peers on one LAN reach each other
	// without ICE, and `switchnetctl check` warns rather than refuses.
	IceServers []IceServer `yaml:"ice_servers"`

	// FrontlineHost is the fro-N hostname the console is told to open its
	// persistent connection to.
	//
	// It MUST be a hostname internal/nintendo/hosts.go redirects. Naming one it
	// does not sends the console's frontline connection to real Nintendo, where
	// it succeeds — and then fails in a way that looks like SwitchNet's fault.
	// internal/nplnrest refuses to start rather than allow it.
	FrontlineHost string `yaml:"frontline_host"`

	// SessionHost and SessionPort are what a match puts in GameSession.host
	// and .port: where each console opens its gamesync session for that match.
	//
	// The tenant host on 443, not gamesync's own :7575. NextendoNetwork/
	// splatoon-3 measured the difference on hardware: announced as a literal
	// address on :7575 the session channel completes TLS and then never sends
	// a request (2321-4992); announced as the tenant name on :443 it works.
	// nginx sends the Gamesync service path on the tenant host to gamesync
	// (see switchnetctl's grpcServerBlock), so nothing else has to move. Like
	// FrontlineHost, it must be a name the console's hosts file redirects.
	SessionHost string `yaml:"session_host"`
	SessionPort int    `yaml:"session_port"`

	// Rotation is the stage and mode rotation the Schedule service serves.
	//
	// Empty is legitimate: the schedule handlers then answer with no schedules,
	// which is what the game sees between seasons on a real server, and the
	// absence is logged once per kind.
	Rotation Rotation `yaml:"rotation"`

	// RestRecordDir, when set, is a directory the JSON front end writes every
	// request and response to.
	//
	// This is how the undocumented vermillion, penne and frontline shapes get
	// settled: point it somewhere, put a console through one boot, and the
	// transcript is on disk. Off by default because it writes whatever the
	// console sends, headers included — credentials are redacted, bodies are
	// not.
	RestRecordDir string `yaml:"rest_record_dir"`

	// AdminListen is the operator API for the matchmaking controls: the queue,
	// force-start, mode sizes, cancelling a ticket. The dashboard is a separate
	// process from this one and the queue lives here, so this is how the two
	// meet.
	//
	// NOT one of the Nintendo hostnames and never reached by a console. Bind it
	// to loopback or a private interface.
	AdminListen string `yaml:"admin_listen"`

	// AdminToken is a shared secret required on every admin request. Empty
	// means the admin API is NOT SERVED AT ALL.
	//
	// Off rather than open, because this API starts matches and changes how
	// many players one takes. An operator who has not set a token has not opted
	// in, and there is no version of these controls that is safe to hand to
	// whoever can reach the port.
	AdminToken string `yaml:"admin_token"`
}

// Rotation is the stage and mode rotation Splatoon 3's lobby shows.
//
// **The identifiers are Splatoon 3's own internal numbering, and nothing public
// documents them** — so they are operator input rather than a table in
// SwitchNet's source. Given as strings so that a typo is reported with the
// offending value: stage 0 is a plausible valid id, so a zero produced by a
// failed parse would be indistinguishable from an intended one.
//
// The mechanics around them — two-hour slot alignment, windowing, ordering, the
// etag — are SwitchNet's and live in internal/toyohr.
type Rotation struct {
	// RegularStages are the stage ids Turf War rotates through, in order.
	RegularStages []string `yaml:"regular_stages"`
	// BankaraRules are the rule ids Anarchy rotates through, in order.
	BankaraRules []string `yaml:"bankara_rules"`
	// BankaraStages are the stage ids Anarchy rotates through, in order.
	BankaraStages []string `yaml:"bankara_stages"`
	// StagesPerSlot is how many stages one slot offers. Zero means two, which
	// is what a real rotation offers.
	StagesPerSlot int `yaml:"stages_per_slot"`
}

// IceServer is one STUN or TURN server offered to a console.
type IceServer struct {
	// Kind is "stun" or "turn".
	Kind string `yaml:"kind"`
	// Host is an address or hostname the CONSOLE can reach.
	Host string `yaml:"host"`
	Port int    `yaml:"port"`
	// Protocol is "udp", "tcp" or "tls". Empty means udp.
	Protocol string `yaml:"protocol"`
	// Username and Password are TURN's long-term credentials. Ignored for stun.
	Username string `yaml:"username"`
	Password string `yaml:"password"`
}

// BCAT configures Splatfest content packs.
type BCAT struct {
	// Converter is a command that turns a source asset into a game format.
	//
	// SwitchNet does not convert textures itself, and that is deliberate: a
	// BFRES texture in the wrong compression or the wrong Tegra swizzle LOADS
	// and renders as garbage, on a console, with nothing on the server to look
	// at. Switch Toolbox and the BinaryTextureLibrary family already do it
	// correctly. See internal/bcat/convert.go for the contract this command
	// has to meet.
	//
	// Empty means there is no converter, and a source asset is then a build
	// error rather than a file packed as-is.
	Converter string `yaml:"converter"`

	// ConverterTimeout bounds one conversion, in seconds. A converter that
	// hangs would otherwise hang the build — and on the dashboard, a request
	// that never answers.
	ConverterTimeout int `yaml:"converter_timeout"`

	// SeedDir is where `switchnetctl fest build` writes its seed folders, and
	// where the dashboard serves them from for the console-side installer.
	//
	// Empty means the seed endpoints answer 404. Doing the install by hand with
	// JKSV works and does not scale past the second fest, which is why the
	// installer exists -- see switch/bcat-installer/.
	SeedDir string `yaml:"seed_dir"`

	// SubstituteFestID rewrites {FEST} in an asset's in-pack path to the fest's
	// own id.
	//
	// A real custom pack had every internal file named for the fest it was
	// DERIVED from while announcing a different one, and nobody has established
	// what the game keys on. On is the consistent choice; off keeps whatever
	// names the source files carry. Either way the decision lives in one place
	// — see internal/bcat.Naming and docs/QUESTIONS.md.
	SubstituteFestID bool `yaml:"substitute_fest_id"`
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

	// NPLNAdminURL and NPLNAdminToken reach cmd/npln's matchmaking controls,
	// and must match that server's npln.admin_listen and npln.admin_token.
	//
	// The queue lives in the npln process. Without these the dashboard's
	// matchmaking panel says it is not configured -- deliberately, rather than
	// showing an empty queue that reads as "nobody is playing".
	NPLNAdminURL   string `yaml:"npln_admin_url"`
	NPLNAdminToken string `yaml:"npln_admin_token"`
}

// Stubs configures the catch-all server.
type Stubs struct {
	Listen string `yaml:"listen"`
	// LogBodies logs request bodies. Useful exactly once, while working out
	// what an unimplemented endpoint is being asked for.
	LogBodies bool `yaml:"log_bodies"`

	// BCATRecordDir is where the BCAT probe writes a transcript of what the
	// console asked BCAT for. Empty means it only logs, and writes no
	// transcript to disk. See internal/bcat.Probe.
	BCATRecordDir string `yaml:"bcat_record_dir"`
}

// Toolbox configures the public API the SwitchNet Toolbox homebrew app
// (github.com/n-popescu/switchnet-nro) talks to. See internal/toolbox's
// package comment for why this exists as its own service rather than a
// route on stubs or dashboard: it is the one service any console reaches
// with no credential at all, and it holds a GitHub token specifically so
// that credential never has to live in a binary distributed to consoles.
type Toolbox struct {
	// Loopback only, same reasoning as dauth.listen: nginx is the only thing
	// that reaches this directly, over its own dedicated TLS port (see
	// deploy/nginx.toolbox.conf) rather than the Nintendo-hostname SNI map --
	// this service answers a homebrew app, not a Nintendo endpoint, and does
	// not belong in internal/nintendo/hosts.go's redirect list.
	Listen string `yaml:"listen"`

	// GitHubOwner and GitHubRepo identify the private switchnet-nro
	// repository /updates/* reads releases from. Leave both empty to serve
	// /status only and have /updates/* answer 503.
	GitHubOwner string `yaml:"github_owner"`
	GitHubRepo  string `yaml:"github_repo"`

	// GitHubToken is a fine-grained personal access token scoped to ONLY
	// github_owner/github_repo, permission Contents: Read-only, nothing
	// else. Generate it at github.com/settings/personal-access-tokens --
	// never a classic token, which cannot be scoped to one repository.
	//
	// This is the one credential in all of SwitchNet that exists specifically
	// so a *client* binary never has to hold one. Put the real value in
	// config/config.yaml (gitignored) or SWITCHNET_TOOLBOX_GITHUB_TOKEN, never
	// here.
	GitHubToken string `yaml:"github_token"`

	// PublicBaseURL is this service's own address as a console reaches it,
	// e.g. "https://203.0.113.5:8443" -- the same public IP as
	// network.public_ip, fixed port 8443 (deploy/nginx.toolbox.conf), always
	// https (that nginx block has no plain-http listener). Used to build
	// /updates/latest's browser_download_url. Deliberately not inferred from
	// the incoming request: this service sits behind nginx terminating TLS
	// and proxying over plain HTTP, so the request this handler actually
	// sees never carries the real scheme, and nginx's Host forwarding drops
	// the port too -- see internal/toolbox's package comment. Required, with
	// the three fields above, for /updates/* to answer anything but 503.
	PublicBaseURL string `yaml:"public_base_url"`

	// ReleaseCacheTTLSeconds bounds how often releases/latest is actually
	// called on GitHub. Several consoles checking for an update within the
	// same window should cost one request, not one per console. Zero means
	// no caching -- fine at this traffic level, but every check then costs a
	// real GitHub API call.
	ReleaseCacheTTLSeconds int `yaml:"release_cache_ttl_seconds"`
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
			TTL: 60,
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
			Listen:              "127.0.0.1:8081",
			Issuer:              "dauth-lp1.ndas.srv.nintendo.net",
			JWKSURL:             "https://dcert-lp1.ndas.srv.nintendo.net/keys",
			TokenTTL:            86400,
			ChallengeTTL:        60,
			ClientCertHeader:    "X-SwitchNet-Client-Cert",
			ClientCertPEMHeader: "X-SwitchNet-Client-Cert-PEM",
		},
		AAuth: AAuth{
			Listen:           "127.0.0.1:8082",
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
			ImageBaseURL:       "https://cdn-image-e0d67c509fb203858ebcb2fe3f88c2aa.baas.nintendo.com",
			MaxImageBytes:      1 << 20,
			AllowRegistration:  true,

			LoginAttemptsPerWindow: 20,
			LoginWindowSeconds:     300,
		},
		SCSI: SCSI{
			Listen:     "127.0.0.1:8091",
			PublicURL:  "https://storage.lp1.scsi.srv.nintendo.net",
			StorageDir: "data/save-archives",
			// 1 GiB: generous for save data (real Switch saves are almost
			// always well under 100 MiB), small enough that even a handful of
			// consoles cannot meaningfully dent a modern disk.
			PerUserQuotaBytes: 1 << 30,
		},
		Stubs: Stubs{
			Listen: "127.0.0.1:8084",
		},
		Dashboard: Dashboard{
			Listen: ":8087",
		},
		NPLN: NPLN{
			Listen:         "127.0.0.1:8085",
			RestListen:     "127.0.0.1:8086",
			GamesyncListen: "127.0.0.1:8088",
			Tenant:         "t-dce9377b-lp1",
			Issuer:         "https://t-dce9377b-lp1.lp1.t.npln.srv.nintendo.net",
			JWKSURL:        "https://t-dce9377b-lp1.lp1.t.npln.srv.nintendo.net/1.0.0/certificates",
			AccessTokenTTL: 10800,
			// Two, so two friends can start a match. See the field comment.
			PlayersPerMatch: 2,
			TicketTTL:       180,
			// fro-3 because that is the member the reference server resolves
			// the frontline to. Any of fro-1..4 is redirected and carries a
			// certificate SAN; this one is the least likely to surprise.
			FrontlineHost: "fro-3.hac.lp1.penne.srv.nintendo.net",
			SessionHost:   "t-dce9377b-lp1.lp1.t.npln.srv.nintendo.net",
			SessionPort:   443,
		},
		BCAT: BCAT{
			ConverterTimeout: 120,
			SubstituteFestID: true,
		},
		Toolbox: Toolbox{
			Listen:                 "127.0.0.1:8090",
			ReleaseCacheTTLSeconds: 120,
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

	envInt("SWITCHNET_DNS_TTL", &c.DNS.TTL)

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
	envInt("SWITCHNET_BAAS_LOGIN_ATTEMPTS_PER_WINDOW", &c.BAAS.LoginAttemptsPerWindow)
	envInt("SWITCHNET_BAAS_LOGIN_WINDOW_SECONDS", &c.BAAS.LoginWindowSeconds)
	envBool("SWITCHNET_BAAS_LOG_BODIES", &c.BAAS.LogBodies)

	envStr("SWITCHNET_SCSI_LISTEN", &c.SCSI.Listen)
	envStr("SWITCHNET_SCSI_PUBLIC_URL", &c.SCSI.PublicURL)
	envStr("SWITCHNET_SCSI_STORAGE_DIR", &c.SCSI.StorageDir)
	envInt64("SWITCHNET_SCSI_PER_USER_QUOTA_BYTES", &c.SCSI.PerUserQuotaBytes)

	envStr("SWITCHNET_STUBS_LISTEN", &c.Stubs.Listen)
	envStr("SWITCHNET_STUBS_BCAT_RECORD_DIR", &c.Stubs.BCATRecordDir)

	envStr("SWITCHNET_BCAT_CONVERTER", &c.BCAT.Converter)
	envStr("SWITCHNET_BCAT_SEED_DIR", &c.BCAT.SeedDir)
	envInt("SWITCHNET_BCAT_CONVERTER_TIMEOUT", &c.BCAT.ConverterTimeout)

	envStr("SWITCHNET_DASHBOARD_LISTEN", &c.Dashboard.Listen)
	envStr("SWITCHNET_DASHBOARD_TOKEN", &c.Dashboard.Token)
	envStr("SWITCHNET_DASHBOARD_NPLN_ADMIN_URL", &c.Dashboard.NPLNAdminURL)
	envStr("SWITCHNET_DASHBOARD_NPLN_ADMIN_TOKEN", &c.Dashboard.NPLNAdminToken)

	envStr("SWITCHNET_NPLN_LISTEN", &c.NPLN.Listen)
	envStr("SWITCHNET_NPLN_REST_LISTEN", &c.NPLN.RestListen)
	envStr("SWITCHNET_NPLN_TENANT", &c.NPLN.Tenant)
	envStr("SWITCHNET_NPLN_ISSUER", &c.NPLN.Issuer)
	envStr("SWITCHNET_NPLN_JWKS_URL", &c.NPLN.JWKSURL)
	envStr("SWITCHNET_NPLN_GAMESYNC_LISTEN", &c.NPLN.GamesyncListen)
	envStr("SWITCHNET_NPLN_FRONTLINE_HOST", &c.NPLN.FrontlineHost)
	envStr("SWITCHNET_NPLN_SESSION_HOST", &c.NPLN.SessionHost)
	envStr("SWITCHNET_NPLN_REST_RECORD_DIR", &c.NPLN.RestRecordDir)
	envInt("SWITCHNET_NPLN_PLAYERS_PER_MATCH", &c.NPLN.PlayersPerMatch)
	envInt("SWITCHNET_NPLN_TICKET_TTL", &c.NPLN.TicketTTL)
	envStr("SWITCHNET_NPLN_ADMIN_LISTEN", &c.NPLN.AdminListen)
	envStr("SWITCHNET_NPLN_ADMIN_TOKEN", &c.NPLN.AdminToken)
	envBool("SWITCHNET_STUBS_LOG_BODIES", &c.Stubs.LogBodies)

	envStr("SWITCHNET_TOOLBOX_LISTEN", &c.Toolbox.Listen)
	envStr("SWITCHNET_TOOLBOX_GITHUB_OWNER", &c.Toolbox.GitHubOwner)
	envStr("SWITCHNET_TOOLBOX_GITHUB_REPO", &c.Toolbox.GitHubRepo)
	envStr("SWITCHNET_TOOLBOX_GITHUB_TOKEN", &c.Toolbox.GitHubToken)
	envStr("SWITCHNET_TOOLBOX_PUBLIC_BASE_URL", &c.Toolbox.PublicBaseURL)
	envInt("SWITCHNET_TOOLBOX_RELEASE_CACHE_TTL_SECONDS", &c.Toolbox.ReleaseCacheTTLSeconds)

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
		parsed := net.ParseIP(ip)
		if parsed == nil {
			errs = append(errs, fmt.Errorf("network.natcheck_secondary_ip: %q is not an IP address", ip))
		} else if parsed.Equal(net.ParseIP(c.Network.PublicIP)) {
			// Parsed IP equality, not a string compare: two different textual
			// forms of the same address (an IPv4-mapped IPv6 form, non-canonical
			// zero-padding, and so on) are the SAME mistake this check exists to
			// catch. There is no second line of defense once this passes:
			// redirection happens off the console, in a resolver SwitchNet does
			// not run, so a string-equality false pass here would not surface
			// until a console tried peer-to-peer play and NAT detection quietly
			// never completed.
			errs = append(errs, errors.New(
				"network.natcheck_secondary_ip is the same as network.public_ip: "+
					"Pia will de-duplicate the second NAT probe and NAT detection will never "+
					"complete. Use a second address, or leave it empty."))
		}
	}
	if c.Database.URL == "" {
		errs = append(errs, errors.New("database.url is required"))
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
	if c.SCSI.StorageDir == "" {
		errs = append(errs, errors.New("scsi.storage_dir is required"))
	}
	if c.SCSI.PerUserQuotaBytes < 0 {
		errs = append(errs, errors.New("scsi.per_user_quota_bytes must not be negative"))
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
	if c.NPLN.PlayersPerMatch == 1 {
		// One is not a small match, it is a match that starts the instant
		// anybody queues and then has nobody to play against.
		errs = append(errs, errors.New("npln.players_per_match must be at least 2"))
	}
	for mode, n := range c.NPLN.ModePlayers {
		if n < 2 {
			errs = append(errs, fmt.Errorf(
				"npln.mode_players[%s] is %d; a match needs at least 2 players", mode, n))
		}
	}
	for i, s := range c.NPLN.IceServers {
		switch s.Kind {
		case "stun", "turn":
		default:
			errs = append(errs, fmt.Errorf(
				"npln.ice_servers[%d].kind: %q must be stun or turn", i, s.Kind))
		}
		switch s.Protocol {
		case "", "udp", "tcp", "tls":
		default:
			errs = append(errs, fmt.Errorf(
				"npln.ice_servers[%d].protocol: %q must be udp, tcp or tls", i, s.Protocol))
		}
		if s.Host == "" {
			errs = append(errs, fmt.Errorf("npln.ice_servers[%d].host is required", i))
		}
		if s.Port <= 0 || s.Port > 65535 {
			errs = append(errs, fmt.Errorf(
				"npln.ice_servers[%d].port: %d is not a port", i, s.Port))
		}
	}

	// toolbox.github_token without owner, repo and public_base_url (or any
	// one of those four without the other three) cannot work:
	// latestReleaseBody would call "https://api.github.com/repos//" or
	// similar and always fail, or /updates/latest would hand out a
	// browser_download_url nothing can resolve. Silently serving /status
	// while /updates/* mysteriously 502s (or 200s with a broken URL) is
	// exactly the "fails much later with an unhelpful error" this function
	// exists to catch instead.
	tb := c.Toolbox
	tbConfigured := tb.GitHubOwner != "" || tb.GitHubRepo != "" ||
		tb.GitHubToken != "" || tb.PublicBaseURL != ""
	if tbConfigured && (tb.GitHubOwner == "" || tb.GitHubRepo == "" ||
		tb.GitHubToken == "" || tb.PublicBaseURL == "") {
		errs = append(errs, errors.New(
			"toolbox.github_owner, toolbox.github_repo, toolbox.github_token and "+
				"toolbox.public_base_url must all be set together, or all left empty "+
				"to disable /updates/*"))
	}
	if tb.PublicBaseURL != "" {
		u, err := url.Parse(tb.PublicBaseURL)
		if err != nil || u.Scheme != "https" || u.Host == "" {
			errs = append(errs, fmt.Errorf(
				"toolbox.public_base_url: %q must be an absolute https:// URL "+
					"(e.g. \"https://203.0.113.5:8443\"), with no path", tb.PublicBaseURL))
		} else if u.Path != "" {
			errs = append(errs, fmt.Errorf(
				"toolbox.public_base_url: %q must not have a path; "+
					"handleUpdatesLatest appends /updates/download itself", tb.PublicBaseURL))
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

func envBool(key string, dst *bool) {
	v, ok := os.LookupEnv(key)
	if !ok {
		return
	}
	if b, err := strconv.ParseBool(v); err == nil {
		*dst = b
	}
}

func envInt64(key string, dst *int64) {
	v, ok := os.LookupEnv(key)
	if !ok {
		return
	}
	if n, err := strconv.ParseInt(v, 10, 64); err == nil {
		*dst = n
	}
}
