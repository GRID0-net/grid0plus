package jwt

// ES256 signing, for NPLN.
//
// NPLN access tokens are the only ES256 tokens in the stack — every other token
// the Switch's auth chain issues is RS256 (see ROADMAP.md §2.2). That is not a
// preference of ours, it is what the game's own gRPC stack expects, so the
// algorithm is fixed rather than configurable.
//
// This lives alongside the RSA path rather than replacing it. Making Key
// generic over the algorithm would touch dauth, aauth and baas — all of them
// verified working — to no benefit, since no issuer ever needs to be told which
// algorithm it uses: an issuer's algorithm is a property of the protocol it
// serves, decided once, here.

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"math/big"
	"os"
	"path/filepath"

	gojwt "github.com/golang-jwt/jwt/v5"
)

// IssuerNPLN signs NPLN access tokens. Published at the NPLN gateway's own JWK
// set, and — unlike every other issuer here — with ES256.
const IssuerNPLN = "npln"

// ECKey is one ECDSA signing key plus the id it is published under.
type ECKey struct {
	ID      KeyID
	Private *ecdsa.PrivateKey
}

// Public returns the verification half of the key.
func (k ECKey) Public() *ecdsa.PublicKey { return &k.Private.PublicKey }

// ECKey returns the ES256 signing key for the named issuer, loading or
// generating it on first use.
//
// Keys are cached separately from the RSA ones, and the two namespaces must not
// overlap: asking for the same name through both would hand out two different
// keys under one filename, and whichever service started second would sign with
// a key nobody publishes.
func (r *Keyring) ECKey(name string) (ECKey, error) {
	if err := validKeyName(name); err != nil {
		return ECKey{}, err
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	if k, ok := r.ecKeys[name]; ok {
		return k, nil
	}

	path := filepath.Join(r.dir, name+".key.pem")
	priv, err := loadECPEM(path)
	switch {
	case err == nil:
	case errors.Is(err, os.ErrNotExist) && r.generateIfMissing:
		priv, err = generateEC(path)
		if err != nil {
			return ECKey{}, err
		}
	default:
		return ECKey{}, fmt.Errorf("load signing key %s: %w", name, err)
	}
	k := ECKey{ID: keyIDFor(&priv.PublicKey), Private: priv}
	r.ecKeys[name] = k
	return k, nil
}

func loadECPEM(path string) (*ecdsa.PrivateKey, error) {
	key, err := parsePEM(path)
	if err != nil {
		return nil, err
	}
	priv, ok := key.(*ecdsa.PrivateKey)
	if !ok {
		return nil, fmt.Errorf("%s: not an EC key", path)
	}
	if priv.Curve != elliptic.P256() {
		// ES256 is ECDSA-on-P-256 by definition (RFC 7518 §3.4). A P-384 key
		// here would sign tokens whose 'alg' says one curve and whose signature
		// is another, which a strict verifier rejects and a lax one accepts —
		// the worst of both.
		return nil, fmt.Errorf("%s: ES256 needs a P-256 key, got %s", path, priv.Curve.Params().Name)
	}
	return priv, nil
}

// generateEC creates a P-256 signing key and writes it to path, exactly once.
// See writeKeyOnce for why "exactly once" is the load-bearing part.
func generateEC(path string) (*ecdsa.PrivateKey, error) {
	priv, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, fmt.Errorf("generate signing key: %w", err)
	}
	existing, err := writeKeyOnce(path, priv)
	if err != nil {
		return nil, err
	}
	if existing == nil {
		return priv, nil
	}
	other, ok := existing.(*ecdsa.PrivateKey)
	if !ok {
		return nil, fmt.Errorf("%s: not an EC key", path)
	}
	return other, nil
}

// ECIssuer signs ES256 tokens with one key.
type ECIssuer struct {
	key     ECKey
	jwksURL string
}

// NewECIssuer returns an ES256 issuer for one signing key.
//
// There is no includeTyp switch as there is for the RSA issuer: every JWT in
// the decrypted capture of a working server carries 'typ: JWT', and nothing
// suggests NPLN differs. If a capture ever shows otherwise, add the switch then
// — with the capture cited, the way the RSA issuer's field is.
func NewECIssuer(key ECKey, jwksURL string) *ECIssuer {
	return &ECIssuer{key: key, jwksURL: jwksURL}
}

// Key returns the signing key, so it can be published in a JWK set.
func (i *ECIssuer) Key() ECKey { return i.key }

// Sign signs claims and returns the compact serialisation.
func (i *ECIssuer) Sign(claims gojwt.Claims) (string, error) {
	return i.sign(map[string]any{
		"alg": "ES256",
		"jku": i.jwksURL,
		"kid": i.key.ID,
		"typ": "JWT",
	}, claims)
}

// SignSessionToken signs claims with the header NPLN's session token carries:
// {alg, kid} and nothing else.
//
// A two-member header where every other ES256 token here has four. The reason
// is a capture, and the absence is the whole point: the response the console
// takes its session token out of — NextendoNetwork/splatoon-3's hardware
// capture of TrackGameSessionCreationTicket, MatchedUserSession field 3 — has a
// header of exactly {"alg":"ES256","kid":"754fe17e-…"}. No 'jku', so a client
// that compared this header against the shape it expects never sees a key-set
// URL it has never heard of; no 'typ'.
//
// Whether the client looks at the header at all is unconfirmed. Emitting the
// measured shape costs nothing and takes the question off the list — the same
// reasoning NewECIssuer's own comment gives for seeing 'typ' everywhere else.
func (i *ECIssuer) SignSessionToken(claims gojwt.Claims) (string, error) {
	return i.sign(map[string]any{
		"alg": "ES256",
		"kid": i.key.ID,
	}, claims)
}

func (i *ECIssuer) sign(header map[string]any, claims gojwt.Claims) (string, error) {
	tok := gojwt.NewWithClaims(gojwt.SigningMethodES256, claims)
	tok.Header = header
	return tok.SignedString(i.key.Private)
}

// ECVerifier checks ES256 tokens that SwitchNet itself issued.
//
// Deliberately a separate type from Verifier rather than one verifier that
// accepts either algorithm. A verifier that allowed both would let a token
// signed by the RSA key be presented where an EC one is expected, and vice
// versa — the algorithm-confusion attack that 'alg' checking exists to stop.
// One verifier, one algorithm, one key.
type ECVerifier struct {
	key ECKey
}

// NewECVerifier returns a verifier for one ES256 signing key.
func NewECVerifier(key ECKey) *ECVerifier { return &ECVerifier{key: key} }

// Verify parses and verifies a token into claims, which must be a pointer.
// Expiry is enforced; a token with no 'exp' is rejected.
func (v *ECVerifier) Verify(token string, claims gojwt.Claims) error {
	parser := gojwt.NewParser(
		gojwt.WithValidMethods([]string{"ES256"}),
		gojwt.WithExpirationRequired(),
	)
	_, err := parser.ParseWithClaims(token, claims, func(t *gojwt.Token) (any, error) {
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

// ECJWKSetFor builds a JWK set containing the given ES256 keys.
func ECJWKSetFor(usage string, keys ...ECKey) JWKSet {
	set := JWKSet{Keys: make([]JWK, 0, len(keys))}
	for _, k := range keys {
		pub := k.Public()
		set.Keys = append(set.Keys, JWK{
			KeyType:   "EC",
			Curve:     "P-256",
			X:         base64.RawURLEncoding.EncodeToString(coordinate(pub.X)),
			Y:         base64.RawURLEncoding.EncodeToString(coordinate(pub.Y)),
			Algorithm: "ES256",
			Use:       "sig",
			KeyID:     k.ID,
			Usage:     usage,
		})
	}
	return set
}

// p256CoordinateBytes is the fixed width of an X or Y coordinate on P-256.
const p256CoordinateBytes = 32

// coordinate encodes an EC public-key coordinate the way RFC 7518 §6.2.1.2
// requires: fixed width for the curve, left-padded with zeros.
//
// big.Int.Bytes() returns the MINIMAL big-endian encoding, so a coordinate that
// happens to start with a zero byte comes out 31 bytes instead of 32 — which
// happens to roughly one key in 256, at random, whenever a key is generated.
// Strict JWK parsers reject the short value outright; lax ones read it as a
// different number. Either way the failure appears long after the key was made,
// on one deployment in a few hundred, and looks nothing like a padding bug.
func coordinate(n *big.Int) []byte {
	buf := make([]byte, p256CoordinateBytes)
	n.FillBytes(buf)
	return buf
}
