package npln

import (
	"context"
	"strings"
	"testing"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/emptypb"

	"github.com/n-popescu/switchnet/internal/db"
	sjwt "github.com/n-popescu/switchnet/internal/jwt"
	authpb "github.com/n-popescu/switchnet/proto/auth/v1"
	maintpb "github.com/n-popescu/switchnet/proto/maintenance/v1"
)

// TestABAASIDTokenBuysAnNPLNSession drives the phase-1 → phase-2 seam end to
// end over a real gRPC connection.
//
// This is the test that proves the two halves of SwitchNet connect at all: an
// id token signed by the key BAAS publishes at /1.0.0/certificates is presented
// to the game server, which verifies it and issues its own session.
func TestABAASIDTokenBuysAnNPLNSession(t *testing.T) {
	h := newHarness(t)
	h.createUser("user-1", "device-account-1")

	client := authpb.NewAuthClient(h.conn)
	resp, err := client.IssueToken(h.ctx("", ""), &authpb.IssueTokenRequest{
		ExternalIdToken: &authpb.ExternalIdToken{
			Token: &authpb.ExternalIdToken_NsaIdToken{
				NsaIdToken: h.baasIDToken("user-1", "device-account-1"),
			},
		},
	})
	if err != nil {
		t.Fatalf("IssueToken: %v", err)
	}

	access := resp.GetToken().GetAccessToken()
	if access == "" {
		t.Fatal("no access token in the response")
	}
	if got := resp.GetToken().GetTtl().AsDuration(); got != 3*time.Hour {
		t.Errorf("ttl = %v, want 3h", got)
	}

	// The token must be ES256 and must verify against the gateway's own key --
	// asserted on the wire, by decoding the header, rather than by trusting the
	// issuer that made it.
	header := decodeJWTHeader(t, access)
	if header["alg"] != "ES256" {
		t.Errorf("alg = %v, want ES256 (NPLN is the only ES256 service in the stack)", header["alg"])
	}
	if header["kid"] != h.accessKey.ID {
		t.Errorf("kid = %v, want the gateway's key id %s", header["kid"], h.accessKey.ID)
	}

	var claims nplnClaims
	if err := sjwt.NewECVerifier(h.accessKey).Verify(access, &claims); err != nil {
		t.Fatalf("the issued token does not verify against the gateway's key: %v", err)
	}
	if claims.Subject != "user-1" {
		t.Errorf("sub = %q, want user-1", claims.Subject)
	}
	if claims.DeviceAccountID != "device-account-1" {
		t.Errorf("npln:did = %q, want device-account-1", claims.DeviceAccountID)
	}
}

// TestAccessTokenCarriesTheNPLNRightsClaim asserts on the wire, not on the Go
// struct: a real access token's payload carries an 'npln' object the client
// reads to know what it may do (NextendoNetwork/splatoon-3's decrypted
// capture, facts only -- see nplnRights' doc comment), and this server sent
// none at all until this test existed. 'deny' must decode as an array, not
// null: a nil Go slice and the capture's "[]" are not the same byte on the
// wire, and a client that parses the field strictly would see one and not
// the other.
func TestAccessTokenCarriesTheNPLNRightsClaim(t *testing.T) {
	h := newHarness(t)
	h.createUser("user-1", "device-account-1")

	access := h.sessionFor(t, "user-1", "device-account-1")
	parts := strings.Split(access, ".")
	if len(parts) != 3 {
		t.Fatalf("expected three token parts, got %d", len(parts))
	}
	payload := decodeJWTSegment(t, parts[1])

	npln, ok := payload["npln"].(map[string]any)
	if !ok {
		t.Fatalf("payload carries no 'npln' object: %v", payload)
	}
	if npln["app_id"] != h.srv.appID {
		t.Errorf("app_id = %v, want %q", npln["app_id"], h.srv.appID)
	}
	if npln["tid"] != testTenant {
		t.Errorf("tid = %v, want %q", npln["tid"], testTenant)
	}
	auth, ok := npln["authorization"].(map[string]any)
	if !ok {
		t.Fatalf("npln carries no 'authorization' object: %v", npln)
	}
	allow, ok := auth["allow"].([]any)
	if !ok || len(allow) != 1 || allow[0] != "**" {
		t.Errorf("authorization.allow = %v, want [\"**\"]", auth["allow"])
	}
	if _, ok := auth["deny"].([]any); !ok {
		t.Errorf("authorization.deny = %v (%T), want a JSON array, not null", auth["deny"], auth["deny"])
	}
}

// TestRefreshTokenBuysANewAccessTokenWithoutGoingBackToBAAS proves the round
// trip IssueToken's own refresh token is for: RefreshToken must accept it and
// hand back a working access token, and must refuse an access token offered
// in its place even though both verify against the same key.
func TestRefreshTokenBuysANewAccessTokenWithoutGoingBackToBAAS(t *testing.T) {
	h := newHarness(t)
	h.createUser("user-1", "device-account-1")

	client := authpb.NewAuthClient(h.conn)
	first, err := client.IssueToken(h.ctx("", ""), &authpb.IssueTokenRequest{
		ExternalIdToken: &authpb.ExternalIdToken{
			Token: &authpb.ExternalIdToken_NsaIdToken{
				NsaIdToken: h.baasIDToken("user-1", "device-account-1"),
			},
		},
	})
	if err != nil {
		t.Fatalf("IssueToken: %v", err)
	}
	if first.GetToken().GetRefreshToken() == "" {
		t.Fatal("no refresh token in the response")
	}

	refreshed, err := client.RefreshToken(h.ctx("", ""), &authpb.RefreshTokenRequest{
		RefreshToken: first.GetToken().GetRefreshToken(),
	})
	if err != nil {
		t.Fatalf("RefreshToken: %v", err)
	}
	if refreshed.GetToken().GetAccessToken() == "" {
		t.Fatal("no access token in the refreshed response")
	}

	var claims nplnClaims
	if err := sjwt.NewECVerifier(h.accessKey).Verify(refreshed.GetToken().GetAccessToken(), &claims); err != nil {
		t.Fatalf("the refreshed access token does not verify: %v", err)
	}
	if claims.Subject != "user-1" {
		t.Errorf("sub = %q, want user-1", claims.Subject)
	}

	_, err = client.RefreshToken(h.ctx("", ""), &authpb.RefreshTokenRequest{
		RefreshToken: first.GetToken().GetAccessToken(),
	})
	if status.Code(err) != codes.Unauthenticated {
		t.Errorf("RefreshToken with an access token: code = %v, want Unauthenticated", status.Code(err))
	}
}

// TestIssuePrearrangedUserTokenBuysAnNPLNSession is the same seam as
// TestABAASIDTokenBuysAnNPLNSession, but for the RPC Splatoon 3 actually
// calls first: confirmed on real hardware (2026-09-23 capture) as the first
// gRPC call the console makes once its TLS handshake to the game server
// succeeds. It differs from IssueToken by also returning a User message, and
// by taking "tenant"/"user_index" fields that this handler intentionally
// ignores for identity -- see the comment on the handler itself.
func TestIssuePrearrangedUserTokenBuysAnNPLNSession(t *testing.T) {
	h := newHarness(t)
	h.createUser("user-1", "device-account-1")

	client := authpb.NewAuthClient(h.conn)
	resp, err := client.IssuePrearrangedUserToken(h.ctx("", ""), &authpb.IssuePrearrangedUserTokenRequest{
		Tenant:    "t-dce9377b-lp1",
		UserIndex: 0,
		ExternalIdToken: &authpb.ExternalIdToken{
			Token: &authpb.ExternalIdToken_NsaIdToken{
				NsaIdToken: h.baasIDToken("user-1", "device-account-1"),
			},
		},
	})
	if err != nil {
		t.Fatalf("IssuePrearrangedUserToken: %v", err)
	}

	if got, want := resp.GetUser().GetName(), h.srv.userResourceName("user-1"); got != want {
		t.Errorf("user.name = %q, want %q", got, want)
	}

	access := resp.GetToken().GetAccessToken()
	if access == "" {
		t.Fatal("no access token in the response")
	}
	if got, want := resp.GetToken().GetUser(), h.srv.userResourceName("user-1"); got != want {
		t.Errorf("token.user = %q, want %q", got, want)
	}

	var claims nplnClaims
	if err := sjwt.NewECVerifier(h.accessKey).Verify(access, &claims); err != nil {
		t.Fatalf("the issued token does not verify against the gateway's key: %v", err)
	}
	if claims.Subject != "user-1" {
		t.Errorf("sub = %q, want user-1", claims.Subject)
	}
}

// TestABAASAccessTokenIsRefusedAtTheGameServerSeam is why BAAS signs id tokens
// with a different key from access tokens.
//
// An id token is handed to a game server; an access token acts on the player's
// own account. If the game server accepted an access token, a copy of one
// reaching it would be usable against the account it belongs to.
func TestABAASAccessTokenIsRefusedAtTheGameServerSeam(t *testing.T) {
	h := newHarness(t)
	h.createUser("user-1", "device-account-1")

	accessToken, err := h.baasAccessIssuer.Sign(sjwt.BAASTokenClaims{
		Subject:         "user-1",
		DeviceAccountID: "device-account-1",
		Issuer:          testBAASIssuer,
		Type:            "token",
		GrantType:       2,
		IssuedAt:        time.Now().Unix(),
		Expires:         time.Now().Add(3 * time.Hour).Unix(),
		TokenID:         sjwt.NewTokenID(),
	})
	if err != nil {
		t.Fatalf("sign: %v", err)
	}

	client := authpb.NewAuthClient(h.conn)
	_, err = client.IssueToken(h.ctx("", ""), &authpb.IssueTokenRequest{
		ExternalIdToken: &authpb.ExternalIdToken{
			Token: &authpb.ExternalIdToken_NsaIdToken{NsaIdToken: accessToken},
		},
	})
	if status.Code(err) != codes.Unauthenticated {
		t.Fatalf("a BAAS access token was accepted as an id token: %v", err)
	}
}

// TestAUIDThatDisagreesWithTheTokenIsRefused is the most important test here.
//
// 'uid' is the client saying who it is. The token is what proves it. A server
// that resolved the caller from 'uid' -- or that quietly preferred it when the
// two disagreed -- would be the bug this whole project is shaped around, in a
// new place: every console acting as whoever it named.
func TestAUIDThatDisagreesWithTheTokenIsRefused(t *testing.T) {
	h := newHarness(t)
	h.createUser("user-1", "device-account-1")
	h.createUser("user-2", "device-account-2")

	access := h.sessionFor(t, "user-1", "device-account-1")

	client := authpb.NewAuthClient(h.conn)
	_, err := client.ValidateToken(h.ctx(access, "user-2"), &emptypb.Empty{})
	if status.Code(err) != codes.PermissionDenied {
		t.Fatalf("a call claiming another user's uid was not refused: %v", err)
	}

	// The same token with its own uid, or with none, is fine -- so the refusal
	// above is about the mismatch, not about uid being present at all.
	for _, uid := range []string{"user-1", ""} {
		if _, err := client.ValidateToken(h.ctx(access, uid), &emptypb.Empty{}); err != nil {
			t.Errorf("ValidateToken with uid %q: %v", uid, err)
		}
	}
}

func TestAnAuthenticatedRPCNeedsAToken(t *testing.T) {
	h := newHarness(t)
	h.createUser("user-1", "device-account-1")

	client := authpb.NewAuthClient(h.conn)
	_, err := client.ValidateToken(h.ctx("", ""), &emptypb.Empty{})
	if status.Code(err) != codes.Unauthenticated {
		t.Fatalf("an unauthenticated call reached an authenticated RPC: %v", err)
	}
}

// TestATokenStopsWorkingWhenItsUserIsDeleted mirrors BAAS's equivalent.
//
// A token stays cryptographically valid until it expires, so a user deleted
// after the token was signed would keep a working session for hours unless the
// user is re-read on every request.
func TestATokenStopsWorkingWhenItsUserIsDeleted(t *testing.T) {
	h := newHarness(t)
	h.createUser("user-1", "device-account-1")
	access := h.sessionFor(t, "user-1", "device-account-1")

	client := authpb.NewAuthClient(h.conn)
	if _, err := client.ValidateToken(h.ctx(access, ""), &emptypb.Empty{}); err != nil {
		t.Fatalf("the session did not work before deletion: %v", err)
	}

	if err := h.store.DeleteDeviceAccount(context.Background(), "user-1", "device-account-1"); err != nil {
		t.Fatalf("delete device account: %v", err)
	}

	if _, err := client.ValidateToken(h.ctx(access, ""), &emptypb.Empty{}); status.Code(err) != codes.Unauthenticated {
		t.Fatalf("a token whose device account was deleted still worked: %v", err)
	}
}

// TestATokenStopsWorkingWhenItsDeviceAccountMovesToAnotherUser is the check
// that has to fail closed: the credential is still real, but it no longer
// belongs to the user the token names.
func TestATokenStopsWorkingWhenItsDeviceAccountMovesToAnotherUser(t *testing.T) {
	h := newHarness(t)
	h.createUser("user-1", "device-account-1")
	h.createUser("user-2", "device-account-2")
	access := h.sessionFor(t, "user-1", "device-account-1")

	if err := h.store.ReassignDeviceAccount(context.Background(), "device-account-1", "user-2"); err != nil {
		t.Fatalf("reassign: %v", err)
	}

	client := authpb.NewAuthClient(h.conn)
	if _, err := client.ValidateToken(h.ctx(access, ""), &emptypb.Empty{}); status.Code(err) != codes.Unauthenticated {
		t.Fatalf("a token whose device account now belongs to somebody else still worked: %v", err)
	}
}

// TestATokenStopsWorkingAfterItsDeviceAccountsTokensAreRevoked mirrors
// BAAS's equivalent: switchnetctl account revoke-tokens must end an
// already-active NPLN session immediately, not merely stop a new one being
// minted.
func TestATokenStopsWorkingAfterItsDeviceAccountsTokensAreRevoked(t *testing.T) {
	h := newHarness(t)
	h.createUser("user-1", "device-account-1")
	access := h.sessionFor(t, "user-1", "device-account-1")

	client := authpb.NewAuthClient(h.conn)
	if _, err := client.ValidateToken(h.ctx(access, ""), &emptypb.Empty{}); err != nil {
		t.Fatalf("the session did not work before revocation: %v", err)
	}

	// A cutoff before the session was minted must not affect it.
	if err := h.store.RevokeDeviceAccountTokens(context.Background(), "device-account-1",
		h.now.Add(-time.Hour)); err != nil {
		t.Fatalf("revoke (past cutoff): %v", err)
	}
	if _, err := client.ValidateToken(h.ctx(access, ""), &emptypb.Empty{}); err != nil {
		t.Fatalf("a revocation with a cutoff before the session was minted affected it: %v", err)
	}

	// A cutoff after the session was minted ends it immediately, on the very
	// next call -- not merely when it would next need to be re-minted.
	if err := h.store.RevokeDeviceAccountTokens(context.Background(), "device-account-1",
		h.now.Add(time.Hour)); err != nil {
		t.Fatalf("revoke (future cutoff): %v", err)
	}
	if _, err := client.ValidateToken(h.ctx(access, ""), &emptypb.Empty{}); status.Code(err) != codes.Unauthenticated {
		t.Fatalf("a token minted before its device account's tokens were revoked still worked: %v", err)
	}
}

func TestAnExpiredSessionIsRefused(t *testing.T) {
	h := newHarness(t)
	h.createUser("user-1", "device-account-1")

	// The session is issued as genuinely stale rather than by winding the
	// harness clock forward afterwards. The server's clock decides a token's
	// 'exp' when it signs one, but the verifier enforces expiry against real
	// time -- so moving h.now after the fact changes nothing. Same approach as
	// internal/jwt's TestVerifierRejectsExpiredToken.
	h.now = time.Now().Add(-4 * time.Hour)
	access := h.sessionFor(t, "user-1", "device-account-1")
	h.now = time.Now()

	client := authpb.NewAuthClient(h.conn)
	if _, err := client.ValidateToken(h.ctx(access, ""), &emptypb.Empty{}); status.Code(err) != codes.Unauthenticated {
		t.Fatalf("an expired session was accepted: %v", err)
	}
}

// ---- metadata -------------------------------------------------------------

// TestMalformedMetadataIsRefusedTheWayNintendoRefusesIt pins a deliberately
// unhelpful status. Nintendo answers metadata it cannot parse with a code that
// says nothing about what was wrong, and the game reacts to the code.
func TestMalformedMetadataIsRefusedTheWayNintendoRefusesIt(t *testing.T) {
	h := newHarness(t)
	client := authpb.NewAuthClient(h.conn)

	cases := []struct {
		name string
		ctx  context.Context
	}{
		{"no metadata at all", context.Background()},
		{"no tenant", metadata.NewOutgoingContext(context.Background(),
			metadata.Pairs(mdAuthorization, "Bearer x"))},
		{"an authorization header that is not Bearer", metadata.NewOutgoingContext(context.Background(),
			metadata.Pairs(mdTenant, testTenant, mdAuthorization, "Basic abcdef"))},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := client.ValidateToken(tc.ctx, &emptypb.Empty{})
			if status.Code(err) != codes.Unimplemented {
				t.Errorf("code = %v, want Unimplemented (12) on production", status.Code(err))
			}
		})
	}
}

func TestAnotherTenantsTrafficIsNotAnswered(t *testing.T) {
	h := newHarness(t)
	client := authpb.NewAuthClient(h.conn)

	ctx := metadata.NewOutgoingContext(context.Background(),
		metadata.Pairs(mdTenant, "0badcafe"))
	_, err := client.ValidateToken(ctx, &emptypb.Empty{})
	if status.Code(err) != codes.Unimplemented {
		t.Fatalf("another tenant's call was answered: %v", err)
	}
}

// TestABearerSchemeIsCaseInsensitive: RFC 7235 says the scheme is
// case-insensitive, so a client sending "bearer" is not malformed.
func TestABearerSchemeIsCaseInsensitive(t *testing.T) {
	h := newHarness(t)
	h.createUser("user-1", "device-account-1")
	access := h.sessionFor(t, "user-1", "device-account-1")

	ctx := metadata.NewOutgoingContext(context.Background(),
		metadata.Pairs(mdTenant, testTenant, mdAuthorization, "bearer "+access))

	client := authpb.NewAuthClient(h.conn)
	if _, err := client.ValidateToken(ctx, &emptypb.Empty{}); err != nil {
		t.Fatalf("a lower-case bearer scheme was refused: %v", err)
	}
}

// ---- maintenance ----------------------------------------------------------

// TestTheMaintenanceStreamKeepsTalkingRatherThanClosing.
//
// "No maintenance" cannot be expressed by returning: the RPC is a stream, and
// returning closes it, which the client reads as the connection failing. The
// protocol has a KeepAlive message in its own oneof precisely because silence
// is not a valid state.
func TestTheMaintenanceStreamKeepsTalkingRatherThanClosing(t *testing.T) {
	h := newHarness(t)

	ctx, cancel := context.WithCancel(h.ctx("", ""))
	defer cancel()

	client := maintpb.NewMaintenanceScheduleServiceClient(h.conn)
	stream, err := client.SubscribeMaintenanceSchedules(ctx,
		&maintpb.SubscribeMaintenanceSchedulesRequest{Tenant: testTenant})
	if err != nil {
		t.Fatalf("subscribe: %v", err)
	}

	msg, err := stream.Recv()
	if err != nil {
		t.Fatalf("recv: %v", err)
	}
	if msg.GetKeepAlive() == nil {
		t.Errorf("first message is not a keep-alive: %v", msg)
	}
	// And no schedule: SwitchNet has no maintenance windows.
	if msg.GetMessage() != nil {
		t.Errorf("a maintenance schedule was announced: %v", msg.GetMessage())
	}
}

// ---- helpers --------------------------------------------------------------

// sessionFor exchanges an id token for an access token, the way the game does.
func (h *harness) sessionFor(t *testing.T, userID, deviceAccountID string) string {
	t.Helper()
	resp, err := authpb.NewAuthClient(h.conn).IssueToken(h.ctx("", ""),
		&authpb.IssueTokenRequest{
			ExternalIdToken: &authpb.ExternalIdToken{
				Token: &authpb.ExternalIdToken_NsaIdToken{
					NsaIdToken: h.baasIDToken(userID, deviceAccountID),
				},
			},
		})
	if err != nil {
		t.Fatalf("IssueToken: %v", err)
	}
	return resp.GetToken().GetAccessToken()
}

func decodeJWTHeader(t *testing.T, token string) map[string]any {
	t.Helper()
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		t.Fatalf("expected three token parts, got %d", len(parts))
	}
	return decodeJWTSegment(t, parts[0])
}

// TestADeletedUsersGameSessionStopsAtOnce: switchnetctl account delete-user
// only sets User.Deleted. BAAS refused such a user on every request; NPLN
// only checked that the row existed, so a removed player kept playing until
// their token expired.
func TestADeletedUsersGameSessionStopsAtOnce(t *testing.T) {
	h := newHarness(t)
	h.createUser("deleted000000001", "device-deleted-1")
	access := h.sessionFor(t, "deleted000000001", "device-deleted-1")
	client := authpb.NewAuthClient(h.conn)
	if _, err := client.ValidateToken(h.ctx(access, ""), &emptypb.Empty{}); err != nil {
		t.Fatalf("ValidateToken before deletion: %v", err)
	}

	deleted := true
	if _, err := h.store.UpdateUser(context.Background(), "deleted000000001", db.UserPatch{Deleted: &deleted}); err != nil {
		t.Fatalf("mark deleted: %v", err)
	}
	if _, err := client.ValidateToken(h.ctx(access, ""), &emptypb.Empty{}); status.Code(err) != codes.Unauthenticated {
		t.Errorf("ValidateToken after deletion: %v, want Unauthenticated", err)
	}
}
