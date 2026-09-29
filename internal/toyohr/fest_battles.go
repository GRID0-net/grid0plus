package toyohr

import (
	"context"
	"strings"
	"time"

	"github.com/n-popescu/grid0plus/internal/db"
	"github.com/n-popescu/grid0plus/internal/gamesync"
)

// RecordFestReport turns the game's final referee report into one durable team
// result. The client supplies the match facts, but the session and membership
// are verified by gamesync and the team names come from stored fest entries.
// Incomplete kickoff reports never count as completed battles.
func RecordFestReport(ctx context.Context, store db.Store, r gamesync.RefereeReport, now time.Time) error {
	mode := strings.ToLower(r.Mode)
	if !strings.Contains(mode, "fest") || r.SessionID == "" ||
		!strings.HasSuffix(r.BattleID, "_"+r.SessionID) {
		return nil
	}
	personal := r.Fields.GetFields()["personal_result"].GetMapValue()
	teamResult := r.Fields.GetFields()["team_result"].GetMapValue()
	if personal == nil || teamResult == nil {
		return nil
	}
	winnerValue := teamResult.GetFields()["winner"]
	if winnerValue == nil || winnerValue.GetValueType() == nil {
		return nil
	}
	winnerSlot := winnerValue.GetIntegerValue()
	if winnerSlot < 1 || winnerSlot > 3 {
		return nil
	}
	fests, err := store.ListFests(ctx)
	if err != nil {
		return err
	}
	requestedFestID := r.Fields.GetFields()["fest_id"].GetStringValue()
	var active *db.Fest
	for _, row := range fests {
		f := festFromRow(row)
		if !row.Enabled || now.Before(f.Timetable().Start) || !now.Before(f.Timetable().Close) {
			continue
		}
		if requestedFestID != "" && requestedFestID != f.ID {
			continue
		}
		if active != nil {
			// Without an explicit fest_id, two simultaneous regional fests
			// cannot be distinguished from this report. Never credit one by
			// list order merely because it was encountered first.
			return nil
		}
		chosen := row
		active = &chosen
	}
	if active != nil {
		f := festFromRow(*active)
		entries, err := store.ListFestEntries(ctx, f.ID)
		if err != nil {
			return err
		}
		votes := make(map[string]string, len(entries))
		for _, e := range entries {
			votes[e.UserID] = e.Team
		}
		members := make(map[string]bool, len(r.Players))
		for _, id := range r.Players {
			members[id] = true
		}
		winnerCounts := map[string]int{}
		ink := map[string]int64{}
		for _, v := range personal.GetFields() {
			fields := v.GetMapValue().GetFields()
			uid := fields["npln_user_id"].GetStringValue()
			team := votes[uid]
			if !members[uid] || team == "" {
				continue
			}
			if fields["team"].GetIntegerValue() == winnerSlot {
				winnerCounts[team]++
			}
			if paint := fields["paint_point"].GetIntegerValue(); paint > 0 {
				ink[team] += paint
			}
		}
		winner, count := "", 0
		for team, n := range winnerCounts {
			if n > count || (n == count && team < winner) {
				winner, count = team, n
			}
		}
		if winner == "" {
			return nil
		}
		return store.RecordFestBattle(ctx, db.FestBattle{
			FestID: f.ID, BattleID: r.BattleID, Mode: r.Mode,
			WinningTeam: winner, Ink: ink, FinishedAt: now,
		})
	}
	return nil
}
