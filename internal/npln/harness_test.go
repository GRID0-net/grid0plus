package npln

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"log/slog"
	"net"
	"path/filepath"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/test/bufconn"

	"github.com/n-popescu/switchnet/config"
	"github.com/n-popescu/switchnet/internal/db"
	sjwt "github.com/n-popescu/switchnet/internal/jwt"
	"github.com/n-popescu/switchnet/internal/presencebus"
)

const (
	testTenant     = "dce9377b"
	testIssuerName = "https://t-dce9377b-lp1.lp1.t.npln.srv.nintendo.net"
	testJWKSURL    = testIssuerName + "/1.0.0/certificates"
	testBAASIssuer = "https://e0d67c509fb203858ebcb2fe3f88c2aa.baas.nintendo.com"

	testPresenceTTL        = 5 * time.Minute
	testConsolePresenceTTL = 5 * time.Minute
)

// harness is an NPLN gateway over an in-process listener, with an in-memory
// store and a clock the test controls.
//
// bufconn rather than a real port: these tests drive the server through a real
// gRPC client over a real connection — metadata, status codes and streaming all
// behave exactly as they would on the wire — without binding anything.
type harness struct {
	t *testing.T

	srv   *Server
	store *db.Memory
	conn  *grpc.ClientConn
	bus   *presencebus.Local

	// baasIDIssuer mints the id tokens a real BAAS would hand the game.
	baasIDIssuer *sjwt.Issuer
	// baasAccessIssuer mints BAAS ACCESS tokens, so a test can prove one is
	// refused at the game-server seam.
	baasAccessIssuer *sjwt.Issuer
	// accessKey is the gateway's own ES256 key.
	accessKey sjwt.ECKey

	now time.Time
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	return newHarnessWith(t, nil)
}

// newHarnessWith builds a harness whose NPLN configuration a test has adjusted.
//
// Separate from newHarness so that the settings a test cares about are visible
// in the test rather than buried in a shared default -- and so the default stays
// the one every other test reads.
func newHarnessWith(t *testing.T, adjust func(*config.NPLN)) *harness {
	t.Helper()

	dir := filepath.Join(t.TempDir(), "keys")
	ring := sjwt.NewKeyring(dir, true)

	accessKey, err := ring.ECKey(sjwt.IssuerNPLN)
	if err != nil {
		t.Fatalf("npln key: %v", err)
	}
	baasIDKey, err := ring.Key(sjwt.IssuerBAASID)
	if err != nil {
		t.Fatalf("baas id key: %v", err)
	}
	baasAccessKey, err := ring.Key(sjwt.IssuerBAASAccess)
	if err != nil {
		t.Fatalf("baas access key: %v", err)
	}

	h := &harness{
		t:                t,
		store:            db.NewMemory(),
		bus:              presencebus.NewLocal(),
		baasIDIssuer:     sjwt.NewIssuer(baasIDKey, testBAASIssuer+"/1.0.0/certificates", true),
		baasAccessIssuer: sjwt.NewIssuer(baasAccessKey, testBAASIssuer+"/1.0.0/internal_certificates", true),
		accessKey:        accessKey,
		// Anchored to real time, not a fixed date. The server's clock decides
		// what 'exp' a token gets, but the verifier enforces expiry against
		// real time -- so a harness pinned to a fixed instant issues tokens
		// that are valid only while the wall clock happens to be within their
		// TTL of it, and the suite starts failing at a certain time of day.
		// A test that fails on the clock rather than on the code is worse than
		// no test. The one case that needs a different instant sets it itself.
		now: time.Now(),
	}

	cfg := config.NPLN{
		Tenant:          testTenant,
		Issuer:          testIssuerName,
		JWKSURL:         testJWKSURL,
		AccessTokenTTL:  10800,
		PlayersPerMatch: 2,
		TicketTTL:       180,
	}
	if adjust != nil {
		adjust(&cfg)
	}

	srv, err := New(Options{
		Store: h.store,
		// Discard: these tests deliberately provoke refusals, and the log lines
		// are noise until one fails.
		Log:                slog.New(slog.NewTextHandler(io.Discard, nil)),
		Config:             cfg,
		AccessKey:          accessKey,
		BAASIDKey:          baasIDKey,
		PresenceTTL:        testPresenceTTL,
		ConsolePresenceTTL: testConsolePresenceTTL,
		Bus:                h.bus,
		Now:                func() time.Time { return h.now },
	})
	if err != nil {
		t.Fatalf("new server: %v", err)
	}
	h.srv = srv

	lis := bufconn.Listen(1 << 20)
	go func() { _ = srv.Serve(lis) }()

	conn, err := grpc.NewClient("passthrough:///bufnet",
		grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) {
			return lis.DialContext(ctx)
		}),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	h.conn = conn

	t.Cleanup(func() {
		_ = conn.Close()
		srv.Stop()
		_ = lis.Close()
		_ = h.bus.Close()
	})
	return h
}

// createUser inserts a user with one device account and returns both ids.
func (h *harness) createUser(id, deviceAccountID string) {
	h.t.Helper()
	err := h.store.CreateUser(context.Background(),
		db.User{ID: id, Nickname: "tester"},
		db.DeviceAccount{
			ID: deviceAccountID, UserID: id,
			PasswordSHA256: "0000000000000000000000000000000000000000000000000000000000000000",
			CreatedAt:      h.now, UpdatedAt: h.now,
		})
	if err != nil {
		h.t.Fatalf("create user: %v", err)
	}
}

// baasIDToken mints the id token BAAS would give the game for a user.
//
// Dated from real time, not from h.now. h.now is the SERVER's clock -- what it
// stamps on the tokens it issues -- whereas this models a token BAAS handed the
// console a moment ago. Keeping them separate is what lets a test wind the
// server's clock back to produce an already-expired session without also
// invalidating the credential being exchanged for it.
func (h *harness) baasIDToken(userID, deviceAccountID string) string {
	h.t.Helper()
	now := time.Now()
	tok, err := h.baasIDIssuer.Sign(sjwt.BAASTokenClaims{
		Subject:         userID,
		DeviceAccountID: deviceAccountID,
		Issuer:          testBAASIssuer,
		Type:            "id_token",
		IssuedAt:        now.Unix(),
		Expires:         now.Add(3 * time.Hour).Unix(),
		TokenID:         sjwt.NewTokenID(),
	})
	if err != nil {
		h.t.Fatalf("sign id token: %v", err)
	}
	return tok
}

// ctx builds a call context with NPLN metadata.
func (h *harness) ctx(bearer, uid string) context.Context {
	pairs := []string{mdTenant, testTenant}
	if bearer != "" {
		pairs = append(pairs, mdAuthorization, "Bearer "+bearer)
	}
	if uid != "" {
		pairs = append(pairs, mdUID, uid)
	}
	return metadata.NewOutgoingContext(context.Background(), metadata.Pairs(pairs...))
}

// decodeJWTSegment decodes one base64url JWT segment as JSON.
//
// Decoded by hand rather than through a JWT library: a header field that is
// present in a Go struct but mis-tagged would round-trip through a verifier and
// still not match a capture. Same rule as internal/jwt's own tests.
func decodeJWTSegment(t *testing.T, segment string) map[string]any {
	t.Helper()
	raw, err := base64.RawURLEncoding.DecodeString(segment)
	if err != nil {
		t.Fatalf("decode token segment: %v", err)
	}
	var out map[string]any
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("parse token segment: %v", err)
	}
	return out
}
