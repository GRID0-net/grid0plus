package toyohr

import (
	"context"
	"testing"
	"time"

	"github.com/n-popescu/grid0plus/internal/db"
)

// TestTheTricolorDefenderIsTheMidtermLeader: with no battles before the
// midpoint, the midterm ranking falls back to the vote, so the team most
// players joined defends.
func TestTheTricolorDefenderIsTheMidtermLeader(t *testing.T) {
	store := db.NewMemory()
	ctx := context.Background()
	row := db.Fest{ID: "JUEA-00017", Resource: "juea-sweat", StartAt: time.Date(2026, 10, 4, 12, 0, 0, 0, time.UTC)}
	for user, team := range map[string]string{"u1": "Bravo", "u2": "Bravo", "u3": "Alpha", "u4": "Charlie"} {
		if err := store.UpsertFestEntry(ctx, db.FestEntry{FestID: row.ID, UserID: user, Team: team}); err != nil {
			t.Fatal(err)
		}
	}
	got, err := FestDefender(ctx, store, row)
	if err != nil || got != "Bravo" {
		t.Fatalf("defender = %q, %v; want Bravo", got, err)
	}
	if got, _ := FestDefender(ctx, store, db.Fest{ID: "NOBODY"}); got != "" {
		t.Errorf("a fest nobody joined has defender %q, want none", got)
	}
}
