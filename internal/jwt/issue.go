package jwt

import (
	"crypto/rsa"
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
func keyIDFor(priv *rsa.PrivateKey) KeyID {
	der, err := x509.MarshalPKIXPublicKey(&priv.PublicKey)
	if err != nil {
		// MarshalPKIXPublicKey only fails for key types it does not know, and
		// RSA is not one of them.
		panic("jwt: marshal RSA public key: " + err.Error())
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
	Type string `json:"typ"`

	// GrantType is 1 for an anonymous (application) token and 2 for a user
	// token. It is absent from id tokens.
	GrantType int `json:"bs:grt,omitempty"`

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
	// includeTyp controls whether the 'typ: JWT' header is emitted. The dauth
	// and aauth servers send it; the BAAS server does not.
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
