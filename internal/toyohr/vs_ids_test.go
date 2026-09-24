package toyohr

import (
	"fmt"
	"testing"
)

// TestVsStagesAreAllRealAndUnique guards the table against a typo:
// duplicate ids or names would mean two rows describe the same real stage
// under different data, or the source was transcribed wrong.
func TestVsStagesAreAllRealAndUnique(t *testing.T) {
	if len(VsStages) != 24 {
		t.Fatalf("got %d stages, want 24", len(VsStages))
	}
	seenID := map[int32]bool{}
	seenName := map[string]bool{}
	for _, s := range VsStages {
		if s.ID < 1 {
			t.Errorf("stage %q has a non-positive id %d", s.Name, s.ID)
		}
		if s.Name == "" {
			t.Errorf("stage %d has no name", s.ID)
		}
		if seenID[s.ID] {
			t.Errorf("id %d appears more than once", s.ID)
		}
		seenID[s.ID] = true
		if seenName[s.Name] {
			t.Errorf("name %q appears more than once", s.Name)
		}
		seenName[s.Name] = true
	}
}

func TestVsStageByID(t *testing.T) {
	got, ok := VsStageByID(1)
	if !ok || got.Name != "Scorch Gorge" {
		t.Errorf("VsStageByID(1) = %+v, %v, want Scorch Gorge, true", got, ok)
	}
	if _, ok := VsStageByID(9999); ok {
		t.Error("VsStageByID(9999) = true, want false")
	}
}

// TestVsRuleNameNeverGuessesAName is the regression test for the whole
// point of VsRuleIDs/VsRuleName: a wrong-but-valid rule id is not a
// protocol error, but a made-up name would be a wrong claim of knowledge
// this project does not have. See VsRuleIDs' own doc comment.
func TestVsRuleNameNeverGuessesAName(t *testing.T) {
	for _, id := range []int32{1, 2, 3, 4} {
		want := fmt.Sprintf("Rule %d", id)
		if got := VsRuleName(id); got != want {
			t.Errorf("VsRuleName(%d) = %q, want %q", id, got, want)
		}
	}
	if got := VsRuleName(5); got != "" {
		t.Errorf("VsRuleName(5) = %q, want empty -- 5 is not a confirmed-valid id", got)
	}
}
