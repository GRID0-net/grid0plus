package gamesync

import (
	"context"
	"testing"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	commonpb "github.com/n-popescu/grid0plus/proto/common"
	syncpb "github.com/n-popescu/grid0plus/proto/gamesync/v1"
)

func TestACommittedRefereeRequestAndReportPublishTheVerdictsTheGameWatches(t *testing.T) {
	h := newHarness(t)
	alice := h.session("token-alice")
	bob := h.session("token-bob")
	h.srv.mu.Lock()
	for _, sess := range h.srv.sessions {
		sid := "other-match"
		if sess.userID == "alice" {
			sid = "match-1"
		}
		sess.details = &SessionDetails{UserID: sess.userID, SessionID: sid}
	}
	h.srv.mu.Unlock()

	const battle = "20260930T160000_match-1"
	const resultName = "docs/@@RefereeResult/" + battle
	watchCtx, cancel := context.WithTimeout(alice, 3*time.Second)
	defer cancel()
	stream, err := h.client.KeepUserSession(watchCtx)
	if err != nil {
		t.Fatal(err)
	}
	if err := stream.Send(&syncpb.KeepUserSessionRequest{
		RequestType: &syncpb.KeepUserSessionRequest_UpdateTarget{
			UpdateTarget: &syncpb.UpdateTargetRequest{Target: &syncpb.Target{
				Name: "userSessions/current/targets/9",
				TargetType: &syncpb.Target_Collection{Collection: &syncpb.CollectionTarget{
					Collection: "docs/@@RefereeResult",
				}},
			}},
		},
	}); err != nil {
		t.Fatal(err)
	}
	for {
		msg, err := stream.Recv()
		if err != nil {
			t.Fatal(err)
		}
		if msg.GetTargetChange().GetTargetChangeType() == syncpb.TargetChange_LISTED {
			break
		}
	}
	write := func(name string, fields map[string]*commonpb.Value) {
		t.Helper()
		if _, err := h.client.WriteDocuments(alice, &syncpb.WriteDocumentsRequest{
			WriteOperations: []*syncpb.WriteOperation{merge(doc(name, fields))},
		}); err != nil {
			t.Fatal(err)
		}
	}

	write(refereeRequestPrefix+battle, map[string]*commonpb.Value{
		"battle_id": verdictString(battle),
		"stage":     intValue(14),
		"users": {ValueType: &commonpb.Value_ArrayValue{ArrayValue: &commonpb.ArrayValue{
			Values: []*commonpb.Value{verdictString("alice"), verdictString("bob")},
		}}},
	})
	for {
		msg, err := stream.Recv()
		if err != nil {
			t.Fatal(err)
		}
		if d := msg.GetDocumentChange(); d != nil && d.GetDocument().GetName() == resultName {
			if d.GetDocument().GetFields().GetFields()["status"].GetIntegerValue() != 0 {
				t.Fatal("watch did not receive the kickoff verdict")
			}
			break
		}
	}
	got, err := h.client.GetDocument(alice, &syncpb.GetDocumentRequest{Name: resultName})
	if err != nil {
		t.Fatal(err)
	}
	f := got.GetFields().GetFields()
	if f["status"].GetIntegerValue() != 0 || f["request_id"].GetStringValue() != battle ||
		f["stage"].GetIntegerValue() != 14 || f["trace_id"].GetStringValue() != refereeTraceID(battle) || f["battle_id"] != nil {
		t.Fatalf("kickoff verdict has wrong wire fields: status=%d request_id=%q stage=%d trace_id_empty=%v battle_id_present=%v",
			f["status"].GetIntegerValue(), f["request_id"].GetStringValue(), f["stage"].GetIntegerValue(),
			f["trace_id"].GetStringValue() == "", f["battle_id"] != nil)
	}
	if _, err := h.client.GetDocument(bob, &syncpb.GetDocumentRequest{Name: resultName}); status.Code(err) != codes.NotFound {
		t.Fatalf("another match read the verdict: %v", err)
	}
	listed, err := h.client.ListDocuments(bob, &syncpb.ListDocumentsRequest{Parent: "docs/@@RefereeResult"})
	if err != nil || len(listed.GetDocuments()) != 0 {
		t.Fatalf("another match listed the verdict: docs=%d err=%v", len(listed.GetDocuments()), err)
	}

	write(refereeReportPrefix+battle+"/User/token-alice", map[string]*commonpb.Value{
		"team_result": {ValueType: &commonpb.Value_MapValue{MapValue: &commonpb.MapValue{Fields: map[string]*commonpb.Value{
			"winner": intValue(1),
		}}}},
		"personal_result": {ValueType: &commonpb.Value_MapValue{MapValue: &commonpb.MapValue{Fields: map[string]*commonpb.Value{
			"token-alice": {ValueType: &commonpb.Value_MapValue{MapValue: &commonpb.MapValue{Fields: map[string]*commonpb.Value{
				"team": intValue(1), "kill_num": intValue(5), "npln_user_id": stringValue("alice"),
			}}}},
		}}}},
	})
	got, err = h.client.GetDocument(alice, &syncpb.GetDocumentRequest{Name: resultName})
	if err != nil {
		t.Fatal(err)
	}
	f = got.GetFields().GetFields()
	player := f["personal_result"].GetMapValue().GetFields()["token-alice"].GetMapValue().GetFields()
	if f["status"].GetIntegerValue() != 1 || f["stage"].GetIntegerValue() != 14 ||
		len(player) != 23 || player["deemed_result"].GetIntegerValue() != 0 || player["kill_num"].GetIntegerValue() != 5 {
		t.Fatalf("final verdict did not preserve the match and fill the player's result: status=%d stage=%d player_fields=%d deemed=%d kills=%d",
			f["status"].GetIntegerValue(), f["stage"].GetIntegerValue(), len(player),
			player["deemed_result"].GetIntegerValue(), player["kill_num"].GetIntegerValue())
	}
	h.srv.mu.Lock()
	for _, sess := range h.srv.sessions {
		if sess.userID == "bob" {
			sess.details = &SessionDetails{UserID: "bob", SessionID: "match-1"}
		}
	}
	h.srv.mu.Unlock()
	mapValue := func(fields map[string]*commonpb.Value) *commonpb.Value {
		return &commonpb.Value{ValueType: &commonpb.Value_MapValue{MapValue: &commonpb.MapValue{Fields: fields}}}
	}
	bobReport := map[string]*commonpb.Value{
		"team_result": mapValue(map[string]*commonpb.Value{"winner": intValue(1)}),
		"personal_result": mapValue(map[string]*commonpb.Value{
			"token-bob": mapValue(map[string]*commonpb.Value{"team": intValue(2), "npln_user_id": stringValue("bob")}),
		}),
	}
	if _, err := h.client.WriteDocuments(bob, &syncpb.WriteDocumentsRequest{
		WriteOperations: []*syncpb.WriteOperation{merge(doc(refereeReportPrefix+battle+"/User/token-bob", bobReport))},
	}); err != nil {
		t.Fatal(err)
	}

	write(refereeRequestPrefix+battle, map[string]*commonpb.Value{
		"users": {ValueType: &commonpb.Value_ArrayValue{ArrayValue: &commonpb.ArrayValue{}}},
	})
	got, err = h.client.GetDocument(alice, &syncpb.GetDocumentRequest{Name: resultName})
	if err != nil {
		t.Fatal(err)
	}
	f = got.GetFields().GetFields()
	people := f["personal_result"].GetMapValue().GetFields()
	if f["status"].GetIntegerValue() != 1 || len(people) != 2 ||
		people["token-alice"].GetMapValue().GetFields()["kill_num"].GetIntegerValue() != 5 {
		t.Fatalf("a later report or retried kickoff discarded the final verdict: status=%d players=%d",
			f["status"].GetIntegerValue(), len(people))
	}
}

func TestBothRoomMembersCanAcknowledgeTheSameRefereeKickoff(t *testing.T) {
	h := newHarness(t)
	alice := h.session("token-alice")
	bob := h.session("token-bob")
	h.srv.mu.Lock()
	for _, sess := range h.srv.sessions {
		sess.details = &SessionDetails{UserID: sess.userID, SessionID: "room-1"}
	}
	h.srv.mu.Unlock()
	name := refereeRequestPrefix + "20261001T130000_room-1"
	write := func(ctx context.Context, stage int64) (*syncpb.WriteDocumentsResponse, error) {
		return h.client.WriteDocuments(ctx, &syncpb.WriteDocumentsRequest{WriteOperations: []*syncpb.WriteOperation{{
			OperationType: &syncpb.WriteOperation_UpdateDocument{UpdateDocument: &syncpb.UpdateDocumentRequest{
				Document: doc(name, map[string]*commonpb.Value{
					"users": {ValueType: &commonpb.Value_ArrayValue{ArrayValue: &commonpb.ArrayValue{Values: []*commonpb.Value{
						verdictString("alice"), verdictString("bob"),
					}}}},
					"stage": intValue(stage),
				}),
				CurrentDocument: &syncpb.Precondition{ConditionType: &syncpb.Precondition_Exists{Exists: false}},
			}},
		}}})
	}
	if _, err := write(alice, 14); err != nil {
		t.Fatalf("first kickoff: %v", err)
	}
	ack, err := write(bob, 99)
	if err != nil || len(ack.GetWriteResults()) != 1 {
		t.Fatalf("second room member did not receive kickoff acknowledgement: results=%v err=%v", ack.GetWriteResults(), err)
	}
	stored, err := h.client.GetDocument(alice, &syncpb.GetDocumentRequest{Name: name})
	if err != nil || stored.GetFields().GetFields()["stage"].GetIntegerValue() != 14 {
		t.Fatalf("duplicate kickoff changed the first request: doc=%v err=%v", stored, err)
	}
}

func TestARefereeRequestForAnotherSessionDoesNotPublishAVerdict(t *testing.T) {
	h := newHarness(t)
	alice := h.session("token-alice")
	h.srv.mu.Lock()
	for _, sess := range h.srv.sessions {
		sess.details = &SessionDetails{UserID: "alice", SessionID: "match-1"}
	}
	h.srv.mu.Unlock()
	const battle = "20260930T160000_match-2"
	_, err := h.client.WriteDocuments(alice, &syncpb.WriteDocumentsRequest{
		WriteOperations: []*syncpb.WriteOperation{merge(doc(refereeRequestPrefix+battle, map[string]*commonpb.Value{
			"users": {ValueType: &commonpb.Value_ArrayValue{ArrayValue: &commonpb.ArrayValue{}}},
		}))},
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = h.client.GetDocument(context.Background(), &syncpb.GetDocumentRequest{Name: refereeResultPrefix + battle})
	if status.Code(err) != codes.Unauthenticated {
		t.Fatalf("unauthenticated read = %v", err)
	}
	for scope, st := range h.srv.docs.stores {
		if _, ok := st.snapshot(refereeResultPrefix + battle); ok {
			t.Fatalf("another session's request created a verdict in %q", scope)
		}
	}
}

func TestTheRefereeTraceIDIsTheSessionIDWithoutDashes(t *testing.T) {
	// The working Nextendo verdict for this battle carries exactly this trace_id.
	got := refereeTraceID("20260930T153234_bc0ffe09-6aa4-443b-aaeb-463be2e7e135")
	if got != "bc0ffe096aa4443baaeb463be2e7e135" {
		t.Fatalf("trace_id = %q", got)
	}
}

// The game ignores the kickoff verdict until it has queued its @@Profile write,
// so a verdict sent only at kickoff leaves every console waiting until its
// 30-second referee timeout.
func TestTheKickoffVerdictIsSentAgainAfterAConsoleWritesItsProfile(t *testing.T) {
	h := newHarness(t)
	alice := h.session("token-alice")
	h.srv.mu.Lock()
	for _, sess := range h.srv.sessions {
		sess.details = &SessionDetails{UserID: sess.userID, SessionID: "match-1"}
	}
	h.srv.mu.Unlock()

	const battle = "20261003T112420_match-1"
	const resultName = refereeResultPrefix + battle
	ctx, cancel := context.WithTimeout(alice, 3*time.Second)
	defer cancel()
	stream, err := h.client.KeepUserSession(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := stream.Send(&syncpb.KeepUserSessionRequest{
		RequestType: &syncpb.KeepUserSessionRequest_UpdateTarget{
			UpdateTarget: &syncpb.UpdateTargetRequest{Target: &syncpb.Target{
				Name: "userSessions/current/targets/8",
				TargetType: &syncpb.Target_Collection{Collection: &syncpb.CollectionTarget{
					Collection: "docs/@@RefereeResult",
				}},
			}},
		},
	}); err != nil {
		t.Fatal(err)
	}
	for {
		msg, err := stream.Recv()
		if err != nil {
			t.Fatal(err)
		}
		if msg.GetTargetChange().GetTargetChangeType() == syncpb.TargetChange_LISTED {
			break
		}
	}
	verdict := func(after string) {
		t.Helper()
		for {
			msg, err := stream.Recv()
			if err != nil {
				t.Fatalf("no kickoff verdict after %s: %v", after, err)
			}
			if d := msg.GetDocumentChange(); d != nil && d.GetDocument().GetName() == resultName {
				if d.GetDocument().GetFields().GetFields()["status"].GetIntegerValue() != 0 {
					t.Fatalf("verdict after %s does not carry status 0", after)
				}
				return
			}
		}
	}
	write := func(name string) {
		t.Helper()
		if _, err := h.client.WriteDocuments(alice, &syncpb.WriteDocumentsRequest{
			WriteOperations: []*syncpb.WriteOperation{merge(doc(name, map[string]*commonpb.Value{
				"battle_id": verdictString(battle),
				"users": {ValueType: &commonpb.Value_ArrayValue{ArrayValue: &commonpb.ArrayValue{
					Values: []*commonpb.Value{verdictString("alice")},
				}}},
			}))},
		}); err != nil {
			t.Fatal(err)
		}
	}
	write(refereeRequestPrefix + battle)
	verdict("the kickoff request")
	write(refereeProfilePrefix + battle + "/User/us-alice")
	verdict("@@Profile")
}

// Nextendo re-sends an unchanged verdict every refresh; a console that ignored
// an earlier copy must get another without having to write anything.
func TestAnUnchangedVerdictIsStillRefreshed(t *testing.T) {
	h := newHarness(t)
	h.srv.refreshEvery = 20 * time.Millisecond
	alice := h.session("token-alice")
	h.srv.mu.Lock()
	for _, sess := range h.srv.sessions {
		sess.details = &SessionDetails{UserID: sess.userID, SessionID: "match-1"}
	}
	h.srv.mu.Unlock()
	const battle = "20261003T112420_match-1"
	if _, err := h.client.WriteDocuments(alice, &syncpb.WriteDocumentsRequest{
		WriteOperations: []*syncpb.WriteOperation{merge(doc(refereeRequestPrefix+battle, map[string]*commonpb.Value{
			"users": {ValueType: &commonpb.Value_ArrayValue{ArrayValue: &commonpb.ArrayValue{
				Values: []*commonpb.Value{verdictString("alice")},
			}}},
		}))},
	}); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(alice, 3*time.Second)
	defer cancel()
	stream, err := h.client.KeepUserSession(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := stream.Send(&syncpb.KeepUserSessionRequest{
		RequestType: &syncpb.KeepUserSessionRequest_UpdateTarget{
			UpdateTarget: &syncpb.UpdateTargetRequest{Target: &syncpb.Target{
				Name: "userSessions/current/targets/8",
				TargetType: &syncpb.Target_Collection{Collection: &syncpb.CollectionTarget{
					Collection: "docs/@@RefereeResult",
				}},
			}},
		},
	}); err != nil {
		t.Fatal(err)
	}
	copies := 0
	for copies < 3 {
		msg, err := stream.Recv()
		if err != nil {
			t.Fatalf("only %d copies of the verdict: %v", copies, err)
		}
		if d := msg.GetDocumentChange(); d != nil && d.GetDocument().GetName() == refereeResultPrefix+battle {
			copies++
		}
	}
}

func TestADisconnectResultIsScoredAsALossNotADisconnect(t *testing.T) {
	integer := func(n int64) *commonpb.Value {
		return &commonpb.Value{ValueType: &commonpb.Value_IntegerValue{IntegerValue: n}}
	}
	player := func(fields map[string]*commonpb.Value) *commonpb.Value {
		return &commonpb.Value{ValueType: &commonpb.Value_MapValue{MapValue: &commonpb.MapValue{Fields: fields}}}
	}
	in := player(map[string]*commonpb.Value{
		"dropped": player(map[string]*commonpb.Value{"team": integer(1), "deemed_result": integer(2)}),
		"flagged": player(map[string]*commonpb.Value{"team": integer(1),
			"disconnected": {ValueType: &commonpb.Value_BooleanValue{BooleanValue: true}}}),
		"winner": player(map[string]*commonpb.Value{"team": integer(1)}),
	})
	teams := player(map[string]*commonpb.Value{"winner": integer(1)})

	got := completePersonalResults(in, teams).GetMapValue().GetFields()
	for id, want := range map[string]int64{"dropped": 1, "flagged": 1, "winner": 0} {
		if d := got[id].GetMapValue().GetFields()["deemed_result"].GetIntegerValue(); d != want {
			t.Errorf("%s: deemed_result = %d, want %d -- 2 is what earns the penalty card", id, d, want)
		}
	}
}
