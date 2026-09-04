package dauth

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"io"
	"log/slog"
	"math/big"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	sjwt "github.com/n-popescu/switchnet/internal/jwt"
)

// harness is a dauth server wired up over httptest.
type harness struct {
	t        *testing.T
	server   *httptest.Server
	key      sjwt.Key
	verifier *sjwt.Verifier
	seen     []string
}

func newHarness(t *testing.T) *harness {
	t.Helper()

	ring := sjwt.NewKeyring(t.TempDir(), true)
	key, err := ring.Key(sjwt.IssuerDevice)
	if err != nil {
		t.Fatalf("key: %v", err)
	}

	h := &harness{t: t, key: key, verifier: sjwt.NewVerifier(key)}

	handler := New(Config{
		Issuer:              "dauth-lp1.ndas.srv.nintendo.net",
		JWKSURL:             "https://dcert-lp1.ndas.srv.nintendo.net/keys",
		TokenTTL:            24 * time.Hour,
		ChallengeTTL:        time.Minute,
		ClientCertHeader:    "X-SwitchNet-Client-Cert",
		ClientCertPEMHeader: "X-SwitchNet-Client-Cert-PEM",
		DeviceIDSecret:      "test-secret",
	}, sjwt.NewIssuer(key, "https://dcert-lp1.ndas.srv.nintendo.net/keys", true),
		slog.New(slog.DiscardHandler),
		func(id, serial, systemVersion string, ist bool) {
			h.seen = append(h.seen, id)
		})

	mux := http.NewServeMux()
	handler.Routes(mux)
	mux.HandleFunc("/", NotFound)

	h.server = httptest.NewServer(mux)
	t.Cleanup(h.server.Close)
	return h
}

// postForm sends a form-encoded request the way the console does.
func (h *harness) postForm(path string, values url.Values, headers ...[2]string) *http.Response {
	h.t.Helper()
	req, err := http.NewRequest("POST", h.server.URL+path, strings.NewReader(values.Encode()))
	if err != nil {
		h.t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("User-Agent", "libcurl (nnDauth; 16f4553f-9eee-4e39-9b61-59bc7c99b7c8; SDK 13.3.0.0)")
	req.Header.Set("X-Nintendo-PowerState", "FA")
	for _, kv := range headers {
		req.Header.Set(kv[0], kv[1])
	}
	resp, err := h.server.Client().Do(req)
	if err != nil {
		h.t.Fatal(err)
	}
	h.t.Cleanup(func() { _ = resp.Body.Close() })
	return resp
}

func (h *harness) postJSON(path, body string) *http.Response {
	h.t.Helper()
	req, err := http.NewRequest("POST", h.server.URL+path, strings.NewReader(body))
	if err != nil {
		h.t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := h.server.Client().Do(req)
	if err != nil {
		h.t.Fatal(err)
	}
	h.t.Cleanup(func() { _ = resp.Body.Close() })
	return resp
}

func decode(t *testing.T, resp *http.Response, dst any) {
	t.Helper()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(raw, dst); err != nil {
		t.Fatalf("response is not JSON (%s): %v", raw, err)
	}
}

// ---- challenge -----------------------------------------------------------

func TestChallengeIsThirtyFiveBytesOnEveryAPIVersion(t *testing.T) {
	h := newHarness(t)

	// Which API version a console uses is decided by its firmware, so all of
	// them have to answer.
	for _, path := range []string{
		"/v5/challenge", "/v6/challenge", "/v7/challenge", "/v8/challenge",
		"/v3-59ed5fa1c25bb2aea8c4d73d74b919a94d89ed48d6865b728f63547943b17404/challenge",
		"/v4-fb411cdeda62ff6da97e57c29d6300bc12b6b709869e56906aec88cb42a299cd/challenge",
	} {
		resp := h.postForm(path, url.Values{"key_generation": {"13"}})
		if resp.StatusCode != 200 {
			t.Errorf("%s: status = %d, want 200", path, resp.StatusCode)
			continue
		}

		var body challengeResponse
		decode(t, resp, &body)

		// Nintendo stopped padding these at the end of 2024 and uses base64url.
		challenge, err := base64.RawURLEncoding.DecodeString(body.Challenge)
		if err != nil {
			t.Errorf("%s: challenge is not unpadded base64url: %v", path, err)
			continue
		}
		if len(challenge) != 35 {
			t.Errorf("%s: challenge is %d bytes, want 35", path, len(challenge))
		}
		// The first byte is a version marker and is always 2 in current captures.
		if challenge[0] != 2 {
			t.Errorf("%s: challenge[0] = %d, want 2", path, challenge[0])
		}

		data, err := base64.RawURLEncoding.DecodeString(body.Data)
		if err != nil {
			t.Errorf("%s: data is not unpadded base64url: %v", path, err)
			continue
		}
		if len(data) != 16 {
			t.Errorf("%s: data is %d bytes, want 16", path, len(data))
		}
	}
}

func TestChallengeDataIsStableAcrossRequests(t *testing.T) {
	h := newHarness(t)

	// On Nintendo's server the 'data' value never changes for a given key
	// generation: it is the AES key the console needs to compute its MAC. A
	// value that changed per request would be wrong even though nothing here
	// checks the MAC.
	var first, second challengeResponse
	decode(t, h.postForm("/v7/challenge", url.Values{"key_generation": {"13"}}), &first)
	decode(t, h.postForm("/v7/challenge", url.Values{"key_generation": {"13"}}), &second)

	if first.Data != second.Data {
		t.Errorf("data changed between requests: %q then %q", first.Data, second.Data)
	}
	if first.Challenge == second.Challenge {
		t.Error("the challenge did not change between requests")
	}
}

// ---- device tokens -------------------------------------------------------

func TestDeviceTokenRequest(t *testing.T) {
	h := newHarness(t)

	// The body of a real v7 request, from
	// https://github.com/kinnay/NintendoClients/wiki/DAuth-Server#examples
	resp := h.postForm("/v7/device_auth_token", url.Values{
		"challenge":      {"mtAvqNqzYSoCEixxL_rjWoHfdDjAH51h5XcKZ6ksq2s="},
		"client_id":      {"8f849b5d34778d8e"},
		"ist":            {"false"},
		"key_generation": {"13"},
		"system_version": {"CusHY#000d0000#r1xneESd4PiTRYIhVIl0bK1ST5L5BUmv_uGPLqc4PPo="},
		"mac":            {"AW9LE1TSN0xrzY1FfHHXwg"},
	})
	if resp.StatusCode != 200 {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}

	var body deviceTokenResponse
	decode(t, resp, &body)

	if body.ExpiresIn != 86400 {
		t.Errorf("expires_in = %d, want 86400", body.ExpiresIn)
	}
	if body.DeviceAuthToken == "" {
		t.Fatal("no device_auth_token")
	}

	var claims sjwt.DeviceTokenClaims
	if err := h.verifier.Verify(body.DeviceAuthToken, &claims); err != nil {
		t.Fatalf("token does not verify: %v", err)
	}
	if claims.Audience != "8f849b5d34778d8e" {
		t.Errorf("aud = %q, want the requested client id", claims.Audience)
	}
	if claims.Issuer != "dauth-lp1.ndas.srv.nintendo.net" {
		t.Errorf("iss = %q", claims.Issuer)
	}
	if len(claims.Subject) != 16 {
		t.Errorf("sub = %q, want 16 hex digits", claims.Subject)
	}
	if claims.Nintendo.ProductCode != "HAC" || claims.Nintendo.DeviceType != "NX Prod 1" {
		t.Errorf("device info = %+v", claims.Nintendo)
	}
	if !strings.HasPrefix(claims.Nintendo.SerialNumber, "XAW") {
		t.Errorf("serial = %q, want an XAW-prefixed serial", claims.Nintendo.SerialNumber)
	}
	if len(h.seen) != 1 {
		t.Errorf("the device-seen callback fired %d times, want 1", len(h.seen))
	}
}

func TestDeviceTokenRequiresClientID(t *testing.T) {
	h := newHarness(t)
	resp := h.postForm("/v7/device_auth_token", url.Values{"ist": {"false"}})
	if resp.StatusCode != 400 {
		t.Fatalf("status = %d, want 400", resp.StatusCode)
	}

	var body errorResponse
	decode(t, resp, &body)
	if len(body.Errors) != 1 || body.Errors[0].Code != "0014" {
		t.Errorf("errors = %+v, want a single 0014", body.Errors)
	}
}

func TestClientCertificateGivesEachConsoleItsOwnDeviceID(t *testing.T) {
	h := newHarness(t)

	token := func(fingerprint string) sjwt.DeviceTokenClaims {
		t.Helper()
		values := url.Values{"client_id": {"8f849b5d34778d8e"}, "ist": {"false"}}
		var headers [][2]string
		if fingerprint != "" {
			headers = append(headers, [2]string{"X-SwitchNet-Client-Cert", fingerprint})
		}
		resp := h.postForm("/v7/device_auth_token", values, headers...)
		var body deviceTokenResponse
		decode(t, resp, &body)
		var claims sjwt.DeviceTokenClaims
		if err := h.verifier.Verify(body.DeviceAuthToken, &claims); err != nil {
			t.Fatalf("token does not verify: %v", err)
		}
		return claims
	}

	a := token("aa:bb:cc")
	b := token("dd:ee:ff")
	if a.Subject == b.Subject {
		t.Error("two client certificates produced the same device id")
	}
	// The same certificate must produce the same id across reboots, or the
	// dashboard's device list gains a row every time a console starts.
	if again := token("aa:bb:cc"); again.Subject != a.Subject {
		t.Errorf("the same certificate produced %q then %q", a.Subject, again.Subject)
	}

	// Without a certificate, every console shares one id. That is documented and
	// harmless — accounts are keyed on the BAAS device account, never on this —
	// but it must be a stable id rather than a random one, or a console's own
	// token subject changes between boots.
	noCertA := token("")
	noCertB := token("")
	if noCertA.Subject != noCertB.Subject {
		t.Error("the no-certificate device id is not stable")
	}
	if noCertA.Subject == a.Subject {
		t.Error("the no-certificate device id collides with a real one")
	}
}

// TestX5tS256IsTheRealThumbprintOfTheClientCertificate exercises the
// 'x5t#S256' claim added after a 2026-08-27 capture of a working Nextendo
// device token showed it present. It must be a genuine SHA-256 digest of the
// certificate nginx passed through — not a fingerprint of anything else, and
// not present at all when no certificate was presented.
func TestX5tS256IsTheRealThumbprintOfTheClientCertificate(t *testing.T) {
	h := newHarness(t)

	der, pemBytes := selfSignedCertPEM(t)
	sum := sha256.Sum256(der)
	want := base64.RawURLEncoding.EncodeToString(sum[:])

	// Mimic nginx's $ssl_client_escaped_cert: percent-encoded PEM text. Must be
	// PathEscape, not QueryEscape -- QueryEscape's '+'-for-space form encoding
	// is not what nginx's escaping does, and PathUnescape (which the handler
	// uses, to avoid misreading a literal '+' in base64 as an encoded space)
	// does not reverse it.
	escaped := url.PathEscape(string(pemBytes))

	resp := h.postForm("/v7/device_auth_token",
		url.Values{"client_id": {"8f849b5d34778d8e"}, "ist": {"false"}},
		[2]string{"X-SwitchNet-Client-Cert-PEM", escaped})

	var body deviceTokenResponse
	decode(t, resp, &body)
	var claims sjwt.DeviceTokenClaims
	if err := h.verifier.Verify(body.DeviceAuthToken, &claims); err != nil {
		t.Fatalf("token does not verify: %v", err)
	}
	if claims.Nintendo.X5tS256 != want {
		t.Errorf("x5t#S256 = %q, want %q (the cert's real SHA-256 thumbprint)", claims.Nintendo.X5tS256, want)
	}

	// No certificate presented: the claim must be absent, not a wrong or
	// placeholder value.
	resp = h.postForm("/v7/device_auth_token",
		url.Values{"client_id": {"8f849b5d34778d8e"}, "ist": {"false"}})
	decode(t, resp, &body)
	var noCertClaims sjwt.DeviceTokenClaims
	if err := h.verifier.Verify(body.DeviceAuthToken, &noCertClaims); err != nil {
		t.Fatalf("token does not verify: %v", err)
	}
	if noCertClaims.Nintendo.X5tS256 != "" {
		t.Errorf("x5t#S256 = %q with no client certificate presented, want empty", noCertClaims.Nintendo.X5tS256)
	}

	// omitempty means a wrong derivation could still round-trip through the Go
	// struct as "" without the key having genuinely been left out of the JSON
	// the console receives; check the raw claim too.
	parts := strings.Split(body.DeviceAuthToken, ".")
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		t.Fatalf("decode payload: %v", err)
	}
	var raw struct {
		Nintendo map[string]any `json:"nintendo"`
	}
	if err := json.Unmarshal(payload, &raw); err != nil {
		t.Fatalf("unmarshal payload: %v", err)
	}
	if _, present := raw.Nintendo["x5t#S256"]; present {
		t.Error(`the 'nintendo' claim has an "x5t#S256" key with no certificate presented, want it omitted entirely`)
	}
}

// TestEsvIsFreshRandomPaddingPerToken exercises the 'esv' claim.
//
// 2026-09-04: NextendoNetwork/nx-dauth's real, running source (source-
// available, PolyForm Shield) shows esv is base64url(33 random bytes),
// freshly generated on every single token minted -- see DeviceInfo's package
// comment. Two device tokens minted in the same /v8/device_auth_tokens
// request must therefore carry different esv values, not the same one
// reused across both, and each must be well-formed base64url.
func TestEsvIsFreshRandomPaddingPerToken(t *testing.T) {
	h := newHarness(t)

	resp := h.postJSON("/v8/device_auth_tokens", `{
		"system_version": "00140001",
		"ist": false,
		"token_requests": [
			{"client_id": "8f849b5d34778d8e"},
			{"client_id": "83b72b05dc3278d7"}
		]
	}`)
	if resp.StatusCode != 200 {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}

	var body multiTokenResponse
	decode(t, resp, &body)
	if len(body.Results) != 2 {
		t.Fatalf("got %d results, want 2", len(body.Results))
	}

	var esvs []string
	for _, res := range body.Results {
		var claims sjwt.DeviceTokenClaims
		if err := h.verifier.Verify(res.DeviceAuthToken, &claims); err != nil {
			t.Fatalf("%s: token does not verify: %v", res.ClientID, err)
		}
		if _, err := base64.RawURLEncoding.DecodeString(claims.Nintendo.Esv); err != nil {
			t.Errorf("%s: esv = %q is not valid unpadded base64url: %v", res.ClientID, claims.Nintendo.Esv, err)
		}
		esvs = append(esvs, claims.Nintendo.Esv)
	}
	if esvs[0] == "" || esvs[1] == "" {
		t.Fatalf("esv values = %q, %q, want both non-empty", esvs[0], esvs[1])
	}
	if esvs[0] == esvs[1] {
		t.Error("both tokens from the same request carry the same esv, want each freshly random")
	}
}

// selfSignedCertPEM returns a throwaway self-signed certificate's raw DER and
// its PEM encoding, standing in for the console's own TLS client certificate.
func selfSignedCertPEM(t *testing.T) (der []byte, pemBytes []byte) {
	t.Helper()
	priv, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "test console"},
		NotBefore:    time.Now(),
		NotAfter:     time.Now().Add(time.Hour),
	}
	der, err = x509.CreateCertificate(rand.Reader, tmpl, tmpl, &priv.PublicKey, priv)
	if err != nil {
		t.Fatalf("create certificate: %v", err)
	}
	pemBytes = pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	return der, pemBytes
}

func TestV8ReturnsOneTokenPerRequestedClientID(t *testing.T) {
	h := newHarness(t)

	// From 20.0.0 the endpoint is plural, takes JSON, and asks for several
	// tokens at once.
	resp := h.postJSON("/v8/device_auth_tokens", `{
		"system_version": "00140001",
		"fw_revision": "aabbccdd",
		"ist": false,
		"key_generation": 20,
		"challenge": "AAAA",
		"mac": "BBBB",
		"token_requests": [
			{"client_id": "8f849b5d34778d8e"},
			{"client_id": "83b72b05dc3278d7"}
		]
	}`)
	if resp.StatusCode != 200 {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}

	var body multiTokenResponse
	decode(t, resp, &body)

	if len(body.Results) != 2 {
		t.Fatalf("got %d results, want 2", len(body.Results))
	}
	for _, res := range body.Results {
		if res.DeviceAuthToken == "" {
			t.Errorf("%s: no token", res.ClientID)
			continue
		}
		var claims sjwt.DeviceTokenClaims
		if err := h.verifier.Verify(res.DeviceAuthToken, &claims); err != nil {
			t.Errorf("%s: token does not verify: %v", res.ClientID, err)
			continue
		}
		// Each token's audience must be the client id it was asked for, or a
		// service will reject a token minted for a different one.
		if claims.Audience != res.ClientID {
			t.Errorf("%s: aud = %q", res.ClientID, claims.Audience)
		}
		if res.ExpiresIn != 86400 {
			t.Errorf("%s: expires_in = %d", res.ClientID, res.ExpiresIn)
		}
	}
	if body.Results[0].ClientID == body.Results[1].ClientID {
		t.Error("both results carry the same client id")
	}
}

func TestV8RejectsAnEmptyTokenRequestList(t *testing.T) {
	h := newHarness(t)
	resp := h.postJSON("/v8/device_auth_tokens", `{"token_requests": []}`)
	if resp.StatusCode != 400 {
		t.Errorf("status = %d, want 400", resp.StatusCode)
	}
}

// ---- edge tokens ---------------------------------------------------------

func TestEdgeTokenIsAkamaiShapedNotAJWT(t *testing.T) {
	h := newHarness(t)

	resp := h.postForm("/v7/edge_token", url.Values{
		"client_id": {"67bf9945b45248c6"},
		"vendor_id": {"akamai"},
		"ist":       {"false"},
	})
	if resp.StatusCode != 200 {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}

	var body edgeTokenResponse
	decode(t, resp, &body)

	// An edge token is not a JWT: it is Akamai's tilde-separated token format.
	for _, want := range []string{"exp=", "~acl=", "~data=", "~hmac="} {
		if !strings.Contains(body.DToken, want) {
			t.Errorf("dtoken is missing %q: %s", want, body.DToken)
		}
	}
	if strings.Count(body.DToken, ".") > 0 && strings.Count(body.DToken, "~") == 0 {
		t.Errorf("dtoken looks like a JWT: %s", body.DToken)
	}
}

// ---- JWK set and 404 ----------------------------------------------------

func TestJWKSetIsServedAtTheJKUPath(t *testing.T) {
	h := newHarness(t)

	// The 'jku' of every device token is <dcert host>/keys, so a console must be
	// able to fetch the key set from there.
	resp, err := h.server.Client().Get(h.server.URL + "/keys")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != 200 {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	var set sjwt.JWKSet
	decode(t, resp, &set)
	if len(set.Keys) != 1 || set.Keys[0].KeyID != h.key.ID {
		t.Errorf("key set = %+v, want the device signing key", set)
	}
}

func TestUnroutedPathReturnsADAuthShapedError(t *testing.T) {
	h := newHarness(t)

	resp, err := h.server.Client().Get(h.server.URL + "/v7/nonexistent")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != 400 {
		t.Errorf("status = %d, want 400", resp.StatusCode)
	}
	var body errorResponse
	decode(t, resp, &body)
	// Nintendo answers an unknown path with error 0012, not with Go's plain-text
	// 404. A console reading a plain-text body gets a parse failure instead of an
	// error it can report.
	if len(body.Errors) != 1 || body.Errors[0].Code != "0012" {
		t.Errorf("errors = %+v, want a single 0012", body.Errors)
	}
}
