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
			paint := fields["paint_point"].GetIntegerValue()
			if paint > 0 {
				ink[team] += paint
			}
			// gamesync keeps only the reporter's own entry, so each console's
			// report carries exactly one player's paint -- the losers' too,
			// which the battle row below never sees once a winner wrote it.
			if uid == r.UserID {
				if err := store.RecordFestBattlePlayer(ctx, db.FestBattlePlayer{
					FestID: f.ID, BattleID: r.BattleID, UserID: uid, Team: team, Paint: max(paint, 0),
				}); err != nil {
					return err
				}
			}
		}
		winner, count := "", 0
		for team, n := range winnerCounts {
			if n > count || (n == count && team < winner) {
				winner, count = team, n
			}
		}
		if winner == "" && !strings.Contains(mode, "tricolor") {
			winner = otherTeam(r, votes)
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

// otherTeam names the winner from a losing player's report in a two-team
// battle: the one fest team among the session's players that is not the
// reporter's. Without it a battle whose winners never reported -- a winner
// who disconnected at the results -- was not counted at all. Tricolor has
// two other teams, so it is never inferred there.
func otherTeam(r gamesync.RefereeReport, votes map[string]string) string {
	own := votes[r.UserID]
	if own == "" {
		return ""
	}
	other := ""
	for _, id := range r.Players {
		team := votes[id]
		if team == "" || team == own {
			continue
		}
		if other != "" && other != team {
			return ""
		}
		other = team
	}
	return other
}

// FestTeamsOf maps the players of a fest battle's report to the teams they
// voted for, for the battle history; nil for any other battle.
func FestTeamsOf(ctx context.Context, store db.Store, r gamesync.RefereeReport) (map[string]string, error) {
	festID := r.Fields.GetFields()["fest_id"].GetStringValue()
	if festID == "" {
		festID = r.Verdict.GetFields()["fest_id"].GetStringValue()
	}
	if festID == "" || !strings.Contains(strings.ToLower(r.Mode), "fest") {
		return nil, nil
	}
	entries, err := store.ListFestEntries(ctx, festID)
	if err != nil {
		return nil, err
	}
	members := make(map[string]bool, len(r.Players))
	for _, id := range r.Players {
		members[id] = true
	}
	out := map[string]string{}
	for _, e := range entries {
		if members[e.UserID] {
			out[e.UserID] = e.Team
		}
	}
	return out, nil
}
