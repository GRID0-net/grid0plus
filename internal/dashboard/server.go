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

	// SeedDir is where `switchnetctl fest build` writes its seed folders. When
	// set, the dashboard serves them to the console-side installer -- see
	// seed.go and switch/bcat-installer/.
	SeedDir string

	// PresenceTTL is how long a reported state stays current. The dashboard
	// applies the same rule BAAS does, so the two never disagree about who is
	// online — a dashboard that says somebody is playing while their friends
	// see them offline is worse than no dashboard.
	PresenceTTL time.Duration

	// Token, when set, is required as a bearer token or ?token= on every
	// request.
	//
	// Nothing in phase 1 authenticates a person — every credential in the stack
	// belongs to a console — so the dashboard has no user accounts to check
	// against. A shared token is the honest minimum: it keeps the fest controls
	// off the open network without pretending to be an identity system.
	Token string

	Now func() time.Time
}

// Server serves the dashboard.
type Server struct {
	store db.Store
	bus   presencebus.Bus
	log   *slog.Logger
	fests *toyohr.Service

	tenant      string
	seedDir     string
	presenceTTL time.Duration
	token       string
	now         func() time.Time

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
		}),
		tenant:      opts.Tenant,
		seedDir:     opts.SeedDir,
		presenceTTL: opts.PresenceTTL,
		token:       opts.Token,
		now:         opts.Now,
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
	mux.HandleFunc("GET /api/fests", s.guard(s.handleFests))
	mux.HandleFunc("POST /api/fests", s.guard(s.handleCreateFest))
	mux.HandleFunc("POST /api/fests/{id}/enabled", s.guard(s.handleSetFestEnabled))
	mux.HandleFunc("DELETE /api/fests/{id}", s.guard(s.handleDeleteFest))
	mux.HandleFunc("GET /api/events", s.guard(s.handleEvents))

	// The BCAT delivery cache, for the console-side installer. Behind the same
	// token as everything else; the installer is configured with it.
	mux.HandleFunc("GET /api/seed/manifest", s.guard(s.handleSeedManifest))
	mux.HandleFunc("GET /api/seed/file/{path...}", s.guard(s.handleSeedFile))

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
	presences, err := s.store.GetPresences(ctx, ids)
	if err != nil {
		return nil, err
	}

	now := s.now()
	out := make([]Player, 0, len(users))
	for _, u := range users {
		p := presences[u.ID]
		state, appID := s.resolvePresence(p, now)

		name := u.Nickname
		if name == "" {
			name = u.ID
		}
		row := Player{ID: u.ID, Nickname: name, State: state, AppID: appID}
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

// resolvePresence applies the same rule internal/baas does.
//
// Deliberately duplicated in shape rather than imported, because BAAS's version
// is a handler-level concern there — but the RULE must not drift. A dashboard
// that disagrees with the friends applet about who is online sends the operator
// hunting for a bug that is only in the dashboard.
//
//	fresh report?           use it exactly, including OFFLINE
//	otherwise seen recently? ONLINE, with the app fields cleared
//	otherwise                OFFLINE
func (s *Server) resolvePresence(p db.Presence, now time.Time) (state, appID string) {
	if !p.ReportedAt.IsZero() && now.Sub(p.ReportedAt) < s.presenceTTL {
		if p.State == "PLAYING" {
			return "PLAYING", p.AppID
		}
		// Not playing: the app fields are cleared. A console that closes a game
		// reports ONLINE without clearing them, so keeping them would leave the
		// dashboard showing a game that is no longer running.
		return p.State, ""
	}
	if !p.LastSeenAt.IsZero() && now.Sub(p.LastSeenAt) < s.presenceTTL {
		// Request traffic is proof of life. Without this branch a console
		// sitting in the Home Menu -- which reports nothing but keeps polling --
		// reads as offline.
		return "ONLINE", ""
	}
	return "OFFLINE", ""
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

	writeJSON(w, map[string]any{
		"players": len(players),
		"online":  online,
		"playing": playing,
		"fest":    live,
		// Matchmaking is not wired to the gateway yet, so this is honestly
		// absent rather than a zero that looks like "nobody is queuing".
		"matchmaking": nil,
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
