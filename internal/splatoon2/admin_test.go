package splatoon2_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/n-popescu/switchnet/internal/rendezvous"
	"github.com/n-popescu/switchnet/internal/splatoon2"
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
	calls, _ := st["calls"].([]any)
	if len(calls) == 0 {
		t.Fatal("no calls recorded")
	}
	if m := calls[0].(map[string]any)["method"].(string); !strings.Contains(m, "DataStore") {
		t.Fatalf("newest call is %q, want the DataStore one", m)
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
