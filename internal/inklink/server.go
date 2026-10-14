// Package inklink serves Grid0's player companion, independently of its operator dashboard.
package inklink

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/subtle"
	"embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io/fs"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"path/filepath"
	"strings"
	"sync"
	"time"

	gojwt "github.com/golang-jwt/jwt/v5"
	"github.com/n-popescu/grid0plus/config"
	"github.com/n-popescu/grid0plus/internal/db"
	"github.com/n-popescu/grid0plus/internal/presence"
	"github.com/n-popescu/grid0plus/internal/toyohr"
	"golang.org/x/crypto/bcrypt"
)

//go:embed assets/*
var assets embed.FS

const cookieName = "inklink_session"
const sessionLifetime = 30 * 24 * time.Hour

type Options struct {
	Discord                         config.DiscordOAuth
	OAuthClient                     *http.Client
	Demo                            bool
	Store                           db.Store
	Key                             *rsa.PrivateKey
	Tenant                          string
	Rotation                        toyohr.Rotation
	SecureCookies                   bool
	ImageDir                        string
	PresenceTTL, ConsolePresenceTTL time.Duration
	Log                             *slog.Logger
	Now                             func() time.Time
}
type attempt struct {
	count int
	until time.Time
}
type Server struct {
	opts      Options
	schedule  *toyohr.Service
	presence  presence.Resolver
	mu        sync.Mutex
	attempts  map[string]attempt
	dummyHash []byte
	flows     map[string]oauthFlow
}
type sessionClaims struct {
	gojwt.RegisteredClaims
	Version   string `json:"version"`
	DiscordID string `json:"discord_id,omitempty"`
}

func New(opts Options) (*Server, error) {
	if opts.Store == nil || opts.Key == nil || opts.Tenant == "" {
		return nil, errors.New("inklink: store, signing key and tenant are required")
	}
	if err := opts.Discord.Validate(); err != nil {
		return nil, err
	}
	if opts.Discord.ClientID != "" && opts.Discord.ClientSecret == "" {
		return nil, errors.New("inklink: Discord client secret must be loaded before starting")
	}
	if opts.Discord.ClientID != "" && strings.HasPrefix(opts.Discord.RedirectURI, "https://") && !opts.SecureCookies {
		return nil, errors.New("inklink: HTTPS Discord callbacks require secure cookies")
	}
	if opts.OAuthClient == nil {
		opts.OAuthClient = &http.Client{Timeout: 12 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	}
	if opts.Now == nil {
		opts.Now = time.Now
	}
	if opts.Log == nil {
		opts.Log = slog.Default()
	}
	if opts.PresenceTTL <= 0 {
		opts.PresenceTTL = 5 * time.Minute
	}
	if opts.ConsolePresenceTTL <= 0 {
		opts.ConsolePresenceTTL = 5 * time.Minute
	}
	dummy, err := bcrypt.GenerateFromPassword([]byte("inklink-nonexistent-account"), bcrypt.DefaultCost)
	if err != nil {
		return nil, err
	}
	return &Server{opts: opts, dummyHash: dummy, flows: map[string]oauthFlow{}, attempts: map[string]attempt{}, schedule: toyohr.NewService(toyohr.Options{Store: opts.Store, Tenant: opts.Tenant, Rotation: opts.Rotation, Now: opts.Now, Log: opts.Log}), presence: presence.Resolver{Store: opts.Store, ReportTTL: opts.PresenceTTL, ConsoleTTL: opts.ConsolePresenceTTL}}, nil
}
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	files, _ := fs.Sub(assets, "assets")
	mux.Handle("GET /assets/", http.StripPrefix("/assets/", http.FileServer(http.FS(files))))
	mux.HandleFunc("GET /api/auth", s.authInfo)
	mux.HandleFunc("GET /auth/discord", s.discordStart)
	mux.HandleFunc("GET /auth/discord/callback", s.discordCallback)
	mux.HandleFunc("POST /api/login", s.login)
	mux.HandleFunc("POST /api/logout", s.logout)
	mux.HandleFunc("GET /api/data", s.data)
	mux.HandleFunc("GET /api/avatar/{id}", s.avatar)
	mux.HandleFunc("GET /", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" {
			http.NotFound(w, r)
			return
		}
		b, _ := assets.ReadFile("assets/index.html")
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Write(b)
	})
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Referrer-Policy", "same-origin")
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("Content-Security-Policy", "default-src 'self'; script-src 'self'; style-src 'self' 'unsafe-inline' https://fonts.googleapis.com; font-src 'self' https://fonts.gstatic.com; img-src 'self' https://cdn.jsdelivr.net data: blob:; connect-src 'self'; frame-ancestors 'none'; base-uri 'none'; form-action 'self'")
		if r.Method != "GET" && r.Method != "HEAD" {
			// Cross-origin forms cannot add this header; no CORS permission is granted.
			if r.Header.Get("X-Inklink-Request") != "1" {
				fail(w, 403, "Reload INKLINK before trying again.")
				return
			}
			if origin := r.Header.Get("Origin"); origin != "" {
				u, err := url.Parse(origin)
				if err != nil || u.Host != r.Host || (u.Scheme != "http" && u.Scheme != "https") {
					fail(w, 403, "Cross-origin request refused.")
					return
				}
			}
		}
		mux.ServeHTTP(w, r)
	})
}
func version(u db.User) string {
	sum := sha256.Sum256([]byte(u.LoginPasswordHash))
	return hex.EncodeToString(sum[:])
}
func (s *Server) cookie(w http.ResponseWriter, token string, expires time.Time, maxAge int) {
	http.SetCookie(w, &http.Cookie{Name: cookieName, Value: token, Path: "/", HttpOnly: true, Secure: s.opts.SecureCookies, SameSite: http.SameSiteStrictMode, Expires: expires, MaxAge: maxAge})
}
func (s *Server) allowLogin(addr string) bool {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		host = addr
	}
	now := s.opts.Now()
	s.mu.Lock()
	defer s.mu.Unlock()
	for k, v := range s.attempts {
		if !now.Before(v.until) {
			delete(s.attempts, k)
		}
	}
	a := s.attempts[host]
	if a.count >= 8 || (a.count == 0 && len(s.attempts) >= 4096) {
		return false
	}
	if a.count == 0 {
		a.until = now.Add(15 * time.Minute)
	}
	a.count++
	s.attempts[host] = a
	return true
}

// Only the local TLS proxy can supply a client address. Direct clients cannot
// evade the limiter by inventing a forwarding header.
func clientAddress(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err == nil {
		if peer := net.ParseIP(host); peer != nil && peer.IsLoopback() {
			if client := net.ParseIP(r.Header.Get("X-Real-IP")); client != nil {
				return client.String()
			}
		}
	}
	return r.RemoteAddr
}

func (s *Server) login(w http.ResponseWriter, r *http.Request) {
	if !s.allowLogin(clientAddress(r)) {
		fail(w, 429, "Too many sign-in attempts. Try again in 15 minutes.")
		return
	}
	var input struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 8192))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&input); err != nil {
		fail(w, 400, "Enter your Grid0 username and password.")
		return
	}
	user, err := s.opts.Store.GetUserByLoginUsername(r.Context(), strings.TrimSpace(input.Username))
	hash := s.dummyHash
	if err == nil && !user.Deleted && user.LoginPasswordHash != "" {
		hash = []byte(user.LoginPasswordHash)
	}
	valid := bcrypt.CompareHashAndPassword(hash, []byte(input.Password)) == nil
	if !valid || err != nil || user.Deleted || user.Provisional || user.LoginPasswordHash == "" {
		fail(w, 401, "Wrong username or password.")
		return
	}
	if err := s.signIn(w, user); err != nil {
		s.internal(w, err)
		return
	}
	write(w, 200, map[string]bool{"signedIn": true})
}
func (s *Server) signIn(w http.ResponseWriter, user db.User, discordID ...string) error {
	now := s.opts.Now()
	expires := now.Add(sessionLifetime)
	id := make([]byte, 24)
	if _, err := rand.Read(id); err != nil {
		return err
	}
	claims := sessionClaims{RegisteredClaims: gojwt.RegisteredClaims{Issuer: "grid0-inklink", Audience: gojwt.ClaimStrings{"inklink"}, Subject: user.ID, ID: hex.EncodeToString(id), IssuedAt: gojwt.NewNumericDate(now), ExpiresAt: gojwt.NewNumericDate(expires)}, Version: version(user)}
	if len(discordID) > 0 {
		claims.DiscordID = discordID[0]
	}
	token, err := gojwt.NewWithClaims(gojwt.SigningMethodRS256, claims).SignedString(s.opts.Key)
	if err != nil {
		return err
	}
	s.cookie(w, token, expires, int(sessionLifetime.Seconds()))
	return nil
}
func (s *Server) user(w http.ResponseWriter, r *http.Request) (db.User, bool) {
	cookie, err := r.Cookie(cookieName)
	if err != nil {
		fail(w, 401, "Sign in to your Grid0 account.")
		return db.User{}, false
	}
	var c sessionClaims
	_, err = gojwt.ParseWithClaims(cookie.Value, &c, func(*gojwt.Token) (any, error) { return &s.opts.Key.PublicKey, nil }, gojwt.WithValidMethods([]string{"RS256"}), gojwt.WithExpirationRequired(), gojwt.WithIssuer("grid0-inklink"), gojwt.WithAudience("inklink"), gojwt.WithTimeFunc(s.opts.Now))
	if err != nil || c.Subject == "" {
		fail(w, 401, "Your session has expired. Please sign in again.")
		return db.User{}, false
	}
	u, err := s.opts.Store.GetUser(r.Context(), c.Subject)
	if err != nil || u.Deleted || u.Provisional || u.LoginPasswordHash == "" || (c.DiscordID != "" && u.DiscordUserID != c.DiscordID) || subtle.ConstantTimeCompare([]byte(c.Version), []byte(version(u))) != 1 {
		fail(w, 401, "Your session has expired. Please sign in again.")
		return db.User{}, false
	}
	return u, true
}
func (s *Server) logout(w http.ResponseWriter, r *http.Request) {
	s.cookie(w, "", time.Unix(1, 0), -1)
	write(w, 200, map[string]bool{"signedIn": false})
}
func (s *Server) avatar(w http.ResponseWriter, r *http.Request) {
	u, ok := s.user(w, r)
	if !ok {
		return
	}
	id := r.PathValue("id")
	allowed := id == u.ID
	if !allowed {
		_, err := s.opts.Store.GetFriendship(r.Context(), u.ID, id)
		allowed = err == nil
	}
	if !allowed {
		http.NotFound(w, r)
		return
	}
	other, err := s.opts.Store.GetUser(r.Context(), id)
	if err != nil || other.Deleted || s.opts.ImageDir == "" {
		http.NotFound(w, r)
		return
	}
	v, err := url.Parse(other.ThumbnailURL)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	name := filepath.Base(v.Path)
	if name == "." || name == "/" || !strings.HasSuffix(name, ".jpg") {
		http.NotFound(w, r)
		return
	}
	http.ServeFile(w, r, filepath.Join(s.opts.ImageDir, name))
}
func write(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}
func fail(w http.ResponseWriter, status int, message string) {
	write(w, status, map[string]string{"error": message})
}
func (s *Server) internal(w http.ResponseWriter, err error) {
	s.opts.Log.Error("inklink: request failed", "error", err)
	fail(w, 500, "Grid0 could not load this data. Please try again.")
}
