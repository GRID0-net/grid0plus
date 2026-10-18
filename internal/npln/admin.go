package npln

import (
	"crypto/subtle"
	"encoding/json"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/n-popescu/grid0plus/internal/matchmaking"
)

// The matchmaking admin API.
//
// The queue lives in THIS process and the dashboard is a different one, so the
// operator's controls need a way across that boundary. An ordinary HTTP API
// rather than a message on the presence bus, for one reason: these are
// questions with answers. "Start a match now" has to come back with the match
// it started, or with "nobody is queuing" -- a fire-and-forget publish would
// leave the page guessing, which is exactly the silent-failure shape
// internal/dashboard's README already warns about for presence.
//
// It is a SEPARATE listener from the gRPC gateway and it is not one of the
// Nintendo hostnames. No console ever reaches it; it is meant for the loopback
// interface or a private network, and `npln.admin_token` is what guards it.

// AdminHandler serves the matchmaking controls.
type AdminHandler struct {
	queue *matchmaking.Queue
	log   *slog.Logger
	token string

	// srv is used only to record a forced match to the history -- see
	// forceStart. Everything else here goes through queue directly, which
	// predates this field and there is no reason to churn it.
	srv *Server
}

// AdminHandler returns the operator API for this server's queue, or nil when no
// admin token is configured.
//
// Nil rather than an unguarded handler: this API can start matches and change
// how many players one takes, and there is no version of it that is safe to
// serve to anyone who can reach the port. An operator who has not set a token
// has not opted in.
func (s *Server) AdminHandler() *AdminHandler {
	if s.adminToken == "" {
		return nil
	}
	return &AdminHandler{queue: s.queue, log: s.log, token: s.adminToken, srv: s}
}

// Handler returns the routed handler.
func (h *AdminHandler) Handler() http.Handler {
	mux := http.NewServeMux()

	mux.HandleFunc("GET /matchmaking", h.guard(h.snapshot))
	mux.HandleFunc("POST /matchmaking/modes/{mode}/start", h.guard(h.forceStart))
	mux.HandleFunc("POST /matchmaking/modes/{mode}/size", h.guard(h.setSize))
	mux.HandleFunc("POST /matchmaking/tickets/{id}/cancel", h.guard(h.cancelTicket))

	return mux
}

// guard requires the shared token on every route.
func (h *AdminHandler) guard(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		presented := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		if subtle.ConstantTimeCompare([]byte(presented), []byte(h.token)) != 1 {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		next(w, r)
	}
}

// ---- responses ------------------------------------------------------------

// These are GRID0+'s own shapes, not NPLN's. Nothing Nintendo-facing reads
// them, so they are written for the page that does: durations already in
// seconds, and names rather than resource paths.

type adminSnapshot struct {
	DefaultSize int `json:"defaultSize"`
	// The grace periods in force, in seconds; SIGHUP reloads them.
	VersusGraceSeconds int            `json:"versusGraceSeconds"`
	CoopGraceSeconds   int            `json:"coopGraceSeconds"`
	Modes              []adminMode    `json:"modes"`
	Sessions           []adminSession `json:"sessions"`
	Waiting            int            `json:"waiting"`
	Started            *adminSession  `json:"started,omitempty"`
	Formed             []adminSession `json:"formed,omitempty"`
}

type adminMode struct {
	Mode       string        `json:"mode"`
	Size       int           `json:"size"`
	Overridden bool          `json:"overridden"`
	Waiting    []adminTicket `json:"waiting"`
}

type adminTicket struct {
	ID     string `json:"id"`
	UserID string `json:"userId"`
	// WaitingForSeconds is a number rather than a Go duration string, because
	// the page formats it and "1m30s" would have to be parsed back apart.
	WaitingForSeconds int `json:"waitingForSeconds"`
	// Placing is true once this ticket's mode has enough players and is
	// counting down its grace period toward forming a match.
	Placing bool `json:"placing"`
}

type adminSession struct {
	ID        string    `json:"id"`
	Mode      string    `json:"mode"`
	Players   []string  `json:"players"`
	Host      string    `json:"host"`
	CreatedAt time.Time `json:"createdAt"`
	Phase     string    `json:"phase"`
	// Forced is true when this match has fewer players than its mode CURRENTLY
	// wants -- either it was force-started, or the mode was resized afterwards.
	// The page says "undersized" rather than "forced" for that reason.
	//
	// Worth surfacing at all because a match that behaves oddly on a console is
	// a different investigation depending on whether it was full.
	Forced bool `json:"forced"`
}

// snapshotBody renders the queue, and returns the per-mode sizes alongside it so
// a caller rendering a session it just created marks it undersized against the
// same numbers.
func (h *AdminHandler) snapshotBody() (adminSnapshot, map[string]int) {
	snap := h.queue.Snapshot()

	out := adminSnapshot{
		DefaultSize:        snap.DefaultSize,
		VersusGraceSeconds: int(snap.VersusGrace.Seconds()),
		CoopGraceSeconds:   int(snap.CoopGrace.Seconds()),
		Modes:              make([]adminMode, 0, len(snap.Modes)),
		Sessions:           make([]adminSession, 0, len(snap.Sessions)),
	}

	sizeOf := map[string]int{}
	for _, m := range snap.Modes {
		sizeOf[m.Mode] = m.Size

		mode := adminMode{
			Mode:       m.Mode,
			Size:       m.Size,
			Overridden: m.Overridden,
			Waiting:    make([]adminTicket, 0, len(m.Waiting)),
		}
		for _, w := range m.Waiting {
			mode.Waiting = append(mode.Waiting, adminTicket{
				ID:                w.ID,
				UserID:            w.UserID,
				WaitingForSeconds: int(w.WaitingFor.Seconds()),
				Placing:           w.Placing,
			})
			out.Waiting++
		}
		out.Modes = append(out.Modes, mode)
	}

	for _, s := range snap.Sessions {
		body := sessionBody(s, sizeOf)
		if h.srv != nil && h.srv.sessionPhase != nil {
			body.Phase = h.srv.sessionPhase(s.ID, len(s.Players))
		}
		// Retention keeps Again! working; it does not make a completed battle
		// live. A later reopen or kickoff makes this session visible again.
		if body.Phase == "results" {
			continue
		}
		out.Sessions = append(out.Sessions, body)
	}
	return out, sizeOf
}

// SetSessionPhaseReader wires Gamesync into the operator API before serving.
func (s *Server) SetSessionPhaseReader(reader func(string, int) string) {
	s.sessionPhase = reader
}

// sessionBody renders a session, working out whether it was undersized.
//
// Inferred from the player count rather than recorded on the session: a mode's
// size can be changed after a match forms, and what matters to somebody reading
// the page is whether this match is smaller than the mode currently wants.
func sessionBody(s matchmaking.Session, sizeOf map[string]int) adminSession {
	forced := false
	if size, ok := sizeOf[s.Mode]; ok && len(s.Players) < size {
		forced = true
	}
	// A session every player has left has a nil slice, which would encode as
	// null and break the page's s.players.length.
	players := s.Players
	if players == nil {
		players = []string{}
	}
	return adminSession{
		ID:        s.ID,
		Mode:      s.Mode,
		Players:   players,
		Host:      s.Host,
		CreatedAt: s.CreatedAt,
		Forced:    forced,
	}
}

// ---- handlers -------------------------------------------------------------

func (h *AdminHandler) snapshot(w http.ResponseWriter, _ *http.Request) {
	body, _ := h.snapshotBody()
	writeAdminJSON(w, body)
}

// forceStart forms a match in a mode from whoever is waiting.
func (h *AdminHandler) forceStart(w http.ResponseWriter, r *http.Request) {
	mode := r.PathValue("mode")

	session, ok, err := h.queue.ForceMatch(mode)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if !ok {
		// Not an error: asking to start an empty queue is a no-op, and the page
		// says so rather than showing a failure.
		http.Error(w, "nobody is queuing for "+mode, http.StatusConflict)
		return
	}

	h.log.Info("npln: operator forced a match",
		"mode", mode, "session", session.ID,
		"players", len(session.Players), "host", session.Host)
	h.srv.recordMatch(r.Context(), session, true)

	body, sizeOf := h.snapshotBody()
	started := sessionBody(session, sizeOf)
	body.Started = &started
	writeAdminJSON(w, body)
}

// setSize changes how many players a mode takes.
func (h *AdminHandler) setSize(w http.ResponseWriter, r *http.Request) {
	mode := r.PathValue("mode")

	var req struct {
		Players int `json:"players"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<10)).Decode(&req); err != nil {
		http.Error(w, "malformed request", http.StatusBadRequest)
		return
	}

	sessions, err := h.queue.SetModeSize(mode, req.Players)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	h.log.Info("npln: operator set a mode size",
		"mode", mode, "players", req.Players, "matches_formed", len(sessions))

	body, sizeOf := h.snapshotBody()
	for _, s := range sessions {
		body.Formed = append(body.Formed, sessionBody(s, sizeOf))
	}
	writeAdminJSON(w, body)
}

// cancelTicket drops one ticket out of the queue.
//
// For a console that queued and went away: its ticket keeps a slot until the
// TTL expires, and on a queue of three that is the difference between a match
// and no match.
func (h *AdminHandler) cancelTicket(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")

	if !h.queue.Cancel(id) {
		// Already matched, already cancelled, or never existed. All three are
		// "there is nothing to cancel", and telling them apart would say more
		// about other people's tickets than the page needs.
		http.Error(w, "no ticket to cancel", http.StatusConflict)
		return
	}

	h.log.Info("npln: operator cancelled a ticket", "ticket", id)
	body, _ := h.snapshotBody()
	writeAdminJSON(w, body)
}

func writeAdminJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}
