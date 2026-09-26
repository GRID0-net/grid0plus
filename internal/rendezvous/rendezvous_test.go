package rendezvous_test

import (
	"bytes"
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/n-popescu/switchnet/internal/db"
	"github.com/n-popescu/switchnet/internal/nex"
	"github.com/n-popescu/switchnet/internal/nex/nextest"
	"github.com/n-popescu/switchnet/internal/rendezvous"
	"github.com/n-popescu/switchnet/internal/rendezvous/rvtest"
)

func login(t *testing.T, e *rvtest.Env, username, token string) rvtest.LoginResult {
	t.Helper()
	auth, err := nextest.Dial(e.URL, nextest.Options{AccessKey: e.Title.AccessKey, Port: rendezvous.AuthPort})
	if err != nil {
		t.Fatal(err)
	}
	defer auth.Close()
	resp, err := auth.Call(rendezvous.ProtocolTicketGranting, 2, rvtest.LoginExBody(username, token))
	if err != nil {
		t.Fatal(err)
	}
	if !resp.OK() {
		t.Fatalf("LoginEx answered an RMC error %#x; a refusal belongs in its Result field", resp.Result)
	}
	res, err := rvtest.ParseLoginEx(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	return res
}

func TestAConsoleLogsInAsItsOwnUserAndReachesTheSecureServer(t *testing.T) {
	e := rvtest.New(t, rvtest.DefaultTitle, nil)
	p := e.Login()
	if fmt.Sprintf("%016x", p.PID) != p.UserID {
		t.Fatalf("PID %d is not user %s", p.PID, p.UserID)
	}
	c := e.Server.Player(p.PID)
	if c == nil || c.PID != p.PID {
		t.Fatalf("secure connection not registered as %d", p.PID)
	}
	if resp := p.Call(t, rendezvous.ProtocolSecureConnection, 5, nil); !resp.OK() || len(resp.Body) != 0 {
		t.Fatalf("TestConnectivity %+v", resp)
	}
}

func TestTheSecureStationSendsTheConsoleBackToTheSameHost(t *testing.T) {
	e := rvtest.New(t, rvtest.DefaultTitle, nil)
	uid, da := e.User()
	pid, _ := rendezvous.PIDForUserID(uid)
	res := login(t, e, fmt.Sprint(pid), e.IDToken(uid, da))
	st := res.Station
	if st.Scheme != "prudps" {
		t.Fatalf("scheme %q: without prudps the console sends no ticket", st.Scheme)
	}
	if st.Get("address") != "0.0.0.1" || st.Int("port") != 1 || st.Int("sid") != rendezvous.SecurePort ||
		st.Int("PID") != rendezvous.SecurePID || st.Int("stream") != 10 {
		t.Fatalf("station %s", st)
	}
	if len(res.SourceKey) != nex.SessionKeySize {
		t.Fatalf("pSourceKey is %d bytes", len(res.SourceKey))
	}
	ct, err := nex.OpenClientTicket(res.SourceKey, res.Ticket)
	if err != nil || ct.Target != uint64(st.Int("PID")) {
		t.Fatalf("ticket target %d err %v: the console would RequestTicket", ct.Target, err)
	}
}

func TestLoginRefusesAUserThatDoesNotExist(t *testing.T) {
	e := rvtest.New(t, rvtest.DefaultTitle, nil)
	tok := e.IDToken("0123456789abcdef", "")
	res := login(t, e, fmt.Sprint(uint64(0x0123456789abcdef)), tok)
	if res.Result&nex.ErrorMask == 0 || res.PID != 0 || len(res.Ticket) != 0 {
		t.Fatalf("an unknown user got %+v", res)
	}
}

func TestLoginRefusesAUsernameThatIsNotTheTokensUser(t *testing.T) {
	// The username is the console's claim; the token is the proof. Claiming
	// someone else with your own valid token must fail, not log you in as
	// either person.
	e := rvtest.New(t, rvtest.DefaultTitle, nil)
	me, myDA := e.User()
	other, _ := e.User()
	otherPID, _ := rendezvous.PIDForUserID(other)
	res := login(t, e, fmt.Sprint(otherPID), e.IDToken(me, myDA))
	if res.Result != nex.ResultPrincipalIDUnmatched|nex.ErrorMask || res.PID != 0 {
		t.Fatalf("got %+v", res)
	}
}

func TestLoginRefusesADeletedUser(t *testing.T) {
	e := rvtest.New(t, rvtest.DefaultTitle, nil)
	uid, da := e.User()
	yes := true
	if _, err := e.Store.UpdateUser(context.Background(), uid, db.UserPatch{Deleted: &yes}); err != nil {
		t.Fatal(err)
	}
	pid, _ := rendezvous.PIDForUserID(uid)
	if res := login(t, e, fmt.Sprint(pid), e.IDToken(uid, da)); res.Result&nex.ErrorMask == 0 {
		t.Fatal("a deleted user logged in")
	}
}

func TestLoginRefusesAForgedToken(t *testing.T) {
	e := rvtest.New(t, rvtest.DefaultTitle, nil)
	other := rvtest.New(t, rvtest.DefaultTitle, nil) // a different signing key
	uid, da := e.User()
	pid, _ := rendezvous.PIDForUserID(uid)
	if res := login(t, e, fmt.Sprint(pid), other.IDToken(uid, da)); res.Result&nex.ErrorMask == 0 {
		t.Fatal("a token signed by another key was accepted")
	}
}

func TestALoginWithNoTokenIsRefused(t *testing.T) {
	e := rvtest.New(t, rvtest.DefaultTitle, nil)
	uid, _ := e.User()
	pid, _ := rendezvous.PIDForUserID(uid)
	auth, err := nextest.Dial(e.URL, nextest.Options{AccessKey: e.Title.AccessKey, Port: rendezvous.AuthPort})
	if err != nil {
		t.Fatal(err)
	}
	defer auth.Close()
	w := nex.NewWriter()
	w.String(fmt.Sprint(pid))
	resp, err := auth.Call(rendezvous.ProtocolTicketGranting, 1, w.Bytes())
	if err != nil {
		t.Fatal(err)
	}
	if nex.NewReader(resp.Body).Result()&nex.ErrorMask == 0 {
		t.Fatal("a tokenless Login succeeded")
	}
}

func TestTheSecureServerRefusesAConnectWithoutATicket(t *testing.T) {
	e := rvtest.New(t, rvtest.DefaultTitle, nil)
	if _, err := nextest.Dial(e.URL, nextest.Options{AccessKey: e.Title.AccessKey, Port: rendezvous.SecurePort}); err == nil {
		t.Fatal("a ticketless CONNECT was admitted")
	}
}

func TestANewerConnectionReplacesAPlayersOlderOne(t *testing.T) {
	e := rvtest.New(t, rvtest.DefaultTitle, nil)
	first := e.Login()
	old := e.Server.Player(first.PID)
	second := e.LoginAs(first.UserID, "")
	_ = second
	select {
	case <-old.Done():
	case <-time.After(2 * time.Second):
		t.Fatal("the older connection was kept")
	}
	if c := e.Server.Player(first.PID); c == nil || c == old {
		t.Fatal("the newer connection is not the player's")
	}
	select {
	case <-first.Secure.Closed():
	case <-time.After(2 * time.Second):
		t.Fatal("the older console was not told")
	}
}

func TestRegisterAddsAPublicStationFromTheConnectionsAddress(t *testing.T) {
	e := rvtest.New(t, rvtest.DefaultTitle, nil)
	p := e.Login()
	public := p.Register(t, "prudp:/address=192.168.1.20;port=49152;natf=0;natm=0;pmp=0;sid=15;type=0;upnp=0;Pl=2")
	c := e.Server.Player(p.PID)

	if public.Get("address") != c.RemoteIP || public.Int("port") != 49152 {
		t.Fatalf("public station %s", public)
	}
	if public.Int("type") != 3 || public.Has("Pa") {
		t.Fatalf("public station type/Pa %s: Splatoon 2's proven server sends type=3 and no Pa", public)
	}
	if public.Get("Pl") != "2" || public.Get("sid") != "15" {
		t.Fatalf("the console's own parameters were dropped: %s", public)
	}
	if public.Uint("PID") != p.PID || uint32(public.Int("RVCID")) != c.ID {
		t.Fatalf("PID/RVCID %s", public)
	}
	st := rendezvous.Stations(c)
	if len(st) != 2 || st[0].Get("address") != "192.168.1.20" {
		t.Fatalf("stored %v", st)
	}
}

func TestReplaceURLKeepsTheConsolesUpdatedStation(t *testing.T) {
	e := rvtest.New(t, rvtest.DefaultTitle, nil)
	p := e.Login()
	p.Register(t, "prudp:/address=192.168.1.20;port=49152;type=0")
	w := nex.NewWriter()
	w.String("prudp:/address=192.168.1.20;port=49152;type=0")
	w.String("prudp:/address=192.168.1.20;port=49152;CID=4;type=0")
	p.MustCall(t, rendezvous.ProtocolSecureConnection, 7, w.Bytes())
	st := rendezvous.Stations(e.Server.Player(p.PID))
	if len(st) != 2 || st[0].Get("CID") != "4" || st[0].Uint("PID") != p.PID {
		t.Fatalf("stations after ReplaceURL: %v", st)
	}
}

func TestRequestURLsReturnsAnotherPlayersStations(t *testing.T) {
	e := rvtest.New(t, rvtest.DefaultTitle, nil)
	host := e.Login()
	host.Register(t, "prudp:/address=10.0.0.5;port=1000;type=0")
	joiner := e.Login()
	w := nex.NewWriter()
	w.U32(e.Server.Player(host.PID).ID)
	w.PID(host.PID)
	r := joiner.MustCall(t, rendezvous.ProtocolSecureConnection, 3, w.Bytes())
	if !r.Bool() {
		t.Fatal("RequestURLs found nothing")
	}
	if urls := nex.ReadList(r, (*nex.Reader).StationURL); len(urls) != 2 || urls[0].Get("address") != "10.0.0.5" {
		t.Fatalf("urls %v", urls)
	}
}

func TestRequestTicketIsOnlyForTheLoggedInPrincipal(t *testing.T) {
	e := rvtest.New(t, rvtest.DefaultTitle, nil)
	uid, da := e.User()
	other, _ := e.User()
	pid, _ := rendezvous.PIDForUserID(uid)
	otherPID, _ := rendezvous.PIDForUserID(other)

	auth, err := nextest.Dial(e.URL, nextest.Options{AccessKey: e.Title.AccessKey, Port: rendezvous.AuthPort})
	if err != nil {
		t.Fatal(err)
	}
	defer auth.Close()
	ask := func(source uint64) uint32 {
		w := nex.NewWriter()
		w.PID(source)
		w.PID(rendezvous.SecurePID)
		resp, err := auth.Call(rendezvous.ProtocolTicketGranting, 3, w.Bytes())
		if err != nil {
			t.Fatal(err)
		}
		return nex.NewReader(resp.Body).Result()
	}
	if ask(pid)&nex.ErrorMask == 0 {
		t.Fatal("a ticket was issued before any login")
	}
	resp, err := auth.Call(rendezvous.ProtocolTicketGranting, 2, rvtest.LoginExBody(fmt.Sprint(pid), e.IDToken(uid, da)))
	if err != nil || !resp.OK() {
		t.Fatal(err)
	}
	if ask(otherPID)&nex.ErrorMask == 0 {
		t.Fatal("a ticket was issued for someone else")
	}
	if ask(pid) != nex.ResultSuccess {
		t.Fatal("RequestTicket refused the logged-in principal")
	}
}

func TestGetPIDAndGetNameKnowOnlyRealUsers(t *testing.T) {
	e := rvtest.New(t, rvtest.DefaultTitle, nil)
	uid, _ := e.User()
	pid, _ := rendezvous.PIDForUserID(uid)
	auth, err := nextest.Dial(e.URL, nextest.Options{AccessKey: e.Title.AccessKey, Port: rendezvous.AuthPort})
	if err != nil {
		t.Fatal(err)
	}
	defer auth.Close()

	w := nex.NewWriter()
	w.String(fmt.Sprint(pid))
	resp, _ := auth.Call(rendezvous.ProtocolTicketGranting, 4, w.Bytes())
	if got := nex.NewReader(resp.Body).PID(); got != pid {
		t.Fatalf("GetPID = %d", got)
	}
	w = nex.NewWriter()
	w.String("guest")
	resp, _ = auth.Call(rendezvous.ProtocolTicketGranting, 4, w.Bytes())
	if got := nex.NewReader(resp.Body).PID(); got != 0 {
		t.Fatalf("GetPID(guest) = %d", got)
	}
	w = nex.NewWriter()
	w.PID(pid)
	resp, _ = auth.Call(rendezvous.ProtocolTicketGranting, 5, w.Bytes())
	if got := nex.NewReader(resp.Body).String(); got != fmt.Sprint(pid) {
		t.Fatalf("GetName = %q", got)
	}
}

func TestAnUnknownMethodIsAnsweredNotImplemented(t *testing.T) {
	e := rvtest.New(t, rvtest.DefaultTitle, nil)
	p := e.Login()
	resp := p.Call(t, 0x7FFE, 1, nil)
	if resp.OK() || resp.Result != nex.ResultNotImplemented|nex.ErrorMask {
		t.Fatalf("got %+v", resp)
	}
	if !bytes.Equal(resp.Body, nil) {
		t.Fatal("an error carries a body")
	}
}

func TestPlayingOverNEXCountsAsBeingOnline(t *testing.T) {
	e := rvtest.New(t, rvtest.DefaultTitle, nil)
	p := e.Login()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		pr, err := e.Store.GetPresence(context.Background(), p.UserID)
		if err == nil && !pr.LastSeenAt.IsZero() {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("a connected NEX player was never seen")
}
