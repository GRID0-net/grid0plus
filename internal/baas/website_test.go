package baas

import (
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/n-popescu/switchnet/internal/db"
)

func TestRegisterPageRenders(t *testing.T) {
	h := newHarness(t)
	resp, err := http.Get(h.server.URL + "/register")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	requireStatus(t, resp, http.StatusOK)
	body, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(body), `name="username"`) {
		t.Error("page has no username field")
	}
}

// TestWebsiteRegistrationRespectsAllowRegistration covers the gap fixed
// alongside this test: registerPage/registerSubmit did not check
// AllowRegistration at all, while the console-facing POST /1.0.0/users
// (registerUser, TestRegistrationCanBeDisabled) did. An operator who turned
// registration off "so that somebody who finds the server cannot add
// themselves to it" (AllowRegistration's own doc comment) was still exposing
// a fully working, unauthenticated account-creation form on this path.
func TestWebsiteRegistrationRespectsAllowRegistration(t *testing.T) {
	h := newHarness(t, func(c *Config) { c.AllowRegistration = false })

	page, err := http.Get(h.server.URL + "/register")
	if err != nil {
		t.Fatal(err)
	}
	defer page.Body.Close()
	requireStatus(t, page, http.StatusForbidden)
	body, _ := io.ReadAll(page.Body)
	if strings.Contains(string(body), `name="username"`) {
		t.Error("registration page still renders the form while registration is disabled")
	}

	resp, err := http.PostForm(h.server.URL+"/register", url.Values{
		"username": {"eve"},
		"password": {"hunter2222"},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	requireStatus(t, resp, http.StatusForbidden)

	if _, err := h.store.GetUserByLoginUsername(t.Context(), "eve"); !errors.Is(err, db.ErrNotFound) {
		t.Errorf("GetUserByLoginUsername(%q) = %v, want db.ErrNotFound -- no account should have been created", "eve", err)
	}
}

func TestRegisterCreatesAPersonWithNoDeviceAccount(t *testing.T) {
	h := newHarness(t)
	resp, err := http.PostForm(h.server.URL+"/register", url.Values{
		"username": {"alice"},
		"password": {"hunter2222"},
		"nickname": {"Alice"},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	requireStatus(t, resp, http.StatusOK)

	u, err := h.store.GetUserByLoginUsername(t.Context(), "alice")
	if err != nil {
		t.Fatalf("registered user was not found: %v", err)
	}
	if u.Nickname != "Alice" {
		t.Errorf("nickname = %q, want Alice", u.Nickname)
	}
	if u.LoginPasswordHash == "" || u.LoginPasswordHash == "hunter2222" {
		t.Error("password was not hashed")
	}

	das, err := h.store.ListDeviceAccounts(t.Context(), u.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(das) != 0 {
		t.Errorf("a website-registered person has %d device accounts, want 0", len(das))
	}
}

func TestRegisterDuplicateUsernameIsRefused(t *testing.T) {
	h := newHarness(t)
	post := func() *http.Response {
		resp, err := http.PostForm(h.server.URL+"/register", url.Values{
			"username": {"alice"},
			"password": {"hunter2222"},
		})
		if err != nil {
			t.Fatal(err)
		}
		return resp
	}
	first := post()
	first.Body.Close()
	requireStatus(t, first, http.StatusOK)

	second := post()
	defer second.Body.Close()
	if second.StatusCode == http.StatusOK {
		t.Error("a duplicate username was accepted")
	}
	body, _ := io.ReadAll(second.Body)
	if !strings.Contains(string(body), "already taken") {
		t.Errorf("body = %s, want a message about the username being taken", body)
	}
}

func TestRegisterRejectsAShortPassword(t *testing.T) {
	h := newHarness(t)
	resp, err := http.PostForm(h.server.URL+"/register", url.Values{
		"username": {"alice"},
		"password": {"short"},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusOK {
		t.Error("a short password was accepted")
	}
	if _, err := h.store.GetUserByLoginUsername(t.Context(), "alice"); err == nil {
		t.Error("a user was created despite the short password")
	}
}

func TestRegisterWithoutNicknameUsesUsername(t *testing.T) {
	h := newHarness(t)
	resp, err := http.PostForm(h.server.URL+"/register", url.Values{
		"username": {"bob"},
		"password": {"hunter2222"},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	requireStatus(t, resp, http.StatusOK)

	u, err := h.store.GetUserByLoginUsername(t.Context(), "bob")
	if err != nil {
		t.Fatal(err)
	}
	if u.Nickname != "bob" {
		t.Errorf("nickname = %q, want bob (falls back to username)", u.Nickname)
	}
}
