package jwt

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"math/big"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	gojwt "github.com/golang-jwt/jwt/v5"
)

func testECKey(t *testing.T, issuer string) ECKey {
	t.Helper()
	ring := NewKeyring(filepath.Join(t.TempDir(), "keys"), true)
	k, err := ring.ECKey(issuer)
	if err != nil {
		t.Fatalf("ec key %s: %v", issuer, err)
	}
	return k
}

func TestAnNPLNTokenIsSignedWithES256(t *testing.T) {
	key := testECKey(t, IssuerNPLN)
	const jku = "https://t-dce9377b-lp1.lp1.t.npln.srv.nintendo.net/keys"
	issuer := NewECIssuer(key, jku)

	now := time.Now().Truncate(time.Second)
	token, err := issuer.Sign(gojwt.MapClaims{
		"sub": "a1b2c3d4e5f60718",
		"iat": now.Unix(),
		"exp": now.Add(time.Hour).Unix(),
	})
	if err != nil {
		t.Fatalf("sign: %v", err)
	}

	header, payload := decodeToken(t, token)

	// ES256 is the whole point of this path: NPLN is the only service in the
	// stack that does not take RS256, so a token that comes out RS256 here is
	// the bug this test exists for.
	assertString(t, header, "alg", "ES256")
	assertString(t, header, "typ", "JWT")
	assertString(t, header, "jku", jku)
	assertString(t, header, "kid", key.ID)
	assertString(t, payload, "sub", "a1b2c3d4e5f60718")
}

// The two algorithm-confusion tests below are the reason ECVerifier is a
// separate type from Verifier. A single verifier accepting either algorithm
// would pass both of these while being exactly the vulnerability 'alg'
// checking exists to prevent.

func TestAnRS256TokenIsRefusedByTheES256Verifier(t *testing.T) {
	rsaKey := testKey(t, IssuerBAASID)
	ecKey := testECKey(t, IssuerNPLN)

	now := time.Now()
	token, err := NewIssuer(rsaKey, "https://example.invalid/keys", true).Sign(gojwt.MapClaims{
		"sub": "attacker",
		"exp": now.Add(time.Hour).Unix(),
	})
	if err != nil {
		t.Fatalf("sign: %v", err)
	}

	var claims gojwt.MapClaims
	if err := NewECVerifier(ecKey).Verify(token, &claims); !errors.Is(err, ErrInvalidToken) {
		t.Fatalf("an RS256 token verified against the ES256 verifier: %v", err)
	}
}

func TestAnES256TokenIsRefusedByTheRS256Verifier(t *testing.T) {
	rsaKey := testKey(t, IssuerBAASID)
	ecKey := testECKey(t, IssuerNPLN)

	now := time.Now()
	token, err := NewECIssuer(ecKey, "https://example.invalid/keys").Sign(gojwt.MapClaims{
		"sub": "attacker",
		"exp": now.Add(time.Hour).Unix(),
	})
	if err != nil {
		t.Fatalf("sign: %v", err)
	}

	var claims gojwt.MapClaims
	if err := NewVerifier(rsaKey).Verify(token, &claims); !errors.Is(err, ErrInvalidToken) {
		t.Fatalf("an ES256 token verified against the RS256 verifier: %v", err)
	}
}

func TestTheES256VerifierRejectsAnotherECKeysToken(t *testing.T) {
	mine := testECKey(t, IssuerNPLN)
	theirs := testECKey(t, IssuerNPLN)

	token, err := NewECIssuer(theirs, "https://example.invalid/keys").Sign(gojwt.MapClaims{
		"sub": "somebody else",
		"exp": time.Now().Add(time.Hour).Unix(),
	})
	if err != nil {
		t.Fatalf("sign: %v", err)
	}

	var claims gojwt.MapClaims
	if err := NewECVerifier(mine).Verify(token, &claims); !errors.Is(err, ErrInvalidToken) {
		t.Fatalf("a token from a different EC key verified: %v", err)
	}
}

func TestTheES256VerifierRequiresExpiry(t *testing.T) {
	key := testECKey(t, IssuerNPLN)
	token, err := NewECIssuer(key, "https://example.invalid/keys").Sign(gojwt.MapClaims{
		"sub": "no expiry",
	})
	if err != nil {
		t.Fatalf("sign: %v", err)
	}

	var claims gojwt.MapClaims
	if err := NewECVerifier(key).Verify(token, &claims); !errors.Is(err, ErrInvalidToken) {
		t.Fatalf("a token with no exp verified: %v", err)
	}
}

// TestAnECJWKCoordinateIsPaddedToTheCurveWidth pins the padding rule directly,
// rather than by generating keys and hoping to hit the case.
//
// big.Int.Bytes() is minimal-width, so a coordinate whose top byte is zero
// encodes one byte short. That happens for about one generated key in 256, at
// random — far too rare to show up in a test run and far too common to leave to
// chance across deployments.
func TestAnECJWKCoordinateIsPaddedToTheCurveWidth(t *testing.T) {
	small := big.NewInt(1)
	got := coordinate(small)

	if len(got) != p256CoordinateBytes {
		t.Fatalf("coordinate width = %d, want %d", len(got), p256CoordinateBytes)
	}
	for i := 0; i < p256CoordinateBytes-1; i++ {
		if got[i] != 0 {
			t.Fatalf("byte %d = %#x, want zero padding", i, got[i])
		}
	}
	if got[p256CoordinateBytes-1] != 1 {
		t.Fatalf("last byte = %#x, want 1", got[p256CoordinateBytes-1])
	}
}

func TestAnECJWKSetPublishesTheVerificationKeyOnTheWire(t *testing.T) {
	key := testECKey(t, IssuerNPLN)
	set := ECJWKSetFor("internal", key)

	raw, err := json.Marshal(set)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	var decoded struct {
		Keys []map[string]any `json:"keys"`
	}
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(decoded.Keys) != 1 {
		t.Fatalf("got %d keys, want 1", len(decoded.Keys))
	}
	jwk := decoded.Keys[0]

	assertString(t, jwk, "kty", "EC")
	assertString(t, jwk, "crv", "P-256")
	assertString(t, jwk, "alg", "ES256")
	assertString(t, jwk, "use", "sig")
	assertString(t, jwk, "kid", key.ID)

	// An EC JWK must carry no RSA parameters. Before 'e' and 'n' gained
	// omitempty they were serialised as empty strings, which is a malformed JWK
	// — a strict parser rejects the whole set, taking every other key in it down
	// with it.
	for _, absent := range []string{"e", "n"} {
		if _, present := jwk[absent]; present {
			t.Errorf("EC JWK carries RSA parameter %q", absent)
		}
	}

	// The coordinates must decode to exactly the curve width, and must be the
	// real public key rather than anything derived from the private half.
	for _, name := range []string{"x", "y"} {
		s, ok := jwk[name].(string)
		if !ok {
			t.Fatalf("%s missing", name)
		}
		b, err := base64.RawURLEncoding.DecodeString(s)
		if err != nil {
			t.Fatalf("%s is not base64url: %v", name, err)
		}
		if len(b) != p256CoordinateBytes {
			t.Errorf("%s decodes to %d bytes, want %d", name, len(b), p256CoordinateBytes)
		}
	}
	pub := key.Public()
	if x := new(big.Int).SetBytes(mustDecode(t, jwk["x"].(string))); x.Cmp(pub.X) != 0 {
		t.Error("x is not the public key's X coordinate")
	}
	if y := new(big.Int).SetBytes(mustDecode(t, jwk["y"].(string))); y.Cmp(pub.Y) != 0 {
		t.Error("y is not the public key's Y coordinate")
	}
}

// TestAnRSAJWKStillCarriesItsParameters guards the other side of adding
// omitempty: the RSA path must be unchanged.
func TestAnRSAJWKStillCarriesItsParameters(t *testing.T) {
	key := testKey(t, IssuerBAASID)
	raw, err := json.Marshal(JWKSetFor("internal", key))
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var decoded struct {
		Keys []map[string]any `json:"keys"`
	}
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	jwk := decoded.Keys[0]

	assertString(t, jwk, "kty", "RSA")
	assertString(t, jwk, "alg", "RS256")
	for _, required := range []string{"e", "n"} {
		if s, ok := jwk[required].(string); !ok || s == "" {
			t.Errorf("RSA JWK lost its %q parameter", required)
		}
	}
	for _, absent := range []string{"crv", "x", "y"} {
		if _, present := jwk[absent]; present {
			t.Errorf("RSA JWK carries EC parameter %q", absent)
		}
	}
}

func TestAnECKeyringRefusesAnRSAKeyFileUnderTheSameName(t *testing.T) {
	dir := t.TempDir()
	ring := NewKeyring(dir, true)

	// Create the RSA key first, so the file on disk is an RSA one.
	if _, err := ring.Key(IssuerNPLN); err != nil {
		t.Fatalf("rsa key: %v", err)
	}

	// Asking for the same name as an EC key must fail rather than silently
	// hand back something that cannot sign ES256.
	if _, err := ring.ECKey(IssuerNPLN); err == nil {
		t.Fatal("an RSA key file was accepted as an ES256 signing key")
	}
}

func TestAnECKeyringRefusesTheWrongCurve(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, IssuerNPLN+".key.pem")

	priv, err := ecdsa.GenerateKey(elliptic.P384(), rand.Reader)
	if err != nil {
		t.Fatalf("generate P-384: %v", err)
	}
	if _, err := writeKeyOnce(path, priv); err != nil {
		t.Fatalf("write: %v", err)
	}

	if _, err := NewKeyring(dir, false).ECKey(IssuerNPLN); err == nil {
		t.Fatal("a P-384 key was accepted for ES256, which is P-256 by definition")
	}
}

// TestConcurrentKeyringsConvergeOnOneECKey mirrors the RSA test: several
// services starting at once share one key directory, and must all end up with
// the same key.
//
// EC key generation is FAST -- microseconds, against RSA-2048's milliseconds --
// which is why this test found a bug the RSA one did not. The window between
// publishing the filename and writing the contents is the same in both; a
// generator that returns almost instantly just lands in it far more often. The
// fix was to write to a temporary file and link it into place, so the name and
// the contents appear together. See writeKeyOnce.
//
// Released together and with enough goroutines that a regression fails reliably
// rather than one run in five.
func TestConcurrentKeyringsConvergeOnOneECKey(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "keys")

	const n = 32
	ids := make([]string, n)
	errs := make([]error, n)

	var wg sync.WaitGroup
	start := make(chan struct{})
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			k, err := NewKeyring(dir, true).ECKey(IssuerNPLN)
			ids[i], errs[i] = k.ID, err
		}(i)
	}
	close(start)
	wg.Wait()

	for i, err := range errs {
		if err != nil {
			// "no PEM block" here means a keyring read a file another had
			// created but not yet filled.
			t.Fatalf("keyring %d: %v", i, err)
		}
	}
	for i := 1; i < n; i++ {
		if ids[i] != ids[0] {
			t.Fatalf("keyring %d converged on %s, keyring 0 on %s", i, ids[i], ids[0])
		}
	}

	// Exactly one file: no leftovers from the temporary files the writers used,
	// each of which would be a stray private key in the key directory.
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read key dir: %v", err)
	}
	if len(entries) != 1 {
		var names []string
		for _, e := range entries {
			names = append(names, e.Name())
		}
		t.Fatalf("key dir holds %v, want exactly one key file", names)
	}
}

func mustDecode(t *testing.T, s string) []byte {
	t.Helper()
	b, err := base64.RawURLEncoding.DecodeString(s)
	if err != nil {
		t.Fatalf("decode %q: %v", s, err)
	}
	return b
}
