package toyohr

import "testing"

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
