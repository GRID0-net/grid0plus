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
	"testing"

	"golang.org/x/crypto/bcrypt"

	"github.com/n-popescu/switchnet/internal/db"
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
