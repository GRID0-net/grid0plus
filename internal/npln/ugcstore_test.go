package npln

import (
	"context"
	"testing"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/encoding/protowire"
	"google.golang.org/protobuf/types/known/fieldmaskpb"

	commonpb "github.com/n-popescu/switchnet/proto/common"
	"github.com/n-popescu/switchnet/proto/errdetails"
	toyohrpb "github.com/n-popescu/switchnet/proto/toyohr/v1"
	ugcpb "github.com/n-popescu/switchnet/proto/ugcstore/v1"
)

func intV(n int64) *commonpb.Value {
	return &commonpb.Value{ValueType: &commonpb.Value_IntegerValue{IntegerValue: n}}
}

func update(name string, fields map[string]*commonpb.Value, mask ...string) *ugcpb.WriteOperation {
	u := &ugcpb.UpdateDocumentRequest{Document: &ugcpb.Document{Name: name, Fields: &commonpb.MapValue{Fields: fields}}}
	if len(mask) > 0 {
		u.UpdateMask = &fieldmaskpb.FieldMask{Paths: mask}
	}
	return &ugcpb.WriteOperation{OperationType: &ugcpb.WriteOperation_UpdateDocument{UpdateDocument: u}}
}

func ugcHarness(t *testing.T) (*harness, context.Context, ugcpb.UgcstoreClient) {
	t.Helper()
	h := newHarness(t)
	h.createUser("ugc0000000000001", "device-ugc-1")
	h.createUser("ugc0000000000002", "device-ugc-2")
	ctx := h.ctx(h.sessionFor(t, "ugc0000000000001", "device-ugc-1"), "")
	return h, ctx, ugcpb.NewUgcstoreClient(h.conn)
}

// TestAMissingDocumentIsATwoFrameNotFound: the answer a fresh player's first
// read has to get. NotFound, with response headers of its own -- a single
// Trailers-Only frame, or an empty Document, is what crashed Splatoon 3.
func TestAMissingDocumentIsATwoFrameNotFound(t *testing.T) {
	_, ctx, c := ugcHarness(t)
	var header, trailer metadata.MD
	doc, err := c.GetDocument(ctx, &ugcpb.GetDocumentRequest{
		Name: "tenants/current/documents/services/Hammer/users/ugc0000000000001/UserAttribute/Data",
	}, grpc.Header(&header), grpc.Trailer(&trailer))
	if status.Code(err) != codes.NotFound {
		t.Fatalf("GetDocument = %v, %v; want NotFound", doc, err)
	}
	if got := header.Get("npln-grpc-type"); len(got) != 2 || got[0] != "ServerStreaming" || got[1] != "Unary" {
		t.Errorf("missing document grpc type = %v, want captured ServerStreaming, Unary", got)
	}
	if got := header.Get("content-length"); len(got) != 1 || got[0] != "0" {
		t.Errorf("missing document headers = %v, want zero-length body", header)
	}
	if len(trailer.Get("grpc-status-details-bin")) == 0 {
		t.Error("missing captured NError status detail")
	}
	st, _ := status.FromError(err)
	wantMessage := `NotFound. reason: "Document:"services/Hammer/users/ugc0000000000001/UserAttribute/Data" Not Found"`
	if st.Message() != wantMessage {
		t.Errorf("NotFound message = %q, want captured grammar %q", st.Message(), wantMessage)
	}
	if got := st.Details(); len(got) != 1 {
		t.Errorf("NotFound details count = %d, want one NError", len(got))
	} else if detail, ok := got[0].(*errdetails.NError); !ok {
		t.Errorf("NotFound detail = %T, want NError", got[0])
	} else {
		if len(detail.GetTraceId()) != 32 {
			t.Errorf("trace marker length = %d, want 32", len(detail.GetTraceId()))
		}
		unknown := detail.ProtoReflect().GetUnknown()
		n, typ, tagBytes := protowire.ConsumeTag(unknown)
		if n != 19 || typ != protowire.BytesType {
			t.Errorf("NError extension tag = %d/%d, want field 19 bytes", n, typ)
		} else if marker, _ := protowire.ConsumeString(unknown[tagBytes:]); len(marker) != 16 {
			t.Errorf("request marker length = %d, want 16", len(marker))
		}
	}
}

// TestAWrittenDocumentReadsBackUnderEitherTenantForm: the game writes and
// reads its own documents and may name its tenant "current" or in full.
func TestAWrittenDocumentReadsBackUnderEitherTenantForm(t *testing.T) {
	_, ctx, c := ugcHarness(t)
	path := "/documents/services/Hammer/users/ugc0000000000001/UserAttribute/Data"
	if _, err := c.CommitDocuments(ctx, &ugcpb.CommitDocumentsRequest{
		WriteOperations: []*ugcpb.WriteOperation{update("tenants/current"+path,
			map[string]*commonpb.Value{"level": intV(3)})},
	}); err != nil {
		t.Fatalf("CommitDocuments: %v", err)
	}
	for _, name := range []string{"tenants/current" + path, "tenants/" + testTenant + path} {
		var header metadata.MD
		doc, err := c.GetDocument(ctx, &ugcpb.GetDocumentRequest{Name: name}, grpc.Header(&header))
		if err != nil {
			t.Fatalf("GetDocument(%s): %v", name, err)
		}
		if got := header.Get("npln-grpc-type"); len(got) != 1 || got[0] != "ServerStreaming" {
			t.Errorf("populated document grpc type = %v, want captured ServerStreaming", got)
		}
		if doc.GetName() != name {
			t.Errorf("name = %q, want the name asked for, %q", doc.GetName(), name)
		}
		if doc.GetFields().GetFields()["level"].GetIntegerValue() != 3 {
			t.Errorf("fields = %v, want level=3", doc.GetFields())
		}
	}
}

// TestCommitAppliesMasksTransformsAndPreconditionsAllOrNothing covers the
// write semantics, and that a refused operation leaves the batch unwritten.
func TestCommitAppliesMasksTransformsAndPreconditionsAllOrNothing(t *testing.T) {
	_, ctx, c := ugcHarness(t)
	name := "tenants/current/documents/services/GameRecord/users/ugc0000000000001/PointCardTotal/Data"
	commit := func(ops ...*ugcpb.WriteOperation) (*ugcpb.CommitDocumentsResponse, error) {
		return c.CommitDocuments(ctx, &ugcpb.CommitDocumentsRequest{WriteOperations: ops})
	}
	get := func() map[string]*commonpb.Value {
		doc, err := c.GetDocument(ctx, &ugcpb.GetDocumentRequest{Name: name})
		if err != nil {
			t.Fatalf("GetDocument: %v", err)
		}
		return doc.GetFields().GetFields()
	}

	if _, err := commit(update(name, map[string]*commonpb.Value{"a": intV(1), "b": intV(2)})); err != nil {
		t.Fatal(err)
	}
	// A mask changes only its paths: a listed path missing from the document
	// is removed, an unlisted one is kept.
	if _, err := commit(update(name, map[string]*commonpb.Value{"a": intV(10)}, "a", "b")); err != nil {
		t.Fatal(err)
	}
	if f := get(); f["a"].GetIntegerValue() != 10 || f["b"] != nil {
		t.Errorf("after masked update: %v, want a=10 and b removed", f)
	}

	inc := &ugcpb.WriteOperation{OperationType: &ugcpb.WriteOperation_TransformDocument{
		TransformDocument: &ugcpb.TransformDocumentRequest{Name: name, FieldTransforms: []*ugcpb.FieldTransform{{
			FieldPath:     "a",
			TransformType: &ugcpb.FieldTransform_Increment{Increment: intV(5)},
		}}},
	}}
	res, err := commit(inc)
	if err != nil {
		t.Fatal(err)
	}
	if got := res.GetWriteResults()[0].GetTransformResults()[0].GetIntegerValue(); got != 15 {
		t.Errorf("increment result = %d, want 15 (and still an integer)", got)
	}

	// exists=false on a document that exists fails -- and the valid update
	// before it in the same batch must not be written either.
	mustNotExist := update(name, map[string]*commonpb.Value{"a": intV(99)})
	mustNotExist.GetUpdateDocument().CurrentDocument = &ugcpb.Precondition{
		ConditionType: &ugcpb.Precondition_Exists{Exists: false}}
	_, err = commit(update(name, map[string]*commonpb.Value{"a": intV(50)}, "a"), mustNotExist)
	if status.Code(err) != codes.FailedPrecondition {
		t.Fatalf("precondition: err = %v, want FailedPrecondition", err)
	}
	if f := get(); f["a"].GetIntegerValue() != 15 {
		t.Errorf("a refused batch was partly written: a = %v, want 15", f["a"])
	}
}

// TestAPlayerCannotWriteAnotherPlayersDocumentOrAnUnparseableName: the
// identity rule, and the name rule that keeps a malformed document out of
// storage (read back on every start, it would crash the game every time).
func TestAPlayerCannotWriteAnotherPlayersDocumentOrAnUnparseableName(t *testing.T) {
	_, ctx, c := ugcHarness(t)
	_, err := c.CommitDocuments(ctx, &ugcpb.CommitDocumentsRequest{WriteOperations: []*ugcpb.WriteOperation{
		update("tenants/current/documents/services/GameRecord/users/ugc0000000000002/VsUserAttribute/Data", nil),
	}})
	if status.Code(err) != codes.PermissionDenied {
		t.Errorf("writing another player's document: err = %v, want PermissionDenied", err)
	}
	for _, bad := range []string{
		"tenants/current/users/current",
		"tenants/current/documents/",
		"tenants/t-other-lp1/documents/x",
		"documents/x",
	} {
		_, err := c.CommitDocuments(ctx, &ugcpb.CommitDocumentsRequest{WriteOperations: []*ugcpb.WriteOperation{update(bad, nil)}})
		if status.Code(err) != codes.InvalidArgument {
			t.Errorf("name %q: err = %v, want InvalidArgument", bad, err)
		}
	}
}

// TestInitializeAttributesCreatesTheDocumentTheConsoleReadsNext is the round
// trip that stopped the game logging in: absent, initialise, read back.
func TestInitializeAttributesCreatesTheDocumentTheConsoleReadsNext(t *testing.T) {
	h, ctx, c := ugcHarness(t)
	gr := toyohrpb.NewGameRecordClient(h.conn)

	vs, err := gr.InitializeAttributes(ctx, &toyohrpb.InitializeAttributesRequest{
		User:      "users/ugc0000000000001",
		InitialVs: &toyohrpb.InitializeAttributesRequest_InitialVs{SeasonId: 7},
	})
	if err != nil {
		t.Fatalf("InitializeAttributes (vs): %v", err)
	}
	if want := "tenants/current/documents/services/GameRecord/users/ugc0000000000001/VsUserAttribute/Data"; vs.GetDocument() != want {
		t.Errorf("document = %q, want %q", vs.GetDocument(), want)
	}
	// Only what the request carries: the season. Nothing invented.
	if f := vs.GetAttributes().GetFields(); len(f) != 1 || f["season_id"].GetIntegerValue() != 7 {
		t.Errorf("attributes = %v, want exactly season_id=7", f)
	}
	doc, err := c.GetDocument(ctx, &ugcpb.GetDocumentRequest{Name: vs.GetDocument()})
	if err != nil || doc.GetFields().GetFields()["season_id"].GetIntegerValue() != 7 {
		t.Fatalf("reading the initialised document back: %v, %v", doc, err)
	}

	// A Salmon Run request gets a Salmon Run record, never the Versus one.
	coop, err := gr.InitializeAttributes(ctx, &toyohrpb.InitializeAttributesRequest{
		CoopShift: &toyohrpb.InitializeAttributesRequest_CoopShift{ShiftId: "shift-1", JobType: "regular"},
	})
	if err != nil {
		t.Fatalf("InitializeAttributes (coop): %v", err)
	}
	if want := "tenants/current/documents/services/GameRecord/users/ugc0000000000001/CoopUserAttribute/Data"; coop.GetDocument() != want {
		t.Errorf("coop document = %q, want %q", coop.GetDocument(), want)
	}

	// Nobody initialises somebody else's record.
	_, err = gr.InitializeAttributes(ctx, &toyohrpb.InitializeAttributesRequest{User: "users/ugc0000000000002"})
	if status.Code(err) != codes.PermissionDenied {
		t.Errorf("initialising another player: err = %v, want PermissionDenied", err)
	}
}

func TestInitializeCurrentUserKeepsEachPlayersDocumentsSeparate(t *testing.T) {
	h, first, docs := ugcHarness(t)
	gr := toyohrpb.NewGameRecordClient(h.conn)
	for i, uid := range []string{"ugc0000000000001", "ugc0000000000002"} {
		ctx := first
		if i == 1 {
			ctx = h.ctx(h.sessionFor(t, uid, "device-ugc-2"), "")
		}
		shift := "shift-" + uid
		resp, err := gr.InitializeAttributes(ctx, &toyohrpb.InitializeAttributesRequest{
			User:      "tenants/current/users/current",
			CoopShift: &toyohrpb.InitializeAttributesRequest_CoopShift{ShiftId: shift, JobType: "Normal"},
		})
		if err != nil {
			t.Fatalf("initialise current user %s: %v", uid, err)
		}
		want := "tenants/current/documents/services/GameRecord/users/" + uid + "/CoopUserAttribute/Data"
		if resp.GetDocument() != want {
			t.Fatalf("document = %q, want %q", resp.GetDocument(), want)
		}
		doc, err := docs.GetDocument(ctx, &ugcpb.GetDocumentRequest{Name: want})
		if err != nil || doc.GetFields().GetFields()["shift_id"].GetStringValue() != shift {
			t.Fatalf("read caller's document: %v, %v", doc, err)
		}
	}
	for _, name := range []string{"tenants/current/users/ugc0000000000002", "tenants/foreign/users/current"} {
		_, err := gr.InitializeAttributes(first, &toyohrpb.InitializeAttributesRequest{User: name})
		if status.Code(err) != codes.PermissionDenied {
			t.Errorf("initialise %q: %v, want PermissionDenied", name, err)
		}
	}
	_, err := gr.InitializeAttributes(h.ctx("", ""), &toyohrpb.InitializeAttributesRequest{User: "tenants/current/users/current"})
	if status.Code(err) != codes.Unauthenticated {
		t.Errorf("anonymous initialise: %v, want Unauthenticated", err)
	}
}

// TestAPlayersAttributeDocumentsExistOnFirstRead: a console told it has a
// cloud save reads CoopUserAttribute/Data and VsUserAttribute/Data and never
// calls InitializeAttributes (Nextendo's measurement of real Nintendo);
// NotFound there was followed by 2321-0384 on hardware on 2026-09-25.
func TestAPlayersAttributeDocumentsExistOnFirstRead(t *testing.T) {
	_, ctx, c := ugcHarness(t)
	base := "tenants/current/documents/services/GameRecord/users/ugc0000000000001/"
	vs, err := c.GetDocument(ctx, &ugcpb.GetDocumentRequest{Name: base + "VsUserAttribute/Data"})
	if err != nil {
		t.Fatalf("VsUserAttribute: %v", err)
	}
	if got := vs.GetFields().GetFields()["season_id"].GetIntegerValue(); got != 16 {
		t.Errorf("season_id = %d, want 16", got)
	}
	if vs.GetName() == "" {
		t.Error("document has no name; an unnamed document aborts the client")
	}
	if _, err := c.GetDocument(ctx, &ugcpb.GetDocumentRequest{Name: base + "CoopUserAttribute/Data"}); err != nil {
		t.Fatalf("CoopUserAttribute: %v", err)
	}
	// Someone else's attribute document is never created on their behalf.
	other := "tenants/current/documents/services/GameRecord/users/ugc0000000000002/VsUserAttribute/Data"
	if _, err := c.GetDocument(ctx, &ugcpb.GetDocumentRequest{Name: other}); status.Code(err) != codes.NotFound {
		t.Errorf("another player's attribute document = %v, want NotFound", err)
	}
}

// TestAnAbsentGameRecordDocumentIsNamedAndEmpty: WeaponPowerMeasurement/16
// answered NotFound was followed by 2000-0000 on hardware; Nextendo's working
// boot answers these with a named, empty document.
func TestAnAbsentGameRecordDocumentIsNamedAndEmpty(t *testing.T) {
	_, ctx, c := ugcHarness(t)
	name := "tenants/current/documents/services/GameRecord/users/ugc0000000000001/WeaponPowerMeasurement/16"
	doc, err := c.GetDocument(ctx, &ugcpb.GetDocumentRequest{Name: name})
	if err != nil {
		t.Fatalf("GetDocument = %v; want a named, empty document", err)
	}
	if doc.GetName() != name || len(doc.GetFields().GetFields()) != 0 {
		t.Errorf("got %v; want name %q and no fields", doc, name)
	}
}
