package splatoon2_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/n-popescu/grid0plus/internal/rendezvous"
	"github.com/n-popescu/grid0plus/internal/splatoon2"
)

func adminDo(t *testing.T, h http.Handler, method, path, token, body string) (int, map[string]any) {
	t.Helper()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	var out map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	return rec.Code, out
}

func TestTheAdminAPIIsNotServedWithoutAToken(t *testing.T) {
	e := newEnv(t)
	if h := e.s2.AdminHandler(""); h != nil {
		t.Fatal("an admin API with no token was served")
	}
}

func TestTheAdminAPIRefusesAWrongToken(t *testing.T) {
	e := newEnv(t)
	h := e.s2.AdminHandler("right")
	for _, tok := range []string{"", "wrong", "righ"} {
		if code, _ := adminDo(t, h, "GET", "/splatoon2", tok, ""); code != http.StatusUnauthorized {
			t.Fatalf("token %q got %d", tok, code)
		}
	}
	p := e.Login()
	uid := rendezvous.UserIDForPID(p.PID)
	if code, _ := adminDo(t, h, "POST", "/splatoon2/players/"+uid+"/disconnect", "wrong", ""); code != http.StatusUnauthorized {
		t.Fatalf("disconnect with a wrong token got %d", code)
	}
	if e.s2.Player(p.PID) == nil {
		t.Fatal("a refused disconnect still disconnected the player")
	}
}

func TestTheAdminStatusListsAConnectedPlayerAndTheirCalls(t *testing.T) {
	e := newEnv(t)
	h := e.s2.AdminHandler("tok")
	p := e.Login()
	p.Call(t, splatoon2.ProtocolDataStore, 8, nil)
	code, st := adminDo(t, h, "GET", "/splatoon2", "tok", "")
	if code != http.StatusOK {
		t.Fatalf("status %d", code)
	}
	players, _ := st["players"].([]any)
	if len(players) != 1 {
		t.Fatalf("players %v", st["players"])
	}
	pl := players[0].(map[string]any)
	if pl["userId"] != rendezvous.UserIDForPID(p.PID) || pl["remoteIp"] == "" || pl["nat"] == nil {
		t.Fatalf("player %v", pl)
	}
	// A PID is above 2^53 as often as not; as a JSON number the page would
	// read it back as a different player.
	if pl["pid"] != strconv.FormatUint(p.PID, 10) {
		t.Fatalf("pid sent as %#v, want the exact decimal string", pl["pid"])
	}
	calls, _ := st["calls"].([]any)
	if len(calls) == 0 {
		t.Fatal("no calls recorded")
	}
	newest := calls[0].(map[string]any)
	if m := newest["method"].(string); !strings.Contains(m, "DataStore") {
		t.Fatalf("newest call is %q, want the DataStore one", m)
	}
	if newest["userId"] != rendezvous.UserIDForPID(p.PID) || newest["pid"] != strconv.FormatUint(p.PID, 10) {
		t.Fatalf("call names %v / %v", newest["userId"], newest["pid"])
	}
	if _, ok := st["methods"].([]any); !ok {
		t.Fatalf("no method counts: %v", st)
	}
}

func TestTheOperatorCanPlaceAndCancelAGearOrder(t *testing.T) {
	e := newEnv(t)
	h := e.s2.AdminHandler("tok")
	p := e.Login()
	uid := rendezvous.UserIDForPID(p.PID)
	code, _ := adminDo(t, h, "POST", "/splatoon2/players/"+uid+"/gear-order", "tok",
		`{"kind":1,"id":21001,"skill":3,"price":4500}`)
	if code != http.StatusOK {
		t.Fatalf("place %d", code)
	}
	if o, ok := e.s2.GearOrderOf(p.PID); !ok || o.ID != 21001 || o.Price != 4500 {
		t.Fatalf("order %+v %v", o, ok)
	}
	if code, _ := adminDo(t, h, "DELETE", "/splatoon2/players/"+uid+"/gear-order", "tok", ""); code != http.StatusOK {
		t.Fatalf("cancel %d", code)
	}
	if _, ok := e.s2.GearOrderOf(p.PID); ok {
		t.Fatal("a cancelled order is still open")
	}
	if code, _ := adminDo(t, h, "POST", "/splatoon2/players/nothex/gear-order", "tok", `{}`); code != http.StatusBadRequest {
		t.Fatalf("a bad user id got %d", code)
	}
}

func TestTheOperatorCanDisconnectAPlayer(t *testing.T) {
	e := newEnv(t)
	h := e.s2.AdminHandler("tok")
	p := e.Login()
	uid := rendezvous.UserIDForPID(p.PID)
	if code, _ := adminDo(t, h, "POST", "/splatoon2/players/"+uid+"/disconnect", "tok", ""); code != http.StatusOK {
		t.Fatalf("disconnect %d", code)
	}
	if code, _ := adminDo(t, h, "POST", "/splatoon2/players/"+uid+"/disconnect", "tok", ""); code != http.StatusNotFound {
		t.Fatalf("a second disconnect got %d, want 404", code)
	}
}

func TestTheAdminRecordsShowWhatTheGameStored(t *testing.T) {
	e := newEnv(t)
	h := e.s2.AdminHandler("tok")
	a, b := e.Login(), e.Login()
	vote(t, a, 4056, 0)
	vote(t, b, 4056, 1)
	festScore(t, a, 4056, 0, 1200)
	a.MustCall(t, rendezvous.ProtocolRanking, 16, leaguePoint(77, "18011508T", 2100, a.PID, b.PID))
	a.MustCall(t, splatoon2.ProtocolDataStore, 47, append(coconutMeta(4056, []byte("drawing")), 0x01, 0x00, 0x00))
	if err := e.s2.PlaceGearOrder(b.PID, splatoon2.GearOrder{Kind: 2, ID: 7, Skill: 1, Price: 900}); err != nil {
		t.Fatal(err)
	}
	b.MustCall(t, splatoon2.ProtocolDataStore, 57, []byte{9, 9, 9})

	_, fests := adminDo(t, h, "GET", "/splatoon2/fests", "tok", "")
	f := fests["fests"].([]any)[0].(map[string]any)
	if f["festivalId"] != float64(4056) || f["votes"].([]any)[0] != float64(1) || f["votes"].([]any)[1] != float64(1) ||
		f["scores"].([]any)[0] != float64(1200) {
		t.Fatalf("fest %v", f)
	}

	_, leagues := adminDo(t, h, "GET", "/splatoon2/leagues", "tok", "")
	l := leagues["leagues"].([]any)[0].(map[string]any)
	team := l["teams"].([]any)[0].(map[string]any)
	if l["leagueId"] != "18011508T" || team["tagId"] != "77" || team["point"] != float64(2100) ||
		len(team["members"].([]any)) != 2 {
		t.Fatalf("league %v", l)
	}

	_, plaza := adminDo(t, h, "GET", "/splatoon2/plaza", "tok", "")
	post := plaza["posts"].([]any)[0].(map[string]any)
	if post["userId"] != rendezvous.UserIDForPID(a.PID) || post["festivalId"] != float64(4056) ||
		post["bytes"] != float64(len("drawing")) || post["hidden"] != false {
		t.Fatalf("post %v", post)
	}
	id := post["dataId"].(string)
	if code, out := adminDo(t, h, "POST", "/splatoon2/plaza/"+id+"/hidden", "tok", `{"hidden":true}`); code != http.StatusOK ||
		out["posts"].([]any)[0].(map[string]any)["hidden"] != true {
		t.Fatalf("hide: %d %v", code, out)
	}
	if code, _ := adminDo(t, h, "POST", "/splatoon2/plaza/1/hidden", "tok", `{"hidden":true}`); code != http.StatusNotFound {
		t.Fatalf("hiding a post that does not exist got %d", code)
	}

	_, orders := adminDo(t, h, "GET", "/splatoon2/gear-orders", "tok", "")
	o := orders["orders"].([]any)[0].(map[string]any)
	if o["userId"] != rendezvous.UserIDForPID(b.PID) || o["order"].(map[string]any)["price"] != float64(900) {
		t.Fatalf("orders %v", orders)
	}

	_, ups := adminDo(t, h, "GET", "/splatoon2/uploads", "tok", "")
	u := ups["uploads"].([]any)[0].(map[string]any)
	if u["kind"] != "time_attack" || u["userId"] != rendezvous.UserIDForPID(b.PID) || u["bytes"] != float64(3) {
		t.Fatalf("uploads %v", ups)
	}

	for _, path := range []string{"/splatoon2/fests", "/splatoon2/leagues", "/splatoon2/plaza",
		"/splatoon2/gear-orders", "/splatoon2/uploads"} {
		if code, _ := adminDo(t, h, "GET", path, "", ""); code != http.StatusUnauthorized {
			t.Errorf("%s without a token got %d", path, code)
		}
	}
}
