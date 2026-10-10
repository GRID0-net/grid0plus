package gamesync

import (
	"testing"

	commonpb "github.com/n-popescu/grid0plus/proto/common"
)

func TestFestWinnersGetCloutAndOtherModesAreLeftAlone(t *testing.T) {
	player := func(slot int64) *commonpb.Value {
		return &commonpb.Value{ValueType: &commonpb.Value_MapValue{MapValue: &commonpb.MapValue{Fields: map[string]*commonpb.Value{
			"team": verdictInt(slot), "contribution": verdictInt(0),
		}}}}
	}
	results := func() *commonpb.Value {
		return &commonpb.Value{ValueType: &commonpb.Value_MapValue{MapValue: &commonpb.MapValue{Fields: map[string]*commonpb.Value{
			"w": player(2), "l": player(1),
		}}}}
	}
	team := &commonpb.Value{ValueType: &commonpb.Value_MapValue{MapValue: &commonpb.MapValue{Fields: map[string]*commonpb.Value{
		"winner": verdictInt(2),
	}}}}
	clout := func(v *commonpb.Value, id string) int64 {
		return v.GetMapValue().GetFields()[id].GetMapValue().GetFields()["contribution"].GetIntegerValue()
	}

	fest := results()
	festContribution("tenants/current/matchmakingConfigs/fest_match_regular_direct_pair_config", fest, team)
	if clout(fest, "w") != festWinnerContribution || clout(fest, "l") != 0 {
		t.Fatalf("fest clout: winner %d, loser %d", clout(fest, "w"), clout(fest, "l"))
	}
	for _, mode := range []string{"regular_normal_config", "fest_match_tricolor_oneshot_config"} {
		other := results()
		festContribution(mode, other, team)
		if clout(other, "w") != 0 {
			t.Fatalf("%s got clout %d", mode, clout(other, "w"))
		}
	}
}
