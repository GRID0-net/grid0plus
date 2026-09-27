package jwt

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"

	gojwt "github.com/golang-jwt/jwt/v5"
)

// ErrInvalidToken is returned when a token does not verify. It deliberately
// carries no detail about why, so a caller cannot use the error to probe the
// signing key.
var ErrInvalidToken = errors.New("jwt: invalid token")

// Verifier checks tokens that GRID0+ itself issued.
//
// It only ever accepts GRID0+'s own signature. A token from real Nintendo
// will not verify here, and GRID0+ makes no attempt to verify one: device
// tokens, tickets and licences from a console are accepted as opaque strings
// on the operator's private network, not validated against Nintendo.
type Verifier struct {
	key Key
}

// NewVerifier returns a verifier for one signing key.
func NewVerifier(key Key) *Verifier { return &Verifier{key: key} }

// Verify parses and verifies a token into claims, which must be a pointer.
// Expiry is enforced; a token with no 'exp' is rejected.
func (v *Verifier) Verify(token string, claims gojwt.Claims) error {
	parser := gojwt.NewParser(
		gojwt.WithValidMethods([]string{"RS256"}),
		gojwt.WithExpirationRequired(),
	)
	_, err := parser.ParseWithClaims(token, claims, func(t *gojwt.Token) (any, error) {
		// Reject a token signed with a different key even if the algorithm
		// matches, by checking the key id when the token carries one.
		if kid, ok := t.Header["kid"].(string); ok && kid != v.key.ID {
			return nil, fmt.Errorf("%w: unknown key id", ErrInvalidToken)
		}
		return v.key.Public(), nil
	})
	if err != nil {
		return fmt.Errorf("%w: %s", ErrInvalidToken, err)
	}
	return nil
}

// ParseUnverified reads a token's claims WITHOUT checking the signature.
//
// This exists for exactly one case: a credential that was issued by somebody
// else and that GRID0+ is not in a position to verify, such as the Nintendo
// Account id token in a /1.0.0/federation request. Never use it for a token
// GRID0+ issued, and never let its output decide an authorisation.
func ParseUnverified(token string, claims gojwt.Claims) error {
	parser := gojwt.NewParser()
	if _, _, err := parser.ParseUnverified(token, claims); err != nil {
		return fmt.Errorf("%w: %s", ErrInvalidToken, err)
	}
	return nil
}

// JWK is one public key in a JWK set.
//
// Reference: https://github.com/kinnay/NintendoClients/wiki/Switch-Tokens#jwk-set
type JWK struct {
	KeyType string `json:"kty"`

	// RSA parameters. omitempty because an EC key has neither, and a JWK
	// carrying "e":"" alongside its real "x"/"y" is malformed — a strict
	// parser rejects the whole set, taking the RSA keys down with it.
	Exponent string `json:"e,omitempty"`
	Modulus  string `json:"n,omitempty"`

	// EC parameters, for the ES256 keys NPLN uses. Same reasoning in reverse.
	Curve string `json:"crv,omitempty"`
	X     string `json:"x,omitempty"`
	Y     string `json:"y,omitempty"`

	Algorithm string `json:"alg"`
	Use       string `json:"use"`
	KeyID     string `json:"kid"`
	// Usage is a BAAS-only field: "developer" or "internal".
	Usage string `json:"usage,omitempty"`
}

// JWKSet is the document served at a token's 'jku' URL.
type JWKSet struct {
	Keys []JWK `json:"keys"`
}

// JWKSetFor builds a JWK set containing the given keys.
//
// Nintendo keeps the two previous keys in the set so that tokens issued before
// a rotation still verify. GRID0+ does not rotate its keys, so the set holds
// exactly the key that is signing today. Rotation would mean handing consoles
// tokens they cannot use until they re-authenticate, which on a private network
// buys nothing.
func JWKSetFor(usage string, keys ...Key) JWKSet {
	set := JWKSet{Keys: make([]JWK, 0, len(keys))}
	for _, k := range keys {
		pub := k.Public()
		set.Keys = append(set.Keys, JWK{
			KeyType:   "RSA",
			Exponent:  base64.RawURLEncoding.EncodeToString(bigEndianBytes(pub.E)),
			Modulus:   base64.RawURLEncoding.EncodeToString(pub.N.Bytes()),
			Algorithm: "RS256",
			Use:       "sig",
			KeyID:     k.ID,
			Usage:     usage,
		})
	}
	return set
}

// JWKSHandler serves a JWK set.
func JWKSHandler(set JWKSet) http.HandlerFunc {
	body, err := json.Marshal(set)
	if err != nil {
		panic("jwt: marshal JWK set: " + err.Error())
	}
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		// The console re-fetches this whenever it needs to verify a token it has
		// no key for. Letting it cache for an hour keeps that cheap without
		// pinning a key the operator may have just replaced.
		w.Header().Set("Cache-Control", "public, max-age=3600")
		_, _ = w.Write(body)
	}
}

// bigEndianBytes encodes a public exponent the way RFC 7518 wants it: the
// minimal big-endian byte string, with no leading zero bytes.
func bigEndianBytes(e int) []byte {
	if e == 0 {
		return []byte{0}
	}
	var buf []byte
	for e > 0 {
		buf = append([]byte{byte(e & 0xff)}, buf...)
		e >>= 8
	}
	return buf
}
