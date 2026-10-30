package baas

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/bcrypt"

	"github.com/n-popescu/grid0plus/internal/db"
)

const testBotToken = "0123456789abcdef0123456789abcdef"

type botHarness struct {
	*harness
	bot *httptest.Server
}

func newBotHarness(t *testing.T, tweak ...func(*Config)) *botHarness {
	h := newHarness(t, tweak...)
	bot := httptest.NewServer(h.srv.BotHandler(BotAPIOptions{Token: testBotToken}))
	t.Cleanup(bot.Close)
	return &botHarness{harness: h, bot: bot}
}

// call makes a bot API request as the given Discord user ("" for none).
func (b *botHarness) call(method, p, discordID string, body any) (*http.Response, map[string]any) {
	b.t.Helper()
	var reader io.Reader
	switch v := body.(type) {
	case nil:
	case []byte:
		reader = bytes.NewReader(v)
	default:
		raw, _ := json.Marshal(v)
		reader = bytes.NewReader(raw)
	}
	req, _ := http.NewRequest(method, b.bot.URL+p, reader)
	req.Header.Set("Authorization", "Bearer "+testBotToken)
	if discordID != "" {
		req.Header.Set("X-Discord-User-ID", discordID)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		b.t.Fatal(err)
	}
	defer resp.Body.Close()
	out := map[string]any{}
	raw, _ := io.ReadAll(resp.Body)
	_ = json.Unmarshal(raw, &out)
	return resp, out
}

func (b *botHarness) registerDiscord(id, username string) map[string]any {
	b.t.Helper()
	resp, body := b.call("POST", "/bot/v1/register", "", map[string]string{
		"discordUserId": id, "username": username, "displayName": username,
	})
	if resp.StatusCode != http.StatusCreated {
		b.t.Fatalf("register %s: status %d, body %v", username, resp.StatusCode, body)
	}
	return body
}

// onConsole gives an account a device account, as signing in from a console
// does. A main account without one gives way to the next /register.
func (b *botHarness) onConsole(username string) {
	b.t.Helper()
	user, err := b.store.GetUserByLoginUsername(b.t.Context(), username)
	if err != nil {
		b.t.Fatal(err)
	}
	if err := b.store.CreateDeviceAccountForUser(b.t.Context(), db.DeviceAccount{ID: "device-account-" + username, UserID: user.ID}); err != nil {
		b.t.Fatal(err)
	}
}

const (
	aliceDiscord = "100000000000000001"
	bobDiscord   = "100000000000000002"
)

func TestTheBotAPIRefusesAMissingOrWrongToken(t *testing.T) {
	b := newBotHarness(t)
	for _, token := range []string{"", "wrong"} {
		req, _ := http.NewRequest("GET", b.bot.URL+"/bot/v1/online", nil)
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusUnauthorized {
			t.Errorf("token %q: status %d, want 401", token, resp.StatusCode)
		}
	}
}

// Registering hands the password out once, stores only its hash, and the
// credentials then work on the same login a console's Nintendo Account link
// checks.
func TestRegisterCreatesALoginThePasswordUnlocks(t *testing.T) {
	b := newBotHarness(t)
	body := b.registerDiscord(aliceDiscord, "alice")

	password, _ := body["password"].(string)
	if len(password) != 8 {
		t.Fatalf("password %q, want 8 characters", password)
	}
	if body["friendCode"] == "" || body["username"] != "alice" {
		t.Errorf("register response = %v", body)
	}
	user, err := b.store.GetUserByLoginUsername(t.Context(), "alice")
	if err != nil {
		t.Fatal(err)
	}
	if user.DiscordUserID != aliceDiscord || user.Provisional {
		t.Errorf("stored user = %+v", user)
	}
	if user.LoginPasswordHash == password ||
		bcrypt.CompareHashAndPassword([]byte(user.LoginPasswordHash), []byte(password)) != nil {
		t.Error("the stored hash does not verify the handed-out password")
	}

	resp, again := b.call("POST", "/bot/v1/register", "", map[string]string{
		"discordUserId": aliceDiscord, "username": "alice2",
	})
	if resp.StatusCode != http.StatusConflict || again["code"] != "already_registered" {
		t.Errorf("second register for one Discord account: %d %v", resp.StatusCode, again)
	}
}

func TestRegisterHonoursAllowRegistration(t *testing.T) {
	b := newBotHarness(t, func(c *Config) { c.AllowRegistration = false })
	resp, body := b.call("POST", "/bot/v1/register", "", map[string]string{
		"discordUserId": aliceDiscord, "username": "alice",
	})
	if resp.StatusCode != http.StatusForbidden || body["code"] != "registration_closed" {
		t.Errorf("register with registration off: %d %v", resp.StatusCode, body)
	}
}

// An unregistered Discord id is refused, never mapped onto somebody.
func TestAnUnknownDiscordUserIsNotRegistered(t *testing.T) {
	b := newBotHarness(t)
	b.registerDiscord(aliceDiscord, "alice")
	resp, body := b.call("GET", "/bot/v1/me", bobDiscord, nil)
	if resp.StatusCode != http.StatusNotFound || body["code"] != "not_registered" {
		t.Errorf("me for an unregistered Discord user: %d %v", resp.StatusCode, body)
	}
}

func TestResetPasswordReplacesTheOldOne(t *testing.T) {
	b := newBotHarness(t)
	old := b.registerDiscord(aliceDiscord, "alice")["password"].(string)
	resp, body := b.call("POST", "/bot/v1/reset-password", aliceDiscord, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("reset: %d %v", resp.StatusCode, body)
	}
	fresh, _ := body["password"].(string)
	user, _ := b.store.GetUserByLoginUsername(t.Context(), "alice")
	if bcrypt.CompareHashAndPassword([]byte(user.LoginPasswordHash), []byte(fresh)) != nil {
		t.Error("the new password does not verify")
	}
	if fresh != old && bcrypt.CompareHashAndPassword([]byte(user.LoginPasswordHash), []byte(old)) == nil {
		t.Error("the old password still verifies")
	}
}

func TestResetPasswordCanKeepLinkedConsolesSignedIn(t *testing.T) {
	b := newBotHarness(t)
	b.registerDiscord(aliceDiscord, "alice")
	user, err := b.store.GetUserByLoginUsername(t.Context(), "alice")
	if err != nil {
		t.Fatal(err)
	}
	da := db.DeviceAccount{ID: "device-account-alice", UserID: user.ID}
	if err := b.store.CreateDeviceAccountForUser(t.Context(), da); err != nil {
		t.Fatal(err)
	}
	resp, body := b.call("POST", "/bot/v1/reset-password", aliceDiscord,
		map[string]bool{"disconnectAllAccounts": false})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("reset without disconnect: %d %v", resp.StatusCode, body)
	}
	stored, err := b.store.GetDeviceAccount(t.Context(), da.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.TokensRevokedBefore != nil {
		t.Errorf("console tokens were revoked at %v", stored.TokensRevokedBefore)
	}
	fresh, _ := body["password"].(string)
	user, err = b.store.GetUserByLoginUsername(t.Context(), "alice")
	if err != nil || bcrypt.CompareHashAndPassword([]byte(user.LoginPasswordHash), []byte(fresh)) != nil {
		t.Errorf("new password was not stored: %v", err)
	}

	resp, body = b.call("POST", "/bot/v1/reset-password", aliceDiscord, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("default reset: %d %v", resp.StatusCode, body)
	}
	stored, err = b.store.GetDeviceAccount(t.Context(), da.ID)
	if err != nil || stored.TokensRevokedBefore == nil {
		t.Errorf("default reset did not revoke console tokens: %v", err)
	}
}

// Two bot users add each other: a request, then accepting it, makes them
// friends through the same rules the console's endpoints use.
func TestFriendRequestsBetweenBotUsers(t *testing.T) {
	b := newBotHarness(t)
	b.registerDiscord(aliceDiscord, "alice")
	bobCode := b.registerDiscord(bobDiscord, "bob")["friendCode"].(string)

	resp, sent := b.call("POST", "/bot/v1/friend-requests", aliceDiscord, map[string]string{"friendCode": bobCode})
	if resp.StatusCode != http.StatusCreated || sent["result"] != "sent" {
		t.Fatalf("send: %d %v", resp.StatusCode, sent)
	}
	if resp, self := b.call("POST", "/bot/v1/friend-requests", bobDiscord,
		map[string]string{"discordUserId": bobDiscord}); resp.StatusCode != http.StatusBadRequest {
		t.Errorf("self request: %d %v", resp.StatusCode, self)
	}

	_, pending := b.call("GET", "/bot/v1/friend-requests", bobDiscord, nil)
	incoming, _ := pending["incoming"].([]any)
	if len(incoming) != 1 {
		t.Fatalf("bob's incoming = %v", pending)
	}
	id := incoming[0].(map[string]any)["id"].(string)

	// Alice cannot accept her own request; Bob can.
	if resp, _ := b.call("POST", "/bot/v1/friend-requests/"+id+"/accept", aliceDiscord, nil); resp.StatusCode != http.StatusNotFound {
		t.Errorf("sender accepting her own request: %d", resp.StatusCode)
	}
	if resp, body := b.call("POST", "/bot/v1/friend-requests/"+id+"/accept", bobDiscord, nil); resp.StatusCode != http.StatusOK {
		t.Fatalf("accept: %d %v", resp.StatusCode, body)
	}

	_, friends := b.call("GET", "/bot/v1/friends", aliceDiscord, nil)
	list, _ := friends["friends"].([]any)
	if len(list) != 1 || list[0].(map[string]any)["username"] != "bob" {
		t.Fatalf("alice's friends = %v", friends)
	}
	bobID := list[0].(map[string]any)["userId"].(string)
	if resp, _ := b.call("DELETE", "/bot/v1/friends/"+bobID, aliceDiscord, nil); resp.StatusCode != http.StatusNoContent {
		t.Errorf("unfriend: %d", resp.StatusCode)
	}
	// A malformed code must never reach the lookup as an empty filter,
	// which would match somebody arbitrary.
	if resp, body := b.call("POST", "/bot/v1/friend-requests", aliceDiscord,
		map[string]string{"friendCode": "12"}); resp.StatusCode != http.StatusBadRequest {
		t.Errorf("malformed friend code: %d %v", resp.StatusCode, body)
	}
	if resp, _ := b.call("POST", "/bot/v1/friend-requests", aliceDiscord,
		map[string]string{"discordUserId": "100000000000000099"}); resp.StatusCode != http.StatusNotFound {
		t.Errorf("request to an unregistered Discord user: %d", resp.StatusCode)
	}
}

// Setting an avatar twice leaves one file on disk, not two.
func TestAvatarReplacesThePreviousFile(t *testing.T) {
	b := newBotHarness(t)
	b.registerDiscord(aliceDiscord, "alice")

	resp, first := b.call("PUT", "/bot/v1/avatar", aliceDiscord, jpegBytes(t, 256, 256))
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("avatar: %d %v", resp.StatusCode, first)
	}
	firstFile := filepath.Join(b.srv.cfg.ImageDir, path.Base(first["url"].(string)))
	if _, err := os.Stat(firstFile); err != nil {
		t.Fatalf("first avatar not on disk: %v", err)
	}
	if resp, _ := b.call("PUT", "/bot/v1/avatar", aliceDiscord, jpegBytes(t, 256, 256)); resp.StatusCode != http.StatusOK {
		t.Fatal("second avatar refused")
	}
	if _, err := os.Stat(firstFile); !os.IsNotExist(err) {
		t.Errorf("the replaced avatar is still on disk: %v", err)
	}
	if resp, body := b.call("PUT", "/bot/v1/avatar", aliceDiscord, []byte("not a jpeg")); resp.StatusCode != http.StatusBadRequest {
		t.Errorf("non-JPEG avatar: %d %v", resp.StatusCode, body)
	}
}

// Somebody whose presence setting is SELF never shows up in /online.
func TestOnlineHidesPeopleWhoHideTheirPresence(t *testing.T) {
	b := newBotHarness(t)
	a := b.register() // a console, seen just now, so ONLINE
	_, body := b.call("GET", "/bot/v1/online", "", nil)
	if users, _ := body["users"].([]any); len(users) != 1 {
		t.Fatalf("online = %v, want the one console", body)
	}

	u, _ := b.store.GetUser(t.Context(), a.UserID)
	perms := u.Permissions
	perms.Presence = "SELF"
	if _, err := b.store.UpdateUser(t.Context(), a.UserID, db.UserPatch{Permissions: &perms}); err != nil {
		t.Fatal(err)
	}
	_, body = b.call("GET", "/bot/v1/online", "", nil)
	if users, _ := body["users"].([]any); len(users) != 0 {
		t.Errorf("online = %v, want nobody", body)
	}
}

// An account made before the bot (the old web page, or an operator) is
// claimed with its login and password, once, by one Discord account.
func TestLinkClaimsAnExistingAccount(t *testing.T) {
	b := newBotHarness(t)
	registerWebsiteUser(t, b.harness, "doughston", "correct-horse")

	link := func(discordID, password string) (*http.Response, map[string]any) {
		return b.call("POST", "/bot/v1/link", "", map[string]string{
			"discordUserId": discordID, "username": "doughston", "password": password,
		})
	}
	if resp, body := link(aliceDiscord, "wrong-password"); resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("wrong password: %d %v", resp.StatusCode, body)
	}
	if resp, body := link(aliceDiscord, "correct-horse"); resp.StatusCode != http.StatusOK {
		t.Fatalf("link: %d %v", resp.StatusCode, body)
	}
	if resp, body := b.call("GET", "/bot/v1/me", aliceDiscord, nil); resp.StatusCode != http.StatusOK || body["username"] != "doughston" {
		t.Errorf("me after linking: %d %v", resp.StatusCode, body)
	}
	if resp, body := link(bobDiscord, "correct-horse"); resp.StatusCode != http.StatusConflict || body["code"] != "claimed" {
		t.Errorf("a second Discord account claiming it: %d %v", resp.StatusCode, body)
	}
}

// One Discord account owning several GRID0+ accounts is the whole point of the
// link table: /register with a chosen login makes another one, the first stays
// the account that commands naming no account act on, and all of them are
// reachable by their own login.
func TestOneDiscordAccountCanOwnSeveralGrid0Accounts(t *testing.T) {
	b := newBotHarness(t)
	b.registerDiscord(aliceDiscord, "alice")
	b.onConsole("alice")

	// Without a chosen login there is nothing to say "and another one".
	resp, body := b.call("POST", "/bot/v1/register", "", map[string]any{
		"discordUserId": aliceDiscord, "username": "alice.psn", "additional": false,
	})
	if resp.StatusCode != http.StatusConflict || body["code"] != "already_registered" {
		t.Errorf("a second registration that did not ask for one: %d %v", resp.StatusCode, body)
	}

	resp, body = b.call("POST", "/bot/v1/register", "", map[string]any{
		"discordUserId": aliceDiscord, "username": "alice.psn", "displayName": "Alice", "additional": true,
	})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("second account: %d %v", resp.StatusCode, body)
	}
	extraPassword, _ := body["password"].(string)
	if body["username"] != "alice.psn" || extraPassword == "" {
		t.Errorf("second account response = %v", body)
	}
	extra, err := b.store.GetUserByLoginUsername(t.Context(), "alice.psn")
	if err != nil {
		t.Fatalf("the second account was not stored: %v", err)
	}
	if extra.Provisional || extra.DiscordUserID != aliceDiscord {
		t.Errorf("stored second account = %+v", extra)
	}

	// The first account is still the one a command that names none means.
	if resp, body := b.call("GET", "/bot/v1/me", aliceDiscord, nil); resp.StatusCode != http.StatusOK || body["username"] != "alice" {
		t.Errorf("me: %d %v", resp.StatusCode, body)
	}
	resp, body = b.call("GET", "/bot/v1/accounts", aliceDiscord, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("accounts: %d %v", resp.StatusCode, body)
	}
	list, _ := body["accounts"].([]any)
	if len(list) != 2 {
		t.Fatalf("accounts = %v, want both", list)
	}
	first, _ := list[0].(map[string]any)
	second, _ := list[1].(map[string]any)
	if first["username"] != "alice" || first["primary"] != true || second["username"] != "alice.psn" || second["primary"] != false {
		t.Errorf("accounts = %v, want the main account first", list)
	}
	// A login that is already somebody else's cannot be taken by naming it.
	if resp, body := b.call("POST", "/bot/v1/register", "", map[string]any{
		"discordUserId": bobDiscord, "username": "alice.psn", "additional": true,
	}); resp.StatusCode != http.StatusConflict || body["code"] != "username_taken" {
		t.Errorf("another person taking that login: %d %v", resp.StatusCode, body)
	}
}

// The point of resolving a named login through the caller's own accounts: a
// reset naming somebody else's login is the same refusal as naming one that
// does not exist, and changes nobody's password.
func TestAResetByLoginOnlyReachesTheCallersOwnAccounts(t *testing.T) {
	b := newBotHarness(t)
	b.registerDiscord(aliceDiscord, "alice")
	if resp, body := b.call("POST", "/bot/v1/register", "", map[string]any{
		"discordUserId": aliceDiscord, "username": "alice.psn", "additional": true,
	}); resp.StatusCode != http.StatusCreated {
		t.Fatalf("second account: %d %v", resp.StatusCode, body)
	}
	bobPassword := b.registerDiscord(bobDiscord, "bob")["password"].(string)

	// Each of the caller's own accounts, by login.
	for _, login := range []string{"alice", "alice.psn", "ALICE.PSN"} {
		resp, body := b.call("POST", "/bot/v1/reset-password", aliceDiscord, map[string]string{"username": login})
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("reset %s: %d %v", login, resp.StatusCode, body)
		}
		fresh, _ := body["password"].(string)
		if fresh == "" {
			t.Errorf("reset %s handed out no password", login)
		}
		user, err := b.store.GetUserByLoginUsername(t.Context(), login)
		if err != nil || bcrypt.CompareHashAndPassword([]byte(user.LoginPasswordHash), []byte(fresh)) != nil {
			t.Errorf("reset %s did not store the password it handed out: %v", login, err)
		}
		if userID, _ := body["userId"].(string); userID != user.ID {
			t.Errorf("reset %s answered for user %q", login, userID)
		}
	}

	// Somebody else's account, and one that does not exist, are one refusal.
	for _, login := range []string{"bob", "nobody"} {
		resp, body := b.call("POST", "/bot/v1/reset-password", aliceDiscord, map[string]string{"username": login})
		if resp.StatusCode != http.StatusNotFound {
			t.Errorf("reset %q: status %d, %v", login, resp.StatusCode, body)
		}
		if body["code"] != "not_your_account" {
			t.Errorf("reset %q: code %v, want not_your_account", login, body["code"])
		}
	}
	bob, err := b.store.GetUserByLoginUsername(t.Context(), "bob")
	if err != nil {
		t.Fatal(err)
	}
	if bcrypt.CompareHashAndPassword([]byte(bob.LoginPasswordHash), []byte(bobPassword)) != nil {
		t.Error("another person's password changed")
	}
}

// A reset that names no account means the main account, even once the person
// owns more than one: "the one I registered first" has to stay a fixed answer.
func TestABareResetPasswordMeansTheMainAccount(t *testing.T) {
	b := newBotHarness(t)
	b.registerDiscord(aliceDiscord, "alice")
	b.onConsole("alice")
	if resp, body := b.call("POST", "/bot/v1/register", "", map[string]any{
		"discordUserId": aliceDiscord, "username": "alice.psn", "additional": true,
	}); resp.StatusCode != http.StatusCreated {
		t.Fatalf("second account: %d %v", resp.StatusCode, body)
	}
	extraBefore, err := b.store.GetUserByLoginUsername(t.Context(), "alice.psn")
	if err != nil {
		t.Fatal(err)
	}

	resp, body := b.call("POST", "/bot/v1/reset-password", aliceDiscord, nil)
	if resp.StatusCode != http.StatusOK || body["username"] != "alice" {
		t.Fatalf("bare reset: %d %v", resp.StatusCode, body)
	}
	extraAfter, err := b.store.GetUserByLoginUsername(t.Context(), "alice.psn")
	if err != nil {
		t.Fatal(err)
	}
	if extraAfter.LoginPasswordHash != extraBefore.LoginPasswordHash {
		t.Error("a bare reset changed an account other than the main one")
	}
}

// /link can add a second account to a Discord account that already has one,
// and cannot take an account another Discord account owns.
func TestLinkCanAddAnAccountBesideTheFirst(t *testing.T) {
	b := newBotHarness(t)
	registerWebsiteUser(t, b.harness, "doughston", "correct-horse")
	registerWebsiteUser(t, b.harness, "relic", "another-horse")

	link := func(discordID, login, password string) (*http.Response, map[string]any) {
		return b.call("POST", "/bot/v1/link", "", map[string]string{
			"discordUserId": discordID, "username": login, "password": password,
		})
	}
	if resp, body := link(aliceDiscord, "doughston", "correct-horse"); resp.StatusCode != http.StatusOK {
		t.Fatalf("link the first account: %d %v", resp.StatusCode, body)
	}
	if resp, body := link(aliceDiscord, "relic", "another-horse"); resp.StatusCode != http.StatusOK {
		t.Fatalf("link a second account: %d %v", resp.StatusCode, body)
	}
	// Linking the same account again is what running /link twice looks like.
	if resp, body := link(aliceDiscord, "relic", "another-horse"); resp.StatusCode != http.StatusOK {
		t.Errorf("re-linking: %d %v", resp.StatusCode, body)
	}
	if resp, body := link(bobDiscord, "relic", "another-horse"); resp.StatusCode != http.StatusConflict || body["code"] != "claimed" {
		t.Errorf("another Discord account taking it: %d %v", resp.StatusCode, body)
	}

	// The first one linked is still the main account, and both are listed.
	if resp, body := b.call("GET", "/bot/v1/me", aliceDiscord, nil); resp.StatusCode != http.StatusOK || body["username"] != "doughston" {
		t.Errorf("me: %d %v", resp.StatusCode, body)
	}
	resp, body := b.call("GET", "/bot/v1/accounts", aliceDiscord, nil)
	list, _ := body["accounts"].([]any)
	if resp.StatusCode != http.StatusOK || len(list) != 2 {
		t.Fatalf("accounts = %d %v", resp.StatusCode, list)
	}
	first, _ := list[0].(map[string]any)
	if first["username"] != "doughston" || first["primary"] != true {
		t.Errorf("accounts = %v, want doughston first", list)
	}
	// And the second one is reachable for a reset by its own login.
	if resp, body := b.call("POST", "/bot/v1/reset-password", aliceDiscord,
		map[string]string{"username": "relic"}); resp.StatusCode != http.StatusOK || body["username"] != "relic" {
		t.Errorf("reset the linked account: %d %v", resp.StatusCode, body)
	}
}

func TestBotPresenceCountIncludesIdleAndPlayingButExpiresAndRespectsPrivacy(t *testing.T) {
	b := newBotHarness(t)
	idle := b.newAccount()
	playing := b.newAccount()
	requireStatus(t, b.reportPresence(playing, db.StatePlaying, splatoon3TitleID, ""), 200)
	check := func(online, games int) {
		t.Helper()
		resp, body := b.call("GET", "/bot/v1/presence-count", "", nil)
		if resp.StatusCode != 200 || body["online"] != float64(online) || body["playing"] != float64(games) || body["total"] != float64(online+games) {
			t.Fatalf("count = %v, status %d", body, resp.StatusCode)
		}
		if len(body) != 3 {
			t.Fatalf("count endpoint exposed additional fields: %v", body)
		}
	}
	check(1, 1)
	u, _ := b.store.GetUser(t.Context(), idle.UserID)
	permissions := u.Permissions
	permissions.Presence = "SELF"
	if _, err := b.store.UpdateUser(t.Context(), idle.UserID, db.UserPatch{Permissions: &permissions}); err != nil {
		t.Fatal(err)
	}
	check(0, 1)
	b.advance(testPresenceTTL + testConsoleAliveTTL + time.Minute)
	check(0, 0)
}

// A Discord display name is free text -- decorative Unicode, emoji -- that a
// console font may not render; the username never is. grid0 shipped
// "☾ 𝓐𝓼𝓱𝓲𝓮 ☽" as a Switch nickname before this.
func TestANewAccountIsNamedAfterTheDiscordUsernameNotTheDisplayName(t *testing.T) {
	b := newBotHarness(t)
	resp, body := b.call("POST", "/bot/v1/register", "", map[string]string{
		"discordUserId": aliceDiscord, "username": "alice_the_inkling", "displayName": "☾ 𝓐𝓵𝓲𝓬𝓮 ☽",
	})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("register: status %d, body %v", resp.StatusCode, body)
	}
	u, err := b.store.GetUser(t.Context(), body["userId"].(string))
	if err != nil {
		t.Fatal(err)
	}
	if u.Nickname != "alice_the_" {
		t.Errorf("nickname = %q, want the username cut to 10 characters, %q", u.Nickname, "alice_the_")
	}
}

// A main account no console ever signed in to is not the one the person
// plays on: TST's web-created main stayed main while his console used the
// account he registered next, so the bot acted on the wrong identity.
func TestAMainAccountNoConsoleUsedGivesWayToTheNextRegistration(t *testing.T) {
	b := newBotHarness(t)
	b.registerDiscord(aliceDiscord, "alice")
	if resp, body := b.call("POST", "/bot/v1/register", "", map[string]any{
		"discordUserId": aliceDiscord, "username": "alice.psn", "additional": true,
	}); resp.StatusCode != http.StatusCreated {
		t.Fatalf("second account: %d %v", resp.StatusCode, body)
	}
	if resp, body := b.call("GET", "/bot/v1/me", aliceDiscord, nil); resp.StatusCode != http.StatusOK || body["username"] != "alice.psn" {
		t.Errorf("me = %d %v, want the new account as main", resp.StatusCode, body)
	}
	resp, body := b.call("GET", "/bot/v1/accounts", aliceDiscord, nil)
	list, _ := body["accounts"].([]any)
	if resp.StatusCode != http.StatusOK || len(list) != 2 {
		t.Fatalf("accounts: %d %v", resp.StatusCode, body)
	}
	if old, _ := list[1].(map[string]any); old["username"] != "alice" || old["primary"] != false {
		t.Errorf("accounts = %v, want the old main demoted, not removed", list)
	}

	// Once the new main is on a console, a third account does not take over.
	b.onConsole("alice.psn")
	if resp, body := b.call("POST", "/bot/v1/register", "", map[string]any{
		"discordUserId": aliceDiscord, "username": "alice.third", "additional": true,
	}); resp.StatusCode != http.StatusCreated {
		t.Fatalf("third account: %d %v", resp.StatusCode, body)
	}
	if resp, body := b.call("GET", "/bot/v1/me", aliceDiscord, nil); resp.StatusCode != http.StatusOK || body["username"] != "alice.psn" {
		t.Errorf("me = %d %v, want the played account to stay main", resp.StatusCode, body)
	}
}

func TestSetPasswordStoresTheChosenOne(t *testing.T) {
	b := newBotHarness(t)
	old := b.registerDiscord(aliceDiscord, "alice")["password"].(string)
	resp, body := b.call("POST", "/bot/v1/set-password", aliceDiscord, map[string]string{"password": "my own pass 42"})
	if resp.StatusCode != http.StatusOK || body["username"] != "alice" {
		t.Fatalf("set: %d %v", resp.StatusCode, body)
	}
	user, _ := b.store.GetUserByLoginUsername(t.Context(), "alice")
	if bcrypt.CompareHashAndPassword([]byte(user.LoginPasswordHash), []byte("my own pass 42")) != nil {
		t.Error("the chosen password does not verify")
	}
	if bcrypt.CompareHashAndPassword([]byte(user.LoginPasswordHash), []byte(old)) == nil {
		t.Error("the generated password still verifies")
	}
}

// Only after /register, only the caller's own accounts, only a usable password.
func TestSetPasswordRefusals(t *testing.T) {
	b := newBotHarness(t)
	if resp, body := b.call("POST", "/bot/v1/set-password", aliceDiscord, map[string]string{"password": "longenough"}); resp.StatusCode != http.StatusNotFound || body["code"] != "not_registered" {
		t.Errorf("before registering: %d %v", resp.StatusCode, body)
	}
	b.registerDiscord(aliceDiscord, "alice")
	b.registerDiscord(bobDiscord, "bob")
	if resp, body := b.call("POST", "/bot/v1/set-password", aliceDiscord, map[string]string{"username": "bob", "password": "longenough"}); resp.StatusCode != http.StatusNotFound || body["code"] != "not_your_account" {
		t.Errorf("someone else's account: %d %v", resp.StatusCode, body)
	}
	for _, p := range []string{"short", strings.Repeat("x", 65), "accentué1"} {
		if resp, body := b.call("POST", "/bot/v1/set-password", aliceDiscord, map[string]string{"password": p}); resp.StatusCode != http.StatusBadRequest || body["code"] != "bad_password" {
			t.Errorf("password %q: %d %v", p, resp.StatusCode, body)
		}
	}
}
