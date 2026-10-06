package ugcstore

import (
	"context"
	"strings"
	"time"

	"github.com/n-popescu/grid0plus/internal/db"
	commonpb "github.com/n-popescu/grid0plus/proto/common"
)

// RecordVsResults stores one finished battle in every participant's in-game
// history, at services/GameRecord/users/<uid>/VsResults/<battleID>.
//
// The console never writes these documents; Nintendo's server creates them
// after each battle and the console only reads them back with RunQuery
// (NextendoNetwork/splatoon-3's capture of an official Splatfest,
// 2026-08-23). battleID is the referee report's own suffix,
// "<compact timestamp>_<game session>", which is Nintendo's battle_id format
// and sorts newest-last by name.
//
// The fields are what the report itself carries -- the team result and each
// player's personal result, split by team into personal_data_team1..3 as
// Nintendo's document is. Values only Nintendo computes (power ratings, its
// own ink ratios) are left out rather than invented.
//
// The top-level fields the history screen reads are in historyFields.
// TODO(capture-needed): mmr_before/mmr_after/rate, which the same reader
// looks for in some modes, and any value Nintendo computes that the report
// does not carry. Change historyFields.
func RecordVsResults(ctx context.Context, store db.Store, tenant, battleID string,
	players []string, report, verdict *commonpb.MapValue, profiles map[string]*commonpb.Value, now time.Time) error {
	if battleID == "" || strings.Contains(battleID, "/") || report == nil {
		return nil
	}
	top := historyFields(report, verdict)
	teams := map[string][]*commonpb.Value{
		"personal_data_team1": nil, "personal_data_team2": nil, "personal_data_team3": nil,
	}
	for _, p := range report.GetFields()["personal_result"].GetMapValue().GetFields() {
		p = withProfile(p, profiles)
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
// result. The history reader (raw 0x7103dc4c10) takes each player's name,
// weapon, gear and name plate from profile.player_info, parsed by
// 0x71032ba410; without it every row showed no name and a default weapon.
// The console writes player_info for each battle to @@Profile, not to its
// report.
func withProfile(p *commonpb.Value, profiles map[string]*commonpb.Value) *commonpb.Value {
	f := p.GetMapValue().GetFields()
	info := profiles[f["npln_user_id"].GetStringValue()]
	if f == nil || info == nil || f["profile"] != nil {
		return p
	}
	out := make(map[string]*commonpb.Value, len(f)+1)
	for k, v := range f {
		out[k] = v
	}
	out["profile"] = &commonpb.Value{ValueType: &commonpb.Value_MapValue{MapValue: &commonpb.MapValue{
		Fields: map[string]*commonpb.Value{"player_info": info},
	}}}
	return &commonpb.Value{ValueType: &commonpb.Value_MapValue{MapValue: &commonpb.MapValue{Fields: out}}}
}

// historyFields are the battle's top-level fields as the history screen
// reads them. Its reader (raw code at 0x7103dc3684, which Ghidra leaves
// undefined) takes game_mode, game_rule, stage, elapsed_time, winner and
// score_team%d as integers, battle_id, npln_user_id and color_pattern as
// strings, started_at as a timestamp, est_mmr_team%d and paint_ratio%d as
// doubles, then the personal_data_team%d arrays, all from the document's
// top level; the history query also orders on a top-level started_at
// (FUN_7103280c00). With them only inside team_result the screen showed
// 1/1/1970, 0p for both teams and a win for everyone. The console reports
// the team values in team_result; the rest are the referee request's,
// carried by the verdict.
func historyFields(report, verdict *commonpb.MapValue) map[string]*commonpb.Value {
	out := map[string]*commonpb.Value{}
	for _, k := range []string{"game_mode", "game_rule", "stage", "color_pattern",
		"est_mmr_team1", "est_mmr_team2", "est_mmr_team3"} {
		if v := verdict.GetFields()[k]; v != nil {
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
