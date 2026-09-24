package dashboard

import (
	"net/http"
	"time"

	"github.com/n-popescu/switchnet/internal/toyohr"
)

// ---- Schedule viewer -------------------------------------------------------
//
// A read-only view of what a console would actually be told right now for
// each mode -- the same functions the wire handlers call
// (toyohr.Service.PreviewVsSchedule/PreviewCoopSchedule), not a second copy
// of the rotation logic, so this can never show something the console
// itself would not see.

// vsSlotRow is one Turf War/Anarchy rotation slot, names resolved the same
// way coopShiftRow resolves ids -- the page has no access to toyohr's
// tables of its own.
type vsSlotRow struct {
	StartAt string        `json:"startAt"`
	EndAt   string        `json:"endAt"`
	Regular []stageRef    `json:"regular,omitempty"`
	Anarchy []anarchyRule `json:"anarchy,omitempty"`
}

type stageRef struct {
	ID   int32  `json:"id"`
	Name string `json:"name"`
}

type anarchyRule struct {
	Rule   int32      `json:"rule"`
	Name   string     `json:"name"`
	Stages []stageRef `json:"stages"`
}

func stageRefs(ids []int32) []stageRef {
	out := make([]stageRef, 0, len(ids))
	for _, id := range ids {
		name := ""
		if s, ok := toyohr.VsStageByID(id); ok {
			name = s.Name
		}
		out = append(out, stageRef{ID: id, Name: name})
	}
	return out
}

func (s *Server) vsSlotRows() []vsSlotRow {
	schedules := s.fests.PreviewVsSchedule(s.now(), 0)
	out := make([]vsSlotRow, 0, len(schedules))
	for _, sched := range schedules {
		row := vsSlotRow{
			StartAt: sched.GetStartTime().AsTime().UTC().Format(time.RFC3339),
			EndAt:   sched.GetEndTime().AsTime().UTC().Format(time.RFC3339),
		}
		if reg := sched.GetRegularSettings(); reg != nil {
			row.Regular = stageRefs(reg.GetStages())
		}
		for _, b := range sched.GetBankaraSettings() {
			row.Anarchy = append(row.Anarchy, anarchyRule{
				Rule:   b.GetRule(),
				Name:   toyohr.VsRuleName(b.GetRule()),
				Stages: stageRefs(b.GetStages()),
			})
		}
		out = append(out, row)
	}
	return out
}

func (s *Server) handleScheduleViewer(w http.ResponseWriter, r *http.Request) {
	// coopShiftRows already computes exactly this merge (stored + live-filled
	// Normal slots) -- reused rather than duplicated so the schedule viewer
	// and the Salmon Run tab can never show two different answers for "what
	// is scheduled right now."
	coopRows, err := s.coopShiftRows(r.Context())
	if err != nil {
		s.fail(w, "coop shift rows", err)
		return
	}

	writeJSON(w, map[string]any{
		"vs":   s.vsSlotRows(),
		"coop": coopRows,
	})
}
