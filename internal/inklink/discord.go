package inklink

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/n-popescu/grid0plus/internal/baas"
	"github.com/n-popescu/grid0plus/internal/db"
)

const oauthCookie = "inklink_oauth_state"

var snowflake = regexp.MustCompile(`^[0-9]{15,21}$`)

type oauthFlow struct{ expires time.Time }

func (s *Server) authInfo(w http.ResponseWriter, r *http.Request) {
	write(w, 200, map[string]bool{"discord": s.opts.Discord.ClientID != "", "demo": s.opts.Demo})
}
func randomOAuthValue() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}
func (s *Server) flowCookie(w http.ResponseWriter, value string, age int) {
	// Discord returns through a cross-site top-level GET, so Strict would drop
	// the flow cookie. The authenticated session cookie remains Strict.
	http.SetCookie(w, &http.Cookie{Name: oauthCookie, Value: value, Path: "/auth/discord", MaxAge: age, HttpOnly: true, Secure: s.opts.SecureCookies, SameSite: http.SameSiteLaxMode})
}
func (s *Server) discordStart(w http.ResponseWriter, r *http.Request) {
	if s.opts.Discord.ClientID == "" {
		fail(w, 503, "Discord sign-in is not configured yet.")
		return
	}
	if !s.allowLogin(clientAddress(r)) {
		fail(w, 429, "Too many sign-in attempts. Try again in 15 minutes.")
		return
	}
	state, err := randomOAuthValue()
	if err != nil {
		s.internal(w, err)
		return
	}
	s.mu.Lock()
	for k, f := range s.flows {
		if !s.opts.Now().Before(f.expires) {
			delete(s.flows, k)
		}
	}
	if len(s.flows) >= 4096 {
		s.mu.Unlock()
		fail(w, 503, "Sign-in is busy. Try again shortly.")
		return
	}
	s.flows[state] = oauthFlow{expires: s.opts.Now().Add(10 * time.Minute)}
	s.mu.Unlock()
	s.flowCookie(w, state, 600)
	q := url.Values{"client_id": {s.opts.Discord.ClientID}, "redirect_uri": {s.opts.Discord.RedirectURI}, "response_type": {"code"}, "scope": {"identify"}, "state": {state}}
	http.Redirect(w, r, "https://discord.com/oauth2/authorize?"+q.Encode(), http.StatusFound)
}
func (s *Server) oauthFailure(w http.ResponseWriter, r *http.Request, reason string) {
	// Neither Discord's error text nor its code/token is reflected into HTML,
	// logs or the post-login URL.
	http.Redirect(w, r, "/?signin="+reason, http.StatusSeeOther)
}
func (s *Server) discordCallback(w http.ResponseWriter, r *http.Request) {
	if s.opts.Discord.ClientID == "" {
		fail(w, 503, "Discord sign-in is not configured yet.")
		return
	}
	c, err := r.Cookie(oauthCookie)
	state := r.URL.Query().Get("state")
	if err != nil || len(state) != 64 || subtle.ConstantTimeCompare([]byte(c.Value), []byte(state)) != 1 {
		s.oauthFailure(w, r, "expired")
		return
	}
	s.flowCookie(w, "", -1)
	s.mu.Lock()
	f, ok := s.flows[state]
	delete(s.flows, state)
	s.mu.Unlock()
	if !ok || !s.opts.Now().Before(f.expires) {
		s.oauthFailure(w, r, "expired")
		return
	}
	if r.URL.Query().Get("error") != "" {
		s.oauthFailure(w, r, "cancelled")
		return
	}
	code := r.URL.Query().Get("code")
	if code == "" || len(code) > 4096 {
		s.oauthFailure(w, r, "failed")
		return
	}
	identity, err := s.discordIdentity(r.Context(), code)
	if err != nil {
		s.oauthFailure(w, r, "failed")
		return
	}
	user, err := s.discordUser(r.Context(), identity)
	if err != nil {
		s.oauthFailure(w, r, "account")
		return
	}
	if err = s.signIn(w, user, identity.ID); err != nil {
		s.internal(w, err)
		return
	}
	http.Redirect(w, r, "/#home", http.StatusSeeOther)
}

type discordIdentity struct {
	ID         string `json:"id"`
	Username   string `json:"username"`
	GlobalName string `json:"global_name"`
	Bot        bool   `json:"bot"`
}

func (s *Server) discordIdentity(ctx context.Context, code string) (discordIdentity, error) {
	var identity discordIdentity
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	form := url.Values{"grant_type": {"authorization_code"}, "code": {code}, "redirect_uri": {s.opts.Discord.RedirectURI}}
	req, err := http.NewRequestWithContext(ctx, "POST", "https://discord.com/api/oauth2/token", strings.NewReader(form.Encode()))
	if err != nil {
		return identity, err
	}
	req.SetBasicAuth(s.opts.Discord.ClientID, s.opts.Discord.ClientSecret)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := s.opts.OAuthClient.Do(req)
	if err != nil {
		return identity, errors.New("discord token exchange unavailable")
	}
	var token struct {
		AccessToken string `json:"access_token"`
		TokenType   string `json:"token_type"`
		Scope       string `json:"scope"`
	}
	err = json.NewDecoder(io.LimitReader(resp.Body, 64<<10)).Decode(&token)
	resp.Body.Close()
	if err != nil || resp.StatusCode != 200 || token.AccessToken == "" || !strings.EqualFold(token.TokenType, "Bearer") {
		return identity, errors.New("discord token exchange refused")
	}
	identified := false
	for _, scope := range strings.Fields(token.Scope) {
		if scope == "identify" {
			identified = true
		}
	}
	if !identified {
		return identity, errors.New("discord identify scope missing")
	}
	req, err = http.NewRequestWithContext(ctx, "GET", "https://discord.com/api/v10/users/@me", nil)
	if err != nil {
		return identity, err
	}
	req.Header.Set("Authorization", "Bearer "+token.AccessToken)
	resp, err = s.opts.OAuthClient.Do(req)
	if err != nil {
		return identity, errors.New("discord identity unavailable")
	}
	defer resp.Body.Close()
	err = json.NewDecoder(io.LimitReader(resp.Body, 64<<10)).Decode(&identity)
	if err != nil || resp.StatusCode != 200 || !snowflake.MatchString(identity.ID) || identity.Bot {
		return identity, errors.New("discord identity refused")
	}
	return identity, nil
}
func (s *Server) discordUser(ctx context.Context, identity discordIdentity) (db.User, error) {
	user, err := s.opts.Store.GetUserByDiscordID(ctx, identity.ID)
	if err == nil {
		if user.Deleted || user.Provisional || user.LoginPasswordHash == "" {
			return db.User{}, errors.New("inactive account")
		}
		return user, nil
	}
	if !errors.Is(err, db.ErrNotFound) {
		return db.User{}, err
	}
	// Missing primary is not proof that this Discord account has no accounts.
	// Never create a replacement identity when an existing link is inconsistent.
	linked, err := s.opts.Store.ListUsersByDiscordID(ctx, identity.ID)
	if err != nil {
		return db.User{}, err
	}
	if len(linked) > 0 {
		return db.User{}, errors.New("account has no active primary")
	}
	password, err := randomOAuthValue()
	if err != nil {
		return db.User{}, err
	}
	nickname := strings.TrimSpace(identity.GlobalName)
	if nickname == "" {
		nickname = identity.Username
	}
	if nickname == "" {
		nickname = "Player"
	}
	runes := []rune(nickname)
	if len(runes) > 10 {
		nickname = string(runes[:10])
	}
	// Reserve a collision-resistant login without ever claiming someone else's
	// matching display name. The unknown random password can later be set via
	// the bot if the player wants password-based console linking.
	user, err = baas.CreateLoginUser(ctx, s.opts.Store, s.opts.Now(), 24*time.Hour, "discord_"+identity.ID, nickname, password, identity.ID, true)
	if errors.Is(err, db.ErrConflict) {
		user, err = s.opts.Store.GetUserByDiscordID(ctx, identity.ID)
	}
	if err != nil {
		return db.User{}, fmt.Errorf("register discord account: %w", err)
	}
	if user.Deleted || user.Provisional || user.DiscordUserID != identity.ID {
		return db.User{}, errors.New("inactive account")
	}
	return user, nil
}
