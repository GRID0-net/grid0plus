package gamesync

import (
	"context"
	"strings"

	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/structpb"
	"google.golang.org/protobuf/types/known/timestamppb"

	commonpb "github.com/n-popescu/grid0plus/proto/common"
	syncpb "github.com/n-popescu/grid0plus/proto/gamesync/v1"
)

// Both room members send the same kickoff request with exists=false. The
// first write creates the document and verdict; the second is a retry of the
// shared battle barrier, not an independent create. Answer that one exact
// duplicate without changing the stored request. Other optimistic-concurrency
// preconditions remain enforced by store.apply.
func (s *Server) duplicateRefereeKickoff(caller *session, req *syncpb.WriteDocumentsRequest) (*syncpb.WriteDocumentsResponse, bool) {
	if caller.details == nil || caller.details.SessionID == "" || len(req.GetWriteOperations()) != 1 ||
		len(req.GetDefermentOperations()) != 0 {
		return nil, false
	}
	update := req.GetWriteOperations()[0].GetUpdateDocument()
	if update == nil || update.GetCurrentDocument() == nil {
		return nil, false
	}
	if exists, ok := update.GetCurrentDocument().GetConditionType().(*syncpb.Precondition_Exists); !ok || exists.Exists {
		return nil, false
	}
	name := update.GetDocument().GetName()
	if !strings.HasPrefix(name, refereeRequestPrefix) || !strings.HasSuffix(name, "_"+caller.details.SessionID) {
		return nil, false
	}
	stored, exists := s.docsFor(caller).snapshot(name)
	if !exists {
		return nil, false
	}
	if _, ready := s.docsFor(caller).snapshot(refereeResultPrefix + strings.TrimPrefix(name, refereeRequestPrefix)); !ready {
		return nil, false
	}
	commitTime := stored.GetUpdateTime()
	if commitTime == nil {
		commitTime = timestamppb.New(s.now())
	}
	return &syncpb.WriteDocumentsResponse{
		WriteResults: []*syncpb.WriteResult{updateResult()},
		CommitTime:   commitTime,
	}, true
}

const (
	refereeRequestPrefix = "docs/@@RefereeRequest/"
	refereeReportPrefix  = "docs/@@RefereeReport/"
	refereeResultPrefix  = "docs/@@RefereeResult/"
	refereeProfilePrefix = "docs/@@Profile/"
)

// resendKickoffVerdictAfterProfile re-delivers a battle's @@RefereeResult once
// a console has written its @@Profile for that battle.
//
// The game only accepts the kickoff verdict (status 0) once its referee is in
// the state it enters when it queues the @@Profile write (Splatoon 3's
// FUN_71014ff540 sets state 3 there; FUN_71014fe4b0 moves 3 -> 4 only on a
// status-0 change). The verdict is published at kickoff, about 15 s earlier,
// while the console is still in an earlier state and ignores it. Without a
// later copy it waits 1800 frames (FUN_7103898e98) and leaves the match with
// GamesyncRefereeTimeoutVs: the private-battle drop 30 s after loading.
// Nextendo's server re-sends the verdict repeatedly, which is why it never
// hits this.
func (s *Server) resendKickoffVerdictAfterProfile(caller *session, ops []*syncpb.WriteOperation) {
	if caller.details == nil || caller.details.SessionID == "" {
		return
	}
	sent := map[string]bool{}
	for _, op := range ops {
		var name string
		if v := op.GetUpdateDocument(); v != nil {
			name = v.GetDocument().GetName()
		} else if v := op.GetMergeDocument(); v != nil {
			name = v.GetDocument().GetName()
		}
		if !strings.HasPrefix(name, refereeProfilePrefix) {
			continue
		}
		battleID := strings.SplitN(strings.TrimPrefix(name, refereeProfilePrefix), "/", 2)[0]
		if battleID == "" || sent[battleID] || !strings.HasSuffix(battleID, "_"+caller.details.SessionID) {
			continue
		}
		sent[battleID] = true
		verdict, ok := s.docsFor(caller).snapshot(refereeResultPrefix + battleID)
		if !ok {
			continue
		}
		verdict.UpdateTime = timestamppb.New(s.now())
		s.watch.publish(scopeOf(caller), []*syncpb.DocumentChange{{
			DocumentChangeType: syncpb.DocumentChange_UPDATED,
			Document:           verdict,
		}})
	}
}

// refereeTraceID is the game-session id with its dashes removed: the working
// Nextendo verdict for battle 20260930T153234_bc0ffe09-6aa4-443b-aaeb-
// 463be2e7e135 carries trace_id bc0ffe096aa4443baaeb463be2e7e135. Every console
// in the match reads it, so a random value is one more thing that could
// disagree with the session it joined.
func refereeTraceID(battleID string) string {
	sessionID := battleID
	if i := strings.Index(battleID, "_"); i >= 0 {
		sessionID = battleID[i+1:]
	}
	return strings.ReplaceAll(sessionID, "-", "")
}

func verdictString(v string) *commonpb.Value {
	return &commonpb.Value{ValueType: &commonpb.Value_StringValue{StringValue: v}}
}

func verdictInt(v int64) *commonpb.Value {
	return &commonpb.Value{ValueType: &commonpb.Value_IntegerValue{IntegerValue: v}}
}

func verdictNull() *commonpb.Value {
	return &commonpb.Value{ValueType: &commonpb.Value_NullValue{NullValue: structpb.NullValue_NULL_VALUE}}
}

// publishRefereeVerdicts answers the two document writes the game uses as
// barriers: a kickoff request before entering the match and a report before
// showing results. The captured working server publishes a direct child of
// docs/@@RefereeResult for both; merely storing the client's writes leaves the
// console waiting at either barrier. Values describing the match come from the
// committed client request, not from a replay of somebody else's battle.
func (s *Server) publishRefereeVerdicts(ctx context.Context, ops []*syncpb.WriteOperation) {
	caller, ok := callerFrom(ctx)
	if !ok || caller.details == nil || caller.details.SessionID == "" {
		return
	}
	for _, op := range ops {
		var name string
		if v := op.GetUpdateDocument(); v != nil {
			name = v.GetDocument().GetName()
		} else if v := op.GetMergeDocument(); v != nil {
			name = v.GetDocument().GetName()
		}
		var battleID string
		var final bool
		switch {
		case strings.HasPrefix(name, refereeRequestPrefix):
			battleID = strings.TrimPrefix(name, refereeRequestPrefix)
		case strings.HasPrefix(name, refereeReportPrefix):
			battleID = strings.SplitN(strings.TrimPrefix(name, refereeReportPrefix), "/", 2)[0]
			final = true
		default:
			continue
		}
		if !strings.HasSuffix(battleID, "_"+caller.details.SessionID) {
			continue
		}
		if err := s.publishRefereeVerdict(caller.details.SessionID, battleID, name, final); err != nil {
			s.log.Warn("gamesync: could not publish referee verdict", "battle", battleID, "error", err)
		}
	}
}

func (s *Server) publishRefereeVerdict(scope, battleID, sourceName string, final bool) error {
	docs := s.docs.in(scope)
	s.refereeMu.Lock()
	defer s.refereeMu.Unlock()

	source, ok := docs.snapshot(sourceName)
	if !ok {
		return nil
	}
	resultName := refereeResultPrefix + battleID
	var fields map[string]*commonpb.Value
	if !final {
		if previous, ok := docs.snapshot(resultName); ok && previous.GetFields().GetFields()["status"] != nil {
			// A retried kickoff must not replace a final verdict or rotate its
			// trace ID while clients are already watching it.
			return nil
		}
		// The request carries the stage, rule, season, teams and players. The
		// working capture's kickoff verdict keeps those fields, drops the four
		// request-only keys and adds this response's own identity and status.
		if source.GetFields().GetFields()["users"] == nil {
			return nil
		}
		fields = map[string]*commonpb.Value{}
		for k, v := range source.GetFields().GetFields() {
			switch k {
			case "battle_id", "event_type", "event_detail", "season", "job_id":
				continue
			}
			fields[k] = proto.Clone(v).(*commonpb.Value)
		}
		fields["request_id"] = verdictString(battleID)
		fields["trace_id"] = verdictString(refereeTraceID(battleID))
		fields["status"] = verdictInt(0)
		if fields["shift_id"] == nil { // The captured VS verdict carries this empty field; Coop does not.
			fields["tournament_id"] = verdictString("")
		}
	} else {
		previous, ok := docs.snapshot(resultName)
		if !ok || source.GetFields().GetFields()["team_result"] == nil ||
			source.GetFields().GetFields()["personal_result"] == nil {
			return nil
		}
		fields = map[string]*commonpb.Value{}
		for k, v := range previous.GetFields().GetFields() {
			fields[k] = proto.Clone(v).(*commonpb.Value)
		}
		fields["status"] = verdictInt(1)
		fields["team_result"] = proto.Clone(source.GetFields().GetFields()["team_result"]).(*commonpb.Value)
		personal := mergePersonalResults(fields["personal_result"], source.GetFields().GetFields()["personal_result"])
		if fields["shift_id"] != nil || fields["scenario"] != nil {
			// Salmon Run's captured verdict has a different per-player shape.
			// Preserve the game's report instead of adding versus-only fields.
			fields["personal_result"] = personal
		} else {
			fields["personal_result"] = completePersonalResults(personal, fields["team_result"])
		}
	}

	doc := &syncpb.Document{Name: resultName, Fields: &commonpb.MapValue{Fields: fields}}
	_, commit, err := docs.apply([]*syncpb.WriteOperation{{
		OperationType: &syncpb.WriteOperation_MergeDocument{
			MergeDocument: &syncpb.MergeDocumentRequest{Document: doc},
		},
	}})
	if err != nil {
		return err
	}
	s.watch.publish(scope, commit.changes)
	return nil
}

func mergePersonalResults(previous, incoming *commonpb.Value) *commonpb.Value {
	out := map[string]*commonpb.Value{}
	for id, entry := range previous.GetMapValue().GetFields() {
		out[id] = proto.Clone(entry).(*commonpb.Value)
	}
	for id, entry := range incoming.GetMapValue().GetFields() {
		fields := map[string]*commonpb.Value{}
		for k, v := range out[id].GetMapValue().GetFields() {
			fields[k] = proto.Clone(v).(*commonpb.Value)
		}
		for k, v := range entry.GetMapValue().GetFields() {
			fields[k] = proto.Clone(v).(*commonpb.Value)
		}
		out[id] = &commonpb.Value{ValueType: &commonpb.Value_MapValue{MapValue: &commonpb.MapValue{Fields: fields}}}
	}
	return &commonpb.Value{ValueType: &commonpb.Value_MapValue{MapValue: &commonpb.MapValue{Fields: out}}}
}

func reportsDisconnect(v *commonpb.Value) bool {
	switch x := v.GetValueType().(type) {
	case *commonpb.Value_BooleanValue:
		return x.BooleanValue
	case *commonpb.Value_IntegerValue:
		return x.IntegerValue != 0
	}
	return false
}

// The captured final verdict has a fixed per-player shape even when an
// individual console omitted fields from its report. The neutral values below
// were measured on the working capture; supplied player values take priority.
func completePersonalResults(in, teamResult *commonpb.Value) *commonpb.Value {
	players := in.GetMapValue().GetFields()
	if players == nil {
		return proto.Clone(in).(*commonpb.Value)
	}
	out := make(map[string]*commonpb.Value, len(players))
	winner := teamResult.GetMapValue().GetFields()["winner"].GetIntegerValue()
	for id, entry := range players {
		fields := map[string]*commonpb.Value{}
		for k, v := range entry.GetMapValue().GetFields() {
			fields[k] = proto.Clone(v).(*commonpb.Value)
		}
		for _, k := range []string{
			"awards", "catalog_point", "death_num", "disconnected", "display_order",
			"group_leader", "individual_stats", "kill_assist_num", "kill_num",
			"npln_user_id", "paint_point", "report_score", "single_ticket",
			"special_num", "team",
		} {
			if fields[k] == nil {
				fields[k] = verdictNull()
			}
		}
		for _, k := range []string{"contribution", "noroshi_try_num", "party_ticket"} {
			if fields[k] == nil {
				fields[k] = verdictInt(0)
			}
		}
		for _, k := range []string{"did_get_noroshi1", "did_get_noroshi2"} {
			if fields[k] == nil {
				fields[k] = &commonpb.Value{ValueType: &commonpb.Value_BooleanValue{BooleanValue: false}}
			}
		}
		if fields["outfit_bonus"] == nil {
			fields["outfit_bonus"] = &commonpb.Value{ValueType: &commonpb.Value_DoubleValue{DoubleValue: 0}}
		}
		if fields["team_byname"] == nil {
			fields["team_byname"] = &commonpb.Value{ValueType: &commonpb.Value_MapValue{MapValue: &commonpb.MapValue{Fields: map[string]*commonpb.Value{
				"template": verdictInt(0), "unity0_tag": verdictString(""), "unity1_tag": verdictString(""),
				"unity2_tag": verdictString(""), "property_tag": verdictString(""), "group_tag": verdictString(""),
			}}}}
		}
		if reportsDisconnect(fields["disconnected"]) || fields["deemed_result"].GetIntegerValue() == 2 {
			// deemed_result 2 is the disconnect result, and the game hands out
			// its penalty card for it. A drop on GRID0+ is usually the network's
			// fault, so it is scored as a plain loss, as Nextendo's server does.
			fields["deemed_result"] = verdictInt(1)
		} else if fields["deemed_result"] == nil {
			deemed := int64(1)
			if winner != 0 && fields["team"].GetIntegerValue() == winner {
				deemed = 0
			}
			fields["deemed_result"] = verdictInt(deemed)
		}
		out[id] = &commonpb.Value{ValueType: &commonpb.Value_MapValue{MapValue: &commonpb.MapValue{Fields: fields}}}
	}
	return &commonpb.Value{ValueType: &commonpb.Value_MapValue{MapValue: &commonpb.MapValue{Fields: out}}}
}
