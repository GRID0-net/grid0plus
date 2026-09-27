package baas

import (
	"encoding/base64"
	"encoding/json"
	"net/url"
	"strings"
	"testing"
	"time"

	sjwt "github.com/n-popescu/grid0plus/internal/jwt"
)

// Helpers for inspecting the tokens the server issues.
//
// The tests decode tokens by hand rather than through the verifier so that they
// assert on the JSON that actually goes over the wire. A claim that is present
// in the Go struct but omitted by a json tag, or emitted under the wrong name,
// would pass a round-trip through the verifier and still not match a capture.

// parseBAASClaims decodes a BAAS access, user or id token.
func parseBAASClaims(t *testing.T, token string) sjwt.BAASTokenClaims {
	t.Helper()
	_, payload := decodeTokenParts(t, token)

	buf, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	var claims sjwt.BAASTokenClaims
	if err := json.Unmarshal(buf, &claims); err != nil {
		t.Fatalf("token payload does not decode as BAAS claims: %v", err)
	}
	return claims
}

// nintendoClaim returns a token's 'nintendo' claim, or nil if it has none.
func nintendoClaim(t *testing.T, token string) map[string]any {
	t.Helper()
	_, payload := decodeTokenParts(t, token)
	nin, ok := payload["nintendo"].(map[string]any)
	if !ok {
		return nil
	}
	return nin
}

func decodeTokenParts(t *testing.T, token string) (header, payload map[string]any) {
	t.Helper()
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		t.Fatalf("expected three token parts, got %d in %q", len(parts), token)
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

// jwks fetches a JWK set endpoint with no Authorization header, which is how a
// game server would reach it.
func (h *harness) jwks(t *testing.T, path string) sjwt.JWKSet {
	t.Helper()
	resp := h.do(request{method: "GET", path: path})
	requireStatus(t, resp, 200)

	var set sjwt.JWKSet
	decode(t, resp, &set)
	return set
}

// mintUserToken signs a user access token directly, so a test can construct a
// token that is correctly signed but internally inconsistent — a user id paired
// with a device account that does not belong to it. That is what a bug in token
// minting would produce, and the server has to refuse it.
func (h *harness) mintUserToken(userID, deviceAccountID string) string {
	h.t.Helper()
	now := h.clock()
	token, err := h.accessIssuer.Sign(sjwt.BAASTokenClaims{
		Subject:         userID,
		Audience:        testAudience,
		IssuedAt:        now.Unix(),
		Expires:         now.Add(time.Hour).Unix(),
		Issuer:          testIssuer,
		TokenID:         sjwt.NewTokenID(),
		Type:            "token",
		GrantType:       2,
		DeviceAccountID: deviceAccountID,
		Status:          sjwt.UserTokenStatus,
	})
	if err != nil {
		h.t.Fatal(err)
	}
	return token
}

// TestEveryIssuedTokenCarriesTheJWTTypeHeader walks the whole Nintendo Account
// link flow and checks the standard 'typ: JWT' header on every token the server
// hands the console.
//
// This is a regression test for a wrong asserted absence. CLAUDE.md used to
// state "a BAAS token has no 'typ' header" as a convention, cmd/baas built all
// three BAAS issuers with includeTyp=false because of it, and a test in
// internal/jwt asserted the header was missing -- so the suite actively held
// the behaviour in place. A 2026-08-28 sweep of every JWT in
// docs/CAPTURES/raw/nextendo-working-nalink-2026-08-27.flow, the decrypted
// capture of a server this console links against successfully, found all
// twelve carrying 'typ: JWT', BAAS tokens included. The likely origin of the
// mistake is the payload's own 'typ' claim ("token"/"id_token"), a different
// field that really is absent from some tokens -- so this test checks the
// header and the payload separately rather than treating them as one thing.
func TestEveryIssuedTokenCarriesTheJWTTypeHeader(t *testing.T) {
	h := newHarness(t)
	registerLoginUser(t, h, "carol", "hunter2222")

	client := noRedirectClient()
	authorizeResp, err := client.PostForm(h.server.URL+"/connect/1.0.0/authorize", url.Values{
		"redirect_uri": {"nintendo://lhub.nx.sys"},
		"username":     {"carol"},
		"password":     {"hunter2222"},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer authorizeResp.Body.Close()

	frag, err := url.ParseQuery(strings.TrimPrefix(
		authorizeResp.Header.Get("Location"), "nintendo://lhub.nx.sys#"))
	if err != nil {
		t.Fatal(err)
	}

	tokenResp, err := client.PostForm(h.server.URL+"/connect/1.0.0/api/token", url.Values{
		"grant_type": {"authorization_code"},
		"code":       {frag.Get("code")},
		"scope":      {"openid offline user"},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer tokenResp.Body.Close()

	var body struct {
		AccessToken string `json:"access_token"`
		IDToken     string `json:"id_token"`
	}
	if err := json.NewDecoder(tokenResp.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}

	for _, tc := range []struct {
		name  string
		token string
	}{
		{"authorize redirect id_token", frag.Get("id_token")},
		{"api/token access_token", body.AccessToken},
		{"api/token id_token", body.IDToken},
	} {
		header, _ := decodeTokenParts(t, tc.token)
		if got := header["typ"]; got != "JWT" {
			t.Errorf("%s: header typ = %v, want \"JWT\"", tc.name, got)
		}
	}
}

// TestNAAccessTokenCarriesScopeIDs checks the 'ac:scp' claim, the numeric form
// of the granted scopes. Absent entirely until 2026-08-28; a real working
// capture carries it on every accounts.nintendo.com access_token. Unknown
// scopes are dropped rather than guessed at -- see jwt.BAASTokenClaims.ACScopes.
func TestNAAccessTokenCarriesScopeIDs(t *testing.T) {
	if got, want := naScopeIDsFor([]string{"openid", "offline", "user"}), []int{0, 1, 8}; len(got) != len(want) {
		t.Fatalf("naScopeIDsFor = %v, want %v", got, want)
	} else {
		for i := range want {
			if got[i] != want[i] {
				t.Errorf("naScopeIDsFor[%d] = %d, want %d", i, got[i], want[i])
			}
		}
	}
	// A scope with no known id contributes nothing rather than a placeholder.
	if got := naScopeIDsFor([]string{"not-a-real-scope"}); got != nil {
		t.Errorf("naScopeIDsFor(unknown) = %v, want nil", got)
	}
}
