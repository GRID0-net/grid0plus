package toyohr

import (
	"context"
	"testing"
	"time"

	"google.golang.org/protobuf/encoding/protowire"
	"google.golang.org/protobuf/proto"

	toyohrpb "github.com/n-popescu/grid0plus/proto/toyohr/v1"
)

// Both preserved working hall sessions set field 1 in every nonempty Versus,
// Coop, Season, and VsParams response. A false proto3 bool disappears from
// the wire entirely, which is what GRID0+ used to send.
func TestPopulatedSchedulesCarryTheCapturedUpdateFlagOnWire(t *testing.T) {
	at := time.Date(2026, 9, 5, 1, 0, 0, 0, time.UTC)
	sc := scheduleServiceFor(t, at, Rotation{RegularStages: []int32{1, 2}})
	vs, err := sc.SelectVsSchedules(context.Background(), &toyohrpb.SelectVsSchedulesRequest{})
	if err != nil {
		t.Fatal(err)
	}
	params, err := sc.SelectVsParams(context.Background(), &toyohrpb.SelectVsParamsRequest{})
	if err != nil {
		t.Fatal(err)
	}
	season, err := sc.SelectSeasonSchedules(context.Background(), &toyohrpb.SelectSeasonSchedulesRequest{})
	if err != nil {
		t.Fatal(err)
	}
	if err := sc.s.SaveCoopShift(context.Background(), validNormalShift()); err != nil {
		t.Fatal(err)
	}
	coop, err := sc.SelectCoopSchedules(context.Background(), &toyohrpb.SelectCoopSchedulesRequest{})
	if err != nil {
		t.Fatal(err)
	}
	if len(coop.GetSchedules()) == 0 {
		t.Fatal("fixture produced no Coop schedule")
	}

	for name, msg := range map[string]proto.Message{
		"Versus": vs, "VsParams": params, "Season": season, "Coop": coop,
	} {
		t.Run(name, func(t *testing.T) {
			b, err := proto.Marshal(msg)
			if err != nil {
				t.Fatal(err)
			}
			for len(b) > 0 {
				num, typ, n := protowire.ConsumeTag(b)
				if n < 0 {
					t.Fatal(protowire.ParseError(n))
				}
				b = b[n:]
				if num == 1 {
					if typ != protowire.VarintType {
						t.Fatalf("field 1 wire type = %v, want varint", typ)
					}
					v, m := protowire.ConsumeVarint(b)
					if m < 0 || v != 1 {
						t.Fatalf("field 1 = %d (parse length %d), want true", v, m)
					}
					return
				}
				m := protowire.ConsumeFieldValue(num, typ, b)
				if m < 0 {
					t.Fatal(protowire.ParseError(m))
				}
				b = b[m:]
			}
			t.Fatal("captured update flag is absent on the wire")
		})
	}
}
