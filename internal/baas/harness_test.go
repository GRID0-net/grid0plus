package baas

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/n-popescu/switchnet/internal/db"
	sjwt "github.com/n-popescu/switchnet/internal/jwt"
	"github.com/n-popescu/switchnet/internal/presencebus"
)

const (
	testAudience         = "ed9e2f05d286f7b8"
	testIssuer           = "https://e0d67c509fb203858ebcb2fe3f88c2aa.baas.nintendo.com"
	splatoon3TitleID     = "0100c2500fc20000"
	marioKart8DTitleID   = "0100152000022000"
	testPresenceTTL      = 5 * time.Minute
	testConsoleAliveTTL  = 5 * time.Minute
	testFriendCodeCooldn = 24 * time.Hour
)

// harness is a BAAS server wired up over httptest, with an in-memory store and a
// clock the test controls.
type harness struct {
	t *testing.T

	server *httptest.Server
	srv    *Server
	store  *db.Memory
	bus    *recordingBus

	deviceIssuer *sjwt.Issuer
	appIssuer    *sjwt.Issuer
	// accessIssuer lets a test mint a correctly-signed but internally
	// inconsistent user token, to prove the server refuses one.
	accessIssuer *sjwt.Issuer

	mu  sync.Mutex
	now time.Time
}

// recordingBus captures published presence events so a test can assert that a
// presence change was fanned out, not just stored.
type recordingBus struct {
	mu     sync.Mutex
	events []presencebus.Event
}

func (b *recordingBus) Publish(_ context.Context, e presencebus.Event) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.events = append(b.events, e)
	return nil
}

func (b *recordingBus) Subscribe(context.Context) (<-chan presencebus.Event, error) {
	ch := make(chan presencebus.Event)
	close(ch)
	return ch, nil
}

// The gauge half of the Bus, which internal/baas does not use: presence is an
// event here, and the matchmaking snapshot belongs to internal/npln.
func (b *recordingBus) PublishStats(context.Context, presencebus.Stats) error { return nil }

func (b *recordingBus) Stats(context.Context) (presencebus.Stats, bool, error) {
	return presencebus.Stats{}, false, nil
}

func (b *recordingBus) Close() error { return nil }

func (b *recordingBus) all() []presencebus.Event {
	b.mu.Lock()
	defer b.mu.Unlock()
	return append([]presencebus.Event(nil), b.events...)
}

func newHarness(t *testing.T, tweak ...func(*Config)) *harness {
	t.Helper()

	ring := sjwt.NewKeyring(t.TempDir(), true)
	key := func(name string) sjwt.Key {
		t.Helper()
		k, err := ring.Key(name)
		if err != nil {
			t.Fatalf("key %s: %v", name, err)
		}
		return k
	}
	accessKey := key(sjwt.IssuerBAASAccess)
	idKey := key(sjwt.IssuerBAASID)
	naIDKey := key(sjwt.IssuerNAID)
	deviceKey := key(sjwt.IssuerDevice)
	appKey := key(sjwt.IssuerApp)

	cfg := Config{
		Issuer:             testIssuer,
		Audience:           testAudience,
		AccessTokenJWKSURL: testIssuer + "/1.0.0/internal_certificates",
		IDTokenJWKSURL:     testIssuer + "/1.0.0/certificates",
		AccessTokenTTL:     3 * time.Hour,
		IDTokenTTL:         3 * time.Hour,
		FriendCodeCooldown: testFriendCodeCooldn,
		PresenceTTL:        testPresenceTTL,
		ConsolePresenceTTL: testConsoleAliveTTL,
		ImageDir:           t.TempDir(),
		ImageBaseURL:       "https://cdn-image.example.invalid",
		MaxImageBytes:      1 << 20,
		AllowRegistration:  true,
	}
	for _, f := range tweak {
		f(&cfg)
	}

	h := &harness{
		t:     t,
		store: db.NewMemory(),
		bus:   &recordingBus{},
		// Anchored to real time, not a fixed instant: JWT expiry is verified
		// against the real wall clock (see the note on the expiry tests below),
		// so a frozen date drifts out of every token's validity window after a
		// few hours and starts failing the entire suite for a reason that has
		// nothing to do with what broke.
		now: time.Now().UTC(),
		deviceIssuer: sjwt.NewIssuer(deviceKey,
			"https://dcert-lp1.ndas.srv.nintendo.net/keys", true),
		appIssuer: sjwt.NewIssuer(appKey,
			"https://acert-lp1.ndas.srv.nintendo.net/keys", true),
		// includeTyp true everywhere, matching cmd/baas -- a harness that signs
		// differently from production is a test that cannot see a header bug.
		accessIssuer: sjwt.NewIssuer(accessKey, testIssuer+"/1.0.0/internal_certificates", true),
	}

	h.srv = New(cfg, Deps{
		Store:          h.store,
		Bus:            h.bus,
		Log:            slog.New(slog.DiscardHandler),
		AccessIssuer:   sjwt.NewIssuer(accessKey, cfg.AccessTokenJWKSURL, true),
		IDIssuer:       sjwt.NewIssuer(idKey, cfg.IDTokenJWKSURL, true),
		NAIDIssuer:     sjwt.NewIssuer(naIDKey, NAIDTokenJWKSURL, true),
		AccessVerifier: sjwt.NewVerifier(accessKey),
		IDVerifier:     sjwt.NewVerifier(naIDKey),
		DeviceVerifier: sjwt.NewVerifier(deviceKey),
		AppVerifier:    sjwt.NewVerifier(appKey),
	})
	h.srv.SetClock(h.clock)

	h.server = httptest.NewServer(h.srv.Handler())
	t.Cleanup(h.server.Close)
	return h
}

func (h *harness) clock() time.Time {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.now
}

// advance moves the test clock forward, so presence TTL behaviour can be tested
// without sleeping.
func (h *harness) advance(d time.Duration) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.now = h.now.Add(d)
}

// ---- requests ------------------------------------------------------------

type request struct {
	method  string
	path    string
	token   string
	form    url.Values
	json    string
	patch   string
	headers map[string]string
}

func (h *harness) do(r request) *http.Response {
	h.t.Helper()

	var body io.Reader
	contentType := ""
	switch {
	case r.form != nil:
		body = strings.NewReader(r.form.Encode())
		contentType = "application/x-www-form-urlencoded"
	case r.json != "":
		body = strings.NewReader(r.json)
		contentType = "application/json"
	case r.patch != "":
		body = strings.NewReader(r.patch)
		contentType = "application/json-patch+json"
	case r.method != "GET":
		body = strings.NewReader("")
		contentType = "application/x-www-form-urlencoded"
	}

	req, err := http.NewRequest(r.method, h.server.URL+r.path, body)
	if err != nil {
		h.t.Fatal(err)
	}
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	if r.token != "" {
		req.Header.Set("Authorization", "Bearer "+r.token)
	}
	req.Header.Set("User-Agent",
		"libcurl (nnFriends; 789f928b-138e-4b2f-afeb-1acae821d897; SDK 17.5.0.0; Add-on 17.5.0.0)")
	for k, v := range r.headers {
		req.Header.Set(k, v)
	}

	resp, err := h.server.Client().Do(req)
	if err != nil {
		h.t.Fatal(err)
	}
	h.t.Cleanup(func() { _ = resp.Body.Close() })
	return resp
}

func decode(t *testing.T, resp *http.Response, dst any) {
	t.Helper()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(raw, dst); err != nil {
		t.Fatalf("response is not JSON (status %d, body %s): %v", resp.StatusCode, raw, err)
	}
}

func requireStatus(t *testing.T, resp *http.Response, want int) {
	t.Helper()
	if resp.StatusCode == want {
		return
	}
	raw, _ := io.ReadAll(resp.Body)
	t.Fatalf("%s %s: status = %d, want %d (body %s)",
		resp.Request.Method, resp.Request.URL.Path, resp.StatusCode, want, raw)
}

// requireError asserts a BAAS-shaped error with a specific errorCode.
//
// The console reads errorCode rather than the HTTP status, so the code is what
// has to be right.
func requireError(t *testing.T, resp *http.Response, status int, code string) {
	t.Helper()
	if resp.StatusCode != status {
		raw, _ := io.ReadAll(resp.Body)
		t.Fatalf("status = %d, want %d (body %s)", resp.StatusCode, status, raw)
	}
	var e APIError
	decode(t, resp, &e)
	if e.ErrorCode != code {
		t.Errorf("errorCode = %q, want %q", e.ErrorCode, code)
	}
	if e.Instance == "" {
		t.Error("the error carries no 'instance' (the failing request path)")
	}
	if !strings.HasPrefix(e.Type, "https://baas.nintendo.com/errors/") {
		t.Errorf("type = %q, want a baas.nintendo.com error URI", e.Type)
	}
}

// ---- the auth chain ------------------------------------------------------

// deviceToken mints the device token SwitchNet's dauth server would issue.
func (h *harness) deviceToken() string {
	h.t.Helper()
	now := h.clock()
	token, err := h.deviceIssuer.Sign(sjwt.DeviceTokenClaims{
		Subject:  "68337aca28815cbb",
		Audience: "8f849b5d34778d8e",
		IssuedAt: now.Unix(),
		Expires:  now.Add(24 * time.Hour).Unix(),
		Issuer:   "dauth-lp1.ndas.srv.nintendo.net",
		TokenID:  sjwt.NewTokenID(),
		Nintendo: sjwt.DeviceInfo{
			SerialNumber: "XAW10012345678", ProductCode: "HAC",
			DeviceType: "NX Prod 1", IsT: false,
		},
	})
	if err != nil {
		h.t.Fatal(err)
	}
	return token
}

// expiredDeviceToken mints a device token that has already expired against the
// wall clock. JWT expiry is validated by the JWT library against real time, not
// against the harness clock, so a test for expiry has to use real timestamps.
func (h *harness) expiredDeviceToken() string {
	h.t.Helper()
	real := time.Now()
	token, err := h.deviceIssuer.Sign(sjwt.DeviceTokenClaims{
		Subject:  "68337aca28815cbb",
		Audience: "8f849b5d34778d8e",
		IssuedAt: real.Add(-48 * time.Hour).Unix(),
		Expires:  real.Add(-24 * time.Hour).Unix(),
		Issuer:   "dauth-lp1.ndas.srv.nintendo.net",
		TokenID:  sjwt.NewTokenID(),
	})
	if err != nil {
		h.t.Fatal(err)
	}
	return token
}

// appToken mints the application token SwitchNet's aauth server would issue.
func (h *harness) appToken(titleID, policy, handler string) string {
	h.t.Helper()
	now := h.clock()
	info := sjwt.AppInfo{
		AppID: titleID, AppVersion: "0007", AppTime: now.Unix(),
		EDI: "b46bda4e1dd5e7ce002430a68b2c6d4e", OnlinePlayPolicy: policy,
	}
	if policy == "MEMBERSHIP_REQUIRED" {
		info.PolicyHandler = handler
	}
	token, err := h.appIssuer.Sign(sjwt.AppTokenClaims{
		Subject:  titleID,
		IssuedAt: now.Unix(),
		Expires:  now.Add(24 * time.Hour).Unix(),
		Issuer:   "aauth-lp1.ndas.srv.nintendo.net",
		TokenID:  sjwt.NewTokenID(),
		Nintendo: info,
	})
	if err != nil {
		h.t.Fatal(err)
	}
	return token
}

// anonymousToken performs POST /1.0.0/application/token.
func (h *harness) anonymousToken() string {
	h.t.Helper()
	resp := h.do(request{
		method: "POST", path: "/1.0.0/application/token",
		form: url.Values{
			"grantType": {"public_client"},
			"assertion": {h.deviceToken()},
		},
	})
	requireStatus(h.t, resp, 200)

	var body tokenResponse
	decode(h.t, resp, &body)
	if body.TokenType != "Bearer" {
		h.t.Fatalf("tokenType = %q, want Bearer", body.TokenType)
	}
	return body.AccessToken
}

// account is a registered user plus the credentials and tokens for it.
type account struct {
	UserID          string
	DeviceAccountID string
	Password        string
	AccessToken     string
	IDToken         string
}

// register performs POST /1.0.0/users.
func (h *harness) register() account {
	h.t.Helper()
	resp := h.do(request{
		method: "POST", path: "/1.0.0/users",
		token: h.anonymousToken(),
	})
	requireStatus(h.t, resp, 201)

	var body UserResponse
	decode(h.t, resp, &body)

	if len(body.DeviceAccounts) != 1 {
		h.t.Fatalf("registration returned %d device accounts, want 1", len(body.DeviceAccounts))
	}
	da := body.DeviceAccounts[0]
	if da.Password == "" {
		h.t.Fatal("registration did not return the device account password; " +
			"it is sent exactly once and the account is unreachable without it")
	}
	return account{UserID: body.ID, DeviceAccountID: da.ID, Password: da.Password}
}

// login performs POST /1.0.0/login and fills in the account's tokens.
func (h *harness) login(a *account, appToken string) loginResponse {
	h.t.Helper()

	form := url.Values{
		"id":           {a.DeviceAccountID},
		"password":     {a.Password},
		"naCountry":    {"NL"},
		"isPersistent": {"true"},
	}
	if appToken != "" {
		form.Set("appAuthNToken", appToken)
	}

	resp := h.do(request{
		method: "POST", path: "/1.0.0/login",
		token: h.anonymousToken(),
		form:  form,
	})
	requireStatus(h.t, resp, 200)

	var body loginResponse
	decode(h.t, resp, &body)

	a.AccessToken = body.AccessToken
	a.IDToken = body.IDToken
	return body
}

// newAccount registers, logs in, and returns a ready-to-use account.
func (h *harness) newAccount() account {
	h.t.Helper()
	a := h.register()
	h.login(&a, "")
	return a
}

// setNickname is what the console does after account creation.
func (h *harness) setNickname(a account, nickname string) {
	h.t.Helper()
	resp := h.do(request{
		method: "PATCH", path: "/1.0.0/users/" + a.UserID,
		token: a.AccessToken,
		patch: `[{"op":"replace","path":"/nickname","value":"` + nickname + `"}]`,
	})
	requireStatus(h.t, resp, 200)
}

// generateFriendCode performs POST /1.0.0/users/<id>/generate_code.
func (h *harness) generateFriendCode(a account) string {
	h.t.Helper()
	resp := h.do(request{
		method: "POST", path: "/1.0.0/users/" + a.UserID + "/generate_code",
		token: a.AccessToken,
		form:  url.Values{"type": {"NX"}},
	})
	requireStatus(h.t, resp, 200)

	var body UserResponse
	decode(h.t, resp, &body)
	if body.Links.FriendCode == nil {
		h.t.Fatal("no friend code in the response")
	}
	return body.Links.FriendCode.ID
}

// reportPresence is the JSON patch the friends sysmodule sends when a game
// starts or the console returns to the Home Menu.
//
// Shape taken from kinnay/NintendoClients, nintendo/switch/baas.py,
// BAASClient.update_presence.
func (h *harness) reportPresence(a account, state, titleID, appField string) *http.Response {
	h.t.Helper()

	ops := []string{
		`{"op":"replace","path":"/presence/state","value":"` + state + `"}`,
	}
	if titleID != "" {
		ops = append(ops,
			`{"op":"add","path":"/presence/extras/friends/appField","value":`+
				jsonString(appField)+`}`,
			`{"op":"add","path":"/presence/extras/friends/appInfo:appId","value":"`+titleID+`"}`,
			`{"op":"add","path":"/presence/extras/friends/appInfo:acdIndex","value":0}`,
			`{"op":"add","path":"/presence/extras/friends/appInfo:presenceGroupId","value":"`+
				titleID+`"}`,
		)
	}

	return h.do(request{
		method: "PATCH",
		path:   "/1.0.0/users/" + a.UserID + "/device_accounts/" + a.DeviceAccountID,
		token:  a.AccessToken,
		patch:  "[" + strings.Join(ops, ",") + "]",
	})
}

func jsonString(s string) string {
	buf, err := json.Marshal(s)
	if err != nil {
		panic(err)
	}
	return string(buf)
}

// friends performs GET /2.0.0/users/<id>/friends.
func (h *harness) friends(a account) []FriendResponse {
	h.t.Helper()
	resp := h.do(request{
		method: "GET", path: "/2.0.0/users/" + a.UserID + "/friends?count=300",
		token: a.AccessToken,
	})
	requireStatus(h.t, resp, 200)

	var env CollectionEnvelope[FriendResponse]
	decode(h.t, resp, &env)
	return env.Items
}

// befriend takes two accounts from strangers to friends through the real
// request/accept flow, and returns nothing: the friendship is the assertion.
func (h *harness) befriend(a, b account) {
	h.t.Helper()

	code := h.generateFriendCode(b)

	resp := h.do(request{
		method: "POST", path: "/2.0.0/friend_requests",
		token: a.AccessToken,
		json:  `{"friendCode":"` + code + `"}`,
	})
	requireStatus(h.t, resp, 201)

	var sent FriendRequestResponse
	decode(h.t, resp, &sent)

	resp = h.do(request{
		method: "PATCH", path: "/2.0.0/friend_requests/" + sent.ID,
		token: b.AccessToken,
		patch: `[{"op":"replace","path":"/state","value":"ACCEPTED"}]`,
	})
	requireStatus(h.t, resp, 200)
}
