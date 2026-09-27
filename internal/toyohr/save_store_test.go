package toyohr

import (
	"testing"
	"time"

	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/n-popescu/grid0plus/proto/common"
)

func mapV(f map[string]*common.Value) *common.Value {
	return &common.Value{ValueType: &common.Value_MapValue{MapValue: &common.MapValue{Fields: f}}}
}
func arrV(v ...*common.Value) *common.Value {
	return &common.Value{ValueType: &common.Value_ArrayValue{ArrayValue: &common.ArrayValue{Values: v}}}
}

var nullV = &common.Value{ValueType: &common.Value_NullValue{}}

// TestASaveDeltaMergesTheWayNintendosServerDoes pins the four merge rules
// NextendoNetwork/splatoon-3 reproduced against real Nintendo records.
func TestASaveDeltaMergesTheWayNintendosServerDoes(t *testing.T) {
	stored := &common.MapValue{Fields: map[string]*common.Value{
		"Money": intValue(100),
		"Coop": mapV(map[string]*common.Value{
			"Grade":   intValue(2),
			"Rewards": mapV(map[string]*common.Value{"0": intValue(7), "1": intValue(8)}),
		}),
		"Tags": arrV(intValue(1), intValue(2)),
	}}
	mergeSaveDelta(stored, &common.MapValue{Fields: map[string]*common.Value{
		"Money": intValue(50),                                                                          // replaces
		"Coop":  mapV(map[string]*common.Value{"Rewards": mapV(map[string]*common.Value{"0": nullV})}), // merges, null deletes
		"Tags":  arrV(),                                                                                // empty array stored as null
		"New":   mapV(map[string]*common.Value{"A": intValue(1)}),                                      // new key
	}})
	f := stored.Fields
	if f["Money"].GetIntegerValue() != 50 {
		t.Errorf("scalar was not replaced: %v", f["Money"])
	}
	coop := f["Coop"].GetMapValue().GetFields()
	if coop["Grade"].GetIntegerValue() != 2 {
		t.Error("merging a nested map dropped a sibling key it did not mention")
	}
	rewards := coop["Rewards"].GetMapValue().GetFields()
	if _, ok := rewards["0"]; ok {
		t.Error("an explicit null did not delete the key")
	}
	if rewards["1"].GetIntegerValue() != 8 {
		t.Error("deleting one key disturbed another")
	}
	if _, ok := f["Tags"].GetValueType().(*common.Value_NullValue); !ok {
		t.Errorf("empty array stored as %v, want null", f["Tags"])
	}
	if f["New"].GetMapValue().GetFields()["A"].GetIntegerValue() != 1 {
		t.Error("new map key not added")
	}
}

// TestTheSaveVersionTokenAlwaysMovesForward: the console presents the last
// update_time as the next write's version, so two writes in the same instant
// (the console sends them concurrently) must still get distinct, ordered
// tokens.
func TestTheSaveVersionTokenAlwaysMovesForward(t *testing.T) {
	now := time.Date(2026, 9, 25, 12, 0, 0, 0, time.UTC)
	a := nextSaveVersion(now, nil)
	b := nextSaveVersion(now, a)
	c := nextSaveVersion(now.Add(-time.Second), b)
	if !b.AsTime().After(a.AsTime()) || !c.AsTime().After(b.AsTime()) {
		t.Errorf("tokens not strictly increasing: %v %v %v", a.AsTime(), b.AsTime(), c.AsTime())
	}
	if got := nextSaveVersion(now.Add(time.Nanosecond*1500), timestamppb.New(now)); got.AsTime().Nanosecond()%1000 != 0 {
		t.Errorf("token %v has sub-microsecond precision the store would not round-trip", got.AsTime())
	}
}
