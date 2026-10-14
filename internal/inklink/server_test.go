package inklink

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/rsa"
	"encoding/json"
	gojwt "github.com/golang-jwt/jwt/v5"
	"github.com/n-popescu/grid0plus/internal/db"
	commonpb "github.com/n-popescu/grid0plus/proto/common"
	"golang.org/x/crypto/bcrypt"
	"google.golang.org/protobuf/proto"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

type harness struct {
	s     *Server
	store *db.Memory
	now   time.Time
}

func setup(t *testing.T) *harness {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	h := &harness{store: db.NewMemory(), now: time.Now()}
	for _, id := range []string{"alice", "bob"} {
		hash, _ := bcrypt.GenerateFromPassword([]byte("password-"+id), bcrypt.MinCost)
		if err := h.store.CreateUserWithLogin(context.Background(), db.User{ID: id, Nickname: id, LoginUsername: id, LoginPasswordHash: string(hash)}, true); err != nil {
			t.Fatal(err)
		}
	}
	h.s, err = New(Options{Store: h.store, Key: key, Tenant: "test", SecureCookies: true, Now: func() time.Time { return h.now }})
	if err != nil {
		t.Fatal(err)
	}
	return h
}
func (h *harness) req(method, path, body string, cookie *http.Cookie) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, path, strings.NewReader(body))
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("X-Inklink-Request", "1")
	if cookie != nil {
		r.AddCookie(cookie)
	}
	w := httptest.NewRecorder()
	h.s.Handler().ServeHTTP(w, r)
	return w
}
func (h *harness) login(t *testing.T) *http.Cookie {
	t.Helper()
	w := h.req("POST", "/api/login", `{"username":"alice","password":"password-alice"}`, nil)
	if w.Code != 200 {
		t.Fatalf("login %d %s", w.Code, w.Body.String())
	}
	return w.Result().Cookies()[0]
}
func TestPlayerSessionSurvivesServerRestartAndPasswordResetInvalidatesIt(t *testing.T) {
	h := setup(t)
	c := h.login(t)
	if !c.HttpOnly || !c.Secure || c.SameSite != http.SameSiteStrictMode || c.Path != "/" {
		t.Fatalf("unsafe cookie settings: %+v", *c)
	}
	again, err := New(h.s.opts)
	if err != nil {
		t.Fatal(err)
	}
	h.s = again
	if w := h.req("GET", "/api/data", "", c); w.Code != 200 {
		t.Fatalf("session did not survive restart: %d %s", w.Code, w.Body.String())
	}
	hash, _ := bcrypt.GenerateFromPassword([]byte("changed"), bcrypt.MinCost)
	hs := string(hash)
	if _, err := h.store.UpdateUser(context.Background(), "alice", db.UserPatch{LoginPasswordHash: &hs}); err != nil {
		t.Fatal(err)
	}
	if w := h.req("GET", "/api/data", "", c); w.Code != 401 {
		t.Fatalf("old password session still works: %d", w.Code)
	}
}
func TestCompanionRefusesMissingExpiredAndOtherServiceCredentials(t *testing.T) {
	h := setup(t)
	if w := h.req("GET", "/api/data", "", nil); w.Code != 401 {
		t.Fatal(w.Code)
	}
	c := h.login(t)
	h.now = h.now.Add(sessionLifetime + time.Second)
	if w := h.req("GET", "/api/data", "", c); w.Code != 401 {
		t.Fatal("expired cookie accepted")
	}
	token, err := gojwt.NewWithClaims(gojwt.SigningMethodRS256, gojwt.RegisteredClaims{Subject: "alice", Issuer: "baas", Audience: gojwt.ClaimStrings{"inklink"}, ExpiresAt: gojwt.NewNumericDate(h.now.Add(time.Hour))}).SignedString(h.s.opts.Key)
	if err != nil {
		t.Fatal(err)
	}
	if w := h.req("GET", "/api/data", "", &http.Cookie{Name: cookieName, Value: token}); w.Code != 401 {
		t.Fatal("other service credential accepted")
	}
}
func TestPlayerCannotChooseAnotherPlayersHistoryOrSave(t *testing.T) {
	h := setup(t)
	for _, uid := range []string{"alice", "bob"} {
		raw, _ := proto.Marshal(&commonpb.MapValue{Fields: map[string]*commonpb.Value{"marker": {ValueType: &commonpb.Value_StringValue{StringValue: "private-" + uid}}}})
		for _, name := range []string{"tenants/test/documents/services/GameRecord/users/" + uid + "/VsResults/battle", "tenants/test/saveRecords/" + uid} {
			if err := h.store.PutUGCDocument(context.Background(), db.UGCDocument{Name: name, Owner: uid, Fields: raw, CreateTime: h.now, UpdateTime: h.now}); err != nil {
				t.Fatal(err)
			}
		}
	}
	raw, _ := proto.Marshal(&commonpb.MapValue{Fields: map[string]*commonpb.Value{"marker": {ValueType: &commonpb.Value_StringValue{StringValue: "owner-mismatch"}}}})
	h.store.PutUGCDocument(context.Background(), db.UGCDocument{Name: "tenants/test/documents/services/GameRecord/users/alice/VsResults/bad", Owner: "bob", Fields: raw})
	w := h.req("GET", "/api/data?user=bob&uid=bob", "", h.login(t))
	if w.Code != 200 {
		t.Fatalf("%d %s", w.Code, w.Body.String())
	}
	if strings.Contains(w.Body.String(), "private-bob") || strings.Contains(w.Body.String(), "owner-mismatch") {
		t.Fatal("another player's data leaked")
	}
	var body struct {
		Records  map[string][]record `json:"records"`
		Progress map[string]any      `json:"progress"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if len(body.Records["VsResults"]) != 1 {
		t.Fatalf("wrong history count: %d", len(body.Records["VsResults"]))
	}
	if strings.Contains(w.Body.String(), "LoginPasswordHash") || strings.Contains(w.Body.String(), "password-alice") {
		t.Fatal("login credential exposed")
	}
}
func TestLoginAndLogoutRejectCrossOriginFormRequests(t *testing.T) {
	h := setup(t)
	for _, path := range []string{"/api/login", "/api/logout"} {
		r := httptest.NewRequest("POST", path, bytes.NewBufferString(`{}`))
		w := httptest.NewRecorder()
		h.s.Handler().ServeHTTP(w, r)
		if w.Code != 403 {
			t.Fatalf("form allowed on %s: %d", path, w.Code)
		}
		r = httptest.NewRequest("POST", path, bytes.NewBufferString(`{}`))
		r.Header.Set("X-Inklink-Request", "1")
		r.Header.Set("Origin", "https://other.example")
		w = httptest.NewRecorder()
		h.s.Handler().ServeHTTP(w, r)
		if w.Code != 403 {
			t.Fatal("cross-origin mutation allowed")
		}
	}
}
func TestLoginIsRateLimitedAndNeverFallsBackToAnotherAccount(t *testing.T) {
	h := setup(t)
	for i := 0; i < 8; i++ {
		w := h.req("POST", "/api/login", `{"username":"missing","password":"password-alice"}`, nil)
		if w.Code != 401 {
			t.Fatalf("attempt %d: %d", i, w.Code)
		}
	}
	if w := h.req("POST", "/api/login", `{"username":"alice","password":"password-alice"}`, nil); w.Code != 429 {
		t.Fatal("login not rate limited")
	}
	h.now = h.now.Add(16 * time.Minute)
	if w := h.req("POST", "/api/login", `{"username":"alice","password":"password-alice"}`, nil); w.Code != 200 {
		t.Fatal("limiter did not expire")
	}
}
func TestLogoutExpiresTheBrowserCookie(t *testing.T) {
	h := setup(t)
	w := h.req("POST", "/api/logout", `{}`, h.login(t))
	cookies := w.Result().Cookies()
	if w.Code != 200 || len(cookies) != 1 || cookies[0].MaxAge != -1 || cookies[0].Value != "" {
		t.Fatal("logout left browser cookie active")
	}
}
func TestMissingPlayerDataIsNotInventedAndUnknownRoutesAreNotAppPages(t *testing.T) {
	h := setup(t)
	w := h.req("GET", "/api/data", "", h.login(t))
	if w.Code != 200 {
		t.Fatal(w.Code)
	}
	var v map[string]json.RawMessage
	json.Unmarshal(w.Body.Bytes(), &v)
	if string(v["savedAt"]) != "null" || string(v["progress"]) != "{}" {
		t.Fatal("missing save represented as existing progress")
	}
	if !strings.Contains(w.Header().Get("Cache-Control"), "no-store") {
		t.Fatal("private API is cacheable")
	}
	if w := h.req("GET", "/api/data/bob", "", nil); w.Code != 404 {
		t.Fatal("unknown route rendered the app")
	}
}
func TestOnlyTheLocalProxyCanSupplyARateLimitAddress(t *testing.T) {
	r := httptest.NewRequest("POST", "/api/login", nil)
	r.RemoteAddr = "203.0.113.4:1234"
	r.Header.Set("X-Real-IP", "198.51.100.8")
	if got := clientAddress(r); got != r.RemoteAddr {
		t.Fatal("untrusted forwarding header honored")
	}
	r.RemoteAddr = "127.0.0.1:3333"
	if got := clientAddress(r); got != "198.51.100.8" {
		t.Fatal("trusted local proxy did not separate clients")
	}
}
