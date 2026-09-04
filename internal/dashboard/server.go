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
	presence presence.Resolver
	token    string
	now      func() time.Time

	// matchmaking is nil when the controls are not configured.
	matchmaking *matchmakingClient

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
		store:  opts.Store,
		bus:    opts.Bus,
		log:    opts.Log,
		fests:  toyohr.NewService(opts.Store, opts.Log, opts.Tenant, opts.Now),
		tenant: opts.Tenant,
		presence: presence.Resolver{
			Store:      opts.Store,
			ReportTTL:  opts.PresenceTTL,
			ConsoleTTL: opts.ConsolePresenceTTL,
		},
		token:       opts.Token,
		now:         opts.Now,
		matchmaking: newMatchmakingClient(opts.NPLNAdminURL, opts.NPLNAdminToken),
		addresses:   opts.Addresses,
		probeClient: &http.Client{Timeout: probeTimeout},
		tmpl:        tmpl,
	}, nil
}

// Handler returns the HTTP handler.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()

	mux.Handle("GET /assets/", http.FileServer(http.FS(assets)))

	mux.HandleFunc("GET /{$}", s.guard(s.handleIndex))
	mux.HandleFunc("GET /api/overview", s.guard(s.handleOverview))
	mux.HandleFunc("GET /api/players", s.guard(s.handlePlayers))
	mux.HandleFunc("GET /api/players/{id}", s.guard(s.handlePlayerDetail))
	mux.HandleFunc("GET /api/fests", s.guard(s.handleFests))
	mux.HandleFunc("POST /api/fests", s.guard(s.handleCreateFest))
	mux.HandleFunc("POST /api/fests/{id}/enabled", s.guard(s.handleSetFestEnabled))
	mux.HandleFunc("DELETE /api/fests/{id}", s.guard(s.handleDeleteFest))
	mux.HandleFunc("GET /api/coop-shifts", s.guard(s.handleCoopShifts))
	mux.HandleFunc("POST /api/coop-shifts", s.guard(s.handleCreateCoopShift))
	mux.HandleFunc("POST /api/coop-shifts/generate", s.guard(s.handleGenerateCoopShifts))
	mux.HandleFunc("DELETE /api/coop-shifts/{id}", s.guard(s.handleDeleteCoopShift))
	mux.HandleFunc("GET /api/coop-reference", s.guard(s.handleCoopReference))
	mux.HandleFunc("GET /api/health", s.guard(s.handleHealth))
	mux.HandleFunc("GET /api/matchmaking", s.guard(s.handleMatchmaking))
	mux.HandleFunc("GET /api/matches", s.guard(s.handleMatches))
	mux.HandleFunc("POST /api/matchmaking/modes/{mode}/start", s.guard(s.handleForceStart))
	mux.HandleFunc("POST /api/matchmaking/modes/{mode}/size", s.guard(s.handleSetModeSize))
	mux.HandleFunc("POST /api/matchmaking/tickets/{id}/cancel", s.guard(s.handleCancelTicket))

	mux.HandleFunc("GET /api/debug/stats", s.guard(s.handleDebugStats))
	mux.HandleFunc("GET /api/debug/accounts/{id}", s.guard(s.handleDebugAccount))
	mux.HandleFunc("POST /api/debug/accounts/{id}/deleted", s.guard(s.handleDebugSetAccountDeleted))
	mux.HandleFunc("POST /api/debug/accounts/{id}/device-accounts/{deviceAccountId}/revoke-tokens", s.guard(s.handleDebugRevokeDeviceAccountTokens))
	mux.HandleFunc("POST /api/debug/accounts/{id}/device-accounts/{deviceAccountId}/deleted", s.guard(s.handleDebugSetDeviceAccountDeleted))

	mux.HandleFunc("GET /api/events", s.guard(s.handleEvents))

	return mux
}

// guard applies the shared token, when one is configured.
func (s *Server) guard(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if s.token == "" {
			next(w, r)
			return
		}
		presented := r.URL.Query().Get("token")
		if h := r.Header.Get("Authorization"); presented == "" && h != "" {
			if scheme, tok, ok := strings.Cut(h, " "); ok && strings.EqualFold(scheme, "Bearer") {
				presented = tok
			}
		}
		// Constant-time compare, for the same reason internal/baas compares a
		// device-account password that way: a token is a secret, and a
		// length-dependent comparison leaks it a byte at a time.
		if !constantTimeEqual(presented, s.token) {
			http.Error(w, "unauthorised", http.StatusUnauthorized)
			return
		}
		next(w, r)
	}
}

func (s *Server) handleIndex(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := s.tmpl.ExecuteTemplate(w, "index.html", map[string]any{
		"Token": r.URL.Query().Get("token"),
	}); err != nil {
		s.log.Error("dashboard: render", "error", err)
	}
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
	CreatedAt      string                `json:"createdAt"`
	LogoutAt       string                `json:"logoutAt,omitempty"`
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
	// Online friends first, then by name -- the same ordering rationale the
	// players list itself uses: what the operator is looking for is who is
	// around, not an alphabetised roster.
	rank := map[string]int{"PLAYING": 0, "ONLINE": 1, "OFFLINE": 2}
	sort.SliceStable(detail.Friends, func(i, j int) bool {
		if ri, rj := rank[detail.Friends[i].State], rank[detail.Friends[j].State]; ri != rj {
			return ri < rj
		}
		return strings.ToLower(detail.Friends[i].Nickname) < strings.ToLower(detail.Friends[j].Nickname)
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
	})
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
}

func (s *Server) handleMatches(w http.ResponseWriter, r *http.Request) {
	matches, err := s.store.ListMatches(r.Context(), db.DefaultMatchListLimit)
	if err != nil {
		s.fail(w, "list matches", err)
		return
	}
	out := make([]matchRow, 0, len(matches))
	for _, m := range matches {
		out = append(out, matchRow{
			ID: m.ID, Mode: m.Mode, HostUserID: m.HostUserID,
			PlayerIDs: m.PlayerIDs, Forced: m.Forced,
			CreatedAt: m.CreatedAt.UTC().Format(time.RFC3339),
		})
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
