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
// TODO(capture-needed): the full VsResults field set the history screen
// reads. If an entry is listed but its detail page is empty or errors, this
// function's field list is what to extend.
func RecordVsResults(ctx context.Context, store db.Store, tenant, battleID string,
	players []string, report *commonpb.MapValue, now time.Time) error {
	if battleID == "" || strings.Contains(battleID, "/") || report == nil {
		return nil
	}
	teams := map[string][]*commonpb.Value{
		"personal_data_team1": nil, "personal_data_team2": nil, "personal_data_team3": nil,
	}
	for _, p := range report.GetFields()["personal_result"].GetMapValue().GetFields() {
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
