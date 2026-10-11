package matchmaking

import "strings"

// Anarchy Battle (Series) is two matchmaking steps, the same shape as
// Splatfest Open's (fest_regular.go) without fest teams.
//
// Splatoon 3's config chooser (FUN_71032c1d20, Bankara = match kind 2)
// queues a player first on bankara_match_challenge_<rule>_team_config, a
// lobby that is ONE side of the match, and the host then re-queues the
// whole lobby on bankara_match_challenge_<rule>_config to meet another
// lobby. The lobby ready check (0x71038aab34) and the delegate check
// (0x71038af140) want exactly 4 for Bankara; the GRID0+ IPS relaxes both to
// "at most", so smaller lobbies play on a patched game.
//
// Before this, both steps went through formVersusLocked: a team lobby of 8,
// which the game never starts (count above the required 4 does nothing),
// or a second step that could pair a lobby of 4 with a lobby of 2.
//
// So team lobbies form in pairs of equal size -- 4 at once, 2 or 1 after the
// grace period -- and the second step pairs equal-size lobbies.

func isBankaraTeamMode(mode string) bool {
	m := strings.ToLower(mode)
	return strings.Contains(m, "bankara_match_challenge") && strings.Contains(m, "_team_config")
}

func isBankaraChallengeMode(mode string) bool {
	m := strings.ToLower(mode)
	return strings.Contains(m, "bankara_match_challenge") && !strings.Contains(m, "_team_config")
}

// bankaraChallengeMode is the second step a team-lobby mode hands on to.
func bankaraChallengeMode(teamMode string) string {
	return strings.Replace(teamMode, "_team_config", "_config", 1)
}

// twoLobbies finds two disjoint sets of whole units of exactly size players
// each, oldest units first.
func twoLobbies(units []festUnit, size int) (a, b []festUnit) {
	a = pickUnits(units, size)
	if a == nil {
		return nil, nil
	}
	used := map[string]bool{}
	for _, u := range a {
		used[u.ids[0]] = true
	}
	var rest []festUnit
	for _, u := range units {
		if !used[u.ids[0]] {
			rest = append(rest, u)
		}
	}
	if b = pickUnits(rest, size); b == nil {
		return nil, nil
	}
	return a, b
}

func (q *Queue) formBankaraTeamLocked(mode string) []Session {
	var formed []Session
	pair := func(size int) bool {
		a, b := twoLobbies(q.festUnitsLocked(q.liveWaitingLocked(mode)), size)
		if a == nil {
			return false
		}
		formed = append(formed, q.formSessionLocked(mode, unitIDs(a)), q.formSessionLocked(mode, unitIDs(b)))
		return true
	}
	for pair(4) {
		delete(q.grace, mode)
	}

	n := len(q.liveWaitingLocked(mode))
	small, _ := twoLobbies(q.festUnitsLocked(q.liveWaitingLocked(mode)), 1)
	small2, _ := twoLobbies(q.festUnitsLocked(q.liveWaitingLocked(mode)), 2)
	if q.cfg.VersusGracePeriod <= 0 || (small == nil && small2 == nil) {
		q.cancelGraceLocked(mode)
		return formed
	}
	g, pending := q.grace[mode]
	if !pending || n != g.lastCount {
		q.grace[mode] = &graceState{deadline: q.now().Add(q.cfg.VersusGracePeriod), lastCount: n}
		q.setModeStateLocked(mode, StatePlacing)
		return formed
	}
	if q.now().Before(g.deadline) {
		return formed
	}
	delete(q.grace, mode)
	for pair(2) {
	}
	if len(formed) == 0 {
		pair(1)
	}
	return formed
}

// formBankaraChallengeLocked is the second step: two lobbies of equal size
// meet. A lobby with no equal opponent waits; its team-lobby step only ever
// forms lobbies in equal pairs, so its partner is on its way.
func (q *Queue) formBankaraChallengeLocked(mode string) []Session {
	var formed []Session
	for {
		units := q.festUnitsLocked(q.liveWaitingLocked(mode))
		a, b := -1, -1
		for i := 0; i < len(units) && a < 0; i++ {
			for j := i + 1; j < len(units); j++ {
				if len(units[j].ids) == len(units[i].ids) {
					a, b = i, j
					break
				}
			}
		}
		if a < 0 {
			return formed
		}
		formed = append(formed, q.formSessionLocked(mode,
			append(append([]string(nil), units[a].ids...), units[b].ids...)))
	}
}
