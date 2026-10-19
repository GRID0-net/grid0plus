package ugcstore

import (
	"context"
	"strings"
	"time"

	"github.com/n-popescu/grid0plus/internal/db"
	commonpb "github.com/n-popescu/grid0plus/proto/common"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// RecordVsResults stores one finished battle in every participant's in-game
// history, at services/GameRecord/users/<uid>/VsResults/<battleID>.
//
// The console never writes these documents; Nintendo's server creates them
// after each battle and the console only reads them back with RunQuery.
// battleID is the referee report's own suffix, "<compact timestamp>_<game
// session>", which is Nintendo's battle_id format and sorts newest-last by name.
//
// The fields are what the report itself carries -- the team result and each
// player's personal result, split by team into personal_data_team1..3 as
// Nintendo's document is. Values only Nintendo computes (power ratings, its
// own ink ratios) are left out rather than invented.
//
// The top-level fields the history screen reads are in historyFields.
// UNCONFIRMED: mmr_before/mmr_after/rate, which the same reader looks for in
// some modes, and any value Nintendo computes that the report does not carry.
// Change historyFields.
func RecordVsResults(ctx context.Context, store db.Store, tenant, battleID, mode string,
	players []string, report, verdict *commonpb.MapValue, profiles map[string]*commonpb.Value,
	festTeams map[string]string, now time.Time) error {
	if battleID == "" || strings.Contains(battleID, "/") || report == nil {
		return nil
	}
	top := historyFields(report, verdict)
	// The verified session supplies the readable matchmaking config even
	// when the client reports only Nintendo's numeric game_mode.
	if top["mode"] == nil && mode != "" {
		top["mode"] = &commonpb.Value{ValueType: &commonpb.Value_StringValue{StringValue: mode}}
	}
	if top["started_at"] == nil {
		// The battle id starts with the match's own UTC start, YYYYMMDDTHHMMSS.
		if ts, _, ok := strings.Cut(battleID, "_"); ok {
			if t, err := time.ParseInLocation("20060102T150405", ts, time.UTC); err == nil {
				top["started_at"] = &commonpb.Value{ValueType: &commonpb.Value_TimestampValue{TimestampValue: timestamppb.New(t)}}
			}
		}
	}
	teams := map[string][]*commonpb.Value{
		"personal_data_team1": nil, "personal_data_team2": nil, "personal_data_team3": nil,
	}
	people, keys := resultPeople(report, verdict)
	for _, key := range keys {
		p := people[key]
		p = withProfile(p, profiles)
		p = historyEntry(p, festTeams)
		switch p.GetMapValue().GetFields()["team"].GetIntegerValue() {
		case 1:
			teams["personal_data_team1"] = append(teams["personal_data_team1"], p)
		case 2:
			teams["personal_data_team2"] = append(teams["personal_data_team2"], p)
		case 3:
			teams["personal_data_team3"] = append(teams["personal_data_team3"], p)
		}
	}
	str := func(v string) *commonpb.Value {
		return &commonpb.Value{ValueType: &commonpb.Value_StringValue{StringValue: v}}
	}
	for _, uid := range players {
		if uid == "" || strings.Contains(uid, "/") {
			continue
		}
		fields := map[string]*commonpb.Value{
			"battle_id":    str(battleID),
			"npln_user_id": str(uid),
		}
		if tr := report.GetFields()["team_result"]; tr != nil {
			fields["team_result"] = tr
		}
		for k, v := range top {
			fields[k] = v
		}
		for k, v := range teams {
			fields[k] = &commonpb.Value{ValueType: &commonpb.Value_ArrayValue{ArrayValue: &commonpb.ArrayValue{Values: v}}}
		}
		encoded, err := encodeFields(&commonpb.MapValue{Fields: fields})
		if err != nil {
			return err
		}
		name := "tenants/" + tenant + documentsSegment + "services/GameRecord/users/" + uid + "/VsResults/" + battleID
		if err := store.PutUGCDocument(ctx, db.UGCDocument{
			Name: name, Owner: uid, Fields: encoded, CreateTime: now, UpdateTime: now,
		}); err != nil {
			return err
		}
	}
	return nil
}

// withProfile adds the player's profile ({player_info: ...}) to one personal
// result. The history reader takes each player's name, weapon, gear and name
// plate from profile.player_info; without it every row shows no name and a
// default weapon. The console writes player_info for each battle to @@Profile,
// not to its report.
func withProfile(p *commonpb.Value, profiles map[string]*commonpb.Value) *commonpb.Value {
	f := p.GetMapValue().GetFields()
	if f == nil {
		return p
	}
	info := profiles[f["npln_user_id"].GetStringValue()]
	if info == nil {
		return p
	}
	profileFields := map[string]*commonpb.Value{}
	// A partial profile must not suppress the battle's captured weapon and
	// gear; preserve its other fields while filling the missing player_info.
	if profile := f["profile"].GetMapValue(); profile != nil {
		for k, v := range profile.GetFields() {
			profileFields[k] = v
		}
	}
	if profileFields["player_info"] != nil {
		return p
	}
	profileFields["player_info"] = info
	out := make(map[string]*commonpb.Value, len(f)+1)
	for k, v := range f {
		out[k] = v
	}
	out["profile"] = &commonpb.Value{ValueType: &commonpb.Value_MapValue{MapValue: &commonpb.MapValue{Fields: profileFields}}}
	return &commonpb.Value{ValueType: &commonpb.Value_MapValue{MapValue: &commonpb.MapValue{Fields: out}}}
}

// historyFields are the battle's top-level fields as the history screen
// reads them. Its reader takes game_mode, game_rule, stage, elapsed_time, winner
// and score_team%d as integers, battle_id, npln_user_id and color_pattern as
// strings, started_at as a timestamp, est_mmr_team%d and paint_ratio%d as
// doubles, then the personal_data_team%d arrays, all from the document's top
// level; the history query also orders on a top-level started_at. With them only
// inside team_result the screen shows 1/1/1970, 0p for both teams and a win for
// everyone. The console reports the team values in team_result; the rest are the
// referee request's, carried by the verdict.
func historyFields(report, verdict *commonpb.MapValue) map[string]*commonpb.Value {
	out := map[string]*commonpb.Value{}
	// The match context next to game_mode/game_rule/stage: copied from the
	// verdict so the history can name the season, the event and the Splatfest.
	for _, k := range []string{"game_mode", "game_rule", "stage", "color_pattern",
		"est_mmr_team1", "est_mmr_team2", "est_mmr_team3",
		"bankara_open_schedule_id", "event_parameter", "fest_id", "fest_region",
		"fest_resource_id", "league_part_id", "mm_region", "mode", "season_id", "tournament_id"} {
		if v := verdict.GetFields()[k]; v != nil {
			out[k] = v
		} else if v := report.GetFields()[k]; v != nil {
			out[k] = v
		}
	}
	team := report.GetFields()["team_result"].GetMapValue().GetFields()
	if team == nil {
		team = verdict.GetFields()["team_result"].GetMapValue().GetFields()
	}
	for _, k := range []string{"started_at", "elapsed_time", "winner",
		"score_team1", "score_team2", "score_team3",
		"paint_ratio1", "paint_ratio2", "paint_ratio3"} {
		if v := team[k]; v != nil {
			out[k] = v
		}
	}
	return out
}

// historyEntry nests a player's stats the way the history screen reads them.
// Its per-player reader takes kill_num, kill_assist_num, death_num, special_num,
// paint_point, deemed_result, disconnected and group_leader from a "result" map
// inside the entry, not from the entry itself; with them only at the top every
// player shows 0p and x00, and a missing deemed_result reads as a win, so the
// loser's history says VICTORY and its Recent Battles count is wrong. The
// top-level copies stay for anything else that reads them.
//
// A Splatfest player also gets fest_info.fest_team (Alpha/Bravo/Charlie) and the
// Tricolor signal fields the same reader takes from fest_info, so the row is
// drawn in its team's colour.
func historyEntry(p *commonpb.Value, festTeams map[string]string) *commonpb.Value {
	f := p.GetMapValue().GetFields()
	if f == nil {
		return p
	}
	out := make(map[string]*commonpb.Value, len(f)+2)
	for k, v := range f {
		out[k] = v
	}
	if out["result"].GetMapValue() == nil {
		result := map[string]*commonpb.Value{}
		for _, k := range []string{"kill_num", "kill_assist_num", "death_num", "special_num",
			"paint_point", "deemed_result", "disconnected", "group_leader"} {
			if v := f[k]; v != nil {
				result[k] = v
			}
		}
		out["result"] = &commonpb.Value{ValueType: &commonpb.Value_MapValue{MapValue: &commonpb.MapValue{Fields: result}}}
	}
	if team := festTeams[f["npln_user_id"].GetStringValue()]; team != "" && out["fest_info"].GetMapValue() == nil {
		info := map[string]*commonpb.Value{
			"fest_team": {ValueType: &commonpb.Value_StringValue{StringValue: team}},
		}
		for _, k := range []string{"did_get_noroshi1", "did_get_noroshi2", "noroshi_try_num", "contribution"} {
			if v := f[k]; v != nil {
				info[k] = v
			}
		}
		out["fest_info"] = &commonpb.Value{ValueType: &commonpb.Value_MapValue{MapValue: &commonpb.MapValue{Fields: info}}}
	}
	return &commonpb.Value{ValueType: &commonpb.Value_MapValue{MapValue: &commonpb.MapValue{Fields: out}}}
}
