package aauth

import (
	"encoding/base64"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	sjwt "github.com/n-popescu/switchnet/internal/jwt"
)

const splatoon3TitleID = "0100c2500fc20000"

type harness struct {
	t *testing.T

	server *httptest.Server

	deviceKey sjwt.Key
	appKey    sjwt.Key

	appVerifier *sjwt.Verifier
}

func newHarness(t *testing.T, policy, handler string, overrides map[string][2]string) *harness {
	t.Helper()

	ring := sjwt.NewKeyring(t.TempDir(), true)
	deviceKey, err := ring.Key(sjwt.IssuerDevice)
	if err != nil {
		t.Fatal(err)
	}
	appKey, err := ring.Key(sjwt.IssuerApp)
	if err != nil {
		t.Fatal(err)
	}

	h := &harness{
		t: t, deviceKey: deviceKey, appKey: appKey,
		appVerifier: sjwt.NewVerifier(appKey),
	}

	srv := New(Config{
		Issuer:   "aauth-lp1.ndas.srv.nintendo.net",
		JWKSURL:  "https://acert-lp1.ndas.srv.nintendo.net/keys",
		TokenTTL: 24 * time.Hour,
		PolicyFor: func(titleID string) (string, string) {
			if o, ok := overrides[titleID]; ok {
				return o[0], o[1]
			}
			return policy, handler
		},
		DeviceIDSecret: "test-secret",
	}, sjwt.NewIssuer(appKey, "https://acert-lp1.ndas.srv.nintendo.net/keys", true),
		sjwt.NewVerifier(deviceKey), slog.New(slog.DiscardHandler))

	mux := http.NewServeMux()
	srv.Routes(mux)
	mux.HandleFunc("/", NotFound)

	h.server = httptest.NewServer(mux)
	t.Cleanup(h.server.Close)
	return h
}

// deviceToken mints the device token a console would present, signed with the
// same key the aauth server verifies against.
func (h *harness) deviceToken() string {
	h.t.Helper()
	now := time.Now()
	token, err := sjwt.NewIssuer(h.deviceKey, "https://dcert-lp1.ndas.srv.nintendo.net/keys", true).
		Sign(sjwt.DeviceTokenClaims{
			Subject:  "68337aca28815cbb",
			Audience: "8f849b5d34778d8e",
			IssuedAt: now.Unix(),
			Expires:  now.Add(24 * time.Hour).Unix(),
			Issuer:   "dauth-lp1.ndas.srv.nintendo.net",
			TokenID:  sjwt.NewTokenID(),
			Nintendo: sjwt.DeviceInfo{
				SerialNumber: "XAW10012345678", ProductCode: "HAC",
				DeviceType: "NX Prod 1", IsT: false,
			},
		})
	if err != nil {
		h.t.Fatal(err)
	}
	return token
}

func (h *harness) postForm(path string, values url.Values) *http.Response {
	h.t.Helper()
	req, err := http.NewRequest("POST", h.server.URL+path, strings.NewReader(values.Encode()))
	if err != nil {
		h.t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("X-Nintendo-PowerState", "FA")
	resp, err := h.server.Client().Do(req)
	if err != nil {
		h.t.Fatal(err)
	}
	h.t.Cleanup(func() { _ = resp.Body.Close() })
	return resp
}

// postRaw sends a body exactly as given, so the console's stray leading
// ampersand can be reproduced.
func (h *harness) postRaw(path, body string) *http.Response {
	h.t.Helper()
	req, err := http.NewRequest("POST", h.server.URL+path, strings.NewReader(body))
	if err != nil {
		h.t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
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

// ---- /v1/time ------------------------------------------------------------

func TestTimeReturnsMillisecondsAndAddressInBodyAndHeaders(t *testing.T) {
	h := newHarness(t, "FREE", "GAME_SERVER", nil)

	resp, err := h.server.Client().Get(h.server.URL + "/v1/time")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != 200 {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "text/plain") {
		t.Errorf("Content-Type = %q, want text/plain", ct)
	}

	// The console reads the clock from here, so both the headers and the body
	// have to be right: a console whose clock is wrong has every token in the
	// chain rejected as expired, and the error blames the token.
	if resp.Header.Get("X-NINTENDO-UNIXTIME") == "" {
		t.Error("X-NINTENDO-UNIXTIME is missing")
	}
	if resp.Header.Get("X-NINTENDO-GLOBAL-IP") == "" {
		t.Error("X-NINTENDO-GLOBAL-IP is missing")
	}

	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Fields(string(raw))
	if len(lines) != 2 {
		t.Fatalf("body has %d lines, want 2 (time, address): %q", len(lines), raw)
	}
	if lines[0] != resp.Header.Get("X-NINTENDO-UNIXTIME") {
		t.Errorf("body time %q does not match the header %q",
			lines[0], resp.Header.Get("X-NINTENDO-UNIXTIME"))
	}
	// Milliseconds, not seconds: a 13-digit value at present.
	if len(lines[0]) < 13 {
		t.Errorf("time %q looks like seconds, not milliseconds", lines[0])
	}
}

func TestTimeTrustsOnlyTheLastForwardedForEntry(t *testing.T) {
	h := newHarness(t, "FREE", "GAME_SERVER", nil)

	req, err := http.NewRequest("GET", h.server.URL+"/v1/time", nil)
	if err != nil {
		t.Fatal(err)
	}
	// The first entry is whatever the client claimed; only the last was written
	// by the proxy in front of us. Trusting the first would let a console have
	// the server echo any address it liked.
	req.Header.Set("X-Forwarded-For", "1.2.3.4, 203.0.113.9")
	resp, err := h.server.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()

	if got := resp.Header.Get("X-NINTENDO-GLOBAL-IP"); got != "203.0.113.9" {
		t.Errorf("global IP = %q, want the last X-Forwarded-For entry", got)
	}
}

// ---- gamecard challenge --------------------------------------------------

func TestChallengeV3ReturnsValueAndSeed(t *testing.T) {
	h := newHarness(t, "FREE", "GAME_SERVER", nil)

	resp := h.postForm("/v3/challenge", url.Values{"device_auth_token": {h.deviceToken()}})
	if resp.StatusCode != 200 {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}

	var body challengeV4Response
	decode(t, resp, &body)

	value, err := base64.StdEncoding.DecodeString(body.Value)
	if err != nil || len(value) != 16 {
		t.Errorf("value = %q (%d bytes, err %v), want 16 bytes", body.Value, len(value), err)
	}
	seed, err := base64.StdEncoding.DecodeString(body.Seed)
	if err != nil || len(seed) != 15 {
		t.Errorf("seed = %q (%d bytes, err %v), want 15 bytes", body.Seed, len(seed), err)
	}
}

func TestChallengeV5ReturnsChallengeAndSource(t *testing.T) {
	h := newHarness(t, "FREE", "GAME_SERVER", nil)

	resp := h.postForm("/v5/challenge", url.Values{"device_auth_token": {h.deviceToken()}})
	if resp.StatusCode != 200 {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}

	var body challengeV5Response
	decode(t, resp, &body)

	// v5 redesigned the gamecard challenge: 32-byte challenge, 9-byte source.
	challenge, err := base64.StdEncoding.DecodeString(body.Challenge)
	if err != nil || len(challenge) != 32 {
		t.Errorf("challenge = %q (%d bytes), want 32 bytes", body.Challenge, len(challenge))
	}
	src, err := base64.StdEncoding.DecodeString(body.ChallengeSrc)
	if err != nil || len(src) != 9 {
		t.Errorf("challenge_src = %q (%d bytes), want 9 bytes", body.ChallengeSrc, len(src))
	}
	if body.Seed == "" {
		t.Error("seed is missing")
	}
}

func TestChallengeAcceptsTheConsolesStrayLeadingAmpersand(t *testing.T) {
	h := newHarness(t, "FREE", "GAME_SERVER", nil)

	// Up to system version 17.0.1 the console prefixes this body with '&' even
	// though device_auth_token is the only parameter. That is what the console
	// sends, so it has to parse.
	resp := h.postRaw("/v3/challenge", "&device_auth_token="+url.QueryEscape(h.deviceToken()))
	if resp.StatusCode != 200 {
		t.Fatalf("status = %d, want 200: the leading '&' was not tolerated", resp.StatusCode)
	}
}

func TestChallengeRequiresADeviceToken(t *testing.T) {
	h := newHarness(t, "FREE", "GAME_SERVER", nil)
	resp := h.postForm("/v3/challenge", url.Values{})
	if resp.StatusCode != 400 {
		t.Errorf("status = %d, want 400", resp.StatusCode)
	}
}

// ---- application tokens --------------------------------------------------

func TestApplicationTokenForADigitalTitle(t *testing.T) {
	h := newHarness(t, "FREE", "GAME_SERVER", nil)

	resp := h.postForm("/v3/application_auth_token", url.Values{
		"application_id":      {splatoon3TitleID},
		"application_version": {"00070000"},
		"device_auth_token":   {h.deviceToken()},
		"media_type":          {"DIGITAL"},
		// Accepted without being looked at: SwitchNet does not check ownership.
		"cert":     {"not-a-real-ticket"},
		"cert_key": {"not-a-real-key"},
	})
	if resp.StatusCode != 200 {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}

	var body appTokenResponse
	decode(t, resp, &body)

	if body.ExpiresIn != 86400 {
		t.Errorf("expires_in = %d, want 86400", body.ExpiresIn)
	}
	if body.Settings == nil {
		// Nintendo sends an empty list, not null. A console decoding null into a
		// list type is a plausible failure and costs nothing to avoid.
		t.Error("settings is null, want an empty list")
	}
	if body.OnlinePlayPolicy != "FREE" {
		t.Errorf("online_play_policy = %q, want FREE", body.OnlinePlayPolicy)
	}
	if body.PolicyHandler != "" {
		t.Errorf("policy_handler = %q, want it absent when the policy is FREE",
			body.PolicyHandler)
	}

	var claims sjwt.AppTokenClaims
	if err := h.appVerifier.Verify(body.ApplicationAuthToken, &claims); err != nil {
		t.Fatalf("token does not verify: %v", err)
	}
	if claims.Subject != splatoon3TitleID {
		t.Errorf("sub = %q, want the title id", claims.Subject)
	}
	if claims.Nintendo.AppID != splatoon3TitleID {
		t.Errorf("ai = %q", claims.Nintendo.AppID)
	}
	// application_version is 8 hex digits in the request; the token's 'av' is
	// the high half.
	if claims.Nintendo.AppVersion != "0007" {
		t.Errorf("av = %q, want 0007 for application_version=00070000", claims.Nintendo.AppVersion)
	}
	if len(claims.Nintendo.EDI) != 32 {
		t.Errorf("edi = %q, want 32 hex digits", claims.Nintendo.EDI)
	}
	// A regular title's token carries no device block.
	if claims.Nintendo.DeviceID != "" || claims.Nintendo.SerialNumber != "" {
		t.Errorf("a non-system title's token carries a device block: %+v", claims.Nintendo)
	}
}

func TestEDIIsStablePerDeviceAndTitle(t *testing.T) {
	h := newHarness(t, "FREE", "GAME_SERVER", nil)

	edi := func(titleID string) string {
		t.Helper()
		resp := h.postForm("/v3/application_auth_token", url.Values{
			"application_id":      {titleID},
			"application_version": {"00010000"},
			"device_auth_token":   {h.deviceToken()},
			"media_type":          {"DIGITAL"},
		})
		var body appTokenResponse
		decode(t, resp, &body)
		var claims sjwt.AppTokenClaims
		if err := h.appVerifier.Verify(body.ApplicationAuthToken, &claims); err != nil {
			t.Fatal(err)
		}
		return claims.Nintendo.EDI
	}

	// A game server keying on 'edi' expects the same console and game to give
	// the same value across reboots.
	if a, b := edi(splatoon3TitleID), edi(splatoon3TitleID); a != b {
		t.Errorf("edi changed for the same device and title: %q then %q", a, b)
	}
	// Two titles must not share one.
	if edi(splatoon3TitleID) == edi("0100152000022000") {
		t.Error("two titles produced the same edi")
	}
}

func TestMembershipRequiredPolicyEmitsPolicyHandler(t *testing.T) {
	h := newHarness(t, "MEMBERSHIP_REQUIRED", "GAME_SERVER", nil)

	resp := h.postForm("/v3/application_auth_token", url.Values{
		"application_id":      {splatoon3TitleID},
		"application_version": {"00070000"},
		"device_auth_token":   {h.deviceToken()},
		"media_type":          {"DIGITAL"},
	})
	var body appTokenResponse
	decode(t, resp, &body)

	if body.OnlinePlayPolicy != "MEMBERSHIP_REQUIRED" {
		t.Errorf("online_play_policy = %q", body.OnlinePlayPolicy)
	}
	if body.PolicyHandler != "GAME_SERVER" {
		t.Errorf("policy_handler = %q, want GAME_SERVER", body.PolicyHandler)
	}

	var claims sjwt.AppTokenClaims
	if err := h.appVerifier.Verify(body.ApplicationAuthToken, &claims); err != nil {
		t.Fatal(err)
	}
	if claims.Nintendo.OnlinePlayPolicy != "MEMBERSHIP_REQUIRED" {
		t.Errorf("opp = %q", claims.Nintendo.OnlinePlayPolicy)
	}
	if claims.Nintendo.PolicyHandler != "GAME_SERVER" {
		t.Errorf("ph = %q", claims.Nintendo.PolicyHandler)
	}
}

func TestPerTitlePolicyOverride(t *testing.T) {
	h := newHarness(t, "FREE", "GAME_SERVER", map[string][2]string{
		splatoon3TitleID: {"MEMBERSHIP_REQUIRED", "GAME_SERVER"},
	})

	for titleID, wantPolicy := range map[string]string{
		splatoon3TitleID:   "MEMBERSHIP_REQUIRED",
		"0100152000022000": "FREE",
	} {
		resp := h.postForm("/v3/application_auth_token", url.Values{
			"application_id":      {titleID},
			"application_version": {"00010000"},
			"device_auth_token":   {h.deviceToken()},
			"media_type":          {"DIGITAL"},
		})
		var body appTokenResponse
		decode(t, resp, &body)
		if body.OnlinePlayPolicy != wantPolicy {
			t.Errorf("%s: online_play_policy = %q, want %q",
				titleID, body.OnlinePlayPolicy, wantPolicy)
		}
	}
}

func TestSystemTitleTokenCarriesTheDeviceBlock(t *testing.T) {
	h := newHarness(t, "FREE", "GAME_SERVER", nil)

	// 010000000000003e is OLSC, one of the titles allowed to use the SYSTEM
	// media type. Its token carries a device block that a regular title's
	// token does not.
	resp := h.postForm("/v3/application_auth_token", url.Values{
		"application_id":      {"010000000000003e"},
		"application_version": {"00000000"},
		"device_auth_token":   {h.deviceToken()},
		"media_type":          {"SYSTEM"},
	})
	var body appTokenResponse
	decode(t, resp, &body)

	var claims sjwt.AppTokenClaims
	if err := h.appVerifier.Verify(body.ApplicationAuthToken, &claims); err != nil {
		t.Fatal(err)
	}
	if claims.Nintendo.DeviceID != "68337aca28815cbb" {
		t.Errorf("di = %q, want the device token's subject", claims.Nintendo.DeviceID)
	}
	if claims.Nintendo.SerialNumber != "XAW10012345678" {
		t.Errorf("sn = %q", claims.Nintendo.SerialNumber)
	}
	if claims.Nintendo.IsT == nil {
		t.Error("ist is missing from a system title's token")
	}
}

func TestSystemMediaTypeOnANonSystemTitleGetsNoDeviceBlock(t *testing.T) {
	h := newHarness(t, "FREE", "GAME_SERVER", nil)

	// Claiming SYSTEM for a title that is not a system title must not hand out a
	// device block: that block is what a system service reads to identify the
	// console, and any game could otherwise ask for it.
	resp := h.postForm("/v3/application_auth_token", url.Values{
		"application_id":      {splatoon3TitleID},
		"application_version": {"00070000"},
		"device_auth_token":   {h.deviceToken()},
		"media_type":          {"SYSTEM"},
	})
	var body appTokenResponse
	decode(t, resp, &body)

	var claims sjwt.AppTokenClaims
	if err := h.appVerifier.Verify(body.ApplicationAuthToken, &claims); err != nil {
		t.Fatal(err)
	}
	if claims.Nintendo.DeviceID != "" || claims.Nintendo.SerialNumber != "" {
		t.Errorf("a non-system title got a device block: %+v", claims.Nintendo)
	}
}

func TestV5AuthTypeParameterIsAccepted(t *testing.T) {
	h := newHarness(t, "FREE", "GAME_SERVER", nil)

	// v5 renamed media_type to auth_type. A console on 19.0.0 or later sends
	// only the new name.
	resp := h.postForm("/v5/application_auth_token", url.Values{
		"application_id":      {splatoon3TitleID},
		"application_version": {"00070000"},
		"device_auth_token":   {h.deviceToken()},
		"auth_type":           {"DIGITAL"},
	})
	if resp.StatusCode != 200 {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
}

func TestApplicationTokenRejectsAnInvalidDeviceToken(t *testing.T) {
	h := newHarness(t, "FREE", "GAME_SERVER", nil)

	for name, token := range map[string]string{
		"garbage": "not-a-token",
		"empty":   "",
		"signed by the wrong key": func() string {
			// A token signed with the application key must not pass as a device
			// token.
			now := time.Now()
			s, err := sjwt.NewIssuer(h.appKey, "https://x.invalid/keys", true).
				Sign(sjwt.DeviceTokenClaims{
					Subject: "68337aca28815cbb", Audience: "8f849b5d34778d8e",
					IssuedAt: now.Unix(), Expires: now.Add(time.Hour).Unix(),
					Issuer: "dauth-lp1.ndas.srv.nintendo.net", TokenID: sjwt.NewTokenID(),
				})
			if err != nil {
				t.Fatal(err)
			}
			return s
		}(),
	} {
		resp := h.postForm("/v3/application_auth_token", url.Values{
			"application_id":      {splatoon3TitleID},
			"application_version": {"00070000"},
			"device_auth_token":   {token},
			"media_type":          {"DIGITAL"},
		})
		if resp.StatusCode != 400 {
			t.Errorf("%s: status = %d, want 400", name, resp.StatusCode)
			continue
		}
		var body errorResponse
		decode(t, resp, &body)
		if len(body.Errors) == 0 {
			t.Errorf("%s: no error in the response", name)
			continue
		}
		// 0103 is "device token expired", which is what Nintendo returns when
		// the presented device token is not usable.
		if body.Errors[0].Code != "0103" && body.Errors[0].Code != "0118" {
			t.Errorf("%s: error code = %q, want 0103 or 0118", name, body.Errors[0].Code)
		}
	}
}

func TestApplicationTokenRejectsAMalformedTitleID(t *testing.T) {
	h := newHarness(t, "FREE", "GAME_SERVER", nil)

	for _, titleID := range []string{"", "abc", strings.Repeat("a", 17)} {
		resp := h.postForm("/v3/application_auth_token", url.Values{
			"application_id":      {titleID},
			"application_version": {"00070000"},
			"device_auth_token":   {h.deviceToken()},
			"media_type":          {"DIGITAL"},
		})
		if resp.StatusCode != 400 {
			t.Errorf("title id %q: status = %d, want 400", titleID, resp.StatusCode)
		}
	}
}

func TestJWKSetIsServedAtTheJKUPath(t *testing.T) {
	h := newHarness(t, "FREE", "GAME_SERVER", nil)

	resp, err := h.server.Client().Get(h.server.URL + "/keys")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()

	var set sjwt.JWKSet
	decode(t, resp, &set)
	if len(set.Keys) != 1 || set.Keys[0].KeyID != h.appKey.ID {
		t.Errorf("key set = %+v, want the application signing key", set)
	}
}
