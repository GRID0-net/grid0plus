package inklink

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/n-popescu/grid0plus/config"
	"github.com/n-popescu/grid0plus/internal/db"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
func enableDiscord(t *testing.T, h *harness, id string) *int {
	t.Helper()
	calls := new(int)
	h.s.opts.Discord = config.DiscordOAuth{ClientID: "123456789012345678", ClientSecret: "test-secret", RedirectURI: "https://example.com/auth/discord/callback"}
	h.s.opts.OAuthClient = &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		*calls++
		body := ""
		switch r.URL.String() {
		case "https://discord.com/api/oauth2/token":
			if r.Method != "POST" {
				t.Error("token exchange is not POST")
			}
			id, secret, ok := r.BasicAuth()
			if !ok || id != h.s.opts.Discord.ClientID || secret != "test-secret" {
				t.Error("missing backend client credentials")
			}
			if !strings.HasPrefix(r.Header.Get("Content-Type"), "application/x-www-form-urlencoded") {
				t.Error("token exchange must be form encoded")
			}
			r.ParseForm()
			if r.Form.Get("code") != "test-code" || r.Form.Get("redirect_uri") != h.s.opts.Discord.RedirectURI {
				t.Error("wrong code or callback")
			}
			body = `{"access_token":"private-discord-access","token_type":"Bearer","scope":"identify"}`
		case "https://discord.com/api/v10/users/@me":
			if r.Header.Get("Authorization") != "Bearer private-discord-access" {
				t.Error("identity request lacks token")
			}
			raw, _ := json.Marshal(map[string]any{"id": id, "username": "alice", "global_name": "Discord Alice"})
			body = string(raw)
		default:
			t.Fatalf("unexpected provider URL %s", r.URL)
		}
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(body)), Header: make(http.Header)}, nil
	})}
	return calls
}
func startDiscord(t *testing.T, h *harness) (string, *http.Cookie) {
	t.Helper()
	w := h.req("GET", "/auth/discord", "", nil)
	if w.Code != 302 {
		t.Fatalf("start: %d %s", w.Code, w.Body.String())
	}
	u, err := url.Parse(w.Header().Get("Location"))
	if err != nil {
		t.Fatal(err)
	}
	if u.Host != "discord.com" || u.Query().Get("scope") != "identify" || u.Query().Get("response_type") != "code" {
		t.Fatal("wrong authorization destination or permissions")
	}
	c := w.Result().Cookies()[0]
	if c.Name != oauthCookie || !c.HttpOnly || !c.Secure || c.SameSite != http.SameSiteLaxMode {
		t.Fatal("flow cookie must work on the Discord callback")
	}
	return u.Query().Get("state"), c
}
func callback(h *harness, state string, c *http.Cookie) *httptest.ResponseRecorder {
	return h.req("GET", "/auth/discord/callback?code=test-code&state="+state, "", c)
}
func sessionCookie(t *testing.T, w *httptest.ResponseRecorder) *http.Cookie {
	t.Helper()
	for _, c := range w.Result().Cookies() {
		if c.Name == cookieName {
			return c
		}
	}
	t.Fatal("missing session cookie")
	return nil
}
func TestDiscordSignsIntoTheLinkedAccountNotAMatchingUsername(t *testing.T) {
	h := setup(t)
	id := "123456789012345678"
	calls := enableDiscord(t, h, id)
	if err := h.store.LinkBotAccount(context.Background(), id, "bob", true); err != nil {
		t.Fatal(err)
	}
	state, c := startDiscord(t, h)
	w := callback(h, state, c)
	if w.Code != 303 || w.Header().Get("Location") != "/#home" {
		t.Fatalf("callback: %d %s", w.Code, w.Header().Get("Location"))
	}
	session := sessionCookie(t, w)
	result := h.req("GET", "/api/data", "", session)
	if result.Code != 200 || !strings.Contains(result.Body.String(), `"id":"bob"`) {
		t.Fatal("Discord matched username instead of verified ownership")
	}
	if strings.Contains(session.Value, "private-discord-access") {
		t.Fatal("provider token persisted")
	}
	again := callback(h, state, c)
	if again.Header().Get("Location") != "/?signin=expired" || *calls != 2 {
		t.Fatal("callback replay reached provider")
	}
}
func TestDiscordSignInWithoutAnAccountAsksToRegisterAndCreatesNothing(t *testing.T) {
	h := setup(t)
	id := "123456789012345679"
	enableDiscord(t, h, id)
	state, c := startDiscord(t, h)
	w := callback(h, state, c)
	if w.Header().Get("Location") != "/?signin=register" {
		t.Fatal(w.Header().Get("Location"))
	}
	for _, ck := range w.Result().Cookies() {
		if ck.Name == cookieName && ck.Value != "" {
			t.Fatal("unregistered Discord user got a session")
		}
	}
	if all, err := h.store.ListUsersByDiscordID(context.Background(), id); err != nil || len(all) != 0 {
		t.Fatal("sign-in created an account", len(all), err)
	}
}
func TestDiscordRejectsMissingMismatchedExpiredStateAndDenialWithoutProviderCalls(t *testing.T) {
	h := setup(t)
	calls := enableDiscord(t, h, "123456789012345678")
	state, c := startDiscord(t, h)
	for _, w := range []*httptest.ResponseRecorder{callback(h, state, nil), callback(h, strings.Repeat("a", 64), c)} {
		if w.Header().Get("Location") != "/?signin=expired" {
			t.Fatal("unbound callback accepted")
		}
	}
	h.now = h.now.Add(11 * time.Minute)
	if callback(h, state, c).Header().Get("Location") != "/?signin=expired" {
		t.Fatal("expired flow accepted")
	}
	state, c = startDiscord(t, h)
	w := h.req("GET", "/auth/discord/callback?error=access_denied&state="+state, "", c)
	if w.Header().Get("Location") != "/?signin=cancelled" || *calls != 0 {
		t.Fatal("denied or invalid flow reached provider")
	}
}
func TestDiscordRefusesAnInactiveExistingIdentityRatherThanRegisteringAgain(t *testing.T) {
	h := setup(t)
	id := "123456789012345678"
	enableDiscord(t, h, id)
	if err := h.store.LinkBotAccount(context.Background(), id, "alice", true); err != nil {
		t.Fatal(err)
	}
	u, _ := h.store.GetUser(context.Background(), "alice")
	u.Provisional = true
	// The Store contract exposes provisional accounts through normal lookups;
	// check this path through a wrapper rather than changing production state.
	h.s.opts.Store = &inactiveDiscordStore{Store: h.store, user: u}
	state, c := startDiscord(t, h)
	w := callback(h, state, c)
	if w.Header().Get("Location") != "/?signin=account" {
		t.Fatal("inactive identity was signed in")
	}
}

type inactiveDiscordStore struct {
	db.Store
	user db.User
}

func (s *inactiveDiscordStore) GetUserByDiscordID(context.Context, string) (db.User, error) {
	return s.user, nil
}
func TestDiscordProviderErrorsDoNotExposeTokensOrCreateAccounts(t *testing.T) {
	h := setup(t)
	enableDiscord(t, h, "123456789012345678")
	h.s.opts.OAuthClient = &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 401, Body: io.NopCloser(strings.NewReader(`{"error":"private provider details"}`)), Header: make(http.Header)}, nil
	})}
	state, c := startDiscord(t, h)
	w := callback(h, state, c)
	if w.Header().Get("Location") != "/?signin=failed" || strings.Contains(w.Body.String(), "private") {
		t.Fatal("provider error leaked")
	}
}

func TestDiscordSessionStopsWorkingWhenItsOwnershipLinkChanges(t *testing.T) {
	h := setup(t)
	id := "123456789012345678"
	enableDiscord(t, h, id)
	h.store.LinkBotAccount(context.Background(), id, "alice", true)
	state, c := startDiscord(t, h)
	session := sessionCookie(t, callback(h, state, c))
	u, _ := h.store.GetUser(context.Background(), "alice")
	u.DiscordUserID = "another-discord-id"
	h.s.opts.Store = &changedOwnerStore{Store: h.store, user: u}
	if h.req("GET", "/api/data", "", session).Code != 401 {
		t.Fatal("Discord session survived ownership change")
	}
}

type changedOwnerStore struct {
	db.Store
	user db.User
}

func (s *changedOwnerStore) GetUser(context.Context, string) (db.User, error) { return s.user, nil }
