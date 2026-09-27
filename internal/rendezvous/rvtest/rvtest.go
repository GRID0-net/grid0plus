// Package rvtest stands up a rendezvous game server over a real socket and
// logs consoles into it the way a Switch does -- id token, LoginEx, ticket,
// secure CONNECT -- so tests of anything above the transport exercise the
// same path a console takes.
package rvtest

import (
	"context"
	"encoding/hex"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/n-popescu/grid0plus/internal/db"
	"github.com/n-popescu/grid0plus/internal/identity"
	sjwt "github.com/n-popescu/grid0plus/internal/jwt"
	"github.com/n-popescu/grid0plus/internal/natobs"
	"github.com/n-popescu/grid0plus/internal/nex"
	"github.com/n-popescu/grid0plus/internal/nex/nextest"
	"github.com/n-popescu/grid0plus/internal/rendezvous"
)

// Env is one running server.
type Env struct {
	T      testing.TB
	Store  *db.Memory
	Server *rendezvous.Server
	URL    string
	// HTTPURL is the same server as plain HTTP.
	HTTPURL string
	// NATObs is what the NAT-check responder has "observed".
	NATObs *natobs.Memory
	Title  rendezvous.Title
	issuer *sjwt.Issuer
	n      int
}

// DefaultTitle is a title for tests that do not care which.
var DefaultTitle = rendezvous.Title{
	Name: "test", AccessKey: "0badc0de", GameServerID: 0x12345678, ServerName: "SwitchNet",
	AuthMinorVersion: 3, SecureMinorVersion: 0, PublicStationType: 3,
}

// Deps is what a server under test is built from.
type Deps struct {
	Store    *db.Memory
	IDTokens *identity.IDTokens
	Log      *slog.Logger
	// NATObs is an in-memory observation store the test can fill.
	NATObs *natobs.Memory
}

// New starts a plain rendezvous server. configure runs before it serves, to
// register a title's protocols.
func New(t testing.TB, title rendezvous.Title, configure func(*rendezvous.Server, *db.Memory)) *Env {
	t.Helper()
	return Start(t, title, func(d Deps) (*rendezvous.Server, http.Handler) {
		srv := rendezvous.New(rendezvous.Options{Title: title, IDTokens: d.IDTokens, Store: d.Store, Log: d.Log,
			NATObservations: d.NATObs})
		if configure != nil {
			configure(srv, d.Store)
		}
		return srv, srv
	})
}

// Start starts whatever build returns -- a title's own server, which
// wraps a rendezvous.Server and may serve more than its WebSocket.
func Start(t testing.TB, title rendezvous.Title, build func(Deps) (*rendezvous.Server, http.Handler)) *Env {
	t.Helper()
	ring := sjwt.NewKeyring(filepath.Join(t.TempDir(), "keys"), true)
	key, err := ring.Key(sjwt.IssuerBAASID)
	if err != nil {
		t.Fatal(err)
	}
	store := db.NewMemory()
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	if testing.Verbose() {
		log = slog.New(slog.NewTextHandler(testWriter{t}, &slog.HandlerOptions{Level: slog.LevelDebug}))
	}
	obs := natobs.NewMemory()
	srv, handler := build(Deps{Store: store, IDTokens: identity.NewIDTokens(key, store, log), Log: log, NATObs: obs})
	ts := httptest.NewServer(handler)
	t.Cleanup(ts.Close)
	return &Env{T: t, Store: store, Server: srv, URL: "ws" + strings.TrimPrefix(ts.URL, "http"),
		HTTPURL: ts.URL, Title: title, NATObs: obs, issuer: sjwt.NewIssuer(key, "", true)}
}

type testWriter struct{ t testing.TB }

func (w testWriter) Write(b []byte) (int, error) {
	w.t.Log(strings.TrimSpace(string(b)))
	return len(b), nil
}

// User creates a user with a console and returns its id and device account.
func (e *Env) User() (userID, deviceAccountID string) {
	e.T.Helper()
	e.n++
	userID = fmt.Sprintf("%016x", uint64(0x1000_0000_0000_0000)+uint64(e.n))
	deviceAccountID = fmt.Sprintf("%016x", uint64(0xda00_0000_0000_0000)+uint64(e.n))
	if err := e.Store.CreateUser(context.Background(), db.User{ID: userID, Nickname: "player" + fmt.Sprint(e.n)},
		db.DeviceAccount{ID: deviceAccountID, UserID: userID, PasswordSHA256: "x"}); err != nil {
		e.T.Fatal(err)
	}
	return userID, deviceAccountID
}

// IDToken mints the BAAS id token a console would hold.
func (e *Env) IDToken(userID, deviceAccountID string) string {
	e.T.Helper()
	now := time.Now()
	tok, err := e.issuer.Sign(sjwt.BAASTokenClaims{Subject: userID, DeviceAccountID: deviceAccountID,
		Type: "id_token", IssuedAt: now.Unix(), Expires: now.Add(time.Hour).Unix(), TokenID: sjwt.NewTokenID()})
	if err != nil {
		e.T.Fatal(err)
	}
	return tok
}

// LoginExBody is a LoginEx request: username and AnyDataHolder<AuthenticationInfo>.
func LoginExBody(username, token string) []byte {
	w := nex.NewWriter()
	w.String(username)
	w.AnyData("AuthenticationInfo", func(w *nex.Writer) {
		w.Struct(0, nil) // nn::nex::Data
		w.Struct(0, func(w *nex.Writer) {
			w.String(token)
			w.U32(4) // ngsVersion, Switch
			w.U8(2)  // token type, Switch
			w.U32(0)
		})
	})
	return w.Bytes()
}

// LoginResult is a parsed LoginEx response.
type LoginResult struct {
	Result    uint32
	PID       uint64
	Ticket    []byte
	Station   *nex.StationURL
	SourceKey []byte
}

// ParseLoginEx parses a LoginEx response body.
func ParseLoginEx(body []byte) (LoginResult, error) {
	r := nex.NewReader(body)
	var out LoginResult
	out.Result = r.Result()
	out.PID = r.PID()
	out.Ticket = r.Buffer()
	r.Struct(func(s *nex.Reader, _ uint8) {
		out.Station = s.StationURL()
		_ = nex.ReadList(s, (*nex.Reader).U8)
		_ = s.StationURL()
		_ = s.DateTime()
	})
	_ = r.String()
	key, err := hex.DecodeString(r.String())
	if err != nil {
		return out, err
	}
	out.SourceKey = key
	if r.Err() == nil && r.Remaining() != 0 {
		return out, fmt.Errorf("LoginEx response has %d trailing bytes", r.Remaining())
	}
	return out, r.Err()
}

// Player is a logged-in console.
type Player struct {
	UserID string
	PID    uint64
	Secure *nextest.Client
}

// Login logs a new user in and connects it to the secure server.
func (e *Env) Login() *Player {
	e.T.Helper()
	userID, da := e.User()
	return e.LoginAs(userID, da)
}

// LoginAs logs an existing user in.
func (e *Env) LoginAs(userID, deviceAccountID string) *Player {
	e.T.Helper()
	return e.login(userID, deviceAccountID, "")
}

// LoginFrom logs a new user in as though its connection came from ip, the
// way nginx reports a console's address (X-Real-IP).
func (e *Env) LoginFrom(ip string) *Player {
	e.T.Helper()
	userID, da := e.User()
	return e.login(userID, da, ip)
}

func (e *Env) login(userID, deviceAccountID, ip string) *Player {
	e.T.Helper()
	pid, err := rendezvous.PIDForUserID(userID)
	if err != nil {
		e.T.Fatal(err)
	}
	auth, err := nextest.Dial(e.URL, nextest.Options{AccessKey: e.Title.AccessKey, Port: rendezvous.AuthPort})
	if err != nil {
		e.T.Fatal(err)
	}
	defer auth.Close()
	resp, err := auth.Call(rendezvous.ProtocolTicketGranting, 2,
		LoginExBody(fmt.Sprint(pid), e.IDToken(userID, deviceAccountID)))
	if err != nil {
		e.T.Fatal(err)
	}
	res, err := ParseLoginEx(resp.Body)
	if err != nil || res.Result != nex.ResultSuccess {
		e.T.Fatalf("login failed: result %#x err %v", res.Result, err)
	}
	ticket, err := nex.OpenClientTicket(res.SourceKey, res.Ticket)
	if err != nil {
		e.T.Fatal(err)
	}
	var hdr http.Header
	if ip != "" {
		hdr = http.Header{"X-Real-Ip": []string{ip}}
	}
	secure, err := nextest.Dial(e.URL, nextest.Options{AccessKey: e.Title.AccessKey, Port: rendezvous.SecurePort,
		ConnectPayload: nex.ConnectRequest(ticket, pid, 1, 7), Header: hdr})
	if err != nil {
		e.T.Fatal(err)
	}
	e.T.Cleanup(func() { _ = secure.Close() })
	// Wait until the server has registered the connection, so a test that
	// looks it up next does not race the CONNECT's hooks.
	deadline := time.Now().Add(2 * time.Second)
	for e.Server.Player(pid) == nil && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	return &Player{UserID: userID, PID: pid, Secure: secure}
}

// Call makes a call and fails the test on a transport error.
func (p *Player) Call(t testing.TB, protocol uint16, method uint32, body []byte) *nex.Response {
	t.Helper()
	resp, err := p.Secure.Call(protocol, method, body)
	if err != nil {
		t.Fatal(err)
	}
	return resp
}

// MustCall makes a call that must succeed and returns a reader over its body.
func (p *Player) MustCall(t testing.TB, protocol uint16, method uint32, body []byte) *nex.Reader {
	t.Helper()
	resp := p.Call(t, protocol, method, body)
	if !resp.OK() {
		t.Fatalf("call %#x.%d failed: %#x", protocol, method, resp.Result)
	}
	return nex.NewReader(resp.Body)
}

// Register registers stations and returns the public one the server derived.
func (p *Player) Register(t testing.TB, stations ...string) *nex.StationURL {
	t.Helper()
	w := nex.NewWriter()
	nex.WriteList(w, stations, func(w *nex.Writer, s string) { w.String(s) })
	r := p.MustCall(t, rendezvous.ProtocolSecureConnection, 1, w.Bytes())
	if res := r.Result(); res != nex.ResultSuccess {
		t.Fatalf("Register result %#x", res)
	}
	_ = r.U32()
	return r.StationURL()
}
