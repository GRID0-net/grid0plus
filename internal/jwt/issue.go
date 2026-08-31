package jwt

import (
	"crypto"
	"crypto/sha256"
	"crypto/x509"
	"encoding/hex"
	"time"

	gojwt "github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
)

// keyIDFor derives a stable, UUID-shaped key id from a key's public half.
//
// Nintendo uses a random UUID v4 as the 'kid'. Deriving it from the key instead
// means the id survives a restart and cannot drift out of sync with the JWK set
// it is published in, while still looking like what a capture shows.
//
// It takes the public key rather than the private one so that RSA and EC keys
// can share it: the id depends on nothing secret.
func keyIDFor(pub crypto.PublicKey) KeyID {
	der, err := x509.MarshalPKIXPublicKey(pub)
	if err != nil {
		// MarshalPKIXPublicKey only fails for key types it does not know, and
		// neither RSA nor ECDSA is one of them.
		panic("jwt: marshal public key: " + err.Error())
	}
	sum := sha256.Sum256(der)
	id, err := uuid.FromBytes(sum[:16])
	if err != nil {
		panic("jwt: build key id: " + err.Error())
	}
	return id.String()
}

// DeviceInfo is the 'nintendo' claim of a device token, and the same block
// inside a BAAS access token.
//
// Reference: https://github.com/kinnay/NintendoClients/wiki/Switch-Tokens#device-information
//
// The wiki does not document two fields a 2026-08-27 capture of a working
// Nextendo device token showed: 'x5t#S256' and 'esv'. Both are 43-character
// unpadded base64url strings — the shape of a SHA-256 digest — present on
// every device token and echoed unchanged into /1.0.0/application/token's
// access token, so they are static per-console attributes minted once by
// dauth, not something recomputed per call.
//
// 'x5t#S256' is implemented: RFC 7517 §4.9 names this exact key for an X.509
// SHA-256 certificate thumbprint, and dauth already reads the console's TLS
// client certificate — untrusted, unverified, exactly as CLAUDE.md requires —
// so hashing bytes already being read is not new trust placed in Nintendo
// data, just a real digest of it. See deviceFrom in internal/dauth/handler.go.
//
// 'esv' is not implemented. Its meaning is genuinely unknown, it is not a
// per-connection value we can safely rehash from the client cert, and every
// plausible source of it (an eTicket, a title key, anything AES/RSA-derived
// from Nintendo key material) is exactly what CLAUDE.md's first ground rule
// forbids SwitchNet from touching. See docs/CAPTURES/README.md.
type DeviceInfo struct {
	// SerialNumber is the console serial, e.g. "XAW10012345678".
	SerialNumber string `json:"sn"`
	// ProductCode is always "HAC" on the Switch.
	ProductCode string `json:"pc"`
	// DeviceType is always "NX Prod 1" on a retail console.
	DeviceType string `json:"dt"`
	// IsT reflects the console's platform region flag.
	IsT bool `json:"ist"`
	// DeviceID is only present inside a BAAS access token's device block, and
	// inside an application token for a system title.
	DeviceID string `json:"di,omitempty"`
	// X5tS256 is the base64url (no padding) SHA-256 thumbprint of the
	// console's TLS client certificate, DER-encoded, per RFC 7517 §4.9. Empty
	// when nginx passed no client certificate through.
	X5tS256 string `json:"x5t#S256,omitempty"`
}

// AppInfo is the 'nintendo' claim of an application token, and the block copied
// into an id token when the console presents an application token.
//
// Reference: https://github.com/kinnay/NintendoClients/wiki/Switch-Tokens#aauth-tokens
type AppInfo struct {
	// AppID is the title id, 16 hex digits.
	AppID string `json:"ai"`
	// AppVersion is the title version, 4 hex digits (the high half of the
	// 8-digit application_version parameter).
	AppVersion string `json:"av"`
	// AppTime is when the token was issued.
	AppTime int64 `json:"at"`
	// EDI is a 32-hex-digit id that is unique per (device, title).
	EDI string `json:"edi"`

	// OnlinePlayPolicy is "FREE" or "MEMBERSHIP_REQUIRED".
	OnlinePlayPolicy string `json:"opp,omitempty"`
	// PolicyHandler is "SYSTEM" or "GAME_SERVER", only when membership is required.
	PolicyHandler string `json:"ph,omitempty"`
	// HasMembership is only set in id tokens, when the policy handler is
	// GAME_SERVER. Game servers read it to decide whether online play is allowed.
	HasMembership *bool `json:"hm,omitempty"`

	// The device block is only present for system titles.
	DeviceID     string `json:"di,omitempty"`
	SerialNumber string `json:"sn,omitempty"`
	ProductCode  string `json:"pc,omitempty"`
	DeviceType   string `json:"dt,omitempty"`
	IsT          *bool  `json:"ist,omitempty"`
}

// DeviceTokenClaims is the payload of a device token.
type DeviceTokenClaims struct {
	Subject  string     `json:"sub"`
	Audience string     `json:"aud"`
	Expires  int64      `json:"exp"`
	IssuedAt int64      `json:"iat"`
	Issuer   string     `json:"iss"`
	TokenID  string     `json:"jti"`
	Nintendo DeviceInfo `json:"nintendo"`
}

// GetExpirationTime implements gojwt.Claims.
func (c DeviceTokenClaims) GetExpirationTime() (*gojwt.NumericDate, error) {
	return gojwt.NewNumericDate(time.Unix(c.Expires, 0)), nil
}

// GetIssuedAt implements gojwt.Claims.
func (c DeviceTokenClaims) GetIssuedAt() (*gojwt.NumericDate, error) {
	return gojwt.NewNumericDate(time.Unix(c.IssuedAt, 0)), nil
}

// GetNotBefore implements gojwt.Claims.
func (c DeviceTokenClaims) GetNotBefore() (*gojwt.NumericDate, error) { return nil, nil }

// GetIssuer implements gojwt.Claims.
func (c DeviceTokenClaims) GetIssuer() (string, error) { return c.Issuer, nil }

// GetSubject implements gojwt.Claims.
func (c DeviceTokenClaims) GetSubject() (string, error) { return c.Subject, nil }

// GetAudience implements gojwt.Claims.
func (c DeviceTokenClaims) GetAudience() (gojwt.ClaimStrings, error) {
	return gojwt.ClaimStrings{c.Audience}, nil
}

// AppTokenClaims is the payload of an application token. It has no 'aud'.
type AppTokenClaims struct {
	Subject  string  `json:"sub"`
	Expires  int64   `json:"exp"`
	IssuedAt int64   `json:"iat"`
	Issuer   string  `json:"iss"`
	TokenID  string  `json:"jti"`
	Nintendo AppInfo `json:"nintendo"`
}

// GetExpirationTime implements gojwt.Claims.
func (c AppTokenClaims) GetExpirationTime() (*gojwt.NumericDate, error) {
	return gojwt.NewNumericDate(time.Unix(c.Expires, 0)), nil
}

// GetIssuedAt implements gojwt.Claims.
func (c AppTokenClaims) GetIssuedAt() (*gojwt.NumericDate, error) {
	return gojwt.NewNumericDate(time.Unix(c.IssuedAt, 0)), nil
}

// GetNotBefore implements gojwt.Claims.
func (c AppTokenClaims) GetNotBefore() (*gojwt.NumericDate, error) { return nil, nil }

// GetIssuer implements gojwt.Claims.
func (c AppTokenClaims) GetIssuer() (string, error) { return c.Issuer, nil }

// GetSubject implements gojwt.Claims.
func (c AppTokenClaims) GetSubject() (string, error) { return c.Subject, nil }

// GetAudience implements gojwt.Claims.
func (c AppTokenClaims) GetAudience() (gojwt.ClaimStrings, error) { return nil, nil }

// BAASTokenClaims is the payload of a BAAS access token, user token or id token.
//
// The three differ in Type, GrantType and which optional blocks are present:
//
//	access token  typ=token     bs:grt=1  nintendo=device info
//	user token    typ=token     bs:grt=2  bs:did set
//	id token      typ=id_token            bs:did set, nintendo=app info
type BAASTokenClaims struct {
	Subject  string `json:"sub"`
	Audience string `json:"aud"`
	Expires  int64  `json:"exp"`
	IssuedAt int64  `json:"iat"`
	Issuer   string `json:"iss"`
	TokenID  string `json:"jti"`

	// Type is "token" for access and user tokens, "id_token" for id tokens.
	// Omitempty: this struct is also reused for accounts.nintendo.com's OIDC
	// tokens (nalink.go), whose id_token never carries a payload 'typ' at all
	// -- confirmed 2026-08-27 against a real capture. Every BAAS call site
	// already sets this explicitly, so omitempty changes nothing for them.
	Type string `json:"typ,omitempty"`

	// GrantType is 1 for an anonymous (application) token and 2 for a user
	// token. It is absent from id tokens.
	GrantType int `json:"bs:grt,omitempty"`

	// ACGrantType is accounts.nintendo.com's own grant-type flavour, 'ac:grt'
	// -- a separate namespace from BAAS's 'bs:grt', only ever set by
	// nalink.go. Only one value has ever been seen (4, for an
	// authorization_code exchange); what other grants map to is
	// TODO(capture-needed).
	ACGrantType int `json:"ac:grt,omitempty"`

	// ACScopes is accounts.nintendo.com's 'ac:scp': the granted scopes, as
	// numeric ids rather than the strings the response body's own "scope"
	// field carries. Only ever set by nalink.go, on the access_token.
	//
	// TODO(capture-needed): the string-to-id mapping is read off a single
	// capture (docs/CAPTURES/raw/nextendo-working-nalink-2026-08-27.flow),
	// where twelve scope strings lined up one-for-one with twelve ids in the
	// same order. That is enough to reproduce the observed list and no more:
	// a scope outside those twelve has no known id, so nalink.go emits
	// nothing rather than guessing one. A capture with a different scope set
	// would settle the rest. Change: naScopeIDs in internal/baas/nalink.go.
	ACScopes []int `json:"ac:scp,omitempty"`

	// ATHash and CHash are the OIDC 'at_hash' and 'c_hash' claims: the
	// left half of the SHA-256 digest of the access_token or authorization
	// code, base64url-encoded without padding (RFC in the OIDC Core spec,
	// not Nintendo-specific). Verified byte-for-byte against a 2026-08-27
	// capture. Only ever set by nalink.go.
	ATHash string `json:"at_hash,omitempty"`
	CHash  string `json:"c_hash,omitempty"`

	// Nonce echoes the OIDC 'nonce' the console sent on the original
	// authorize request. A pointer because a real capture showed the key
	// present even when the console sent no nonce at all (empty string,
	// not absent) -- omitempty on a plain string would drop it in exactly
	// that case. Only ever set by nalink.go's naAuthorizeSubmit.
	Nonce *string `json:"nonce,omitempty"`

	// DeviceAccountID is the BAAS device account the token was minted for.
	// Present in user tokens and id tokens.
	DeviceAccountID string `json:"bs:did,omitempty"`

	// Status is an opaque bitfield array. Nintendo sends [385] in access tokens
	// and a seven-element array in user tokens.
	//
	// TODO(capture-needed): the meaning of these values is unknown. Nothing on
	// the console appears to read them, but a capture of a user token from a
	// console that is banned, or that has no membership, would show whether
	// they encode account state.
	Status []int64 `json:"bs:sts,omitempty"`

	// Nintendo carries a *DeviceInfo for an access token, or a *AppInfo for an
	// id token that was minted from an application token. It is nil otherwise.
	Nintendo any `json:"nintendo,omitempty"`
}

// GetExpirationTime implements gojwt.Claims.
func (c BAASTokenClaims) GetExpirationTime() (*gojwt.NumericDate, error) {
	return gojwt.NewNumericDate(time.Unix(c.Expires, 0)), nil
}

// GetIssuedAt implements gojwt.Claims.
func (c BAASTokenClaims) GetIssuedAt() (*gojwt.NumericDate, error) {
	return gojwt.NewNumericDate(time.Unix(c.IssuedAt, 0)), nil
}

// GetNotBefore implements gojwt.Claims.
func (c BAASTokenClaims) GetNotBefore() (*gojwt.NumericDate, error) { return nil, nil }

// GetIssuer implements gojwt.Claims.
func (c BAASTokenClaims) GetIssuer() (string, error) { return c.Issuer, nil }

// GetSubject implements gojwt.Claims.
func (c BAASTokenClaims) GetSubject() (string, error) { return c.Subject, nil }

// GetAudience implements gojwt.Claims.
func (c BAASTokenClaims) GetAudience() (gojwt.ClaimStrings, error) {
	return gojwt.ClaimStrings{c.Audience}, nil
}

// Status values observed in real tokens. See BAASTokenClaims.Status.
var (
	// AccessTokenStatus is the bs:sts of an anonymous access token.
	AccessTokenStatus = []int64{385}
	// UserTokenStatus is the bs:sts of a user access token.
	UserTokenStatus = []int64{10414578180576298, 272640, 1, 0, 0, 19316357715722240, 16}
)

// Issuer signs tokens with one key, and stamps the 'jku' header so the console
// knows where to fetch the verification key.
type Issuer struct {
	key     Key
	jwksURL string
	// includeTyp controls whether the 'typ: JWT' header is emitted.
	//
	// Every issuer now sets it. It used to be off for the BAAS and
	// accounts.nintendo.com issuers, on the belief that "a BAAS token has no
	// typ header" -- stated as a rule in CLAUDE.md and asserted by a test.
	// A 2026-08-28 sweep of every JWT in the fully decrypted capture of a
	// genuinely working server (docs/CAPTURES/raw/nextendo-working-nalink-
	// 2026-08-27.flow) contradicted it outright: all twelve tokens in that
	// flow carry 'typ: JWT', including application/token's, federation's and
	// login's. The belief appears to have come from reasoning about the
	// payload's own 'typ' claim ("token"/"id_token"), which is a different
	// field and genuinely is absent from some tokens -- not from a capture.
	//
	// Kept as a field rather than hardcoded: it is the standard JWT header,
	// and an issuer that legitimately needs it off should be able to say so
	// with a capture behind it.
	includeTyp bool
}

// NewIssuer returns an issuer for one signing key.
func NewIssuer(key Key, jwksURL string, includeTyp bool) *Issuer {
	return &Issuer{key: key, jwksURL: jwksURL, includeTyp: includeTyp}
}

// Key returns the signing key, so it can be published in a JWK set.
func (i *Issuer) Key() Key { return i.key }

// Sign signs claims and returns the compact serialisation.
func (i *Issuer) Sign(claims gojwt.Claims) (string, error) {
	tok := gojwt.NewWithClaims(gojwt.SigningMethodRS256, claims)
	// golang-jwt seeds the header with alg and typ. Nintendo's BAAS tokens have
	// no typ, so remove it there. JSON object key order is not significant and
	// no client parses these positionally.
	tok.Header = map[string]any{
		"alg": "RS256",
		"jku": i.jwksURL,
		"kid": i.key.ID,
	}
	if i.includeTyp {
		tok.Header["typ"] = "JWT"
	}
	return tok.SignedString(i.key.Private)
}

// NewTokenID returns a fresh 'jti'.
func NewTokenID() string { return uuid.NewString() }

// DeriveHexID returns a deterministic lowercase hex id of n bytes, derived from
// secret and the given parts.
//
// It is used for the ids that have to be stable for a console across reboots
// but must not be guessable from public information: device ids, and the 'edi'
// of an application token.
func DeriveHexID(secret string, n int, parts ...string) string {
	h := sha256.New()
	h.Write([]byte(secret))
	for _, p := range parts {
		h.Write([]byte{0})
		h.Write([]byte(p))
	}
	sum := h.Sum(nil)
	if n > len(sum) {
		n = len(sum)
	}
	return hex.EncodeToString(sum[:n])
}
