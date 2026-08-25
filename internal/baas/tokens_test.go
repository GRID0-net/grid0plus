package baas

import (
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"
	"time"

	sjwt "github.com/n-popescu/switchnet/internal/jwt"
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
