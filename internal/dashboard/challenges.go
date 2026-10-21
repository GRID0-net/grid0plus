package dashboard

import (
	"crypto/rand"
	"encoding/json"
	"fmt"
	"net/http"
	"regexp"
	"sort"
	"time"

	"github.com/n-popescu/grid0plus/internal/db"
	"github.com/n-popescu/grid0plus/internal/toyohr"
)

// A Challenge's event type is the game's own LeagueTypeInfo row name. The list
// is game data and is not shipped here; the operator types it.
var eventTypeIDPattern = regexp.MustCompile(`^[A-Za-z0-9_]{1,64}$`)

const (
	challengeSlot     = 2 * time.Hour
	maxChallengeSlots = 24
)

type challengeRow struct {
	ID          string          `json:"id"`
	EventTypeID string          `json:"eventTypeId"`
	Rule        int32           `json:"rule"`
	Stages      []challengeName `json:"stages"`
	Slots       []db.LeagueSlot `json:"slots"`
	Status      string          `json:"status"`
}

type challengeName struct {
	ID   int32  `json:"id"`
	Name string `json:"name"`
}

func (s *Server) handleChallenges(w http.ResponseWriter, r *http.Request) {
	events, err := s.store.ListLeagueEvents(r.Context())
	if err != nil {
		s.fail(w, "list challenges", err)
		return
	}
	now := time.Now()
	rows := make([]challengeRow, 0, len(events))
	for _, e := range events {
		row := challengeRow{ID: e.ID, EventTypeID: e.EventTypeID, Rule: e.Rule, Slots: e.Slots, Status: "upcoming"}
		for _, id := range e.Stages {
			st, _ := toyohr.VsStageByID(id)
			row.Stages = append(row.Stages, challengeName{ID: id, Name: st.Name})
		}
		for _, sl := range e.Slots {
			if !now.Before(sl.StartAt) && now.Before(sl.EndAt) {
				row.Status = "live"
			}
		}
		if n := len(e.Slots); n > 0 && !e.Slots[n-1].EndAt.After(now) {
			row.Status = "ended"
		}
		rows = append(rows, row)
	}
	writeJSON(w, rows)
}

// handleChallengeReference fills the form's stage list.
func (s *Server) handleChallengeReference(w http.ResponseWriter, _ *http.Request) {
	stages := make([]challengeName, 0, len(toyohr.VsStages))
	for _, st := range toyohr.VsStages {
		stages = append(stages, challengeName{ID: st.ID, Name: st.Name})
	}
	writeJSON(w, map[string]any{"stages": stages})
}

func (s *Server) handleCreateChallenge(w http.ResponseWriter, r *http.Request) {
	var body struct {
		EventTypeID string   `json:"eventTypeId"`
		Rule        int32    `json:"rule"`
		Stages      []int32  `json:"stages"`
		Slots       []string `json:"slots"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	e, err := newChallenge(body.EventTypeID, body.Rule, body.Stages, body.Slots)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if err := s.store.UpsertLeagueEvent(r.Context(), e); err != nil {
		s.fail(w, "save challenge", err)
		return
	}
	s.log.Info("dashboard: challenge scheduled", "challenge", e.ID, "event_type", e.EventTypeID, "slots", len(e.Slots))
	w.WriteHeader(http.StatusCreated)
}

func (s *Server) handleDeleteChallenge(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if err := s.store.DeleteLeagueEvent(r.Context(), id); err != nil {
		s.fail(w, "delete challenge", err)
		return
	}
	s.log.Info("dashboard: challenge deleted", "challenge", id)
	w.WriteHeader(http.StatusNoContent)
}

// challengeID is 20 lowercase letters and digits, the shape of a captured
// leagueSchedules id; slot names are "<id>_N".
func challengeID() (string, error) {
	const alphabet = "abcdefghijklmnopqrstuvwxyz0123456789"
	b := make([]byte, 20)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	for i := range b {
		b[i] = alphabet[int(b[i])%len(alphabet)]
	}
	return string(b), nil
}

// newChallenge validates what the form sent. Slots are two-hour windows on
// the game's slot boundary, like every other rotation it shows.
func newChallenge(eventType string, rule int32, stages []int32, slotStarts []string) (db.LeagueEvent, error) {
	if !eventTypeIDPattern.MatchString(eventType) {
		return db.LeagueEvent{}, fmt.Errorf("event type must be the game's event id: letters, digits and _")
	}
	if rule < 0 || rule > 4 {
		return db.LeagueEvent{}, fmt.Errorf("rule must be 0 (Turf War) to 4")
	}
	if len(stages) != 2 || stages[0] == stages[1] {
		return db.LeagueEvent{}, fmt.Errorf("pick two different stages")
	}
	for _, id := range stages {
		if _, ok := toyohr.VsStageByID(id); !ok {
			return db.LeagueEvent{}, fmt.Errorf("stage %d is not a known stage", id)
		}
	}
	if len(slotStarts) == 0 || len(slotStarts) > maxChallengeSlots {
		return db.LeagueEvent{}, fmt.Errorf("give 1 to %d time slots", maxChallengeSlots)
	}
	seen := map[int64]bool{}
	var slots []db.LeagueSlot
	for _, raw := range slotStarts {
		start, err := time.Parse(time.RFC3339, raw)
		if err != nil {
			return db.LeagueEvent{}, fmt.Errorf("slot %q is not a time", raw)
		}
		start = start.UTC()
		if !toyohr.IsSlotAligned(start) {
			return db.LeagueEvent{}, fmt.Errorf("slot %s is not on a two-hour boundary (00:00, 02:00, ... UTC)", start.Format(time.RFC3339))
		}
		if seen[start.Unix()] {
			continue
		}
		seen[start.Unix()] = true
		slots = append(slots, db.LeagueSlot{StartAt: start, EndAt: start.Add(challengeSlot)})
	}
	sort.Slice(slots, func(i, j int) bool { return slots[i].StartAt.Before(slots[j].StartAt) })
	id, err := challengeID()
	if err != nil {
		return db.LeagueEvent{}, err
	}
	return db.LeagueEvent{
		ID:          id,
		EventTypeID: eventType,
		Rule:        rule,
		Stages:      stages,
		Slots:       slots,
	}, nil
}
