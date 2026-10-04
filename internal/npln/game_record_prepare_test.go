package npln

import (
	"strings"
	"testing"

	"google.golang.org/protobuf/encoding/protowire"
	"google.golang.org/protobuf/proto"

	toyohrpb "github.com/n-popescu/grid0plus/proto/toyohr/v1"
	ugcpb "github.com/n-popescu/grid0plus/proto/ugcstore/v1"
)

// wireFields splits one protobuf message into its top-level fields by number,
// keeping the raw bytes of each length-delimited one.
func wireFields(t *testing.T, b []byte) map[protowire.Number][]byte {
	t.Helper()
	out := map[protowire.Number][]byte{}
	for len(b) > 0 {
		num, typ, n := protowire.ConsumeTag(b)
		if n < 0 {
			t.Fatalf("bad tag: %v", protowire.ParseError(n))
		}
		b = b[n:]
		if typ == protowire.BytesType {
			v, m := protowire.ConsumeBytes(b)
			out[num], b = v, b[m:]
			continue
		}
		m := protowire.ConsumeFieldValue(num, typ, b)
		out[num], b = nil, b[m:]
	}
	return out
}

// TestAPreparedWeaponPowerMeasurementIsTheDocumentTheGameReads: the shape the
// game parses (recovered from its generated parser) is a response whose field
// 1 is a measurement -- name 1, fields 2, times 3/4, document 5 -- and the
// document must be the one it already reads, WeaponPowerMeasurement/<weapon>.
func TestAPreparedWeaponPowerMeasurementIsTheDocumentTheGameReads(t *testing.T) {
	h, ctx, c := ugcHarness(t)
	gr := toyohrpb.NewGameRecordClient(h.conn)

	resp, err := gr.PrepareWeaponPowerMeasurement(ctx, &toyohrpb.PrepareWeaponPowerMeasurementRequest{
		Parent: "tenants/current/users/current", Weapon: 16,
	})
	if err != nil {
		t.Fatalf("PrepareWeaponPowerMeasurement: %v", err)
	}
	b, _ := proto.Marshal(resp)
	m := wireFields(t, wireFields(t, b)[1])
	want := "tenants/current/documents/services/GameRecord/users/ugc0000000000001/WeaponPowerMeasurement/16"
	if string(m[5]) != want || string(m[1]) != want {
		t.Errorf("measurement name/document = %q/%q, want %q", m[1], m[5], want)
	}
	if _, ok := m[3]; !ok {
		t.Error("measurement has no create_time (field 3)")
	}
	if doc, err := c.GetDocument(ctx, &ugcpb.GetDocumentRequest{Name: want}); err != nil || doc.GetName() == "" {
		t.Fatalf("reading the measurement back: %v, %v", doc, err)
	}

	for _, kind := range []string{"Bankara", "League"} {
		var out proto.Message
		if kind == "Bankara" {
			out, err = gr.PrepareBankaraPowerMeasurement(ctx, &toyohrpb.PrepareBankaraPowerMeasurementRequest{Unknown_3: 3})
		} else {
			out, err = gr.PrepareLeaguePowerMeasurement(ctx, &toyohrpb.PrepareLeaguePowerMeasurementRequest{Unknown_3: 3})
		}
		if err != nil {
			t.Fatalf("Prepare%sPowerMeasurement: %v", kind, err)
		}
		b, _ := proto.Marshal(out)
		if doc := string(wireFields(t, wireFields(t, b)[1])[5]); !strings.HasSuffix(doc, "/users/ugc0000000000001/"+kind+"PowerMeasurement/3") {
			t.Errorf("%s measurement document = %q", kind, doc)
		}
	}
}

// TestPrepareTagAndResetUdemaeAnswerWellFormed: shapes recovered, meaning not;
// the answers must at least parse as the game's messages, and PrepareTag's
// response must carry its tag (field 1), which the game copies out.
func TestPrepareTagAndResetUdemaeAnswerWellFormed(t *testing.T) {
	h, ctx, _ := ugcHarness(t)
	gr := toyohrpb.NewGameRecordClient(h.conn)

	tag, err := gr.PrepareTag(ctx, &toyohrpb.PrepareTagRequest{Tenant: "tenants/current", Users: []string{"a", "b"}})
	if err != nil {
		t.Fatalf("PrepareTag: %v", err)
	}
	b, _ := proto.Marshal(tag)
	if _, ok := wireFields(t, b)[1]; !ok {
		t.Error("PrepareTag response has no tag (field 1)")
	}
	if _, err := gr.ResetUdemae(ctx, &toyohrpb.ResetUdemaeRequest{}); err != nil {
		t.Fatalf("ResetUdemae: %v", err)
	}
}
