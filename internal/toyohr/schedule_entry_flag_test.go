package toyohr

import (
	"context"
	"testing"
	"time"

	"google.golang.org/protobuf/encoding/protowire"
	"google.golang.org/protobuf/proto"

	toyohrpb "github.com/n-popescu/switchnet/proto/toyohr/v1"
)

func firstScheduleWire(t *testing.T, response proto.Message) []byte {
	t.Helper()
	wire, err := proto.Marshal(response)
	if err != nil {
		t.Fatal(err)
	}
	for len(wire) > 0 {
		num, typ, n := protowire.ConsumeTag(wire)
		if n < 0 {
			t.Fatal(protowire.ParseError(n))
		}
		wire = wire[n:]
		if num == 2 && typ == protowire.BytesType {
			nested, n := protowire.ConsumeBytes(wire)
			if n < 0 {
				t.Fatal(protowire.ParseError(n))
			}
			return nested
		}
		n = protowire.ConsumeFieldValue(num, typ, wire)
		if n < 0 {
			t.Fatal(protowire.ParseError(n))
		}
		wire = wire[n:]
	}
	t.Fatal("no schedule entry on the wire")
	return nil
}

func requireEntryVarint(t *testing.T, wire []byte, field protowire.Number, want uint64) {
	t.Helper()
	for len(wire) > 0 {
		num, typ, n := protowire.ConsumeTag(wire)
		if n < 0 {
			t.Fatal(protowire.ParseError(n))
		}
		wire = wire[n:]
		if num == field {
			if typ != protowire.VarintType {
				t.Fatalf("entry field %d wire type %d, want varint", field, typ)
			}
			got, n := protowire.ConsumeVarint(wire)
			if n < 0 || got != want {
				t.Fatalf("entry field %d = %d (parse length %d), want %d", field, got, n, want)
			}
			return
		}
		n = protowire.ConsumeFieldValue(num, typ, wire)
		if n < 0 {
			t.Fatal(protowire.ParseError(n))
		}
		wire = wire[n:]
	}
	t.Fatalf("entry field %d absent on wire", field)
}

// The two successful captures agree on every populated entry: Coop field 2
// is 1 (35 entries), while Season fields 2 and 3 are both 16 (7 entries).
func TestScheduleEntriesCarryTheCapturedFlagsOnWire(t *testing.T) {
	at := time.Date(2026, 9, 5, 1, 0, 0, 0, time.UTC)
	sc := scheduleServiceFor(t, at, Rotation{})
	if err := sc.s.SaveCoopShift(context.Background(), validNormalShift()); err != nil {
		t.Fatal(err)
	}
	coop, err := sc.SelectCoopSchedules(context.Background(), &toyohrpb.SelectCoopSchedulesRequest{})
	if err != nil {
		t.Fatal(err)
	}
	season, err := sc.SelectSeasonSchedules(context.Background(), &toyohrpb.SelectSeasonSchedulesRequest{})
	if err != nil {
		t.Fatal(err)
	}
	requireEntryVarint(t, firstScheduleWire(t, coop), 2, 1)
	seasonWire := firstScheduleWire(t, season)
	requireEntryVarint(t, seasonWire, 2, 16)
	requireEntryVarint(t, seasonWire, 3, 16)
}
