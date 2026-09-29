package baas

import (
	"context"
	"net/http"

	"github.com/n-popescu/grid0plus/internal/db"
	"golang.org/x/crypto/bcrypt"
)

// A person's account, independent of any console: one persistent GRID0+
// identity per person, which they link onto a local user profile through the
// Nintendo Account link stub (nalink.go). The Discord bot is the only way to
// create one (botapi.go); the self-registration web page it replaced is gone.

// createLoginUser makes a person's account: login credentials, a friend code,
// and no device account until they link a console, owned by the Discord
// account in discordUserID. primary says whether it is that Discord account's
// main account, the one commands that name no account act on; a person
// registering a second account is always an extra beside the first.
// db.ErrConflict if the username is taken, or if that Discord account already
// has a main account.
func (s *Server) createLoginUser(ctx context.Context, username, nickname, password, discordUserID string, primary bool) (db.User, error) {
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		return db.User{}, err
	}
	userID, err := newHexID(8)
	if err != nil {
		return db.User{}, err
	}

	now := s.now()
	user := db.User{
		ID:                  userID,
		Nickname:            nickname,
		Birthday:            "0000-00-00",
		Permissions:         db.DefaultPermissions(now),
		Extras:              db.EmptyExtras(),
		ThumbnailUploadedAt: &now,
		BlocksUpdatedAt:     now,
		FriendsUpdatedAt:    now,
		CreatedAt:           now,
		UpdatedAt:           now,
		LoginUsername:       username,
		LoginPasswordHash:   string(hash),
		DiscordUserID:       discordUserID,
	}
	if err := s.store.CreateUserWithLogin(ctx, user, primary); err != nil {
		return db.User{}, err
	}
	if _, err := assignFriendCode(ctx, s.store, now, userID, s.cfg.FriendCodeCooldown); err != nil {
		return db.User{}, err
	}
	return user, nil
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

// htmlPageStyle is the shared look of the Nintendo Account link pages.
const htmlPageStyle = `body{background:#1a1a2e;color:#eee;font-family:sans-serif;` +
	`max-width:420px;margin:80px auto;padding:0 20px}` +
	`h1{font-size:1.3em}input{width:100%;box-sizing:border-box;padding:8px;` +
	`margin:6px 0;background:#16213e;border:1px solid #444;color:#eee;border-radius:4px}` +
	`button{width:100%;padding:10px;margin-top:10px;background:#0f3460;color:#fff;` +
	`border:none;border-radius:4px;cursor:pointer}` +
	`.problem{color:#ff6b6b;margin-bottom:10px}`
