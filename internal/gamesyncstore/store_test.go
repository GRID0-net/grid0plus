package gamesyncstore

import (
	"testing"
	"time"

	"google.golang.org/protobuf/types/known/fieldmaskpb"

	commonpb "github.com/n-popescu/switchnet/proto/common"
	v1 "github.com/n-popescu/switchnet/proto/gamesync/v1"
)

func fixedNow() func() time.Time {
	t := time.Date(2026, 9, 3, 12, 0, 0, 0, time.UTC)
	return func() time.Time { return t }
}

func strVal(s string) *commonpb.Value {
	return &commonpb.Value{ValueType: &commonpb.Value_StringValue{StringValue: s}}
}

func intVal(i int64) *commonpb.Value {
	return &commonpb.Value{ValueType: &commonpb.Value_IntegerValue{IntegerValue: i}}
}

func TestUpdateCreatesADocumentThatDidNotExist(t *testing.T) {
	s := New(fixedNow())
	doc, err := s.Update(&v1.Document{
		Name:   "tenants/t/games/g1",
		Fields: &commonpb.MapValue{Fields: map[string]*commonpb.Value{"state": strVal("lobby")}},
	}, nil, nil)
	if err != nil {
		t.Fatalf("Update: %v", err)
	}
	if doc.GetFields().GetFields()["state"].GetStringValue() != "lobby" {
		t.Errorf("got fields %+v", doc.GetFields())
	}
	if doc.GetCreateTime() == nil || !doc.GetCreateTime().AsTime().Equal(doc.GetUpdateTime().AsTime()) {
		t.Errorf("expected create_time == update_time on first write, got %+v", doc)
	}
}

func TestUpdateWithoutAMaskReplacesEveryField(t *testing.T) {
	s := New(fixedNow())
	name := "tenants/t/games/g1"
	if _, err := s.Update(&v1.Document{Name: name, Fields: &commonpb.MapValue{Fields: map[string]*commonpb.Value{
		"state": strVal("lobby"), "players": intVal(2),
	}}}, nil, nil); err != nil {
		t.Fatal(err)
	}

	doc, err := s.Update(&v1.Document{Name: name, Fields: &commonpb.MapValue{Fields: map[string]*commonpb.Value{
		"state": strVal("in_progress"),
	}}}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := doc.GetFields().GetFields()["players"]; ok {
		t.Errorf("full replace should have dropped 'players', got %+v", doc.GetFields())
	}
}

func TestUpdateWithAMaskLeavesOtherFieldsAlone(t *testing.T) {
	s := New(fixedNow())
	name := "tenants/t/games/g1"
	if _, err := s.Update(&v1.Document{Name: name, Fields: &commonpb.MapValue{Fields: map[string]*commonpb.Value{
		"state": strVal("lobby"), "players": intVal(2),
	}}}, nil, nil); err != nil {
		t.Fatal(err)
	}

	doc, err := s.Update(&v1.Document{Name: name, Fields: &commonpb.MapValue{Fields: map[string]*commonpb.Value{
		"state": strVal("in_progress"),
	}}}, &fieldmaskpb.FieldMask{Paths: []string{"state"}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if doc.GetFields().GetFields()["players"].GetIntegerValue() != 2 {
		t.Errorf("masked update should have kept 'players', got %+v", doc.GetFields())
	}
	if doc.GetFields().GetFields()["state"].GetStringValue() != "in_progress" {
		t.Errorf("masked update should have set 'state', got %+v", doc.GetFields())
	}
}

func TestUpdateExistsPreconditionRefusesACreateWhenTheDocumentAlreadyExists(t *testing.T) {
	s := New(fixedNow())
	name := "tenants/t/games/g1"
	if _, err := s.Update(&v1.Document{Name: name}, nil, nil); err != nil {
		t.Fatal(err)
	}
	_, err := s.Update(&v1.Document{Name: name}, nil, &v1.Precondition{ConditionType: &v1.Precondition_Exists{Exists: false}})
	if err == nil {
		t.Fatal("expected a precondition failure, got nil")
	}
}

func TestMergeKeepsFieldsTheIncomingDocumentDoesNotMention(t *testing.T) {
	s := New(fixedNow())
	name := "tenants/t/games/g1"
	if _, err := s.Merge(&v1.Document{Name: name, Fields: &commonpb.MapValue{Fields: map[string]*commonpb.Value{
		"state": strVal("lobby"), "players": intVal(2),
	}}}); err != nil {
		t.Fatal(err)
	}

	doc, err := s.Merge(&v1.Document{Name: name, Fields: &commonpb.MapValue{Fields: map[string]*commonpb.Value{
		"state": strVal("in_progress"),
	}}})
	if err != nil {
		t.Fatal(err)
	}
	if doc.GetFields().GetFields()["players"].GetIntegerValue() != 2 {
		t.Errorf("merge should have kept 'players', got %+v", doc.GetFields())
	}
}

func TestMergeRecursesIntoNestedMaps(t *testing.T) {
	s := New(fixedNow())
	name := "tenants/t/games/g1"
	nested := func(fields map[string]*commonpb.Value) *commonpb.Value {
		return &commonpb.Value{ValueType: &commonpb.Value_MapValue{MapValue: &commonpb.MapValue{Fields: fields}}}
	}
	if _, err := s.Merge(&v1.Document{Name: name, Fields: &commonpb.MapValue{Fields: map[string]*commonpb.Value{
		"score": nested(map[string]*commonpb.Value{"team_a": intVal(1), "team_b": intVal(2)}),
	}}}); err != nil {
		t.Fatal(err)
	}

	doc, err := s.Merge(&v1.Document{Name: name, Fields: &commonpb.MapValue{Fields: map[string]*commonpb.Value{
		"score": nested(map[string]*commonpb.Value{"team_a": intVal(5)}),
	}}})
	if err != nil {
		t.Fatal(err)
	}
	scoreFields := doc.GetFields().GetFields()["score"].GetMapValue().GetFields()
	if scoreFields["team_a"].GetIntegerValue() != 5 {
		t.Errorf("nested merge should have updated team_a, got %+v", scoreFields)
	}
	if scoreFields["team_b"].GetIntegerValue() != 2 {
		t.Errorf("nested merge should have kept team_b, got %+v", scoreFields)
	}
}

func TestTransformIncrementAddsToTheExistingValue(t *testing.T) {
	s := New(fixedNow())
	name := "tenants/t/games/g1"
	if _, err := s.Merge(&v1.Document{Name: name, Fields: &commonpb.MapValue{Fields: map[string]*commonpb.Value{
		"score": intVal(10),
	}}}); err != nil {
		t.Fatal(err)
	}

	doc, results, err := s.Transform(name, []*v1.FieldTransform{{
		FieldPath:     "score",
		TransformType: &v1.FieldTransform_Increment{Increment: intVal(5)},
	}})
	if err != nil {
		t.Fatal(err)
	}
	if doc.GetFields().GetFields()["score"].GetIntegerValue() != 15 {
		t.Errorf("got score %+v", doc.GetFields().GetFields()["score"])
	}
	if len(results) != 1 || results[0].GetIntegerValue() != 15 {
		t.Errorf("got results %+v", results)
	}
}

func TestTransformIncrementOnAMissingFieldTreatsItAsZero(t *testing.T) {
	s := New(fixedNow())
	doc, _, err := s.Transform("tenants/t/games/g1", []*v1.FieldTransform{{
		FieldPath:     "score",
		TransformType: &v1.FieldTransform_Increment{Increment: intVal(3)},
	}})
	if err != nil {
		t.Fatal(err)
	}
	if doc.GetFields().GetFields()["score"].GetIntegerValue() != 3 {
		t.Errorf("got %+v", doc.GetFields().GetFields()["score"])
	}
}

func TestTransformMaximumAndMinimum(t *testing.T) {
	s := New(fixedNow())
	name := "tenants/t/games/g1"
	if _, err := s.Merge(&v1.Document{Name: name, Fields: &commonpb.MapValue{Fields: map[string]*commonpb.Value{
		"best": intVal(10), "worst": intVal(10),
	}}}); err != nil {
		t.Fatal(err)
	}

	doc, _, err := s.Transform(name, []*v1.FieldTransform{
		{FieldPath: "best", TransformType: &v1.FieldTransform_Maximum{Maximum: intVal(20)}},
		{FieldPath: "worst", TransformType: &v1.FieldTransform_Minimum{Minimum: intVal(3)}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if doc.GetFields().GetFields()["best"].GetIntegerValue() != 20 {
		t.Errorf("got best %+v", doc.GetFields().GetFields()["best"])
	}
	if doc.GetFields().GetFields()["worst"].GetIntegerValue() != 3 {
		t.Errorf("got worst %+v", doc.GetFields().GetFields()["worst"])
	}
}

func TestTransformSetServerValueWritesTheCurrentTime(t *testing.T) {
	now := fixedNow()
	s := New(now)
	doc, results, err := s.Transform("tenants/t/games/g1", []*v1.FieldTransform{{
		FieldPath:     "updated_at",
		TransformType: &v1.FieldTransform_SetServerValue{SetServerValue: v1.FieldTransform_REQUEST_TIME},
	}})
	if err != nil {
		t.Fatal(err)
	}
	got := doc.GetFields().GetFields()["updated_at"].GetTimestampValue().AsTime()
	if !got.Equal(now()) {
		t.Errorf("got %v, want %v", got, now())
	}
	if !results[0].GetTimestampValue().AsTime().Equal(now()) {
		t.Errorf("result did not match: %+v", results[0])
	}
}

func TestDeleteIsIdempotentWithNoPrecondition(t *testing.T) {
	s := New(fixedNow())
	if err := s.Delete("tenants/t/games/nonexistent", nil); err != nil {
		t.Errorf("deleting a missing document with no precondition should not error: %v", err)
	}
}

func TestDeleteExistsPreconditionRefusesDeletingSomethingMissing(t *testing.T) {
	s := New(fixedNow())
	err := s.Delete("tenants/t/games/nonexistent", &v1.Precondition{ConditionType: &v1.Precondition_Exists{Exists: true}})
	if err == nil {
		t.Fatal("expected a precondition failure, got nil")
	}
}

func TestListReturnsOnlyDirectChildrenOfParent(t *testing.T) {
	s := New(fixedNow())
	for _, name := range []string{
		"tenants/t/games/g1", "tenants/t/games/g2",
		"tenants/t/games/g1/players/p1", // grandchild, must not appear
	} {
		if _, err := s.Merge(&v1.Document{Name: name}); err != nil {
			t.Fatal(err)
		}
	}

	docs, next := s.List("tenants/t/games", 0, "")
	if next != "" {
		t.Errorf("expected no next page, got %q", next)
	}
	if len(docs) != 2 {
		t.Fatalf("got %d docs, want 2: %+v", len(docs), docs)
	}
}

func TestListPaginates(t *testing.T) {
	s := New(fixedNow())
	for _, name := range []string{"tenants/t/games/g1", "tenants/t/games/g2", "tenants/t/games/g3"} {
		if _, err := s.Merge(&v1.Document{Name: name}); err != nil {
			t.Fatal(err)
		}
	}

	page1, next := s.List("tenants/t/games", 2, "")
	if len(page1) != 2 || next == "" {
		t.Fatalf("page1 = %+v, next = %q", page1, next)
	}
	page2, next := s.List("tenants/t/games", 2, next)
	if len(page2) != 1 || next != "" {
		t.Fatalf("page2 = %+v, next = %q", page2, next)
	}
}

func TestCollectionIDsReturnsDirectChildCollectionsOnly(t *testing.T) {
	s := New(fixedNow())
	for _, name := range []string{
		"tenants/t/games/g1/players/p1",
		"tenants/t/games/g1/rounds/r1",
		"tenants/t/games/g1/rounds/r1/events/e1", // deeper, must not appear
	} {
		if _, err := s.Merge(&v1.Document{Name: name}); err != nil {
			t.Fatal(err)
		}
	}

	ids := s.CollectionIDs("tenants/t/games/g1")
	if len(ids) != 2 || ids[0] != "players" || ids[1] != "rounds" {
		t.Errorf("got %v", ids)
	}
}

func TestCommitTransactionAppliesEverythingOrNothing(t *testing.T) {
	s := New(fixedNow())
	if _, err := s.Update(&v1.Document{Name: "tenants/t/games/g1"}, nil, nil); err != nil {
		t.Fatal(err)
	}

	token := s.BeginTransaction()
	ops := []*v1.WriteOperation{
		{OperationType: &v1.WriteOperation_UpdateDocument{UpdateDocument: &v1.UpdateDocumentRequest{
			Document: &v1.Document{Name: "tenants/t/games/g1", Fields: &commonpb.MapValue{Fields: map[string]*commonpb.Value{"state": strVal("started")}}},
		}}},
		{OperationType: &v1.WriteOperation_DeleteDocument{DeleteDocument: &v1.DeleteDocumentRequest{
			Name:            "tenants/t/games/does-not-exist",
			CurrentDocument: &v1.Precondition{ConditionType: &v1.Precondition_Exists{Exists: true}},
		}}},
	}
	_, err := s.CommitTransaction(token, ops)
	if err == nil {
		t.Fatal("expected the commit to fail on the second operation's precondition")
	}

	// Nothing from the batch should have landed, including the first
	// operation, which had no precondition problem of its own.
	if got := s.Get("tenants/t/games/g1").GetFields().GetFields()["state"]; got != nil {
		t.Errorf("first operation's write leaked despite the batch failing: %+v", got)
	}
}

func TestCommitTransactionRefusesAClosedToken(t *testing.T) {
	s := New(fixedNow())
	token := s.BeginTransaction()
	s.RollbackTransaction(token)
	if _, err := s.CommitTransaction(token, nil); err == nil {
		t.Fatal("expected committing a rolled-back transaction to fail")
	}
}

func TestSubscribeReceivesWritesMadeAfterSubscribing(t *testing.T) {
	s := New(fixedNow())
	ch, cancel := s.Subscribe()
	defer cancel()

	name := "tenants/t/games/g1"
	if _, err := s.Merge(&v1.Document{Name: name, Fields: &commonpb.MapValue{Fields: map[string]*commonpb.Value{"state": strVal("lobby")}}}); err != nil {
		t.Fatal(err)
	}

	select {
	case c := <-ch:
		if c.Type != Created || c.Name != name {
			t.Errorf("got %+v", c)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for the change")
	}
}

func TestSubscribeReceivesADeleteWithANilDocument(t *testing.T) {
	s := New(fixedNow())
	name := "tenants/t/games/g1"
	if _, err := s.Merge(&v1.Document{Name: name}); err != nil {
		t.Fatal(err)
	}

	ch, cancel := s.Subscribe()
	defer cancel()
	if err := s.Delete(name, nil); err != nil {
		t.Fatal(err)
	}

	select {
	case c := <-ch:
		if c.Type != Deleted || c.Document != nil {
			t.Errorf("got %+v", c)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for the change")
	}
}

func TestCancelStopsFurtherDelivery(t *testing.T) {
	s := New(fixedNow())
	ch, cancel := s.Subscribe()
	cancel()

	if _, err := s.Merge(&v1.Document{Name: "tenants/t/games/g1"}); err != nil {
		t.Fatal(err)
	}
	if _, ok := <-ch; ok {
		t.Error("expected the channel to be closed after cancel")
	}
}

func TestGetOfAMissingDocumentIsNilNotAZeroValue(t *testing.T) {
	s := New(fixedNow())
	if doc := s.Get("tenants/t/games/nonexistent"); doc != nil {
		t.Errorf("got %+v, want nil", doc)
	}
}

func TestMutatingAReturnedDocumentDoesNotAffectTheStore(t *testing.T) {
	s := New(fixedNow())
	name := "tenants/t/games/g1"
	if _, err := s.Merge(&v1.Document{Name: name, Fields: &commonpb.MapValue{Fields: map[string]*commonpb.Value{"state": strVal("lobby")}}}); err != nil {
		t.Fatal(err)
	}

	doc := s.Get(name)
	doc.GetFields().GetFields()["state"] = strVal("tampered")

	if got := s.Get(name).GetFields().GetFields()["state"].GetStringValue(); got != "lobby" {
		t.Errorf("store's own copy was mutated through the returned document: got %q", got)
	}
}
