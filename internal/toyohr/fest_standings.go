package toyohr

import (
	"context"
	"time"

	"github.com/n-popescu/grid0plus/internal/db"
	toyohrpb "github.com/n-popescu/grid0plus/proto/toyohr/v1"
)

// Standings is what Splatoon 3 itself shows about a fest at one moment, and
// nothing more. The Discord bot posts from it, and a player must not be able
// to read on Discord what the game would not tell them yet: no tally while
// play is open, the halftime ranking from the second half on, the results
// once they are out.
type Standings struct {
	Phase Phase
	// Midterm is the halftime ranking, best first. Set from the second half
	// on, when the game shows its halftime report.
	Midterm []TeamShare
	// Final is set once the results are published.
	Final *FinalStandings
}

// TeamShare is one team's share of one ranking, as the game is sent it.
type TeamShare struct {
	Team  string  `json:"team"`
	Ratio float64 `json:"ratio"`
}

// FinalStandings is the published result.
type FinalStandings struct {
	// Categories holds each ranking the result is made of, best first, under
	// the protocol's names: Yobisai, PlayerCount, Regular, Challenge, Tricolor.
	Categories map[string][]TeamShare `json:"categories"`
	// Points is each team's total, best first.
	Points []TeamPoints `json:"points"`
	// Winner is the team with the most points, empty on a tie for first.
	Winner string `json:"winner"`
}

// TeamPoints is one team's result points.
type TeamPoints struct {
	Team   string `json:"team"`
	Points int64  `json:"points"`
}

// FestStandings reads a fest's standings at now. The ranking rules are the
// same ones GetFestResult answers a console with, so the bot and the game
// cannot disagree about who won.
func FestStandings(ctx context.Context, store db.Store, row db.Fest, now time.Time) (Standings, error) {
	f := festFromRow(row)
	out := Standings{Phase: f.PhaseAt(now)}
	if out.Phase != PhaseSecondHalf && out.Phase != PhaseCounting && out.Phase != PhaseClosed {
		return out, nil
	}
	entries, err := store.ListFestEntries(ctx, row.ID)
	if err != nil {
		return Standings{}, err
	}
	if len(entries) == 0 {
		return out, nil
	}
	battles, err := store.ListFestBattles(ctx, row.ID)
	if err != nil {
		return Standings{}, err
	}
	baseline := festShares(voteRatios(entries))
	mid := f.Timetable().Mid
	out.Midterm = teamShares(rankedShares(midpointShares(battles, mid, baseline)))
	if out.Phase != PhaseClosed {
		return out, nil
	}
	_, final, overall := finalFestResult(entries, battles, baseline, mid)
	result := &FinalStandings{Categories: map[string][]TeamShare{
		"Yobisai":     teamShares(rankedShares(baseline)),
		"PlayerCount": teamShares(final.GetTeamRatios1()),
		"Regular":     teamShares(final.GetTeamRatios2()),
		"Challenge":   teamShares(final.GetTeamRatios3()),
		"Tricolor":    teamShares(rankedShares(categoryShares(battles, "Tricolor", baseline))),
	}}
	for _, p := range overall.GetPoints() {
		result.Points = append(result.Points, TeamPoints{Team: p.GetFestTeam(), Points: p.GetPoints()})
	}
	if len(result.Points) > 0 && (len(result.Points) == 1 || result.Points[0].Points > result.Points[1].Points) {
		result.Winner = result.Points[0].Team
	}
	out.Final = result
	return out, nil
}

func teamShares(in []*toyohrpb.FestTeamRatio) []TeamShare {
	out := make([]TeamShare, 0, len(in))
	for _, r := range in {
		out = append(out, TeamShare{Team: r.GetFestTeam(), Ratio: r.GetRatio()})
	}
	return out
}
