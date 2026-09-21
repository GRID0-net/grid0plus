package baas

import (
	"errors"
	"fmt"
	"html"
	"net/http"
	"strings"

	"github.com/n-popescu/switchnet/internal/db"
	"golang.org/x/crypto/bcrypt"
)

// Self-service registration for a person, independent of any console.
//
// This is what a real friend system needs and a shared passphrase cannot
// give: one persistent SwitchNet identity per person, that they can later
// link onto any local user profile through the Nintendo Account link stub
// (nalink.go) — not a fresh anonymous identity minted every time a profile
// links. See internal/db/store.go's LoginUsername/LoginPasswordHash and
// CreateUserWithLogin.
//
// Served directly on the BAAS listen address, not behind a
// Nintendo-mimicking hostname: this is for a person's own browser, not a
// console. A nicer hostname or TLS front for it is a separate concern for
// later, not a blocker here.

// registerPage renders the registration form.
func (s *Server) registerPage(w http.ResponseWriter, r *http.Request) {
	if !s.cfg.AllowRegistration {
		writeHTML(w, http.StatusForbidden, registrationClosedHTML())
		return
	}
	writeHTML(w, http.StatusOK, registerPageHTML(""))
}

// registerSubmit creates a person's SwitchNet account.
//
// Gated on AllowRegistration for the same reason POST /1.0.0/users is: an
// operator turns it off once every console in the group is registered, "so
// that somebody who finds the server cannot add themselves to it" (see that
// field's own doc comment in router.go). Before this check existed, that lock
// only covered the console-facing endpoint -- this page, reachable by anyone
// who could reach the BAAS port, could still create a full login-credentialed
// person account regardless of the setting, and internal/baas/nalink.go's
// Nintendo Account link flow exists specifically to attach an account created
// here onto a console.
func (s *Server) registerSubmit(w http.ResponseWriter, r *http.Request) {
	if !s.cfg.AllowRegistration {
		s.log.Warn("baas: refused website registration, registration is disabled")
		writeHTML(w, http.StatusForbidden, registrationClosedHTML())
		return
	}

	form, err := parseForm(r)
	if err != nil {
		writeHTML(w, http.StatusBadRequest, registerPageHTML("Could not read that form submission."))
		return
	}

	username := strings.TrimSpace(form.Get("username"))
	password := form.Get("password")
	nickname := strings.TrimSpace(form.Get("nickname"))
	if nickname == "" {
		nickname = username
	}

	if problem := validateRegistration(username, password); problem != "" {
		writeHTML(w, http.StatusBadRequest, registerPageHTML(problem))
		return
	}

	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		writeInternalError(s.log, w, r, "hash registration password", err)
		return
	}

	userID, err := newHexID(8)
	if err != nil {
		writeInternalError(s.log, w, r, "generate user id", err)
		return
	}

	now := s.now()
	user := db.User{
		ID:                userID,
		Nickname:          nickname,
		Birthday:          "0000-00-00",
		Permissions:       db.DefaultPermissions(now),
		Extras:            db.EmptyExtras(),
		BlocksUpdatedAt:   now,
		FriendsUpdatedAt:  now,
		CreatedAt:         now,
		UpdatedAt:         now,
		LoginUsername:     username,
		LoginPasswordHash: string(hash),
	}

	if err := s.store.CreateUserWithLogin(r.Context(), user); err != nil {
		if errors.Is(err, db.ErrConflict) {
			writeHTML(w, http.StatusConflict, registerPageHTML("That username is already taken."))
			return
		}
		writeInternalError(s.log, w, r, "create user with login", err)
		return
	}

	if _, err := assignFriendCode(r.Context(), s.store, now, userID, s.cfg.FriendCodeCooldown); err != nil {
		writeInternalError(s.log, w, r, "assign friend code", err)
		return
	}

	s.log.Info("baas: website registration", "user", userID, "login_username", username)
	writeHTML(w, http.StatusOK, registerSuccessHTML(username))
}

// validateRegistration returns a user-facing problem description, or "" if
// the submission is acceptable. Deliberately loose: this is a private,
// operator-run server, not a public service needing strict password policy
// enforcement.
func validateRegistration(username, password string) string {
	switch {
	case username == "":
		return "Username is required."
	case len(username) > 64:
		return "Username is too long."
	case strings.ContainsAny(username, " \t\n"):
		return "Username cannot contain whitespace."
	case len(password) < 8:
		return "Password must be at least 8 characters."
	case len(password) > 256:
		return "Password is too long."
	}
	return ""
}

// writeHTML sends a self-contained HTML page: inline CSS, no external
// assets, no JS needed for a simple form. That also means this page does not
// depend on cdn.accounts.nintendo.com or any other host being reachable.
func writeHTML(w http.ResponseWriter, status int, body string) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	// A real capture of the Nintendo Account link page showed these two
	// present (Cache-Control: no-store, without the ", no-cache" writeJSON's
	// responses carry -- this page's own value, not copied from there).
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Pragma", "no-cache")
	w.WriteHeader(status)
	_, _ = w.Write([]byte(body))
}

const htmlPageStyle = `body{background:#1a1a2e;color:#eee;font-family:sans-serif;` +
	`max-width:420px;margin:80px auto;padding:0 20px}` +
	`h1{font-size:1.3em}input{width:100%;box-sizing:border-box;padding:8px;` +
	`margin:6px 0;background:#16213e;border:1px solid #444;color:#eee;border-radius:4px}` +
	`button{width:100%;padding:10px;margin-top:10px;background:#0f3460;color:#fff;` +
	`border:none;border-radius:4px;cursor:pointer}` +
	`.problem{color:#ff6b6b;margin-bottom:10px}`

func registerPageHTML(problem string) string {
	var problemHTML string
	if problem != "" {
		problemHTML = fmt.Sprintf(`<p class="problem">%s</p>`, html.EscapeString(problem))
	}
	return fmt.Sprintf(`<!doctype html><html><head><meta charset="utf-8">`+
		`<title>SwitchNet — create an account</title><style>%s</style></head><body>`+
		`<h1>Create a SwitchNet account</h1>%s`+
		`<form method="post" action="/register">`+
		`<input name="username" placeholder="Username" autocomplete="username" required>`+
		`<input name="nickname" placeholder="Nickname (optional)">`+
		`<input name="password" type="password" placeholder="Password" `+
		`autocomplete="new-password" required>`+
		`<button type="submit">Create account</button>`+
		`</form></body></html>`, htmlPageStyle, problemHTML)
}

func registrationClosedHTML() string {
	return fmt.Sprintf(`<!doctype html><html><head><meta charset="utf-8">`+
		`<title>SwitchNet — registration closed</title><style>%s</style></head><body>`+
		`<h1>Registration is closed</h1>`+
		`<p>The operator has turned off self-service registration on this server.</p>`+
		`</body></html>`, htmlPageStyle)
}

func registerSuccessHTML(username string) string {
	return fmt.Sprintf(`<!doctype html><html><head><meta charset="utf-8">`+
		`<title>SwitchNet — account created</title><style>%s</style></head><body>`+
		`<h1>Account created</h1>`+
		`<p>Signed in as <strong>%s</strong>.</p>`+
		`<p>Use this username and password on the console's Nintendo Account link `+
		`screen to attach it to a local user profile.</p>`+
		`</body></html>`, htmlPageStyle, html.EscapeString(username))
}
