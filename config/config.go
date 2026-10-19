// Package config loads and validates GRID0+'s configuration.
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

// Config is the whole GRID0+ configuration.
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
	NEX       NEX       `yaml:"nex"`
	BCAT      BCAT      `yaml:"bcat"`
	Dashboard Dashboard `yaml:"dashboard"`
	Inklink   Inklink   `yaml:"inklink"`
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
// (`switchnetctl dnsmasq`/`unbound`). GRID0+ does not run its own DNS
// server; redirection is done by a resolver the operator already runs.
type DNS struct {
	// TTL of the answers GRID0+ synthesises, in seconds. Short on purpose:
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

	// BotAPI is the Discord bot's account API (internal/baas/botapi.go),
	// the only way to create a person's account.
	BotAPI BotAPI `yaml:"bot_api"`
}

// BotAPI configures the Discord bot's account API.
type BotAPI struct {
	// Listen is where it listens. Keep it on loopback: the bot runs on the
	// same host, and nothing else should reach it. Empty turns it off.
	Listen string `yaml:"listen"`
	// Token is the shared secret the bot sends as a bearer token, at least
	// 32 characters (openssl rand -hex 32). The same value goes in the
	// bot's own configuration.
	Token string `yaml:"token"`
	// Splatoon3SaveTable is the path of the game's 128-byte save key table,
	// which turns on the save import (internal/splat3save). It is game key
	// material and stays an operator file. Empty turns the import off.
	Splatoon3SaveTable string `yaml:"splatoon3_save_table"`
	// Splatoon3SaveNames is an optional file of extra save field names, one
	// per line, cut from the game's own strings. It resolves fields newer
	// than the built-in list; anything still unknown is left out of an
	// import and kept for review. Empty uses the built-in list only.
	Splatoon3SaveNames string `yaml:"splatoon3_save_names"`
}

// SCSI configures the OLSC (Online Save Cloud) cloud-save server.
//
// Stands in for storage.hac.lp1.scsi.srv.nintendo.net and
// permission.lp1.sata.srv.nintendo.net. See internal/scsi for the endpoints.
type SCSI struct {
	Listen string `yaml:"listen"`

	// PublicURL is this service's own externally-reachable base URL, used to
	// build the signed_uri a component-file upload/download is reached
	// through. Must be a host in the redirect list (storage.lp1.scsi.srv.
	// nintendo.net), the same requirement ImageBaseURL documents for BAAS's
	// own profile images.
	PublicURL string `yaml:"public_url"`

	// StorageDir is where save data blobs are written, on the same machine already
	// running GRID0+. Local disk adds no recurring cost beyond the machine itself.
	StorageDir string `yaml:"storage_dir"`

	// PerUserQuotaBytes caps one BAAS user's total stored save data across
	// every title and every archive. Zero means unlimited: Validate permits
	// it, since a small private deployment may reasonably accept that risk, but
	// a hard cap should normally exist so one console cannot fill the disk and take
	// the rest of GRID0+ down with it. Set this in any real deployment.
	PerUserQuotaBytes int64 `yaml:"per_user_quota_bytes"`
}

// NPLN configures the Splatoon 3 game server.
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
	// form a Nintendo access token carries in its tid, and the one the console
	// sends as npln-tenant-id. It names this tenant's resources ("tenants/<id>/...")
	// and goes into every token's tid. The short "dce9377b" is still accepted
	// on incoming calls -- see npln.sameTenant.
	Tenant string `yaml:"tenant"`
	// AppID is the title id an access token's 'npln.app_id' claim carries.
	// Splatoon 3's is 0100c2500fc20000 -- a public, documented constant (every
	// Switch title id is), not a guessed Nintendo internal.
	AppID string `yaml:"app_id"`
	// Issuer is the 'iss' of the ES256 access tokens this server signs.
	Issuer string `yaml:"issuer"`
	// JWKSURL is where those tokens' 'jku' points.
	JWKSURL string `yaml:"jwks_url"`
	// AccessTokenTTL is the lifetime of an NPLN access token, in seconds. 28800
	// (8h) is what a real server issues: exp = iat + 28800.
	AccessTokenTTL int `yaml:"access_token_ttl"`

	// PlayersPerMatch is the most players a match takes for any mode
	// mode_players does not name.
	//
	// Eight, the real 4v4. For a versus mode it is a cap, not a target: a full
	// lobby starts at once, and below it an even count of players starts after
	// versus_grace_period, so a handful of consoles still get to play.
	PlayersPerMatch int `yaml:"players_per_match"`

	// ModePlayers overrides players_per_match for one matchmaking mode.
	//
	// Keyed on the mode name the game sends in the ticket's matchmaking_config.
	// The default holds Splatoon 3's three Salmon Run configs at four; every
	// versus mode is eight, players_per_match. Entries in config.yaml are
	// added to the default, so an operator only lists what they change.
	ModePlayers map[string]int `yaml:"mode_players"`

	// CoopModes names the modes formed like Salmon Run -- one team, not two,
	// no even split -- rather than like Turf War/Anarchy. Keyed the same way
	// as ModePlayers. The default is Splatoon 3's three Salmon Run configs:
	// coop_regular_config is seen on hardware, Big Run and Eggstra Work come
	// from the game binary. Without them a fresh install would split a
	// Salmon Run crew into two versus teams. A list in config.yaml replaces
	// the default.
	CoopModes []string `yaml:"coop_modes"`

	// VersusGracePeriod is how many seconds a versus-shaped match below its
	// size waits with an even number of players (two teams of equal size)
	// before forming anyway. Any arrival or departure restarts it; an odd
	// count stops it until one more player joins. A full match never waits.
	// Zero turns early starts off, so matches form only when full.
	VersusGracePeriod int `yaml:"versus_grace_period"`

	// CoopGracePeriod is how many seconds a Salmon Run-shaped match waits
	// after its second player joins, restarting on every further arrival, up
	// to the mode's size (which forms immediately). Zero forms the instant
	// two players are present. A lone player is never on this clock.
	CoopGracePeriod int `yaml:"coop_grace_period"`
	// TrackMinimumSeconds holds a completed ticket before sending SUCCEEDED,
	// measured from opening TrackMatchmakingTicket. Zero disables the hold.
	// This is a diagnostic for the reference's >=14-second Track duration.
	TrackMinimumSeconds int `yaml:"track_minimum_seconds"`

	// RegionalLatencyServers advertises one reachable latency endpoint under
	// seven measured regions for a controlled hardware compatibility check.
	RegionalLatencyServers bool `yaml:"regional_latency_servers"`

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
	// TurnAuthSecret is shared with coturn's static-auth-secret. When set,
	// TURN entries receive distinct, expiring credentials for each signed user.
	TurnAuthSecret string `yaml:"turn_auth_secret"`

	// FrontlineHost is the fro-N hostname the console is told to open its
	// persistent connection to.
	//
	// It MUST be a hostname internal/nintendo/hosts.go redirects. Naming one it
	// does not sends the console's frontline connection to real Nintendo, where
	// it succeeds — and then fails in a way that looks like GRID0+'s fault.
	// internal/nplnrest refuses to start rather than allow it.
	FrontlineHost string `yaml:"frontline_host"`

	// SessionHost and SessionPort are what a match puts in GameSession.host
	// and .port: where each console opens its gamesync session for that match.
	//
	// Clients differ: some use a literal IPv4 address on :7575, while for others the
	// tenant hostname on :443 works where a literal address did not. Both are
	// supported by the edge; the deployed value should follow the client being
	// used. Names must be in the redirected hosts list.
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

// NEX configures cmd/nex: the NEX game servers, which is to say Splatoon 2.
//
// NEX is the protocol stack before NPLN. A Switch reaches a NEX title's
// server as a WebSocket on g<game server id>-lp1.s.n.srv.nintendo.net:443,
// which nginx terminates and hands to Listen. One listener serves every
// title; nginx's Host header picks which.
type NEX struct {
	// Listen is the WebSocket backend nginx proxies to. Plain HTTP: nginx
	// terminates TLS.
	Listen string `yaml:"listen"`

	// IdleTimeout, in seconds, drops a console that has sent nothing for that
	// long. A console that went away without closing is otherwise still "in"
	// its lobby, and a lobby with a player who is not there never starts.
	IdleTimeout int `yaml:"idle_timeout"`

	// AdminListen and AdminToken are the operator API the dashboard reads:
	// who is connected, the lobbies, recent calls. Like npln's, it is served
	// only when a token is set and is never reached by a console.
	AdminListen string `yaml:"admin_listen"`
	AdminToken  string `yaml:"admin_token"`

	Splatoon2 Splatoon2 `yaml:"splatoon2"`

	Relay NEXRelay `yaml:"relay"`
}

// NEXRelay configures the relay for consoles that cannot reach each other
// directly (internal/rendezvous/relay.go): one UDP port per pair of players,
// on this server's public address, forwarding between them.
//
// Off by default, and even when on it relays only the players Allow names
// (or everyone who needs it, with AllowAll): it moves match traffic through
// the operator's own connection, and deciding who "needs" it can go wrong
// often enough that players who connected fine get pulled onto it. Start with
// the one player who cannot connect.
type NEXRelay struct {
	Enabled bool `yaml:"enabled"`
	// AdvertiseIP is the public address consoles send relayed traffic to.
	// Empty means network.public_ip.
	AdvertiseIP string `yaml:"advertise_ip"`
	// ListenIP is what the relay's sockets bind to. Empty is every interface.
	ListenIP string `yaml:"listen_ip"`
	// PortBase and PortSpan are the UDP port range pairs are given ports
	// from. The router must forward the whole range to this machine.
	PortBase int `yaml:"port_base"`
	PortSpan int `yaml:"port_span"`
	// Allow lists user ids (16 hex digits) that may be relayed; prefix one
	// with "!" to relay it straight away rather than after its punches fail.
	Allow []string `yaml:"allow"`
	// AllowAll relays anyone whose NAT is strict or whose punches all fail.
	AllowAll bool `yaml:"allow_all"`
}

// Splatoon2 configures the Splatoon 2 game server.
type Splatoon2 struct {
	// Enabled serves Splatoon 2 at all.
	Enabled bool `yaml:"enabled"`

	// SecureAddress and SecurePort move the secure half of the game server.
	//
	// Empty (the default) keeps it on the same WebSocket as the
	// authentication half: the station the console is handed says "0.0.0.1",
	// which a Switch reads as "where you already are" (Kinnay's wiki). That is
	// one hostname, one certificate name and one nginx block, and it is the
	// shape to try first. If a console turns out not to honour it, point
	// these at the edge (a second TLS port, or the same one) instead.
	SecureAddress string `yaml:"secure_address"`
	SecurePort    int    `yaml:"secure_port"`
}

// Rotation is the stage and mode rotation Splatoon 3's lobby shows.
//
// **The identifiers are Splatoon 3's own internal numbering.** Stage ids are
// public and documented -- internal/toyohr.VsStages, sourced from
// github.com/Leanny/splat3's own VersusSceneInfo.json, the same public data
// dump CoopStages already uses -- so RegularStages/BankaraStages are real,
// checkable ids, not a guess. Rule ids (Splat Zones/Tower Control/Rainmaker/
// Clam Blitz as the wire's own int32) are a different question and remain
// genuinely undocumented: neither that data dump (game content, not NPLN
// wire protocol) nor kinnay/NPLN-Protocols' own schedule.proto gives real
// numbers for BankaraRules. Given as strings so that a typo is reported
// with the offending value: stage 0 is a plausible valid id, so a zero
// produced by a failed parse would be indistinguishable from an intended
// one.
//
// The mechanics around them — two-hour slot alignment, windowing, ordering, the
// etag — are GRID0+'s and live in internal/toyohr.
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
	// GRID0+ does not convert textures itself, and that is deliberate: a
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

	// SeedDir is the delivery cache served to consoles and emulators, as
	// <seed_dir>/<title id>/<directory>/<file>. `switchnetctl fest build
	// -enable` stages the fest it builds there, and the dashboard shows and
	// serves Splatoon 3's folder to the console-side installer.
	//
	// Empty means the seed endpoints answer 404. Doing the install by hand with
	// JKSV works and does not scale past the second fest, which is why the
	// installer exists -- see switch/bcat-installer/.
	SeedDir string `yaml:"seed_dir"`

	// SubstituteFestID rewrites {FEST} in an asset's in-pack path to the fest's
	// own id.
	//
	// On keeps the internal names consistent with the fest that announces them;
	// off keeps whatever names the source files carry. Either way the decision
	// lives in one place: see internal/bcat.Naming.
	SubstituteFestID bool `yaml:"substitute_fest_id"`
}

// Dashboard configures the operator's web view.
type Dashboard struct {
	Listen string `yaml:"listen"`

	// Token is a shared secret required on every request. Empty means no
	// authentication at all.
	//
	// Every credential in the stack belongs to a console, not a person, so there is
	// no user account to check against. A shared token is the honest minimum: it
	// keeps the Splatfest controls off the open network without pretending to be
	// an identity system.
	Token string `yaml:"token"`

	// NPLNAdminURL and NPLNAdminToken reach cmd/npln's matchmaking controls,
	// and must match that server's npln.admin_listen and npln.admin_token.
	//
	// The queue lives in the npln process. Without these the dashboard's
	// matchmaking panel says it is not configured -- deliberately, rather than
	// showing an empty queue that reads as "nobody is playing".
	NPLNAdminURL   string `yaml:"npln_admin_url"`
	NPLNAdminToken string `yaml:"npln_admin_token"`

	// NEXAdminURL and NEXAdminToken reach cmd/nex's Splatoon 2 operator API,
	// and must match nex.admin_listen and nex.admin_token. Without them the
	// Splatoon 2 panel says it is not configured.
	NEXAdminURL   string `yaml:"nex_admin_url"`
	NEXAdminToken string `yaml:"nex_admin_token"`
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

// Inklink is the player companion, separate from the operator dashboard.
type DiscordOAuth struct {
	ClientID string `yaml:"client_id"`
	// The secret is environment-only or read from a private file, never YAML.
	ClientSecret     string `yaml:"-" json:"-"`
	ClientSecretFile string `yaml:"client_secret_file"`
	RedirectURI      string `yaml:"redirect_uri"`
}

type Inklink struct {
	Discord DiscordOAuth `yaml:"discord"`
	Listen  string       `yaml:"listen"`
	// Disable only for local HTTP development. Public deployments use HTTPS.
	SecureCookies bool `yaml:"secure_cookies"`
}

// Toolbox configures the public API the GRID0+ Toolbox homebrew app
// (GRID0-net/GRID0plus-cfw, toolbox/) talks to. See internal/toolbox's
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

	// GitHubOwner and GitHubRepo identify the private repository whose
	// releases carry the Toolbox .nro /updates/* reads releases from. Leave both empty to serve
	// /status only and have /updates/* answer 503.
	GitHubOwner string `yaml:"github_owner"`
	GitHubRepo  string `yaml:"github_repo"`

	// GitHubToken is a fine-grained personal access token scoped to ONLY
	// github_owner/github_repo, permission Contents: Read-only, nothing
	// else. Generate it at github.com/settings/personal-access-tokens --
	// never a classic token, which cannot be scoped to one repository.
	//
	// This is the one credential in all of GRID0+ that exists specifically
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

	// PatchesRepo is the public "owner/name" repository whose
	// patches-<serial> releases this service mirrors at /grid0plus/patches/,
	// for Toolboxes that cannot reach GitHub. Empty turns the mirror off.
	PatchesRepo string `yaml:"patches_repo"`
	// PatchesDir keeps the last verified patch set, so the mirror still
	// answers after a restart while GitHub is unreachable.
	PatchesDir string `yaml:"patches_dir"`
	// PatchesRefreshSeconds is how often GitHub is asked for a newer set.
	PatchesRefreshSeconds int `yaml:"patches_refresh_seconds"`
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
	// Type102SamePort answers type 102 from the port it was sent to. From
	// 10125 a port-restricted home router drops the reply.
	Type102SamePort bool `yaml:"type102_same_port"`
	// StunListen answers STUN (RFC 5389) Binding requests -- a different
	// protocol from the NCS one above, but the same kind of lightweight UDP
	// connectivity responder, so it runs alongside it rather than as a separate
	// service. Splatoon 3 sends these to whatever host
	// npln.ListLatencyMeasurementServers names (see internal/stunresponder). Empty
	// disables it.
	StunListen string `yaml:"stun_listen"`
}

// Log configures logging.
type Log struct {
	// Level is one of debug, info, warn, error.
	Level string `yaml:"level"`
	// Format is "text" or "json".
	Format string `yaml:"format"`
}

// salmonRunConfig names one of Splatoon 3's Salmon Run matchmaking configs.
func salmonRunConfig(kind string) string {
	return "tenants/current/matchmakingConfigs/coop_" + kind + "_config"
}

// Default returns the configuration GRID0+ uses when nothing is set. The
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
			PublicURL:  "https://storage.hac.lp1.scsi.srv.nintendo.net",
			StorageDir: "data/save-archives",
			// 1 GiB: generous for save data (real Switch saves are almost
			// always well under 100 MiB), small enough that even a handful of
			// consoles cannot meaningfully dent a modern disk.
			PerUserQuotaBytes: 1 << 30,
		},
		Stubs: Stubs{
			Listen: "127.0.0.1:8084",
		},
		Inklink: Inklink{Listen: "127.0.0.1:8097", SecureCookies: true},
		Dashboard: Dashboard{
			Listen: ":8087",
		},
		NPLN: NPLN{
			Listen:         "127.0.0.1:8085",
			RestListen:     "127.0.0.1:8086",
			GamesyncListen: "127.0.0.1:8088",
			Tenant:         "t-dce9377b-lp1",
			AppID:          "0100c2500fc20000",
			Issuer:         "https://t-dce9377b-lp1.lp1.t.npln.srv.nintendo.net",
			JWKSURL:        "https://t-dce9377b-lp1.lp1.t.npln.srv.nintendo.net/1.0.0/certificates",
			AccessTokenTTL: 28800,
			// Eight as a cap; smaller even matches start after the grace
			// period. See the field comments.
			PlayersPerMatch: 8,
			ModePlayers: map[string]int{
				salmonRunConfig("regular"):      4,
				salmonRunConfig("bigrun"):       4,
				salmonRunConfig("team_contest"): 4,
			},
			CoopModes: []string{
				salmonRunConfig("regular"),
				salmonRunConfig("bigrun"),
				salmonRunConfig("team_contest"),
			},
			VersusGracePeriod: 20,
			CoopGracePeriod:   20,
			TicketTTL:         180,
			// fro-3 because that is the member the reference server resolves
			// the frontline to. Any of fro-1..4 is redirected and carries a
			// certificate SAN; this one is the least likely to surprise.
			FrontlineHost: "fro-3.hac.lp1.penne.srv.nintendo.net",
			SessionHost:   "t-dce9377b-lp1.lp1.t.npln.srv.nintendo.net",
			SessionPort:   443,
		},
		NEX: NEX{
			Listen:      "127.0.0.1:8092",
			IdleTimeout: 60,
			AdminListen: "127.0.0.1:8093",
			Splatoon2:   Splatoon2{Enabled: true},
			Relay:       NEXRelay{PortBase: 30100, PortSpan: 900},
		},
		BCAT: BCAT{
			ConverterTimeout: 120,
			SubstituteFestID: true,
		},
		Toolbox: Toolbox{
			Listen:                 "127.0.0.1:8090",
			ReleaseCacheTTLSeconds: 120,
			PatchesRepo:            "GRID0-net/GRID0plus-cfw",
			PatchesDir:             "data/toolbox-patches",
			PatchesRefreshSeconds:  600,
		},
		NATCheck: NATCheck{
			PrimaryListen:   ":10025",
			SecondaryListen: ":10125",
			Type102SamePort: true,
			StunListen:      ":3478",
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
	envStr("SWITCHNET_BAAS_BOT_API_LISTEN", &c.BAAS.BotAPI.Listen)
	envStr("SWITCHNET_BAAS_BOT_API_TOKEN", &c.BAAS.BotAPI.Token)
	envStr("SWITCHNET_BAAS_BOT_API_SPLATOON3_SAVE_TABLE", &c.BAAS.BotAPI.Splatoon3SaveTable)
	envStr("SWITCHNET_BAAS_BOT_API_SPLATOON3_SAVE_NAMES", &c.BAAS.BotAPI.Splatoon3SaveNames)
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

	envStr("SWITCHNET_INKLINK_LISTEN", &c.Inklink.Listen)
	envStr("SWITCHNET_INKLINK_DISCORD_CLIENT_ID", &c.Inklink.Discord.ClientID)
	envStr("SWITCHNET_INKLINK_DISCORD_CLIENT_SECRET", &c.Inklink.Discord.ClientSecret)
	envStr("SWITCHNET_INKLINK_DISCORD_CLIENT_SECRET_FILE", &c.Inklink.Discord.ClientSecretFile)
	envStr("SWITCHNET_INKLINK_DISCORD_REDIRECT_URI", &c.Inklink.Discord.RedirectURI)
	envBool("SWITCHNET_INKLINK_SECURE_COOKIES", &c.Inklink.SecureCookies)
	envStr("SWITCHNET_DASHBOARD_LISTEN", &c.Dashboard.Listen)
	envStr("SWITCHNET_DASHBOARD_TOKEN", &c.Dashboard.Token)
	envStr("SWITCHNET_DASHBOARD_NPLN_ADMIN_URL", &c.Dashboard.NPLNAdminURL)
	envStr("SWITCHNET_DASHBOARD_NPLN_ADMIN_TOKEN", &c.Dashboard.NPLNAdminToken)
	envStr("SWITCHNET_DASHBOARD_NEX_ADMIN_URL", &c.Dashboard.NEXAdminURL)
	envStr("SWITCHNET_DASHBOARD_NEX_ADMIN_TOKEN", &c.Dashboard.NEXAdminToken)

	envStr("SWITCHNET_NPLN_LISTEN", &c.NPLN.Listen)
	envStr("SWITCHNET_NPLN_REST_LISTEN", &c.NPLN.RestListen)
	envStr("SWITCHNET_NPLN_TENANT", &c.NPLN.Tenant)
	envStr("SWITCHNET_NPLN_ISSUER", &c.NPLN.Issuer)
	envStr("SWITCHNET_NPLN_JWKS_URL", &c.NPLN.JWKSURL)
	envStr("SWITCHNET_NPLN_GAMESYNC_LISTEN", &c.NPLN.GamesyncListen)
	envStr("SWITCHNET_NPLN_TURN_AUTH_SECRET", &c.NPLN.TurnAuthSecret)
	envStr("SWITCHNET_NPLN_FRONTLINE_HOST", &c.NPLN.FrontlineHost)
	envStr("SWITCHNET_NPLN_SESSION_HOST", &c.NPLN.SessionHost)
	envStr("SWITCHNET_NPLN_REST_RECORD_DIR", &c.NPLN.RestRecordDir)
	envInt("SWITCHNET_NPLN_PLAYERS_PER_MATCH", &c.NPLN.PlayersPerMatch)
	envInt("SWITCHNET_NPLN_TICKET_TTL", &c.NPLN.TicketTTL)
	envInt("SWITCHNET_NPLN_VERSUS_GRACE_PERIOD", &c.NPLN.VersusGracePeriod)
	envInt("SWITCHNET_NPLN_COOP_GRACE_PERIOD", &c.NPLN.CoopGracePeriod)
	envInt("SWITCHNET_NPLN_TRACK_MINIMUM_SECONDS", &c.NPLN.TrackMinimumSeconds)
	envBool("SWITCHNET_NPLN_REGIONAL_LATENCY_SERVERS", &c.NPLN.RegionalLatencyServers)
	envStr("SWITCHNET_NEX_LISTEN", &c.NEX.Listen)
	envStr("SWITCHNET_NEX_ADMIN_LISTEN", &c.NEX.AdminListen)
	envStr("SWITCHNET_NEX_ADMIN_TOKEN", &c.NEX.AdminToken)
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
	envBool("SWITCHNET_NATCHECK_TYPE102_SAME_PORT", &c.NATCheck.Type102SamePort)

	envStr("SWITCHNET_LOG_LEVEL", &c.Log.Level)
	envStr("SWITCHNET_LOG_FORMAT", &c.Log.Format)
}

// Validate checks the configuration is usable. It rejects the mistakes that
// would otherwise fail much later, on a console, with an unhelpful error.
func (c Config) Validate() error {
	var errs []error
	if err := c.Inklink.Discord.Validate(); err != nil {
		errs = append(errs, err)
	}
	if _, port, err := net.SplitHostPort(c.Inklink.Listen); err != nil || port == "" {
		errs = append(errs, fmt.Errorf("inklink.listen must be a host:port address"))
	} else if n, err := strconv.Atoi(port); err != nil || n < 1 || n > 65535 {
		errs = append(errs, fmt.Errorf("inklink.listen must use a numeric port from 1 to 65535"))
	}

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
			// redirection happens off the console, in a resolver GRID0+ does
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
	if c.BAAS.BotAPI.Listen != "" && len(c.BAAS.BotAPI.Token) < 32 {
		errs = append(errs, errors.New(
			"baas.bot_api.token must be at least 32 characters when baas.bot_api.listen is set "+
				"(openssl rand -hex 32)"))
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
	if c.NEX.Listen == "" {
		errs = append(errs, errors.New("nex.listen is required: nginx proxies the NEX hostnames to it"))
	}
	if c.NEX.IdleTimeout <= 0 {
		errs = append(errs, errors.New("nex.idle_timeout must be positive"))
	}
	if a := c.NEX.Splatoon2.SecureAddress; a != "" {
		if p := c.NEX.Splatoon2.SecurePort; p <= 0 || p > 65535 {
			errs = append(errs, fmt.Errorf(
				"nex.splatoon2.secure_port: %d is not a port, and secure_address %q needs one", p, a))
		}
	} else if c.NEX.Splatoon2.SecurePort != 0 {
		errs = append(errs, errors.New(
			"nex.splatoon2.secure_port is set without secure_address, so it would be ignored: "+
				"set both to move the secure server, or neither to keep it on the same host"))
	}
	if r := c.NEX.Relay; r.Enabled {
		ip := r.AdvertiseIP
		if ip == "" {
			ip = c.Network.PublicIP
		}
		if net.ParseIP(ip) == nil {
			errs = append(errs, errors.New(
				"nex.relay is enabled but neither nex.relay.advertise_ip nor network.public_ip is an IP address: "+
					"consoles would be told to send their match traffic nowhere"))
		}
		if r.PortBase <= 0 || r.PortSpan <= 0 || r.PortBase+r.PortSpan > 65536 {
			errs = append(errs, fmt.Errorf("nex.relay: ports %d..%d are not a valid UDP range",
				r.PortBase, r.PortBase+r.PortSpan-1))
		}
		for _, id := range r.Allow {
			id = strings.TrimPrefix(id, "!")
			if _, err := strconv.ParseUint(id, 16, 64); err != nil || len(id) != 16 {
				errs = append(errs, fmt.Errorf("nex.relay.allow: %q is not a 16-hex-digit user id", id))
			}
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
		if s.Kind == "turn" {
			if c.NPLN.TurnAuthSecret == "" && (s.Username == "" || s.Password == "") {
				errs = append(errs, fmt.Errorf("npln.ice_servers[%d]: TURN needs both static credentials or npln.turn_auth_secret", i))
			}
			if c.NPLN.TurnAuthSecret != "" && (s.Username != "" || s.Password != "") {
				errs = append(errs, fmt.Errorf("npln.ice_servers[%d]: remove static TURN credentials when turn_auth_secret is set", i))
			}
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

// Validate keeps partially configured OAuth and unsafe callbacks from appearing
// as a working sign-in option. Loopback HTTP is only for local development.
func (c DiscordOAuth) Validate() error {
	if c.ClientID == "" && c.ClientSecret == "" && c.ClientSecretFile == "" && c.RedirectURI == "" {
		return nil
	}
	if c.ClientID == "" || (c.ClientSecret == "" && c.ClientSecretFile == "") || c.RedirectURI == "" {
		return errors.New("inklink.discord requires client_id, a secret or secret file, and redirect_uri")
	}
	for _, r := range c.ClientID {
		if r < '0' || r > '9' {
			return errors.New("inklink.discord.client_id must be numeric")
		}
	}
	u, err := url.Parse(c.RedirectURI)
	if err != nil || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || u.Path != "/auth/discord/callback" {
		return errors.New("inklink.discord.redirect_uri must be an absolute /auth/discord/callback URL without query or fragment")
	}
	ip := net.ParseIP(u.Hostname())
	local := u.Hostname() == "localhost" || (ip != nil && ip.IsLoopback())
	if u.Scheme != "https" && !(u.Scheme == "http" && local) {
		return errors.New("inklink.discord.redirect_uri requires HTTPS except on loopback")
	}
	return nil
}
