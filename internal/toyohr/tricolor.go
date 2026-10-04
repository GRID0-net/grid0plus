package toyohr

import (
	"context"

	"github.com/n-popescu/grid0plus/internal/db"
)

// FestDefender returns the team that defends in a fest's Tricolor Turf War:
// the leader of the midterm ranking, the same ranking GetFestResult publishes
// for the second half (Splatoon 3's rule; NextendoNetwork's notes on an
// official Tricolor capture show the four defenders all on the midterm
// winner's team). Empty when nobody has joined the fest.
func FestDefender(ctx context.Context, store db.Store, row db.Fest) (string, error) {
	entries, err := store.ListFestEntries(ctx, row.ID)
	if err != nil {
		return "", err
	}
	if len(entries) == 0 {
		return "", nil
	}
	battles, err := store.ListFestBattles(ctx, row.ID)
	if err != nil {
		return "", err
	}
	ranked := rankedShares(midpointShares(battles, festFromRow(row).Timetable().Mid, festShares(voteRatios(entries))))
	if len(ranked) == 0 {
		return "", nil
	}
	return ranked[0].GetFestTeam(), nil
}
