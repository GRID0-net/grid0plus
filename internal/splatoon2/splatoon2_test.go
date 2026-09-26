package splatoon2_test

import (
	"bytes"
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/n-popescu/switchnet/config"
	"github.com/n-popescu/switchnet/internal/nex"
	"github.com/n-popescu/switchnet/internal/nex/nextest"
	"github.com/n-popescu/switchnet/internal/rendezvous"
	"github.com/n-popescu/switchnet/internal/rendezvous/rvtest"
	"github.com/n-popescu/switchnet/internal/splatoon2"
)

type env struct {
	*rvtest.Env
	s2 *splatoon2.Server
}

func newEnv(t *testing.T) *env {
	t.Helper()
	var s2 *splatoon2.Server
	title := rendezvous.Title{Name: "splatoon2", AccessKey: splatoon2.AccessKey}
	e := rvtest.Start(t, title, func(d rvtest.Deps) (*rendezvous.Server, http.Handler) {
		s2 = splatoon2.New(splatoon2.Options{Config: config.Splatoon2{Enabled: true},
			Store: d.Store, IDTokens: d.IDTokens, Log: d.Log})
		return s2.Server, s2
	})
	return &env{Env: e, s2: s2}
}

func TestTheSecureHalfOffersPRUDPMinorVersionZero(t *testing.T) {
	e := newEnv(t)
	auth, err := nextest.Dial(e.URL, nextest.Options{AccessKey: splatoon2.AccessKey, Port: rendezvous.AuthPort})
	if err != nil {
		t.Fatal(err)
	}
	defer auth.Close()
	if auth.MinorVersion != 5 {
		t.Fatalf("authentication half negotiated %d, want 5", auth.MinorVersion)
	}
	e.Login() // the secure half: rvtest would fail the test if CONNECT did
	// Kinnay's client rejects a CONNECT/ACK whose version differs from the
	// SYN/ACK's, so a successful login is itself the check that 0 round-trips.
}

func TestUpdateCurrentUserIsAnEmptySuccessAndAdoptsTheSavesUniqueID(t *testing.T) {
	e := newEnv(t)
	p := e.Login()
	w := nex.NewWriter()
	w.Struct(0, func(w *nex.Writer) {
		w.Struct(0, func(w *nex.Writer) { w.U64(424242); w.U64(7) })
		w.U8(2)
	})
	resp := p.Call(t, rendezvous.ProtocolUtility, 10, w.Bytes())
	if !resp.OK() || len(resp.Body) != 0 {
		t.Fatalf("UpdateCurrentUser answered %#x with %d bytes; Nintendo's reply is empty", resp.Result, len(resp.Body))
	}
	ids := e.s2.UniqueIDsOf(p.PID)
	if len(ids) != 1 || ids[0].ID != 424242 {
		t.Fatalf("unique ids %+v", ids)
	}
	// Someone else presenting the same id does not get it.
	q := e.Login()
	q.MustCall(t, rendezvous.ProtocolUtility, 10, w.Bytes())
	if ids := e.s2.UniqueIDsOf(q.PID); len(ids) != 0 {
		t.Fatalf("a second player took over %+v", ids)
	}
}

func TestATagIDIsTheSameForTheSameTeamInAnyOrder(t *testing.T) {
	e := newEnv(t)
	p := e.Login()
	ask := func(ids ...uint64) uint64 {
		w := nex.NewWriter()
		nex.WriteList(w, ids, (*nex.Writer).U64)
		return p.MustCall(t, rendezvous.ProtocolUtility, 9, w.Bytes()).U64()
	}
	a, b := ask(1, 2, 3, 4), ask(4, 3, 2, 1)
	if a == 0 || a != b || a == ask(1, 2) {
		t.Fatalf("tags %d %d", a, b)
	}
}

func vote(t *testing.T, p *rvtest.Player, fid uint32, team uint8) {
	w := nex.NewWriter()
	w.Struct(0, func(w *nex.Writer) { w.U32(fid); w.U8(team) })
	if resp := p.Call(t, rendezvous.ProtocolRanking, 19, w.Bytes()); !resp.OK() || len(resp.Body) != 0 {
		t.Fatalf("UploadFestivalVote %+v", resp)
	}
}

func festScore(t *testing.T, p *rvtest.Player, fid uint32, team uint8, score uint32) {
	w := nex.NewWriter()
	w.Struct(0, func(w *nex.Writer) {
		w.U64(p.PID)
		w.U8(1)
		w.U32(fid)
		w.U8(team)
		w.U32(score)
		w.U32(0)
		w.U32(1)
		w.QBuffer(nil)
	})
	p.MustCall(t, rendezvous.ProtocolRanking, 20, w.Bytes())
}

func TestASplatfestResultTalliesVotesAndScoresPerTeam(t *testing.T) {
	e := newEnv(t)
	a, b, c := e.Login(), e.Login(), e.Login()
	vote(t, a, 7, 0)
	vote(t, b, 7, 1)
	vote(t, c, 7, 1)
	festScore(t, a, 7, 0, 10)
	festScore(t, a, 7, 0, 15)
	festScore(t, b, 7, 1, 8)

	w := nex.NewWriter()
	w.U32(7)
	r := a.MustCall(t, rendezvous.ProtocolRanking, 18, w.Bytes())
	var fid, total uint32
	var parts, scores []uint32
	var scores64 []uint64
	var version uint8
	r.Struct(func(r *nex.Reader, v uint8) {
		version = v
		fid, total = r.U32(), r.U32()
		parts = nex.ReadList(r, (*nex.Reader).U32)
		scores = nex.ReadList(r, (*nex.Reader).U32)
		_ = r.DateTime()
		_ = r.U8()
		scores64 = nex.ReadList(r, (*nex.Reader).U64)
	})
	if r.Err() != nil || version != 1 || fid != 7 || total != 3 {
		t.Fatalf("result v%d fid %d total %d err %v", version, fid, total, r.Err())
	}
	if parts[0] != 1 || parts[1] != 2 || scores[0] != 25 || scores[1] != 8 || scores64[0] != 25 {
		t.Fatalf("parts %v scores %v %v", parts, scores, scores64)
	}
}

func leaguePoint(tag uint64, league string, point uint32, members ...uint64) []byte {
	w := nex.NewWriter()
	w.Struct(0, func(w *nex.Writer) {
		w.U64(tag)
		w.U8(1)
		w.String(league)
		w.U32(point)
		w.U32(uint32(len(members)))
		for _, m := range members {
			w.U64(m)
			w.Struct(0, func(w *nex.Writer) { w.PID(m); w.S32(40) })
		}
		w.QBuffer([]byte{1})
	})
	return w.Bytes()
}

func TestALeagueTeamIsRankedAgainstItsLeague(t *testing.T) {
	e := newEnv(t)
	p := e.Login()
	p.MustCall(t, rendezvous.ProtocolRanking, 16, leaguePoint(100, "L1", 2000, p.PID))
	p.MustCall(t, rendezvous.ProtocolRanking, 16, leaguePoint(200, "L1", 2500, p.PID))
	p.MustCall(t, rendezvous.ProtocolRanking, 16, leaguePoint(100, "L1", 1500, p.PID)) // worse: kept 2000

	get := func(tag uint64) *nex.Response {
		w := nex.NewWriter()
		w.String("L1")
		w.U64(tag)
		return p.Call(t, rendezvous.ProtocolRanking, 17, w.Bytes())
	}
	resp := get(100)
	if !resp.OK() {
		t.Fatalf("GetLeagueResult %#x", resp.Result)
	}
	r := nex.NewReader(resp.Body)
	var point, rank, tags, matches uint32
	r.Struct(func(r *nex.Reader, _ uint8) {
		r.Struct(func(r *nex.Reader, _ uint8) {
			_, _, _ = r.U64(), r.U8(), r.String()
			point = r.U32()
			n := r.U32()
			for i := uint32(0); i < n; i++ {
				_ = r.U64()
				r.Struct(func(r *nex.Reader, _ uint8) { _, _ = r.PID(), r.S32() })
			}
			_ = r.QBuffer()
		})
		_ = r.U8()
		rank = r.U32()
		_ = r.U8()
		tags, matches = r.U32(), r.U32()
	})
	if r.Err() != nil || point != 2000 || rank != 2 || tags != 2 || matches != 2 {
		t.Fatalf("point %d rank %d of %d, %d matches, err %v", point, rank, tags, matches, r.Err())
	}
	if resp := get(999); resp.Result != nex.ResultRankingNotFound|nex.ErrorMask {
		t.Fatalf("an unknown team answered %#x", resp.Result)
	}
}

func TestAMatchIsRecordedWhenItsLobbyCloses(t *testing.T) {
	e := newEnv(t)
	host := e.Login()
	w := nex.NewWriter()
	w.Struct(0, func(w *nex.Writer) {
		rendezvous.WriteMatchmakeSession(w, &rendezvous.MatchmakeSession{
			Gathering: rendezvous.Gathering{MaxParticipants: 8}, GameMode: 12, OpenParticipation: true})
		w.U32(0)
		w.U32(0)
		w.U32(0)
		w.String("")
		w.U16(1)
		w.U32(0)
		w.U32(0)
		w.Struct(0, func(w *nex.Writer) { w.U32(0) })
	})
	s := rendezvous.ReadMatchmakeSession(host.MustCall(t, rendezvous.ProtocolMatchmakeExtension, 40, w.Bytes()))
	w = nex.NewWriter()
	w.U32(s.ID)
	host.MustCall(t, rendezvous.ProtocolMatchmakeExtension, 1, w.Bytes())

	ms, err := e.Store.ListMatches(context.Background(), 10)
	if err != nil || len(ms) != 1 || ms[0].Tenant != splatoon2.Tenant || ms[0].HostUserID != host.UserID {
		t.Fatalf("matches %+v err %v", ms, err)
	}
}

func TestAFriendsLobbyIsNamedByMethod60(t *testing.T) {
	e := newEnv(t)
	host, friend := e.Login(), e.Login()
	if err := e.Store.AddFriendship(context.Background(), host.UserID, friend.UserID, time.Now()); err != nil {
		t.Fatal(err)
	}
	// Put the host in a lobby.
	w := nex.NewWriter()
	w.Struct(0, func(w *nex.Writer) {
		rendezvous.WriteMatchmakeSession(w, &rendezvous.MatchmakeSession{
			Gathering: rendezvous.Gathering{MaxParticipants: 8}, GameMode: 1, OpenParticipation: true})
		w.U32(0)
		w.U32(0)
		w.U32(0)
		w.String("")
		w.U16(1)
		w.U32(0)
		w.U32(0)
		w.Struct(0, func(w *nex.Writer) { w.U32(0) })
	})
	s := rendezvous.ReadMatchmakeSession(host.MustCall(t, rendezvous.ProtocolMatchmakeExtension, 40, w.Bytes()))

	w = nex.NewWriter()
	nex.WriteList(w, []uint64{host.PID}, (*nex.Writer).PID)
	w.U8(0)
	w.U8(0)
	r := friend.MustCall(t, rendezvous.ProtocolMatchmakeExtension, 60, w.Bytes())
	var gid uint32
	n := r.U32()
	r.Struct(func(r *nex.Reader, _ uint8) { _ = r.PID(); gid = r.U32(); _, _ = r.U32(), r.U32() })
	if n != 1 || gid != s.ID {
		t.Fatalf("%d sessions, gid %d want %d", n, gid, s.ID)
	}
}

func TestMatchResultsAreKeptWithTheirModeRuleAndStage(t *testing.T) {
	e := newEnv(t)
	p := e.Login()
	w := nex.NewWriter()
	w.Struct(0, func(w *nex.Writer) { w.U32(1); w.S32(3); w.U8(1); w.S32(9); w.U32(0xdead) })
	w.Struct(0, func(w *nex.Writer) { w.S32(50); w.S32(40); w.S32(3) })
	p.MustCall(t, splatoon2.ProtocolDataStore, 61, w.Bytes())
	res := e.s2.RecentResults(10)
	if len(res) != 1 || res[0].Method != "UploadRegularMatchResultV2" || res[0].Rule != 3 || res[0].Stage != 9 ||
		res[0].Result != 1 || res[0].PID != p.PID {
		t.Fatalf("results %+v", res)
	}
}

func TestAnniesOrderIsPlacedCollectedAndGone(t *testing.T) {
	e := newEnv(t)
	p := e.Login()
	if resp := p.Call(t, splatoon2.ProtocolDataStore, 55, nil); resp.Result != nex.ResultDataStoreNotFound|nex.ErrorMask {
		t.Fatalf("no order answered %#x", resp.Result)
	}
	if err := e.s2.PlaceGearOrder(p.PID, splatoon2.GearOrder{Kind: 1, ID: 21001, Skill: 3, Price: 4500}); err != nil {
		t.Fatal(err)
	}
	r := p.MustCall(t, splatoon2.ProtocolDataStore, 55, nil)
	var kind, id, skill, price int32
	r.Struct(func(r *nex.Reader, _ uint8) { kind, id, skill, price = r.S32(), r.S32(), r.S32(), r.S32() })
	if kind != 1 || id != 21001 || skill != 3 || price != 4500 {
		t.Fatalf("order %d %d %d %d", kind, id, skill, price)
	}
	w := nex.NewWriter()
	w.Struct(0, func(w *nex.Writer) { w.S32(kind); w.S32(id); w.S32(skill); w.S32(price) })
	p.MustCall(t, splatoon2.ProtocolDataStore, 56, w.Bytes())
	if _, ok := e.s2.GearOrderOf(p.PID); ok {
		t.Fatal("a collected order is still open")
	}
}

func TestAnUndocumentedDataStoreMethodGetsTheEmptySuccessTheGameAccepts(t *testing.T) {
	e := newEnv(t)
	p := e.Login()
	if resp := p.Call(t, splatoon2.ProtocolDataStore, 8, []byte{1, 2, 3}); !resp.OK() || len(resp.Body) != 0 {
		t.Fatalf("fallback answered %+v", resp)
	}
}

func coconutMeta(fid uint32, binary []byte) []byte {
	w := nex.NewWriter()
	w.Struct(0, func(w *nex.Writer) {
		w.U64(0)
		w.U64(0)
		w.U8(1)
		w.U8(0)
		w.U8(1)
		w.U32(fid)
		w.String("en")
		w.QBuffer(binary)
	})
	return w.Bytes()
}

func TestAPlazaPostIsSharedAndServedBack(t *testing.T) {
	e := newEnv(t)
	author, reader := e.Login(), e.Login()
	body := append(coconutMeta(5, []byte("drawing")), 0x01, 0x00, 0x00) // String url ""
	author.MustCall(t, splatoon2.ProtocolDataStore, 47, body)

	w := nex.NewWriter()
	w.Struct(0, func(w *nex.Writer) { w.U32(0); w.U8(0); w.U8(1); w.U32(5) })
	r := reader.MustCall(t, splatoon2.ProtocolDataStore, 49, w.Bytes())
	var url string
	var bin []byte
	n := r.U32()
	r.Struct(func(r *nex.Reader, _ uint8) {
		r.Struct(func(r *nex.Reader, _ uint8) {
			url = r.String()
			_ = nex.ReadList(r, func(r *nex.Reader) int {
				r.Struct(func(r *nex.Reader, _ uint8) { _, _ = r.String(), r.String() })
				return 0
			})
			_ = r.U32()
			_ = r.Buffer()
			_ = r.U64()
		})
		r.Struct(func(r *nex.Reader, _ uint8) {
			_, _, _, _, _, _, _ = r.U64(), r.U64(), r.U8(), r.U8(), r.U8(), r.U32(), r.String()
			bin = r.QBuffer()
		})
	})
	if results := nex.ReadList(r, (*nex.Reader).Result); n != 1 || len(results) != 1 || r.Err() != nil {
		t.Fatalf("%d posts, results %v, err %v", n, results, r.Err())
	}
	if string(bin) != "drawing" || !strings.HasPrefix(url, "https://"+splatoon2.Host+"/datastore/coconut/") {
		t.Fatalf("post %q at %q", bin, url)
	}
	resp, err := http.Get(e.HTTPURL + strings.TrimPrefix(url, "https://"+splatoon2.Host))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	got := new(bytes.Buffer)
	_, _ = got.ReadFrom(resp.Body)
	if resp.StatusCode != 200 || got.String() != "drawing" {
		t.Fatalf("served %d %q", resp.StatusCode, got)
	}
}

func TestAPlayLogUploadIsAcceptedOnceForAPreparedID(t *testing.T) {
	e := newEnv(t)
	p := e.Login()
	r := p.MustCall(t, splatoon2.ProtocolDataStore, 66, []byte{0})
	var url string
	r.Struct(func(r *nex.Reader, _ uint8) { _ = r.U64(); url = r.String() })
	path := strings.TrimPrefix(url, "https://"+splatoon2.Host)
	post := func(p string) int {
		resp, err := http.Post(e.HTTPURL+p, "application/octet-stream", strings.NewReader("log"))
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		return resp.StatusCode
	}
	if code := post(path); code != 200 {
		t.Fatalf("upload answered %d", code)
	}
	if code := post(path); code != 404 {
		t.Fatalf("second upload answered %d", code)
	}
	if code := post("/datastore/playlog/12345"); code != 404 {
		t.Fatalf("an unprepared id answered %d", code)
	}
}
