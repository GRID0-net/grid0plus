package dashboard

import (
	"context"
	"encoding/json"
	"net/http"
	"sort"
	"time"

	"github.com/n-popescu/grid0plus/internal/toyohr"
	"github.com/n-popescu/grid0plus/internal/ugcstore"
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
	RewardBorders  []int64  `json:"rewardBorders,omitempty"`
	IsSpecial      bool     `json:"isSpecial,omitempty"`
	StartAt        string   `json:"startAt"`
	EndAt          string   `json:"endAt"`
	// Status is "upcoming", "live" or "ended" -- ended shifts are still
	// listed here (unlike SelectCoopSchedules, which drops them so the
	// console is not told about a closed shift) so the operator can see what
	// already ran rather than have it vanish the moment it does.
	Status string `json:"status"`
	// Generated marks a Normal shift nobody stored -- toyohr.liveNormalCoopShifts
	// filled it in live, the same shift a console is currently being told
	// about. Nothing to delete or edit here: it is not a row in the store,
	// recomputed fresh on every request, so the page must not offer a
	// delete button for one (deleting it would do nothing and reappear on
	// the next refresh).
	Generated bool `json:"generated"`
}

func (s *Server) coopShiftRows(ctx context.Context) ([]coopShiftRow, error) {
	stored, err := s.fests.ListCoopShiftsForOperator(ctx)
	if err != nil {
		return nil, err
	}
	now := s.now()

	// Same slots a console would be shown right now (toyohr.PreviewCoopSchedule),
	// so the operator's own list never falls behind what SelectCoopSchedules
	// is actually answering -- stored shifts already past their end are kept
	// here (the operator's own history), live-only ones are not, since there
	// is nothing to keep a record of once they end.
	live, err := s.fests.PreviewCoopSchedule(ctx, now)
	if err != nil {
		return nil, err
	}
	storedIDs := make(map[string]bool, len(stored))
	for _, c := range stored {
		storedIDs[c.ID()] = true
	}
	shifts := append([]toyohr.CoopShift(nil), stored...)
	for _, c := range live {
		if !storedIDs[c.ID()] {
			shifts = append(shifts, c)
		}
	}
	sort.Slice(shifts, func(i, j int) bool { return shifts[i].Start.Before(shifts[j].Start) })

	out := make([]coopShiftRow, 0, len(shifts))
	for _, c := range shifts {
		stage, _ := toyohr.CoopStageByID(c.StageID)
		kuma, _ := toyohr.CoopKumaWeaponByID(c.KumaWeaponID)

		weaponNames := make([]string, 0, len(c.Weapons))
		for _, id := range c.Weapons {
			if id == 0 {
				continue
			}
			if name, ok := coopRandomSlotNames[id]; ok {
				weaponNames = append(weaponNames, name)
				continue
			}
			if w, ok := toyohr.CoopWeaponByID(id); ok {
				weaponNames = append(weaponNames, w.Name)
				continue
			}
			// A main weapon slot can now legitimately be a Grizzco weapon
			// (grizzcoWeaponChance's own generation policy) -- without this
			// fallback such a slot silently disappeared from the row's
			// names instead of showing what it actually is.
			if w, ok := toyohr.CoopKumaWeaponByID(id); ok {
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
			RewardBorders:  borderList(c.RewardBorders),
			IsSpecial:      c.IsSpecial,
			StartAt:        c.Start.UTC().Format(time.RFC3339),
			EndAt:          c.End.UTC().Format(time.RFC3339),
			Status:         status,
			Generated:      !storedIDs[c.ID()],
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
		StageID      int32   `json:"stageId"`
		Boss         string  `json:"boss"`
		Weapons      []int64 `json:"weapons"`
		KumaWeaponID int64   `json:"kumaWeaponId"`
		RewardGearID int32   `json:"rewardGearId"`
		Rewards      []int64 `json:"rewards"`
		// RewardBorders is gold, silver, bronze; required for a Big Run.
		RewardBorders []int64 `json:"rewardBorders"`
		IsSpecial     bool    `json:"isSpecial"`
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
	now := s.now()
	visible, err := s.fests.PreviewCoopSchedule(r.Context(), now)
	if err != nil {
		s.fail(w, "read coop rotation", err)
		return
	}
	var slot *toyohr.CoopShift
	for i := range visible {
		if visible[i].Kind == toyohr.CoopShiftNormal && visible[i].Start.Equal(start) {
			slot = &visible[i]
			break
		}
	}
	if slot == nil {
		http.Error(w, "startAt must be the start of a current or upcoming normal rotation", http.StatusBadRequest)
		return
	}
	end := slot.End
	if kind != toyohr.CoopShiftNormal {
		// The current rotation is allowed as well as an upcoming one: an
		// operator testing a Big Run wants it now. The console only picks it
		// up after a game restart (it keeps the schedule it loaded).
		end = start.Add(48 * time.Hour)
		if !end.After(now) {
			http.Error(w, "that rotation would put the event entirely in the past", http.StatusBadRequest)
			return
		}
	}
	stored, err := s.fests.ListCoopShiftsForOperator(r.Context())
	if err != nil {
		s.fail(w, "read stored coop shifts", err)
		return
	}
	for _, existing := range stored {
		if start.Before(existing.End) && existing.Start.Before(end) &&
			!(kind == toyohr.CoopShiftNormal && existing.Start.Equal(start) && existing.End.Equal(end)) {
			http.Error(w, "selected window overlaps a stored shift; remove that shift first", http.StatusBadRequest)
			return
		}
	}

	shift := toyohr.CoopShift{
		Kind: kind, StageID: body.StageID, Boss: body.Boss,
		KumaWeaponID: body.KumaWeaponID, RewardGearID: body.RewardGearID,
		Rewards: body.Rewards, IsSpecial: body.IsSpecial,
		Start: toyohr.AlignToSlot(start), End: toyohr.AlignToSlot(end),
	}
	if len(body.RewardBorders) > 3 {
		http.Error(w, "rewardBorders is gold, silver, bronze: at most three values", http.StatusBadRequest)
		return
	}
	copy(shift.RewardBorders[:], body.RewardBorders)
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

// handleGenerateCoopShifts auto-fills a stretch of the normal rotation --
// the dashboard's own front end for toyohr.GenerateAndSaveCoopShifts, the
// same algorithm switchnetctl coop generate uses. See that method's doc
// comment: it never overwrites a slot that already has a shift, hand-
// scheduled or previously generated, which is what makes the manual form
// above and this button safe to use on overlapping ranges.
func (s *Server) handleGenerateCoopShifts(w http.ResponseWriter, r *http.Request) {
	var body struct {
		FromAt      string `json:"fromAt"`
		Count       int    `json:"count"`
		DurationHrs int    `json:"durationHours"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}

	from, err := time.Parse(time.RFC3339, body.FromAt)
	if err != nil {
		http.Error(w, "fromAt must be RFC3339", http.StatusBadRequest)
		return
	}
	if body.Count <= 0 {
		http.Error(w, "count must be positive", http.StatusBadRequest)
		return
	}
	duration := toyohr.DefaultCoopShiftDuration
	if body.DurationHrs > 0 {
		duration = time.Duration(body.DurationHrs) * time.Hour
	}

	created, skipped, err := s.fests.GenerateAndSaveCoopShifts(r.Context(), from, body.Count, duration, nil)
	if err != nil {
		s.fail(w, "generate coop shifts", err)
		return
	}
	s.log.Info("dashboard: coop shifts generated", "created", len(created), "skipped", len(skipped))
	writeJSON(w, map[string]any{"created": len(created), "skipped": len(skipped)})
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
	weapons := make([]coopReferenceEntry, 0, len(toyohr.CoopWeapons)+2)
	for _, id := range []int64{toyohr.CoopRandomWeapon, toyohr.CoopRandomGrizzcoWeapon} {
		weapons = append(weapons, coopReferenceEntry{ID: id, Name: coopRandomSlotNames[id]})
	}
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

// coopRandomSlotNames names the two random main-weapon slots (see
// toyohr.CoopRandomWeapon).
var coopRandomSlotNames = map[int64]string{
	toyohr.CoopRandomWeapon:        "Random (green ?)",
	toyohr.CoopRandomGrizzcoWeapon: "Random Grizzco (gold ?)",
}

// handleBigRunEggs reports the special Big Run's community golden-egg total,
// the number GlobalCounterService/GetSeason hands the game.
func (s *Server) handleBigRunEggs(w http.ResponseWriter, r *http.Request) {
	count, jobs, err := ugcstore.BigRunEggs(r.Context(), s.store, s.tenant)
	if err != nil {
		s.fail(w, "read the Big Run egg total", err)
		return
	}
	writeJSON(w, map[string]any{"count": count, "jobs": jobs, "goal": 700000000})
}

func borderList(b [3]int64) []int64 {
	if b == [3]int64{} {
		return nil
	}
	return b[:]
}
