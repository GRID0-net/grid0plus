package npln

// knownModes is every matchmaking config name Splatoon 3's main links (read
// out of its string table on 2026-10-04, build 28C4287A...), minus the
// infrastructure configs (eds_*, npln_config, route_config, service_config,
// ssl_do_config). They are the game's own names, not a guess: the dashboard
// lists them all so an operator can set a mode's size before anyone queues
// for it. Sizes are still configuration, not this list's business.
var knownModes = []string{
	"tenants/current/matchmakingConfigs/bankara_match_challenge_ar_config",
	"tenants/current/matchmakingConfigs/bankara_match_challenge_ar_team_config",
	"tenants/current/matchmakingConfigs/bankara_match_challenge_cl_config",
	"tenants/current/matchmakingConfigs/bankara_match_challenge_cl_team_config",
	"tenants/current/matchmakingConfigs/bankara_match_challenge_gl_config",
	"tenants/current/matchmakingConfigs/bankara_match_challenge_gl_team_config",
	"tenants/current/matchmakingConfigs/bankara_match_challenge_lf_config",
	"tenants/current/matchmakingConfigs/bankara_match_challenge_lf_team_config",
	"tenants/current/matchmakingConfigs/bankara_match_open_oneshot_config",
	"tenants/current/matchmakingConfigs/coop_bigrun_config",
	"tenants/current/matchmakingConfigs/coop_private_config",
	"tenants/current/matchmakingConfigs/coop_regular_config",
	"tenants/current/matchmakingConfigs/coop_team_contest_config",
	"tenants/current/matchmakingConfigs/fest_match_challenge_oneshot_config",
	"tenants/current/matchmakingConfigs/fest_match_regular_direct_pair_config",
	"tenants/current/matchmakingConfigs/fest_match_regular_normal_config",
	"tenants/current/matchmakingConfigs/fest_match_tricolor_oneshot_config",
	"tenants/current/matchmakingConfigs/fest_match_tricolor_team_config",
	"tenants/current/matchmakingConfigs/join_room_2_no_referee_config",
	"tenants/current/matchmakingConfigs/join_room_4_no_referee_config",
	"tenants/current/matchmakingConfigs/join_room_8_no_referee_config",
	"tenants/current/matchmakingConfigs/league_match_ignore_weapon_type_oneshot_config",
	"tenants/current/matchmakingConfigs/league_match_normal_oneshot_config",
	"tenants/current/matchmakingConfigs/league_match_pair_oneshot_config",
	"tenants/current/matchmakingConfigs/league_match_tournament_oneshot_config",
	"tenants/current/matchmakingConfigs/league_match_x_playoff_oneshot_config",
	"tenants/current/matchmakingConfigs/minigame_config",
	"tenants/current/matchmakingConfigs/private_match_config",
	"tenants/current/matchmakingConfigs/regular_match_config",
	"tenants/current/matchmakingConfigs/x_match_lower_oneshot_config",
	"tenants/current/matchmakingConfigs/x_match_upper_oneshot_config",
}
