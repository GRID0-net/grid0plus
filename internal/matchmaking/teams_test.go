package matchmaking

import "testing"

func TestAnarchyOpenPlayersAreSplitAcrossTwoTeams(t *testing.T) {
	tickets := map[string]*Ticket{
		"a": {ID: "a", UserID: "ua"},
		"b": {ID: "b", UserID: "ub"},
	}
	slots := splitTeams(tickets, []string{"a", "b"})
	if slots["ua"] == "" || slots["ub"] == "" || slots["ua"] == slots["ub"] {
		t.Fatalf("two solo players got slots %v, want one on each team", slots)
	}
}

func TestASquadStaysTogetherWhenTeamsAreSplit(t *testing.T) {
	tickets := map[string]*Ticket{
		"lead": {ID: "lead", UserID: "u1"},
		"mate": {ID: "mate", UserID: "u2", Party: "lead"},
		"c":    {ID: "c", UserID: "u3"},
		"d":    {ID: "d", UserID: "u4"},
	}
	slots := splitTeams(tickets, []string{"c", "lead", "mate", "d"})
	if slots["u1"] != slots["u2"] {
		t.Fatalf("squad split across teams: %v", slots)
	}
	if slots["u3"] != slots["u4"] || slots["u3"] == slots["u1"] {
		t.Fatalf("slots %v, want the two solos together against the squad", slots)
	}
}

func TestOnlyTheRankedSoloConfigsGetServerTeams(t *testing.T) {
	for mode, want := range map[string]bool{
		"bankara_match_open_oneshot_config":              true,
		"league_match_ignore_weapon_type_oneshot_config": true,
		"x_match_upper_oneshot_config":                   true,
		"regular_match_config":                           false,
		"bankara_match_challenge_ar_team_config":         false,
		"coop_regular_config":                            false,
	} {
		if got := isServerTeamedMode(mode); got != want {
			t.Errorf("isServerTeamedMode(%q) = %v, want %v", mode, got, want)
		}
	}
}
