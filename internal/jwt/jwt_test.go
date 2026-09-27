package jwt

import (
	"crypto/rsa"
	"encoding/base64"
	"encoding/json"
	"math/big"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	gojwt "github.com/golang-jwt/jwt/v5"
)

func TestKeyringGeneratesAndReloadsTheSameKey(t *testing.T) {
	dir := t.TempDir()

	first := NewKeyring(dir, true)
	a, err := first.Key(IssuerDevice)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}

	// A fresh keyring over the same directory must load the key from disk, not
	// make a new one. If it made a new one, every restart would invalidate every
	// token the consoles are holding.
	second := NewKeyring(dir, false)
	b, err := second.Key(IssuerDevice)
	if err != nil {
		t.Fatalf("reload key: %v", err)
	}

	if a.ID != b.ID {
		t.Errorf("key id changed across reload: %q then %q", a.ID, b.ID)
	}
	if a.Private.N.Cmp(b.Private.N) != 0 {
		t.Error("reloaded a different key")
	}
}

func TestKeyringRefusesToGenerateWhenNotAllowed(t *testing.T) {
	// The point of generate_if_missing=false is that a lost key directory fails
	// loudly rather than silently minting a new identity for the whole network.
	ring := NewKeyring(t.TempDir(), false)
	if _, err := ring.Key(IssuerDevice); err == nil {
		t.Fatal("expected an error for a missing key with generation disabled")
	}
}

func TestKeyringIssuersHaveDistinctKeys(t *testing.T) {
	ring := NewKeyring(t.TempDir(), true)

	seen := map[string]string{}
	for _, issuer := range []string{IssuerDevice, IssuerApp, IssuerBAASAccess, IssuerBAASID} {
		k, err := ring.Key(issuer)
		if err != nil {
			t.Fatalf("key %s: %v", issuer, err)
		}
		if other, dup := seen[k.ID]; dup {
			// Sharing a key between issuers would let a token minted for one
			// purpose verify as another: an id token would pass as an access
			// token, so a game server's copy of a player's token could act as
			// that player against their account.
			t.Fatalf("issuers %s and %s share a key", issuer, other)
		}
		seen[k.ID] = issuer
	}
}

func TestKeyringRejectsPathTraversalInKeyName(t *testing.T) {
	ring := NewKeyring(t.TempDir(), true)
	for _, name := range []string{"../escape", "a/b", "", "Upper", "with space"} {
		if _, err := ring.Key(name); err == nil {
			t.Errorf("accepted invalid key name %q", name)
		}
	}
}

// ---- device tokens -------------------------------------------------------

func TestDeviceTokenHasNintendoClaimShape(t *testing.T) {
	key := testKey(t, IssuerDevice)
	const jku = "https://dcert-lp1.ndas.srv.nintendo.net/keys"
	issuer := NewIssuer(key, jku, true)

	now := time.Now().Truncate(time.Second)
	token, err := issuer.Sign(DeviceTokenClaims{
		Subject:  "68337aca28815cbb",
		Audience: "8f849b5d34778d8e",
		IssuedAt: now.Unix(),
		Expires:  now.Add(24 * time.Hour).Unix(),
		Issuer:   "dauth-lp1.ndas.srv.nintendo.net",
		TokenID:  "e59a0b0e-94e1-44aa-b5db-0fc0cf502ada",
		Nintendo: DeviceInfo{
			SerialNumber: "XAW10012345678",
			ProductCode:  "HAC",
			DeviceType:   "NX Prod 1",
			IsT:          false,
		},
	})
	if err != nil {
		t.Fatalf("sign: %v", err)
	}

	header, payload := decodeToken(t, token)

	// Compared against the example device token in
	// https://github.com/kinnay/NintendoClients/wiki/Switch-Tokens#dauth-tokens
	assertString(t, header, "alg", "RS256")
	assertString(t, header, "typ", "JWT")
	assertString(t, header, "jku", jku)
	assertString(t, header, "kid", key.ID)

	assertString(t, payload, "sub", "68337aca28815cbb")
	assertString(t, payload, "aud", "8f849b5d34778d8e")
	assertString(t, payload, "iss", "dauth-lp1.ndas.srv.nintendo.net")
	assertString(t, payload, "jti", "e59a0b0e-94e1-44aa-b5db-0fc0cf502ada")

	nin, ok := payload["nintendo"].(map[string]any)
	if !ok {
		t.Fatal("no nintendo claim")
	}
	assertString(t, nin, "sn", "XAW10012345678")
	assertString(t, nin, "pc", "HAC")
	assertString(t, nin, "dt", "NX Prod 1")
	if nin["ist"] != false {
		t.Errorf("ist = %v, want false", nin["ist"])
	}
	// A device token's nintendo block carries no device id: the device id is the
	// subject. Emitting one would not match a capture.
	if _, present := nin["di"]; present {
		t.Error("device token nintendo block should not carry 'di'")
	}
}

func TestVerifierAcceptsOwnTokenAndRejectsOthers(t *testing.T) {
	ring := NewKeyring(t.TempDir(), true)
	deviceKey, err := ring.Key(IssuerDevice)
	if err != nil {
		t.Fatal(err)
	}
	appKey, err := ring.Key(IssuerApp)
	if err != nil {
		t.Fatal(err)
	}

	now := time.Now()
	claims := DeviceTokenClaims{
		Subject:  "aaaaaaaaaaaaaaaa",
		Audience: "8f849b5d34778d8e",
		IssuedAt: now.Unix(),
		Expires:  now.Add(time.Hour).Unix(),
		Issuer:   "dauth-lp1.ndas.srv.nintendo.net",
		TokenID:  NewTokenID(),
	}
	token, err := NewIssuer(deviceKey, "https://example.invalid/keys", true).Sign(claims)
	if err != nil {
		t.Fatal(err)
	}

	var out DeviceTokenClaims
	if err := NewVerifier(deviceKey).Verify(token, &out); err != nil {
		t.Fatalf("own token did not verify: %v", err)
	}
	if out.Subject != claims.Subject {
		t.Errorf("sub = %q, want %q", out.Subject, claims.Subject)
	}

	// A token signed by the application key must not verify as a device token.
	if err := NewVerifier(appKey).Verify(token, &DeviceTokenClaims{}); err == nil {
		t.Fatal("a token signed by another issuer verified")
	}
}

func TestVerifierRejectsExpiredToken(t *testing.T) {
	key := testKey(t, IssuerDevice)
	now := time.Now()
	token, err := NewIssuer(key, "https://example.invalid/keys", true).Sign(DeviceTokenClaims{
		Subject:  "aaaaaaaaaaaaaaaa",
		Audience: "8f849b5d34778d8e",
		IssuedAt: now.Add(-2 * time.Hour).Unix(),
		Expires:  now.Add(-time.Hour).Unix(),
		Issuer:   "dauth-lp1.ndas.srv.nintendo.net",
		TokenID:  NewTokenID(),
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := NewVerifier(key).Verify(token, &DeviceTokenClaims{}); err == nil {
		t.Fatal("an expired token verified")
	}
}

func TestVerifierRejectsUnsignedAndAlteredTokens(t *testing.T) {
	key := testKey(t, IssuerDevice)
	verifier := NewVerifier(key)

	now := time.Now()
	valid, err := NewIssuer(key, "https://example.invalid/keys", true).Sign(DeviceTokenClaims{
		Subject: "aaaaaaaaaaaaaaaa", Audience: "x",
		IssuedAt: now.Unix(), Expires: now.Add(time.Hour).Unix(),
		Issuer: "iss", TokenID: NewTokenID(),
	})
	if err != nil {
		t.Fatal(err)
	}

	parts := strings.Split(valid, ".")
	if len(parts) != 3 {
		t.Fatalf("expected three parts, got %d", len(parts))
	}

	cases := map[string]string{
		// 'alg: none' is the classic JWT bypass. WithValidMethods must reject it.
		"alg none":     unsignedToken(t),
		"no signature": parts[0] + "." + parts[1] + ".",
		"bad signature": parts[0] + "." + parts[1] + "." +
			base64.RawURLEncoding.EncodeToString([]byte("not a signature")),
		"garbage": "not.a.token",
		"empty":   "",
	}
	for name, token := range cases {
		if err := verifier.Verify(token, &DeviceTokenClaims{}); err == nil {
			t.Errorf("%s: verified when it should not have", name)
		}
	}

	// Editing a claim must invalidate the signature.
	var claims map[string]any
	decodeSegment(t, parts[1], &claims)
	claims["sub"] = "bbbbbbbbbbbbbbbb"
	altered, err := json.Marshal(claims)
	if err != nil {
		t.Fatal(err)
	}
	tampered := parts[0] + "." + base64.RawURLEncoding.EncodeToString(altered) + "." + parts[2]
	if err := verifier.Verify(tampered, &DeviceTokenClaims{}); err == nil {
		t.Error("a token with an edited subject verified")
	}
}

func unsignedToken(t *testing.T) string {
	t.Helper()
	tok := gojwt.NewWithClaims(gojwt.SigningMethodNone, DeviceTokenClaims{
		Subject: "aaaaaaaaaaaaaaaa",
		Expires: time.Now().Add(time.Hour).Unix(),
	})
	s, err := tok.SignedString(gojwt.UnsafeAllowNoneSignatureType)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

// ---- application tokens --------------------------------------------------

func TestAppTokenHasNoAudienceAndCarriesAppInfo(t *testing.T) {
	key := testKey(t, IssuerApp)
	issuer := NewIssuer(key, "https://acert-lp1.ndas.srv.nintendo.net/keys", true)

	now := time.Now()
	token, err := issuer.Sign(AppTokenClaims{
		Subject:  "0100abf008968000",
		IssuedAt: now.Unix(),
		Expires:  now.Add(24 * time.Hour).Unix(),
		Issuer:   "aauth-lp1.ndas.srv.nintendo.net",
		TokenID:  "82df667b-0da1-4381-87e4-1ae403c8b568",
		Nintendo: AppInfo{
			AppID:            "0100abf008968000",
			AppVersion:       "0007",
			AppTime:          now.Unix(),
			EDI:              "b46bda4e1dd5e7ce002430a68b2c6d4e",
			OnlinePlayPolicy: "MEMBERSHIP_REQUIRED",
			PolicyHandler:    "GAME_SERVER",
		},
	})
	if err != nil {
		t.Fatalf("sign: %v", err)
	}

	_, payload := decodeToken(t, token)

	// An application token has no 'aud'. Emitting one would not match a capture.
	if _, present := payload["aud"]; present {
		t.Error("application token should not carry 'aud'")
	}
	assertString(t, payload, "sub", "0100abf008968000")

	nin, ok := payload["nintendo"].(map[string]any)
	if !ok {
		t.Fatal("no nintendo claim")
	}
	assertString(t, nin, "ai", "0100abf008968000")
	assertString(t, nin, "av", "0007")
	assertString(t, nin, "edi", "b46bda4e1dd5e7ce002430a68b2c6d4e")
	assertString(t, nin, "opp", "MEMBERSHIP_REQUIRED")
	assertString(t, nin, "ph", "GAME_SERVER")
	// 'hm' only belongs in an id token.
	if _, present := nin["hm"]; present {
		t.Error("application token should not carry 'hm'")
	}
}

func TestFreePolicyOmitsPolicyHandler(t *testing.T) {
	key := testKey(t, IssuerApp)
	issuer := NewIssuer(key, "https://example.invalid/keys", true)

	now := time.Now()
	token, err := issuer.Sign(AppTokenClaims{
		Subject: "0100abf008968000",
		Expires: now.Add(time.Hour).Unix(), IssuedAt: now.Unix(),
		Issuer: "iss", TokenID: NewTokenID(),
		Nintendo: AppInfo{
			AppID: "0100abf008968000", AppVersion: "0007",
			EDI: "b46bda4e1dd5e7ce002430a68b2c6d4e", OnlinePlayPolicy: "FREE",
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	_, payload := decodeToken(t, token)
	nin := payload["nintendo"].(map[string]any)
	// Nintendo only sends the policy handler when membership is required.
	if _, present := nin["ph"]; present {
		t.Error("'ph' should be absent when the policy is FREE")
	}
}

// ---- BAAS tokens ---------------------------------------------------------

func TestBAASAccessTokenShape(t *testing.T) {
	key := testKey(t, IssuerBAASAccess)
	const jku = "https://e0d67c509fb203858ebcb2fe3f88c2aa.baas.nintendo.com/1.0.0/internal_certificates"
	// BAAS tokens carry 'typ: JWT', like every other token in a real working
	// server's capture. See Issuer.includeTyp for what this used to be.
	issuer := NewIssuer(key, jku, true)

	now := time.Now()
	token, err := issuer.Sign(BAASTokenClaims{
		Subject:   "ed9e2f05d286f7b8",
		Audience:  "ed9e2f05d286f7b8",
		IssuedAt:  now.Unix(),
		Expires:   now.Add(3 * time.Hour).Unix(),
		Issuer:    "https://e0d67c509fb203858ebcb2fe3f88c2aa.baas.nintendo.com",
		TokenID:   "878d0735-571a-4b94-82a6-2bf183114db1",
		Type:      "token",
		GrantType: 1,
		Status:    AccessTokenStatus,
		Nintendo: DeviceInfo{
			DeviceType: "NX Prod 1", ProductCode: "HAC",
			DeviceID: "68337aca28815cbb", SerialNumber: "XAW10012345678",
		},
	})
	if err != nil {
		t.Fatalf("sign: %v", err)
	}

	header, payload := decodeToken(t, token)

	// The header 'typ' and the payload 'typ' are different fields: the header
	// is the standard JWT type ("JWT") and the payload is BAAS's own token
	// kind ("token"/"id_token"). Conflating the two is what previously had
	// this issuer emitting no header 'typ' at all.
	assertString(t, header, "typ", "JWT")
	assertString(t, header, "jku", jku)

	assertString(t, payload, "typ", "token")
	if payload["bs:grt"] != float64(1) {
		t.Errorf("bs:grt = %v, want 1", payload["bs:grt"])
	}
	sts, ok := payload["bs:sts"].([]any)
	if !ok || len(sts) != 1 || sts[0] != float64(385) {
		t.Errorf("bs:sts = %v, want [385]", payload["bs:sts"])
	}
	// An anonymous token names no device account.
	if _, present := payload["bs:did"]; present {
		t.Error("an anonymous access token should not carry 'bs:did'")
	}

	nin := payload["nintendo"].(map[string]any)
	// Unlike a device token, a BAAS access token's device block DOES carry 'di'.
	assertString(t, nin, "di", "68337aca28815cbb")
}

func TestBAASUserTokenShape(t *testing.T) {
	key := testKey(t, IssuerBAASAccess)
	issuer := NewIssuer(key, "https://example.invalid/certs", true)

	now := time.Now()
	token, err := issuer.Sign(BAASTokenClaims{
		Subject: "b4922963e6b8deb2", Audience: "ed9e2f05d286f7b8",
		IssuedAt: now.Unix(), Expires: now.Add(3 * time.Hour).Unix(),
		Issuer:  "https://e0d67c509fb203858ebcb2fe3f88c2aa.baas.nintendo.com",
		TokenID: NewTokenID(), Type: "token", GrantType: 2,
		DeviceAccountID: "2ded458f5e0beee2", Status: UserTokenStatus,
	})
	if err != nil {
		t.Fatal(err)
	}

	_, payload := decodeToken(t, token)
	assertString(t, payload, "sub", "b4922963e6b8deb2")
	assertString(t, payload, "bs:did", "2ded458f5e0beee2")
	if payload["bs:grt"] != float64(2) {
		t.Errorf("bs:grt = %v, want 2", payload["bs:grt"])
	}
	if got := len(payload["bs:sts"].([]any)); got != 7 {
		t.Errorf("bs:sts has %d elements, want 7", got)
	}
	// A user token carries no device block.
	if _, present := payload["nintendo"]; present {
		t.Error("a user access token should not carry a 'nintendo' claim")
	}
}

func TestIDTokenCarriesMembershipForGameServerPolicy(t *testing.T) {
	key := testKey(t, IssuerBAASID)
	issuer := NewIssuer(key, "https://example.invalid/certificates", true)

	hasMembership := true
	now := time.Now()
	token, err := issuer.Sign(BAASTokenClaims{
		Subject: "b4922963e6b8deb2", Audience: "ed9e2f05d286f7b8",
		IssuedAt: now.Unix(), Expires: now.Add(3 * time.Hour).Unix(),
		Issuer: "iss", TokenID: NewTokenID(),
		Type: "id_token", DeviceAccountID: "2ded458f5e0beee2",
		Nintendo: AppInfo{
			AppID: "0100c2500fc20000", AppVersion: "0007", AppTime: now.Unix(),
			EDI:              "84e16d390427028b3788ef082d342ce0",
			OnlinePlayPolicy: "MEMBERSHIP_REQUIRED", PolicyHandler: "GAME_SERVER",
			HasMembership: &hasMembership,
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	_, payload := decodeToken(t, token)
	assertString(t, payload, "typ", "id_token")
	// An id token has no grant type.
	if _, present := payload["bs:grt"]; present {
		t.Error("an id token should not carry 'bs:grt'")
	}
	nin := payload["nintendo"].(map[string]any)
	assertString(t, nin, "ai", "0100c2500fc20000")
	if nin["hm"] != true {
		t.Errorf("hm = %v, want true", nin["hm"])
	}
}

// ---- JWK set -------------------------------------------------------------

func TestJWKSetPublishesTheVerificationKey(t *testing.T) {
	key := testKey(t, IssuerBAASAccess)
	set := JWKSetFor("internal", key)

	if len(set.Keys) != 1 {
		t.Fatalf("set has %d keys, want 1", len(set.Keys))
	}
	jwk := set.Keys[0]
	if jwk.KeyType != "RSA" || jwk.Algorithm != "RS256" || jwk.Use != "sig" {
		t.Errorf("unexpected JWK metadata: %+v", jwk)
	}
	if jwk.KeyID != key.ID {
		t.Errorf("kid = %q, want %q", jwk.KeyID, key.ID)
	}
	if jwk.Usage != "internal" {
		t.Errorf("usage = %q, want internal", jwk.Usage)
	}

	// The published modulus and exponent must actually reconstruct the key, or
	// a game server verifying an id token against the JWK set fails while
	// everything looks correct.
	nBytes, err := base64.RawURLEncoding.DecodeString(jwk.Modulus)
	if err != nil {
		t.Fatalf("decode modulus: %v", err)
	}
	eBytes, err := base64.RawURLEncoding.DecodeString(jwk.Exponent)
	if err != nil {
		t.Fatalf("decode exponent: %v", err)
	}
	rebuilt := &rsa.PublicKey{
		N: new(big.Int).SetBytes(nBytes),
		E: int(new(big.Int).SetBytes(eBytes).Int64()),
	}
	if rebuilt.N.Cmp(key.Public().N) != 0 || rebuilt.E != key.Public().E {
		t.Error("the published JWK does not reconstruct the signing key")
	}
	// RFC 7518 wants the minimal big-endian encoding: no leading zero byte.
	if len(eBytes) > 0 && eBytes[0] == 0 {
		t.Error("exponent has a leading zero byte")
	}
}

func TestJWKSHandlerServesJSON(t *testing.T) {
	key := testKey(t, IssuerDevice)
	rec := httptest.NewRecorder()
	JWKSHandler(JWKSetFor("", key)).ServeHTTP(rec, httptest.NewRequest("GET", "/keys", nil))

	if rec.Code != 200 {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
		t.Errorf("Content-Type = %q", ct)
	}

	var set JWKSet
	if err := json.Unmarshal(rec.Body.Bytes(), &set); err != nil {
		t.Fatalf("response is not a JWK set: %v", err)
	}
	if len(set.Keys) != 1 || set.Keys[0].KeyID != key.ID {
		t.Errorf("unexpected key set: %+v", set)
	}
	// 'usage' is a BAAS-only field and must be absent elsewhere.
	if set.Keys[0].Usage != "" {
		t.Errorf("usage = %q, want empty for a non-BAAS key set", set.Keys[0].Usage)
	}
}

// ---- derived ids ---------------------------------------------------------

func TestDeriveHexIDIsStableAndSecretDependent(t *testing.T) {
	a := DeriveHexID("secret", 8, "device", "fingerprint")
	b := DeriveHexID("secret", 8, "device", "fingerprint")
	if a != b {
		t.Errorf("not stable: %q then %q", a, b)
	}
	if len(a) != 16 {
		t.Errorf("length = %d, want 16 hex digits for 8 bytes", len(a))
	}

	if DeriveHexID("other", 8, "device", "fingerprint") == a {
		t.Error("changing the secret did not change the id")
	}
	if DeriveHexID("secret", 8, "device", "other") == a {
		t.Error("changing a part did not change the id")
	}
	// The parts must not be concatenated ambiguously: ("ab","c") and ("a","bc")
	// have to differ, or two consoles could collide onto one device id.
	if DeriveHexID("s", 8, "ab", "c") == DeriveHexID("s", 8, "a", "bc") {
		t.Error("parts are joined ambiguously")
	}
}

// ---- helpers -------------------------------------------------------------

func testKey(t *testing.T, issuer string) Key {
	t.Helper()
	ring := NewKeyring(filepath.Join(t.TempDir(), "keys"), true)
	k, err := ring.Key(issuer)
	if err != nil {
		t.Fatalf("key %s: %v", issuer, err)
	}
	return k
}

func decodeToken(t *testing.T, token string) (header, payload map[string]any) {
	t.Helper()
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		t.Fatalf("expected three token parts, got %d", len(parts))
	}
	decodeSegment(t, parts[0], &header)
	decodeSegment(t, parts[1], &payload)
	return header, payload
}

func decodeSegment(t *testing.T, segment string, dst any) {
	t.Helper()
	raw, err := base64.RawURLEncoding.DecodeString(segment)
	if err != nil {
		t.Fatalf("decode token segment: %v", err)
	}
	if err := json.Unmarshal(raw, dst); err != nil {
		t.Fatalf("parse token segment: %v", err)
	}
}

func assertString(t *testing.T, m map[string]any, key, want string) {
	t.Helper()
	got, ok := m[key].(string)
	if !ok {
		t.Errorf("%s is missing or not a string (%v)", key, m[key])
		return
	}
	if got != want {
		t.Errorf("%s = %q, want %q", key, got, want)
	}
}

// TestConcurrentKeyringsConvergeOnOneKey is the regression test for a race that
// broke a real first run.
//
// Every GRID0+ service shares one key directory, and starting them all at
// once -- `switchnetctl run`, or `systemctl start switchnet.target` -- means
// they all find the key missing on a fresh directory. If each generates its
// own, the last writer wins on disk while every process keeps the one it made
// in memory — so dauth signs with one key and aauth verifies with another.
// The console is then told "device token expired", which points nowhere near
// the cause.
func TestConcurrentKeyringsConvergeOnOneKey(t *testing.T) {
	dir := t.TempDir()

	const goroutines = 8
	keys := make([]Key, goroutines)
	errs := make([]error, goroutines)

	var wg sync.WaitGroup
	start := make(chan struct{})
	for i := range goroutines {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			// Release them together, to make the collision likely rather than
			// theoretical.
			<-start
			// A separate keyring per goroutine: that is the point. Sharing one
			// would be serialised by its own mutex and prove nothing about two
			// processes.
			keys[i], errs[i] = NewKeyring(dir, true).Key(IssuerDevice)
		}(i)
	}
	close(start)
	wg.Wait()

	for i, err := range errs {
		if err != nil {
			t.Fatalf("keyring %d: %v", i, err)
		}
	}

	// Every keyring must have ended up with the same key, and it must be the one
	// on disk.
	onDisk, err := NewKeyring(dir, false).Key(IssuerDevice)
	if err != nil {
		t.Fatalf("reload the key from disk: %v", err)
	}
	for i, k := range keys {
		if k.ID != onDisk.ID {
			t.Errorf("keyring %d has key %s, but %s is on disk: the services would "+
				"sign and verify with different keys", i, k.ID, onDisk.ID)
		}
	}

	// And there is exactly one key file, not eight.
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Errorf("%d files in the key directory, want 1: %v", len(entries), entries)
	}
}
