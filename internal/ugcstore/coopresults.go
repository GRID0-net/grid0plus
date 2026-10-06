package ugcstore

import (
	"context"
	"strings"
	"time"

	"github.com/n-popescu/grid0plus/internal/db"
	commonpb "github.com/n-popescu/grid0plus/proto/common"
)

// RecordCoopResults stores one finished Salmon Run job in every participant's
// job history, at services/GameRecord/users/<uid>/CoopResults/<battleID>, the
// collection the game queries (the string at 0x7104f599b8).
//
// The shape is what the game's reader takes (raw 0x710383a0b0, called from
// the CoopResults query at 0x71032818c8):
//
//   - top level: job_id, shift_id, shift_time, started_at, result,
//     final_wave, challenge_grade_point, scenario (the whole map),
//     copper/silver/gold_scale_num, personal_data, wave_detail;
//   - personal_data[i]: npln_user_id, is_supporter, disconnected,
//     group_leader, kuma_point, grade_rate, grade_before/after,
//     grade_point_before/after, smell_before/after, personal_wave_result
//     (wave, weapon_id, special_id, special_count, golden_ikura_num,
//     assist_golden_ikura_num, ikura_num, rescued_num, rescue_num) and
//     personal_profile {player_info, skin_id, report_score}.
//
// Every value comes from the job itself: job_id, shift_id, shift_time,
// challenge_grade_point and scenario from the kickoff request the verdict
// keeps, the team result and each player's waves from the report, the
// profiles from @@Profile. kuma_point is the point cards' own rule.
//
// TODO(capture-needed): grade_before/after, grade_point_before/after and
// grade_rate. Only Nintendo computes a player's Grizzco grade, so they are
// left out and the game shows its default. Change coopPersonalData.
func RecordCoopResults(ctx context.Context, store db.Store, tenant, battleID string,
	report, verdict *commonpb.MapValue, sessionUsers map[string]string,
	profiles map[string]*commonpb.Value, now time.Time) error {
	if battleID == "" || strings.Contains(battleID, "/") || report == nil {
		return nil
	}
	rf, vf := report.GetFields(), verdict.GetFields()
	team := rf["team_result"]
	if team == nil {
		team = vf["team_result"]
	}
	tf := team.GetMapValue().GetFields()

	top := map[string]*commonpb.Value{"job_id": str(battleID)}
	for _, k := range []string{"job_id", "shift_id", "shift_time", "challenge_grade_point", "scenario"} {
		if v := vf[k]; v != nil {
			top[k] = v
		} else if v := rf[k]; v != nil {
			top[k] = v
		}
	}
	for _, k := range []string{"started_at", "result", "final_wave", "copper_scale_num",
		"silver_scale_num", "gold_scale_num", "wave_detail"} {
		if v := tf[k]; v != nil {
			top[k] = v
		}
	}

	finalWave := firstNamed(team, "final_wave")
	points := jobPoints(sumNamed(team, "delivered_golden_ikura_num"), finalWave)
	smell := vf["scenario"].GetMapValue().GetFields()["smell_num"]

	var players []string
	var personal []*commonpb.Value
	for key, entry := range rf["personal_result"].GetMapValue().GetFields() {
		uid := entry.GetMapValue().GetFields()["npln_user_id"].GetStringValue()
		if uid == "" {
			uid = sessionUsers[key]
		}
		if uid == "" || strings.Contains(uid, "/") {
			continue
		}
		players = append(players, uid)
		personal = append(personal, coopPersonalData(uid, entry, profiles[uid], points, smell))
	}
	top["personal_data"] = &commonpb.Value{ValueType: &commonpb.Value_ArrayValue{
		ArrayValue: &commonpb.ArrayValue{Values: personal}}}

	encoded, err := encodeFields(&commonpb.MapValue{Fields: top})
	if err != nil {
		return err
	}
	for _, uid := range players {
		name := "tenants/" + tenant + documentsSegment + "services/GameRecord/users/" + uid + "/CoopResults/" + battleID
		if err := store.PutUGCDocument(ctx, db.UGCDocument{
			Name: name, Owner: uid, Fields: encoded, CreateTime: now, UpdateTime: now,
		}); err != nil {
			return err
		}
	}
	return nil
}

// coopPersonalData is one player's personal_data entry, from their report.
func coopPersonalData(uid string, entry, playerInfo *commonpb.Value, points int64, smell *commonpb.Value) *commonpb.Value {
	ef := entry.GetMapValue().GetFields()
	out := map[string]*commonpb.Value{
		"npln_user_id": str(uid),
		"is_supporter": {ValueType: &commonpb.Value_BooleanValue{BooleanValue: false}},
		"kuma_point":   integer(points),
	}
	for _, k := range []string{"disconnected", "group_leader", "personal_wave_result", "report_score"} {
		if v := ef[k]; v != nil {
			out[k] = v
		}
	}
	if smell != nil {
		out["smell_before"], out["smell_after"] = smell, smell
	}
	profile := map[string]*commonpb.Value{}
	if playerInfo != nil {
		profile["player_info"] = playerInfo
	}
	if v := ef["report_score"]; v != nil {
		// The reader takes report_score from personal_profile.
		profile["report_score"] = v
	}
	out["personal_profile"] = &commonpb.Value{ValueType: &commonpb.Value_MapValue{
		MapValue: &commonpb.MapValue{Fields: profile}}}
	return &commonpb.Value{ValueType: &commonpb.Value_MapValue{MapValue: &commonpb.MapValue{Fields: out}}}
}
