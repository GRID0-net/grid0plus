package toyohr

import (
	"context"
	"testing"
	"time"

	"google.golang.org/protobuf/encoding/protowire"

	toyohrpb "github.com/n-popescu/grid0plus/proto/toyohr/v1"
)

func wireBytesField(t *testing.T, wire []byte, want protowire.Number) []byte {
	t.Helper()
	for len(wire) > 0 {
		num, typ, n := protowire.ConsumeTag(wire)
		if n < 0 {
			t.Fatal(protowire.ParseError(n))
		}
		wire = wire[n:]
		if num == want {
			if typ != protowire.BytesType {
				t.Fatalf("field %d has wire type %d, want bytes", num, typ)
			}
			value, n := protowire.ConsumeBytes(wire)
			if n < 0 {
				t.Fatal(protowire.ParseError(n))
			}
			return value
		}
		n = protowire.ConsumeFieldValue(num, typ, wire)
		if n < 0 {
			t.Fatal(protowire.ParseError(n))
		}
		wire = wire[n:]
	}
	t.Fatalf("field %d absent on wire", want)
	return nil
}

// Every Normal Coop entry in both successful captures carries a real reward
// kind and id. This verifies the response bytes, including proto3 omission.
func TestNormalCoopShiftIncludesARealGearRewardOnWire(t *testing.T) {
	at := time.Date(2026, 9, 5, 1, 0, 0, 0, time.UTC)
	sc := scheduleServiceFor(t, at, Rotation{})
	shift := validNormalShift()
	if err := sc.s.SaveCoopShift(context.Background(), shift); err != nil {
		t.Fatal(err)
	}
	resp, err := sc.SelectCoopSchedules(context.Background(), &toyohrpb.SelectCoopSchedulesRequest{})
	if err != nil {
		t.Fatal(err)
	}
	entry := firstScheduleWire(t, resp)
	normal := wireBytesField(t, entry, 6)
	typeValue := string(wireBytesField(t, normal, 5))
	idBytes := normal
	var gearID uint64
	for len(idBytes) > 0 {
		num, typ, n := protowire.ConsumeTag(idBytes)
		if n < 0 {
			t.Fatal(protowire.ParseError(n))
		}
		idBytes = idBytes[n:]
		if num == 6 {
			if typ != protowire.VarintType {
				t.Fatalf("reward_gear_id has wire type %d", typ)
			}
			gearID, n = protowire.ConsumeVarint(idBytes)
			if n < 0 {
				t.Fatal(protowire.ParseError(n))
			}
			break
		}
		n = protowire.ConsumeFieldValue(num, typ, idBytes)
		if n < 0 {
			t.Fatal(protowire.ParseError(n))
		}
		idBytes = idBytes[n:]
	}
	if gearID == 0 {
		t.Fatal("reward_gear_id missing or zero on wire")
	}
	found := false
	for _, gear := range CoopRewardGears {
		if gear.ID == int32(gearID) && gear.Kind == typeValue {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("reward type/id (%s, %d) is not a real gear pair", typeValue, gearID)
	}
	again := coopRewardForShift(0, shift.Start)
	if again.ID != int32(gearID) || again.Kind != typeValue {
		t.Fatal("reward changed for the same shift")
	}
}

// TestEveryCapturedUsedRewardIsInTheRewardCatalog checks the catalog's rule
// against the only real data there is: every piece Nintendo listed as an
// already-used reward must be one the catalog can pick.
func TestEveryCapturedUsedRewardIsInTheRewardCatalog(t *testing.T) {
	catalog := map[CoopRewardGear]bool{}
	for _, g := range CoopRewardGears {
		catalog[g] = true
	}
	for _, g := range coopUsedRewardGears {
		if !catalog[g] {
			t.Errorf("captured used reward %+v is not in CoopRewardGears", g)
		}
	}
}
