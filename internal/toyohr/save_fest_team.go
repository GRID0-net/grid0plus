package toyohr

import (
	"context"
	"errors"

	"github.com/n-popescu/grid0plus/internal/db"
	"github.com/n-popescu/grid0plus/proto/common"
)

// saveTeamIndex is Fest.VoteTeam in a Splatoon 3 save: the team's position,
// -1 for none. Read off the 13 records on grid0 on 2026-10-17, where every
// save with a team held 0 for an Alpha entry, 1 for Bravo, 2 for Charlie.
var saveTeamIndex = map[string]int64{"Alpha": 0, "Bravo": 1, "Charlie": 2}

// syncFestTeam makes a save's fest team the player's fest entry, and reports
// whether it changed anything.
//
// The game takes its team from the save, never from the entry: on hardware
// (2026-10-17) a team picked on Discord left the save at -1, the game asked
// the player to pick, and froze once GetFestEntry answered with the entry it
// did not expect. With VoteTeam set in the stored record, the next boot's
// GetSaveRecord put the player on that team. So whenever the server has an
// entry for the fest the save is on, the save carries that team -- the first
// pick is final in the save too.
//
// Only a save already on that fest (Fest.FestId, which the game sets itself
// with its InitFest write) is touched: building the Fest map for a fest the
// game has not started in the save would be inventing its shape.
func (s *Service) syncFestTeam(ctx context.Context, uid string, data *common.MapValue) bool {
	fest := data.GetFields()["Fest"].GetMapValue()
	festID := fest.GetFields()["FestId"].GetStringValue()
	if festID == "" {
		return false
	}
	entry, err := s.store.GetFestEntry(ctx, festID, uid)
	if errors.Is(err, db.ErrNotFound) {
		return false
	}
	if err != nil {
		s.log.Warn("toyohr: fest entry for a save", "user", uid, "fest", festID, "error", err)
		return false
	}
	want, ok := saveTeamIndex[entry.Team]
	if !ok {
		return false
	}
	cur, has := fest.GetFields()["VoteTeam"].GetValueType().(*common.Value_IntegerValue)
	if has && cur.IntegerValue == want {
		return false
	}
	from := "absent"
	if has {
		from = entry.Team
		for team, i := range saveTeamIndex {
			if i == cur.IntegerValue {
				from = team
			}
		}
		if cur.IntegerValue < 0 {
			from = "none"
		}
	}
	fest.Fields["VoteTeam"] = &common.Value{ValueType: &common.Value_IntegerValue{IntegerValue: want}}
	s.log.Info("toyohr: set the save's fest team from the fest entry", "user", uid, "fest", festID,
		"from", from, "to", entry.Team)
	return true
}
