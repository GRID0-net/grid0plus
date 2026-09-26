// Package dashboard is the operator's window into SwitchNet: who is online,
// what they are playing, and the Splatfest controls.
//
// It is the one part of SwitchNet a person looks at rather than a console, so
// it is the one part where the interface matters. Everything else in this
// repository answers a Switch; this answers the operator.
//
// It reads the same store BAAS and NPLN write, and takes its live view from
// internal/presencebus rather than polling. **Set redis.url before running this
// as a separate process**: the in-process bus delivers nothing across a process
// boundary, and it does so silently — the page simply never updates and nothing
// anywhere says why.
package dashboard

import (
	"context"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"html/template"
	"log/slog"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/n-popescu/switchnet/internal/db"
	"github.com/n-popescu/switchnet/internal/presence"
	"github.com/n-popescu/switchnet/internal/presencebus"
	"github.com/n-popescu/switchnet/internal/toyohr"
)

//go:embed assets/*
var assets embed.FS

//go:embed templates/*.html
var templates embed.FS

// Options configures the dashboard.
type Options struct {
	Store db.Store
	Bus   presencebus.Bus
	Log   *slog.Logger

	// Tenant is the NPLN tenant the fest controls act on.
	Tenant string

	// Rotation is the same Vs stage/rule rotation cfg.NPLN's Schedule
	// service serves consoles -- passed through so the schedule viewer tab
	// shows the real Turf War/Anarchy rotation rather than always-empty.
	// Left zero, PreviewVsSchedule answers no slots, same as the wire RPC
	// would for an unconfigured rotation.
	Rotation toyohr.Rotation

	// SeedDir is where `switchnetctl fest build` writes its seed folders. When
	// set, the dashboard serves them to the console-side installer -- see
	// seed.go and switch/bcat-installer/.
	SeedDir string

	// PresenceTTL and ConsolePresenceTTL apply the exact same rule BAAS does
	// (internal/presence), so the two never disagree about who is online — a
	// dashboard that says somebody is playing while their friends see them
	// offline is worse than no dashboard. Pass the same values as BAAS's own
	// config: cfg.BAAS.PresenceTTL and cfg.BAAS.ConsolePresenceTTL.
	PresenceTTL time.Duration
	// ConsolePresenceTTL is how long a console counts as ONLINE after its last
	// authenticated request. Left zero, it defaults to PresenceTTL's default
	// rather than to PresenceTTL's own value, because the two are configured
	// independently and defaulting one from the other would silently reproduce
	// the exact single-TTL bug this field exists to fix.
	ConsolePresenceTTL time.Duration

	// Token, when set, is required as a bearer token or ?token= on every
	// request.
	//
	// Nothing in phase 1 authenticates a person — every credential in the stack
	// belongs to a console — so the dashboard has no user accounts to check
	// against. A shared token is the honest minimum: it keeps the fest controls
	// off the open network without pretending to be an identity system.
	Token string

	// NPLNAdminURL and NPLNAdminToken reach cmd/npln's matchmaking controls.
	//
	// The queue is in that process, not this one. Leave either empty and the
	// matchmaking panel says it is not configured rather than showing an empty
	// queue, which would read as "nobody is playing".
	NPLNAdminURL   string
	NPLNAdminToken string

	// NEXAdminURL and NEXAdminToken reach cmd/nex's Splatoon 2 operator API,
	// on the same terms: leave either empty and the panel says so.
	NEXAdminURL   string
	NEXAdminToken string

	// Addresses is where each service listens, for the health view. Taken from
	// the same config the services themselves read, so a changed port cannot
	// leave the page probing the old one.
	Addresses ServiceAddresses

	Now func() time.Time
}

// Server serves the dashboard.
type Server struct {
	store db.Store
	bus   presencebus.Bus
	log   *slog.Logger
	fests *toyohr.Service

	tenant   string
	seedDir  string
	presence presence.Resolver
	token    string
	now      func() time.Time

	// matchmaking is nil when the controls are not configured.
	matchmaking *adminClient
	// splatoon2 is nil when the Splatoon 2 panel is not configured.
	splatoon2 *adminClient

	addresses ServiceAddresses
	// probeClient is separate from the matchmaking client: health probes must
	// not be queued behind a slow control call, or a service looks down because
	// a different one is busy.
	probeClient *http.Client

	tmpl *template.Template
}

// New builds the dashboard.
func New(opts Options) (*Server, error) {
	if opts.Store == nil {
		return nil, errors.New("dashboard: no store")
	}
	if opts.Log == nil {
		opts.Log = slog.Default()
	}
	if opts.Now == nil {
		opts.Now = time.Now
	}
	if opts.PresenceTTL <= 0 {
		opts.PresenceTTL = 5 * time.Minute
	}
	if opts.ConsolePresenceTTL <= 0 {
		opts.ConsolePresenceTTL = 5 * time.Minute
	}

	tmpl, err := template.ParseFS(templates, "templates/*.html")
	if err != nil {
		return nil, fmt.Errorf("dashboard: parse templates: %w", err)
	}

	return &Server{
		store: opts.Store,
		bus:   opts.Bus,
		log:   opts.Log,
		// No CallerID: the dashboard schedules fests and never serves an RPC
		// through this service, so its player-scoped handlers stay fail-closed.
		fests: toyohr.NewService(toyohr.Options{
			Store: opts.Store, Log: opts.Log, Tenant: opts.Tenant, Now: opts.Now,
			Rotation: opts.Rotation,
		}),
		tenant:  opts.Tenant,
		seedDir: opts.SeedDir,
		presence: presence.Resolver{
			Store:      opts.Store,
			ReportTTL:  opts.PresenceTTL,
			ConsoleTTL: opts.ConsolePresenceTTL,
		},
		token:       opts.Token,
		now:         opts.Now,
		matchmaking: newMatchmakingClient(opts.NPLNAdminURL, opts.NPLNAdminToken),
		splatoon2:   newSplatoon2Client(opts.NEXAdminURL, opts.NEXAdminToken),
		addresses:   opts.Addresses,
		probeClient: &http.Client{Timeout: probeTimeout},
		tmpl:        tmpl,
	}, nil
}

// Handler returns the HTTP handler.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()

	mux.Handle("GET /assets/", http.FileServer(http.FS(assets)))

	mux.HandleFunc("GET /{$}", s.guardPage(s.handleIndex))
	mux.HandleFunc("GET /login", s.handleLoginPage)
	mux.HandleFunc("POST /login", s.handleLogin)
	mux.HandleFunc("POST /logout", s.handleLogout)
	mux.HandleFunc("GET /api/overview", s.guard(s.handleOverview))
	mux.HandleFunc("GET /api/players", s.guard(s.handlePlayers))
	mux.HandleFunc("GET /api/players/{id}", s.guard(s.handlePlayerDetail))
	mux.HandleFunc("GET /api/matches", s.guard(s.handleMatches))
	mux.HandleFunc("GET /api/fests", s.guard(s.handleFests))
	mux.HandleFunc("POST /api/fests", s.guard(s.handleCreateFest))
	mux.HandleFunc("POST /api/fests/{id}/enabled", s.guard(s.handleSetFestEnabled))
	mux.HandleFunc("DELETE /api/fests/{id}", s.guard(s.handleDeleteFest))
	mux.HandleFunc("GET /api/coop-shifts", s.guard(s.handleCoopShifts))
	mux.HandleFunc("POST /api/coop-shifts", s.guard(s.handleCreateCoopShift))
	mux.HandleFunc("POST /api/coop-shifts/generate", s.guard(s.handleGenerateCoopShifts))
	mux.HandleFunc("DELETE /api/coop-shifts/{id}", s.guard(s.handleDeleteCoopShift))
	mux.HandleFunc("GET /api/coop-reference", s.guard(s.handleCoopReference))
	mux.HandleFunc("GET /api/schedule-viewer", s.guard(s.handleScheduleViewer))
	mux.HandleFunc("GET /api/health", s.guard(s.handleHealth))
	mux.HandleFunc("GET /api/matchmaking", s.guard(s.handleMatchmaking))
	mux.HandleFunc("POST /api/matchmaking/modes/{mode}/start", s.guard(s.handleForceStart))
	mux.HandleFunc("POST /api/matchmaking/modes/{mode}/size", s.guard(s.handleSetModeSize))
	mux.HandleFunc("POST /api/matchmaking/tickets/{id}/cancel", s.guard(s.handleCancelTicket))
	mux.HandleFunc("GET /api/splatoon2", s.guard(s.handleSplatoon2))
	mux.HandleFunc("POST /api/splatoon2/players/{id}/gear-order", s.guard(s.handleSplatoon2GearOrder))
	mux.HandleFunc("DELETE /api/splatoon2/players/{id}/gear-order", s.guard(s.handleSplatoon2CancelGearOrder))
	mux.HandleFunc("POST /api/splatoon2/players/{id}/disconnect", s.guard(s.handleSplatoon2Disconnect))

	mux.HandleFunc("GET /api/debug/stats", s.guard(s.handleDebugStats))
	mux.HandleFunc("GET /api/debug/accounts/{id}", s.guard(s.handleDebugAccount))
	mux.HandleFunc("POST /api/debug/accounts/{id}/deleted", s.guard(s.handleDebugSetAccountDeleted))
	mux.HandleFunc("POST /api/debug/accounts/{id}/device-accounts/{deviceAccountId}/revoke-tokens", s.guard(s.handleDebugRevokeDeviceAccountTokens))
	mux.HandleFunc("POST /api/debug/accounts/{id}/device-accounts/{deviceAccountId}/deleted", s.guard(s.handleDebugSetDeviceAccountDeleted))
	mux.HandleFunc("GET /api/debug/tables", s.guard(s.handleDebugTables))
	mux.HandleFunc("GET /api/debug/tables/{table}", s.guard(s.handleDebugTableRows))
	mux.HandleFunc("PATCH /api/debug/tables/{table}", s.guard(s.handleDebugUpdateRow))

	mux.HandleFunc("GET /api/events", s.guard(s.handleEvents))

	// The BCAT delivery cache, for the console-side installer. Behind the same
	// token as everything else; the installer is configured with it.
	mux.HandleFunc("GET /api/seed/manifest", s.guard(s.handleSeedManifest))
	mux.HandleFunc("GET /api/seed/file/{path...}", s.guard(s.handleSeedFile))

	return mux
}

// authCookieName holds the shared token once a browser has presented it
// once, either via /login's form or a bookmarked ?token= link. It carries
// the same secret the query string and Authorization header already
// accept -- there is no separate session store, on purpose: phase 1 has no
// person to attach a session to (see TODO.md §3.3), so a cookie here is
// just a fourth, browser-friendly place to say the one shared secret,
// never a stand-in for an account.
const authCookieName = "switchnet_dashboard_session"

// presentedToken returns whatever credential the request offers: the query
// string, the Authorization header, or the login cookie, in that order.
// Checking the query and header first keeps a script using ?token= or
// Authorization working exactly as before a cookie ever entered the
// picture.
func (s *Server) presentedToken(r *http.Request) string {
	if t := r.URL.Query().Get("token"); t != "" {
		return t
	}
	if h := r.Header.Get("Authorization"); h != "" {
		if scheme, tok, ok := strings.Cut(h, " "); ok && strings.EqualFold(scheme, "Bearer") {
			return tok
		}
	}
	if c, err := r.Cookie(authCookieName); err == nil {
		return c.Value
	}
	return ""
}

// authorized reports whether the request carries the configured token, or
// whether no token is configured at all.
//
// Constant-time compare, for the same reason internal/baas compares a
// device-account password that way: a token is a secret, and a
// length-dependent comparison leaks it a byte at a time.
func (s *Server) authorized(r *http.Request) bool {
	return s.token == "" || constantTimeEqual(s.presentedToken(r), s.token)
}

// guard applies the shared token, when one is configured. Used for every
// API route: on failure it answers 401, which is the right shape for a
// fetch() call, not a redirect.
func (s *Server) guard(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !s.authorized(r) {
			http.Error(w, "unauthorised", http.StatusUnauthorized)
			return
		}
		next(w, r)
	}
}

// guardPage is guard's counterpart for the one page a browser navigates to
// directly: on failure it sends the operator to the login form instead of
// a bare "unauthorised" response, which is meaningless outside a fetch
// call.
func (s *Server) guardPage(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !s.authorized(r) {
			http.Redirect(w, r, "/login", http.StatusFound)
			return
		}
		next(w, r)
	}
}

func (s *Server) setAuthCookie(w http.ResponseWriter, r *http.Request, token string) {
	http.SetCookie(w, &http.Cookie{
		Name:     authCookieName,
		Value:    token,
		Path:     "/",
		HttpOnly: true,
		Secure:   r.TLS != nil,
		SameSite: http.SameSiteLaxMode,
		MaxAge:   int((30 * 24 * time.Hour).Seconds()),
	})
}

func (s *Server) clearAuthCookie(w http.ResponseWriter, r *http.Request) {
	http.SetCookie(w, &http.Cookie{
		Name:     authCookieName,
		Value:    "",
		Path:     "/",
		HttpOnly: true,
		Secure:   r.TLS != nil,
		SameSite: http.SameSiteLaxMode,
		MaxAge:   -1,
	})
}

func (s *Server) handleIndex(w http.ResponseWriter, r *http.Request) {
	// A bookmarked ?token= link still works (guardPage already accepted it
	// above); set the cookie too, so the *next* visit no longer needs the
	// query string. This is the same upgrade /login itself performs, just
	// reached by the older path.
	if t := r.URL.Query().Get("token"); t != "" && s.token != "" && constantTimeEqual(t, s.token) {
		s.setAuthCookie(w, r, t)
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := s.tmpl.ExecuteTemplate(w, "index.html", map[string]any{
		"Token":           r.URL.Query().Get("token"),
		"TokenConfigured": s.token != "",
	}); err != nil {
		s.log.Error("dashboard: render", "error", err)
	}
}

// handleLoginPage serves the login form. With no token configured there is
// nothing to log in with, so it redirects straight to the dashboard rather
// than showing a form that can never fail or succeed meaningfully.
func (s *Server) handleLoginPage(w http.ResponseWriter, r *http.Request) {
	if s.token == "" || s.authorized(r) {
		http.Redirect(w, r, "/", http.StatusFound)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := s.tmpl.ExecuteTemplate(w, "login.html", map[string]any{
		"Failed": r.URL.Query().Get("failed") == "1",
	}); err != nil {
		s.log.Error("dashboard: render login", "error", err)
	}
}

// handleLogin checks the submitted token against the configured one and, on
// a match, sets the cookie guard/guardPage both accept. There is no session
// to create beyond that cookie -- see authCookieName's comment.
func (s *Server) handleLogin(w http.ResponseWriter, r *http.Request) {
	if s.token == "" {
		http.Redirect(w, r, "/", http.StatusFound)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Redirect(w, r, "/login?failed=1", http.StatusFound)
		return
	}
	submitted := r.PostFormValue("token")
	if !constantTimeEqual(submitted, s.token) {
		http.Redirect(w, r, "/login?failed=1", http.StatusFound)
		return
	}
	s.setAuthCookie(w, r, submitted)
	http.Redirect(w, r, "/", http.StatusFound)
}

func (s *Server) handleLogout(w http.ResponseWriter, r *http.Request) {
	s.clearAuthCookie(w, r)
	http.Redirect(w, r, "/login", http.StatusFound)
}

// ---- players and presence -------------------------------------------------

// Player is one row of the players view.
type Player struct {
	ID       string `json:"id"`
	Nickname string `json:"nickname"`
	// State is the EFFECTIVE state, resolved the same way BAAS resolves it.
	State string `json:"state"`
	// Activity is what they are doing, when the state is PLAYING.
	Activity   string `json:"activity,omitempty"`
	AppID      string `json:"appId,omitempty"`
	LastSeenAt string `json:"lastSeenAt,omitempty"`
}

func (s *Server) players(ctx context.Context) ([]Player, error) {
	users, err := s.store.ListUsers(ctx, 500)
	if err != nil {
		return nil, err
	}
	ids := make([]string, 0, len(users))
	for _, u := range users {
		ids = append(ids, u.ID)
	}
	presences, err := s.presence.EffectiveMany(ctx, ids, s.now())
	if err != nil {
		return nil, err
	}

	out := make([]Player, 0, len(users))
	for _, u := range users {
		p := presences[u.ID]
		// Resolve has already cleared AppID for anything but PLAYING.
		appID := p.AppID

		name := u.Nickname
		if name == "" {
			name = u.ID
		}
		row := Player{ID: u.ID, Nickname: name, State: p.State, AppID: appID}
		if appID != "" {
			row.Activity = titleName(appID)
		}
		if !p.LastSeenAt.IsZero() {
			row.LastSeenAt = p.LastSeenAt.UTC().Format(time.RFC3339)
		}
		out = append(out, row)
	}

	// Online first, then playing, then by name: the operator is looking for who
	// is around, and burying that under alphabetical order wastes the view.
	rank := map[string]int{"PLAYING": 0, "ONLINE": 1, "OFFLINE": 2}
	sort.SliceStable(out, func(i, j int) bool {
		if ri, rj := rank[out[i].State], rank[out[j].State]; ri != rj {
			return ri < rj
		}
		return strings.ToLower(out[i].Nickname) < strings.ToLower(out[j].Nickname)
	})
	return out, nil
}

// titleName maps the title ids SwitchNet is likely to see onto something a
// person can read. Unknown ids are shown as themselves rather than guessed at.
func titleName(appID string) string {
	switch strings.ToLower(appID) {
	case "0100c2500fc20000":
		return "Splatoon 3"
	case "01003bc0000a0000", "0100f8f0000a2000", "01003c700009c000":
		// Americas, Europe, Japan: internal/splatoon2's README.
		return "Splatoon 2"
	case "0100152000022000":
		return "Mario Kart 8 Deluxe"
	case "01006f8002326000":
		return "Animal Crossing: New Horizons"
	}
	return appID
}

func (s *Server) handlePlayers(w http.ResponseWriter, r *http.Request) {
	players, err := s.players(r.Context())
	if err != nil {
		s.fail(w, "list players", err)
		return
	}
	writeJSON(w, players)
}

// ---- player detail ---------------------------------------------------------

// playerFriend is one row of a player's friend list, enriched with the
// friend's own effective presence -- the same resolution the players list and
// the friends applet itself use, so a friend shown here as ONLINE is never
// out of step with what they see of each other.
type playerFriend struct {
	ID       string `json:"id"`
	Nickname string `json:"nickname"`
	State    string `json:"state"`
	Activity string `json:"activity,omitempty"`
	Favorite bool   `json:"favorite"`
	Since    string `json:"since"`
}

// playerDeviceAccount is one console linked to a player. The password is
// never included -- it is a permanent, unrotatable per-console credential,
// and the dashboard has no legitimate use for it.
type playerDeviceAccount struct {
	ID        string `json:"id"`
	CreatedAt string `json:"createdAt"`
}

// playerDetail is everything the Players tab's detail panel needs for one
// player, assembled from stores that already exist rather than a new one:
// internal/db already has the friendship and device-account stores, and
// internal/presence already resolves effective state the one way BAAS and
// the friends applet do.
type playerDetail struct {
	Player
	Country   string `json:"country,omitempty"`
	CreatedAt string `json:"createdAt"`
	LogoutAt  string `json:"logoutAt,omitempty"`

	// ReportedState and ReportedAt are what the console itself last said, and
	// when -- kept apart from the EFFECTIVE state on Player. "Reported
	// OFFLINE" and "never reported" are different states, and conflating them
	// is what makes a friend list permanently offline (see CLAUDE.md's
	// "friends were never online"). Both are absent when the console has
	// never reported: presence.reported_at is NULL until it does, and filling
	// ReportedState unconditionally would claim a report that never happened.
	ReportedState string `json:"reportedState,omitempty"`
	ReportedAt    string `json:"reportedAt,omitempty"`

	FriendCode string `json:"friendCode,omitempty"`

	Friends        []playerFriend        `json:"friends"`
	DeviceAccounts []playerDeviceAccount `json:"deviceAccounts"`
}

func (s *Server) handlePlayerDetail(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	ctx := r.Context()

	u, err := s.store.GetUser(ctx, id)
	if err != nil {
		if errors.Is(err, db.ErrNotFound) {
			http.Error(w, "no such player", http.StatusNotFound)
			return
		}
		s.fail(w, "load player", err)
		return
	}

	now := s.now()
	pres, err := s.presence.Effective(ctx, id, now)
	if err != nil {
		s.fail(w, "resolve presence", err)
		return
	}

	name := u.Nickname
	if name == "" {
		name = u.ID
	}
	detail := playerDetail{
		Player:    Player{ID: u.ID, Nickname: name, State: pres.State, AppID: pres.AppID},
		Country:   u.Country,
		CreatedAt: u.CreatedAt.UTC().Format(time.RFC3339),
	}
	if pres.AppID != "" {
		detail.Activity = titleName(pres.AppID)
	}
	if !pres.LastSeenAt.IsZero() {
		detail.LastSeenAt = pres.LastSeenAt.UTC().Format(time.RFC3339)
	}
	if pres.LogoutAt != nil {
		detail.LogoutAt = pres.LogoutAt.UTC().Format(time.RFC3339)
	}

	// The RAW reported state, separate from the effective one above. "Reported
	// OFFLINE" and "never reported" are different states -- see playerDetail's
	// doc comment -- and the stored state column defaults to OFFLINE, so
	// filling ReportedState unconditionally would claim a report that never
	// happened.
	if raw, err := s.store.GetPresence(ctx, id); err == nil {
		if !raw.ReportedAt.IsZero() {
			detail.ReportedState = raw.State
			detail.ReportedAt = raw.ReportedAt.UTC().Format(time.RFC3339)
		}
	} else if !errors.Is(err, db.ErrNotFound) {
		s.log.Debug("dashboard: load raw presence", "user", id, "error", err)
	}

	if code, err := s.store.GetFriendCode(ctx, id); err == nil {
		detail.FriendCode = code.Code
	} else if !errors.Is(err, db.ErrNotFound) {
		s.log.Debug("dashboard: load friend code", "user", id, "error", err)
	}

	friendships, err := s.store.ListFriends(ctx, id, 500)
	if err != nil {
		s.fail(w, "list friends", err)
		return
	}
	friendIDs := make([]string, 0, len(friendships))
	for _, f := range friendships {
		friendIDs = append(friendIDs, f.FriendID)
	}
	friendUsers, err := s.store.FindUsers(ctx, db.UserQuery{IDs: friendIDs})
	if err != nil {
		s.fail(w, "load friends", err)
		return
	}
	friendNames := make(map[string]string, len(friendUsers))
	for _, fu := range friendUsers {
		friendNames[fu.ID] = fu.Nickname
	}
	friendPresences, err := s.presence.EffectiveMany(ctx, friendIDs, now)
	if err != nil {
		s.fail(w, "resolve friend presence", err)
		return
	}
	detail.Friends = make([]playerFriend, 0, len(friendships))
	for _, f := range friendships {
		fp := friendPresences[f.FriendID]
		fname := friendNames[f.FriendID]
		if fname == "" {
			fname = f.FriendID
		}
		row := playerFriend{
			ID: f.FriendID, Nickname: fname, State: fp.State,
			Favorite: f.Favorite, Since: f.BecameFriendAt.UTC().Format(time.RFC3339),
		}
		if fp.AppID != "" {
			row.Activity = titleName(fp.AppID)
		}
		detail.Friends = append(detail.Friends, row)
	}
	// Favourites first, then online, then by name -- the same ordering
	// instinct as the players list: what the operator is looking for should
	// not be buried alphabetically.
	rank := map[string]int{"PLAYING": 0, "ONLINE": 1, "OFFLINE": 2}
	sort.SliceStable(detail.Friends, func(i, j int) bool {
		a, b := detail.Friends[i], detail.Friends[j]
		if a.Favorite != b.Favorite {
			return a.Favorite
		}
		if ra, rb := rank[a.State], rank[b.State]; ra != rb {
			return ra < rb
		}
		return strings.ToLower(a.Nickname) < strings.ToLower(b.Nickname)
	})

	das, err := s.store.ListDeviceAccounts(ctx, id)
	if err != nil {
		s.fail(w, "list device accounts", err)
		return
	}
	detail.DeviceAccounts = make([]playerDeviceAccount, 0, len(das))
	for _, da := range das {
		detail.DeviceAccounts = append(detail.DeviceAccounts, playerDeviceAccount{
			ID: da.ID, CreatedAt: da.CreatedAt.UTC().Format(time.RFC3339),
		})
	}

	writeJSON(w, detail)
}

// ---- overview -------------------------------------------------------------

func (s *Server) handleOverview(w http.ResponseWriter, r *http.Request) {
	players, err := s.players(r.Context())
	if err != nil {
		s.fail(w, "overview", err)
		return
	}

	var online, playing int
	for _, p := range players {
		switch p.State {
		case "PLAYING":
			playing++
			online++
		case "ONLINE":
			online++
		}
	}

	fests, err := s.festRows(r.Context())
	if err != nil {
		s.fail(w, "overview fests", err)
		return
	}
	var live *festRow
	for i := range fests {
		if fests[i].Enabled && fests[i].Phase != "none" && fests[i].Phase != "closed" {
			live = &fests[i]
			break
		}
	}

	// Matchmaking's own "in queue" stat tile is populated separately, from
	// /api/matchmaking (see handleMatchmaking and dashboard.js's
	// renderMatchmaking) rather than folded in here: that endpoint already
	// has to say "not configured" honestly rather than a zero that looks
	// like "nobody is queuing", and one source for that answer is simpler
	// than keeping two in sync.
	writeJSON(w, map[string]any{
		"players": len(players),
		"online":  online,
		"playing": playing,
		"fest":    live,
		// Null when the game server is not reporting, NOT zero. A zero reads as
		// "nobody is queuing", which is a different fact from "cmd/npln is not
		// running or redis.url is unset" -- and only one of them is worth acting
		// on. The page renders the two differently.
		"matchmaking": s.matchmakingGauge(r.Context()),
		"seed":        s.seedStatus(),
	})
}

// matchmakingGauge reads the game server's live queue gauge. Named apart from
// the Server.matchmaking field (the admin-API client the matchmaking
// controls panel uses) -- same concept, different purpose.
//
// nil when there is no bus, no snapshot, or the last snapshot has gone stale.
// The dashboard is a separate process from cmd/npln, so this is the seam between
// them -- and it is a gauge rather than an event because a freshly-loaded page
// needs the current value, not the next change.
func (s *Server) matchmakingGauge(ctx context.Context) map[string]any {
	if s.bus == nil {
		return nil
	}
	stats, ok, err := s.bus.Stats(ctx)
	if err != nil {
		// Logged, not surfaced: a cosmetic tile must not take the page down.
		s.log.Debug("dashboard: read matchmaking stats", "error", err)
		return nil
	}
	if !ok {
		return nil
	}

	waiting := 0
	for _, n := range stats.Matchmaking {
		waiting += n
	}
	return map[string]any{
		"waiting":  waiting,
		"byMode":   stats.Matchmaking,
		"reportAt": stats.At.UTC().Format(time.RFC3339),
	}
}

// ---- splatfests -----------------------------------------------------------

type festRow struct {
	ID        string   `json:"id"`
	Resource  string   `json:"resource"`
	Revision  string   `json:"revision"`
	StartAt   string   `json:"startAt"`
	Regions   []string `json:"regions"`
	Enabled   bool     `json:"enabled"`
	Phase     string   `json:"phase"`
	HasKeys   bool     `json:"hasKeys"`
	Timetable struct {
		Open  string `json:"open"`
		Start string `json:"start"`
		Mid   string `json:"mid"`
		End   string `json:"end"`
		Close string `json:"close"`
	} `json:"timetable"`
}

func (s *Server) festRows(ctx context.Context) ([]festRow, error) {
	rows, err := s.store.ListFests(ctx)
	if err != nil {
		return nil, err
	}
	now := s.now()

	out := make([]festRow, 0, len(rows))
	for _, r := range rows {
		f := toyohr.Fest{
			ID: r.ID, Resource: r.Resource, Revision: r.Revision,
			Start: r.StartAt.UTC(), Regions: r.Regions,
		}
		tt := f.Timetable()

		row := festRow{
			ID: r.ID, Resource: r.Resource, Revision: r.Revision,
			StartAt: r.StartAt.UTC().Format(time.RFC3339),
			Regions: r.Regions, Enabled: r.Enabled,
			Phase:   f.PhaseAt(now).String(),
			HasKeys: len(r.PackKeys) > 0,
		}
		row.Timetable.Open = tt.Open.Format(time.RFC3339)
		row.Timetable.Start = tt.Start.Format(time.RFC3339)
		row.Timetable.Mid = tt.Mid.Format(time.RFC3339)
		row.Timetable.End = tt.End.Format(time.RFC3339)
		row.Timetable.Close = tt.Close.Format(time.RFC3339)
		out = append(out, row)
	}
	return out, nil
}

func (s *Server) handleFests(w http.ResponseWriter, r *http.Request) {
	rows, err := s.festRows(r.Context())
	if err != nil {
		s.fail(w, "list fests", err)
		return
	}
	writeJSON(w, rows)
}

func (s *Server) handleCreateFest(w http.ResponseWriter, r *http.Request) {
	var body struct {
		StartAt  string   `json:"startAt"`
		Regions  []string `json:"regions"`
		Resource string   `json:"resource"`
		Revision string   `json:"revision"`
		Enabled  bool     `json:"enabled"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}

	start := s.now().UTC()
	if body.StartAt != "" {
		parsed, err := time.Parse(time.RFC3339, body.StartAt)
		if err != nil {
			http.Error(w, "startAt must be RFC3339", http.StatusBadRequest)
			return
		}
		start = parsed
	} else {
		start = toyohr.NextFestStart(start)
	}

	f := toyohr.NewFest(start, body.Regions)
	// An operator-supplied resource and revision win: once a pack exists, its
	// summary.yml is the authority and the announcement has to match it exactly
	// or the game answers BcatInvalid.
	if body.Resource != "" {
		f.Resource = strings.ToLower(strings.TrimSpace(body.Resource))
	}
	if body.Revision != "" {
		f.Revision = strings.ToLower(strings.TrimSpace(body.Revision))
	}

	if err := s.fests.SaveFest(r.Context(), f, body.Enabled, nil); err != nil {
		// These errors explain themselves -- slot alignment, resource case --
		// and are exactly what the operator needs to see, so they are passed
		// through rather than flattened into "invalid".
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	s.log.Info("dashboard: fest saved", "fest", f.ID, "enabled", body.Enabled)
	writeJSON(w, map[string]any{"id": f.ID, "resource": f.Resource, "revision": f.Revision})
}

func (s *Server) handleSetFestEnabled(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	var body struct {
		Enabled bool `json:"enabled"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}

	row, err := s.store.GetFest(r.Context(), id)
	if err != nil {
		if errors.Is(err, db.ErrNotFound) {
			http.Error(w, "no such fest", http.StatusNotFound)
			return
		}
		s.fail(w, "load fest", err)
		return
	}
	row.Enabled = body.Enabled
	if err := s.store.UpsertFest(r.Context(), row); err != nil {
		s.fail(w, "save fest", err)
		return
	}
	s.log.Info("dashboard: fest toggled", "fest", id, "enabled", body.Enabled)
	writeJSON(w, map[string]any{"id": id, "enabled": body.Enabled})
}

func (s *Server) handleDeleteFest(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if err := s.store.DeleteFest(r.Context(), id); err != nil {
		s.fail(w, "delete fest", err)
		return
	}
	s.log.Info("dashboard: fest deleted", "fest", id)
	w.WriteHeader(http.StatusNoContent)
}

// ---- match history ----------------------------------------------------------

// matchRow is one row of the match history. Player and host ids are left for
// the page to resolve against the players list it already has, the same way
// it already resolves the live matchmaking sessions view -- one lookup table
// on the client rather than a join on every history row.
type matchRow struct {
	ID         string   `json:"id"`
	Mode       string   `json:"mode"`
	HostUserID string   `json:"hostUserId"`
	PlayerIDs  []string `json:"playerIds"`
	Forced     bool     `json:"forced"`
	CreatedAt  string   `json:"createdAt"`
	// Game is set for a match that is not Splatoon 3's: the history is
	// shared, and "mode 12" alone does not say which game it was.
	Game string `json:"game,omitempty"`
}

// splatoon2Tenant is the tenant cmd/nex files Splatoon 2's matches under
// (splatoon2.Tenant; not imported, to keep the game server out of this
// binary).
const splatoon2Tenant = "splatoon2"

func (s *Server) handleMatches(w http.ResponseWriter, r *http.Request) {
	matches, err := s.store.ListMatches(r.Context(), db.DefaultMatchListLimit)
	if err != nil {
		s.fail(w, "list matches", err)
		return
	}
	out := make([]matchRow, 0, len(matches))
	for _, m := range matches {
		row := matchRow{
			ID: m.ID, Mode: m.Mode, HostUserID: m.HostUserID,
			PlayerIDs: m.PlayerIDs, Forced: m.Forced,
			CreatedAt: m.CreatedAt.UTC().Format(time.RFC3339),
		}
		if m.Tenant == splatoon2Tenant {
			row.Game = "Splatoon 2"
		}
		out = append(out, row)
	}
	writeJSON(w, out)
}

// ---- live events ----------------------------------------------------------

// handleEvents streams presence changes to the page.
//
// Server-sent events rather than WebSockets: the traffic is one-way, SSE
// reconnects on its own, and it survives an ordinary HTTP proxy without any
// upgrade handling. ROADMAP §2.6 asks for one or the other; this is the one
// that needs no client library.
func (s *Server) handleEvents(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	// Without this an nginx in front buffers the stream and the page updates in
	// bursts, or not at all.
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	flusher.Flush()

	if s.bus == nil {
		// No bus configured. Say so on the stream rather than holding a
		// connection open that will never carry anything.
		fmt.Fprintf(w, "event: nobus\ndata: {}\n\n")
		flusher.Flush()
		return
	}

	events, err := s.bus.Subscribe(r.Context())
	if err != nil {
		s.log.Error("dashboard: subscribe", "error", err)
		return
	}

	// A heartbeat, because an idle SSE connection is indistinguishable from a
	// dead one to every proxy between here and the browser.
	ticker := time.NewTicker(20 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-r.Context().Done():
			return
		case <-ticker.C:
			fmt.Fprint(w, ": keep-alive\n\n")
			flusher.Flush()
		case e, ok := <-events:
			if !ok {
				return
			}
			payload, err := json.Marshal(e)
			if err != nil {
				continue
			}
			fmt.Fprintf(w, "event: presence\ndata: %s\n\n", payload)
			flusher.Flush()
		}
	}
}

// ---- helpers --------------------------------------------------------------

func (s *Server) fail(w http.ResponseWriter, what string, err error) {
	s.log.Error("dashboard: "+what, "error", err)
	http.Error(w, "internal error", http.StatusInternalServerError)
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	_ = json.NewEncoder(w).Encode(v)
}

func constantTimeEqual(a, b string) bool {
	if len(a) != len(b) {
		// Lengths differing is already observable through timing; what matters
		// is that a same-length wrong guess reveals nothing about which byte
		// was wrong.
		return false
	}
	var diff byte
	for i := 0; i < len(a); i++ {
		diff |= a[i] ^ b[i]
	}
	return diff == 0
}
