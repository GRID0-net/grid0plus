package matchmaking

import "strings"

// Splatfest Open is two matchmaking steps, the same shape as Tricolor's.
//
// Splatoon 3 picks the config for FestRegular (match kind 6): a player or a squad
// of friends first queues fest_match_regular_normal_config, a lobby that is ONE
// side, and the host then hands the whole lobby to
// fest_match_regular_direct_pair_config, where it meets a lobby of the other fest
// team. The lobby only proceeds at exactly 4 players, or exactly 2; the delegated
// squad must be 4 or 2 again. Any other size waits in the lobby.
//
// So this server forms regular_normal lobbies of one fest team, 4 or 2 in size,
// and always in matched pairs: a lobby of 4 Bravo is only formed when another team
// can form a lobby of 4 too, otherwise it would wait in direct_pair for an
// opponent that may never come. A squad is never split, so a squad of 3 waits for
// one more player of its team.
//
// One exception keeps the smallest games playable: when no team pair can make even
// two-player lobbies, one solo of each of two teams share a mixed lobby of 2, which
// the game accepts and re-queues as a lone squad; direct_pair plays that squad
// alone with a fest-team slot each (1 v 1).

// festUnit is one ticket and its squad members: always matched whole.
type festUnit struct {
	ids  []string
	team string // "" when the squad spans fest teams
}

func (q *Queue) festUnitsLocked(ids []string) []festUnit {
	index := map[string]int{}
	var units []festUnit
	for _, id := range ids {
		t := q.tickets[id]
		g := t.Party
		if g == "" {
			g = t.ID
		}
		i, ok := index[g]
		if !ok {
			i = len(units)
			index[g] = i
			units = append(units, festUnit{team: t.FestTeam})
		}
		u := &units[i]
		if u.team != t.FestTeam {
			u.team = ""
		}
		u.ids = append(u.ids, id)
	}
	return units
}

// pickUnits returns, in queue order, whole units adding up to exactly target
// players, or nil.
func pickUnits(units []festUnit, target int) []festUnit {
	var out []festUnit
	var walk func(i, left int) bool
	walk = func(i, left int) bool {
		if left == 0 {
			return true
		}
		for ; i < len(units); i++ {
			if n := len(units[i].ids); n <= left {
				out = append(out, units[i])
				if walk(i+1, left-n) {
					return true
				}
				out = out[:len(out)-1]
			}
		}
		return false
	}
	if walk(0, target) {
		return out
	}
	return nil
}

func unitIDs(units []festUnit) []string {
	var ids []string
	for _, u := range units {
		ids = append(ids, u.ids...)
	}
	return ids
}

// festTeams groups single-team units by fest team, the team whose player has
// waited longest first. Name order made Alpha and Bravo always pair first, so
// with all three teams queued Charlie waited out every match.
func festTeams(units []festUnit) (map[string][]festUnit, []string) {
	by := map[string][]festUnit{}
	var teams []string
	for _, u := range units {
		if u.team == "" {
			continue
		}
		if _, seen := by[u.team]; !seen {
			teams = append(teams, u.team)
		}
		by[u.team] = append(by[u.team], u)
	}
	return by, teams
}

// pairedLobbies finds two different teams that can each make a one-team lobby
// of exactly size players.
func pairedLobbies(units []festUnit, size int) (a, b []festUnit) {
	by, teams := festTeams(units)
	for i := 0; i < len(teams); i++ {
		pa := pickUnits(by[teams[i]], size)
		if pa == nil {
			continue
		}
		for j := i + 1; j < len(teams); j++ {
			if pb := pickUnits(by[teams[j]], size); pb != nil {
				return pa, pb
			}
		}
	}
	return nil, nil
}

func isFestRegularNormalMode(mode string) bool {
	return strings.Contains(strings.ToLower(mode), "fest_match_regular_normal")
}

// formFestRegularNormalLocked forms Splatfest Open's first step.
func (q *Queue) formFestRegularNormalLocked(mode string) []Session {
	var formed []Session
	for {
		a, b := pairedLobbies(q.festUnitsLocked(q.liveWaitingLocked(mode)), 4)
		if a == nil {
			break
		}
		delete(q.grace, mode)
		formed = append(formed, q.formSessionLocked(mode, unitIDs(a)), q.formSessionLocked(mode, unitIDs(b)))
	}

	units := q.festUnitsLocked(q.liveWaitingLocked(mode))
	by, teams := festTeams(units)
	solos := 0
	for _, t := range teams {
		if pickUnits(by[t], 1) != nil {
			solos++
		}
	}
	a2, _ := pairedLobbies(units, 2)
	if q.cfg.VersusGracePeriod <= 0 || (a2 == nil && solos < 2) {
		// Nothing smaller can start: wait for a full pair of lobbies.
		q.cancelGraceLocked(mode)
		return formed
	}

	n := len(q.liveWaitingLocked(mode))
	g, pending := q.grace[mode]
	if !pending || n != g.lastCount {
		q.grace[mode] = q.regraceLocked(mode, q.cfg.VersusGracePeriod, n)
		q.setModeStateLocked(mode, StatePlacing)
		return formed
	}
	if q.now().Before(g.deadline) {
		return formed
	}
	delete(q.grace, mode)
	return append(formed, q.formSmallFestLobbiesLocked(mode)...)
}

// formSmallFestLobbiesLocked runs when the grace period ends: paired lobbies
// of 2 if two teams can make them, else a mixed 1 v 1 lobby.
func (q *Queue) formSmallFestLobbiesLocked(mode string) []Session {
	var formed []Session
	for {
		a, b := pairedLobbies(q.festUnitsLocked(q.liveWaitingLocked(mode)), 2)
		if a == nil {
			break
		}
		formed = append(formed, q.formSessionLocked(mode, unitIDs(a)), q.formSessionLocked(mode, unitIDs(b)))
	}
	if len(formed) > 0 {
		return formed
	}
	by, teams := festTeams(q.festUnitsLocked(q.liveWaitingLocked(mode)))
	var pair []string
	for _, t := range teams {
		if solo := pickUnits(by[t], 1); solo != nil && len(pair) < 2 {
			pair = append(pair, solo[0].ids...)
		}
	}
	if len(pair) == 2 {
		formed = append(formed, q.formSessionLocked(mode, pair))
	}
	return formed
}

// formFestDirectPairLocked is the second step, where two lobbies meet: it
// pairs one-team squads of equal size from two different fest teams. A squad
// spanning both teams (the mixed 1 v 1 lobby, or a whole lobby choosing
// "continue with this team") already holds both sides and plays alone: at
// once when nobody waits for Splatfest Open, otherwise after the grace period,
// so a crowd queuing for Open is not split into separate small matches.
func (q *Queue) formFestDirectPairLocked(mode string) []Session {
	var formed []Session
	for {
		units := q.festUnitsLocked(q.liveWaitingLocked(mode))
		a, b := -1, -1
		for i := 0; i < len(units) && a < 0; i++ {
			if units[i].team == "" {
				continue
			}
			for j := i + 1; j < len(units); j++ {
				if units[j].team != "" && units[j].team != units[i].team && len(units[j].ids) == len(units[i].ids) {
					a, b = i, j
					break
				}
			}
		}
		if a < 0 {
			break
		}
		formed = append(formed, q.formSessionLocked(mode, append(append([]string(nil), units[a].ids...), units[b].ids...)))
	}

	var mixed []festUnit
	for _, u := range q.festUnitsLocked(q.liveWaitingLocked(mode)) {
		if u.team == "" {
			mixed = append(mixed, u)
		}
	}
	if len(mixed) == 0 {
		q.cancelGraceLocked(mode)
		return formed
	}
	if len(q.liveWaitingLocked(directPairOpenMode(mode))) == 0 || q.cfg.VersusGracePeriod <= 0 {
		q.cancelGraceLocked(mode)
		for _, u := range mixed {
			formed = append(formed, q.formSessionLocked(mode, u.ids))
		}
		return formed
	}
	n := len(mixed)
	g, pending := q.grace[mode]
	if !pending || n != g.lastCount {
		q.grace[mode] = q.regraceLocked(mode, q.cfg.VersusGracePeriod, n)
		q.setModeStateLocked(mode, StatePlacing)
		return formed
	}
	if q.now().Before(g.deadline) {
		return formed
	}
	delete(q.grace, mode)
	for _, u := range mixed {
		formed = append(formed, q.formSessionLocked(mode, u.ids))
	}
	return formed
}
