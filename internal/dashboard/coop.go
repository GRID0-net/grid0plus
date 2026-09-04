package dashboard

import (
	"context"
	"encoding/json"
	"net/http"
	"time"

	"github.com/n-popescu/switchnet/internal/toyohr"
)

// ---- Salmon Run / Big Run schedule -----------------------------------------
//
// The same split the Splatfest controls make: internal/toyohr's CoopShift is
// the operator-facing domain type and Validate is where a bad stage/weapon/
// boss id gets explained, so this file only ever builds one and hands it to
// s.fests -- it does not re-check anything Validate already checks.

// coopShiftRow is one shift, with the names its ids resolve to precomputed --
// the same reason festRow carries a Timetable rather than making the page
// recompute one from Start: a page has no access to coop_ids.go's tables,
// and hardcoding a second copy of them in JavaScript is exactly the kind of
// drift CLAUDE.md's "hostnames live in exactly one place" reasoning warns
// about, just for a different table.
type coopShiftRow struct {
	ID             string   `json:"id"`
	Kind           string   `json:"kind"`
	StageID        int32    `json:"stageId"`
	StageName      string   `json:"stageName"`
	Boss           string   `json:"boss"`
	Weapons        []int64  `json:"weapons"`
	WeaponNames    []string `json:"weaponNames"`
	KumaWeaponID   int64    `json:"kumaWeaponId"`
	KumaWeaponName string   `json:"kumaWeaponName"`
	RewardGearID   int32    `json:"rewardGearId"`
	Rewards        []int64  `json:"rewards"`
	StartAt        string   `json:"startAt"`
	EndAt          string   `json:"endAt"`
	// Status is "upcoming", "live" or "ended" -- ended shifts are still
	// listed here (unlike SelectCoopSchedules, which drops them so the
	// console is not told about a closed shift) so the operator can see what
	// already ran rather than have it vanish the moment it does.
	Status string `json:"status"`
}

func (s *Server) coopShiftRows(ctx context.Context) ([]coopShiftRow, error) {
	shifts, err := s.fests.ListCoopShiftsForOperator(ctx)
	if err != nil {
		return nil, err
	}
	now := s.now()

	out := make([]coopShiftRow, 0, len(shifts))
	for _, c := range shifts {
		stage, _ := toyohr.CoopStageByID(c.StageID)
		kuma, _ := toyohr.CoopKumaWeaponByID(c.KumaWeaponID)

		weaponNames := make([]string, 0, len(c.Weapons))
		for _, id := range c.Weapons {
			if id == 0 {
				continue
			}
			if w, ok := toyohr.CoopWeaponByID(id); ok {
				weaponNames = append(weaponNames, w.Name)
			}
		}

		status := "upcoming"
		switch {
		case !c.End.After(now):
			status = "ended"
		case !c.Start.After(now):
			status = "live"
		}

		out = append(out, coopShiftRow{
			ID: c.ID(), Kind: c.Kind.String(),
			StageID: c.StageID, StageName: stage.Name,
			Boss:           c.Boss,
			Weapons:        append([]int64(nil), c.Weapons[:]...),
			WeaponNames:    weaponNames,
			KumaWeaponID:   c.KumaWeaponID,
			KumaWeaponName: kuma.Name,
			RewardGearID:   c.RewardGearID,
			Rewards:        append([]int64(nil), c.Rewards...),
			StartAt:        c.Start.UTC().Format(time.RFC3339),
			EndAt:          c.End.UTC().Format(time.RFC3339),
			Status:         status,
		})
	}
	return out, nil
}

func (s *Server) handleCoopShifts(w http.ResponseWriter, r *http.Request) {
	rows, err := s.coopShiftRows(r.Context())
	if err != nil {
		s.fail(w, "list coop shifts", err)
		return
	}
	writeJSON(w, rows)
}

func (s *Server) handleCreateCoopShift(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Kind         string  `json:"kind"`
		StartAt      string  `json:"startAt"`
		EndAt        string  `json:"endAt"`
		StageID      int32   `json:"stageId"`
		Boss         string  `json:"boss"`
		Weapons      []int64 `json:"weapons"`
		KumaWeaponID int64   `json:"kumaWeaponId"`
		RewardGearID int32   `json:"rewardGearId"`
		Rewards      []int64 `json:"rewards"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}

	kind, err := toyohr.ParseCoopShiftKind(body.Kind)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	start, err := time.Parse(time.RFC3339, body.StartAt)
	if err != nil {
		http.Error(w, "startAt must be RFC3339", http.StatusBadRequest)
		return
	}
	end, err := time.Parse(time.RFC3339, body.EndAt)
	if err != nil {
		http.Error(w, "endAt must be RFC3339", http.StatusBadRequest)
		return
	}

	shift := toyohr.CoopShift{
		Kind: kind, StageID: body.StageID, Boss: body.Boss,
		KumaWeaponID: body.KumaWeaponID, RewardGearID: body.RewardGearID,
		Rewards: body.Rewards,
		Start:   toyohr.AlignToSlot(start), End: toyohr.AlignToSlot(end),
	}
	// Left short of four rather than rejected here: a wrong count still
	// reaches Validate, whose "not a real Salmon Run weapon" message (for
	// the zero-value slots this leaves behind) explains the problem exactly
	// as well as a length check would, without a second place that has to
	// agree with Validate about what "wrong" means.
	copy(shift.Weapons[:], body.Weapons)

	if err := s.fests.SaveCoopShift(r.Context(), shift); err != nil {
		// Validate's errors explain themselves -- an unreal stage/weapon/boss
		// id, a misaligned slot -- and are exactly what the operator needs to
		// see, the same reasoning handleCreateFest already applies.
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	s.log.Info("dashboard: coop shift saved", "shift", shift.ID(), "kind", shift.Kind)
	writeJSON(w, map[string]any{"id": shift.ID()})
}

func (s *Server) handleDeleteCoopShift(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if err := s.fests.DeleteCoopShift(r.Context(), id); err != nil {
		s.fail(w, "delete coop shift", err)
		return
	}
	s.log.Info("dashboard: coop shift deleted", "shift", id)
	w.WriteHeader(http.StatusNoContent)
}

// coopReferenceEntry is one row of a reference table (a stage, a weapon or a
// boss) -- what fills the schedule form's dropdowns, so the page never has
// to hardcode a second copy of coop_ids.go's tables.
type coopReferenceEntry struct {
	ID   int64  `json:"id,omitempty"`
	Name string `json:"name"`
	// Never omitempty: a normal-rotation stage has BigRun=false, and the
	// page's filter (populateCoopStageSelect in dashboard.js) compares this
	// field against a JS boolean -- an omitted key reads back as undefined,
	// which is never === false, so every normal stage would silently vanish
	// from the dropdown. Caught by actually running the page in a browser,
	// not by the Go-side tests, which had no reason to notice a field that
	// round-trips fine through Go's own json.Unmarshal.
	BigRun bool `json:"bigRun"`
}

func (s *Server) handleCoopReference(w http.ResponseWriter, r *http.Request) {
	stages := make([]coopReferenceEntry, 0, len(toyohr.CoopStages))
	for _, st := range toyohr.CoopStages {
		stages = append(stages, coopReferenceEntry{ID: int64(st.ID), Name: st.Name, BigRun: st.BigRun})
	}
	weapons := make([]coopReferenceEntry, 0, len(toyohr.CoopWeapons))
	for _, wp := range toyohr.CoopWeapons {
		weapons = append(weapons, coopReferenceEntry{ID: wp.ID, Name: wp.Name})
	}
	kumaWeapons := make([]coopReferenceEntry, 0, len(toyohr.CoopKumaWeapons))
	for _, wp := range toyohr.CoopKumaWeapons {
		kumaWeapons = append(kumaWeapons, coopReferenceEntry{ID: wp.ID, Name: wp.Name})
	}
	writeJSON(w, map[string]any{
		"stages":      stages,
		"weapons":     weapons,
		"kumaWeapons": kumaWeapons,
		"bosses":      toyohr.CoopBosses,
	})
}
