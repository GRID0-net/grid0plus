package gamesync

import (
	"context"
	"strings"

	"google.golang.org/protobuf/proto"

	commonpb "github.com/n-popescu/grid0plus/proto/common"
	syncpb "github.com/n-popescu/grid0plus/proto/gamesync/v1"
)

// observeRefereeReports runs only after the entire write batch has committed.
// A report can be retried by every participant, so the durable consumer must
// deduplicate by battle ID. Nothing is inferred from a kickoff request.
func (s *Server) observeRefereeReports(ctx context.Context, ops []*syncpb.WriteOperation) {
	if s.onRefereeReport == nil {
		return
	}
	caller, ok := callerFrom(ctx)
	if !ok || caller.details == nil {
		return
	}
	s.reportMu.Lock()
	defer s.reportMu.Unlock()
	for _, op := range ops {
		var doc *syncpb.Document
		if update := op.GetUpdateDocument(); update != nil {
			doc = update.GetDocument()
		} else if merge := op.GetMergeDocument(); merge != nil {
			doc = merge.GetDocument()
		}
		if doc == nil {
			continue
		}
		const prefix = "docs/@@RefereeReport/"
		if !strings.HasPrefix(doc.GetName(), prefix) {
			continue
		}
		battleID := strings.SplitN(strings.TrimPrefix(doc.GetName(), prefix), "/", 2)[0]
		committed, ok := s.docsFor(caller).snapshot(doc.GetName())
		if !ok || battleID == "" || caller.details.SessionID == "" ||
			!strings.HasSuffix(battleID, "_"+caller.details.SessionID) || committed.GetFields().GetFields()["team_result"] == nil ||
			committed.GetFields().GetFields()["personal_result"] == nil {
			continue
		}
		d := caller.details
		// The consumers see the personal results as the verdict scores them
		// (deemed_result filled in, a disconnect scored as a loss), not the
		// console's raw report, so the battle history shows the same outcome
		// the game was told.
		fields := proto.Clone(committed.GetFields()).(*commonpb.MapValue)
		verified, err := reporterResult(caller, fields.Fields["personal_result"])
		if err != nil {
			continue
		}
		fields.Fields["personal_result"] = verified
		coop := isCoopReport(fields) || strings.Contains(d.Mode, "coop")
		if !coop {
			fields.Fields["personal_result"] = completePersonalResults(
				fields.Fields["personal_result"], fields.Fields["team_result"])
			festContribution(d.Mode, fields.Fields["personal_result"], fields.Fields["team_result"])
		}
		report := RefereeReport{
			BattleID: battleID, SessionID: d.SessionID, Mode: d.Mode,
			UserID: caller.userID, Players: append([]string(nil), d.Players...),
			Fields: fields, Coop: coop,
		}
		if verdict, ok := s.docsFor(caller).snapshot(refereeResultPrefix + battleID); ok {
			report.Verdict = verdict.GetFields()
		}
		if coop {
			report.SessionUsers = s.sessionUsers(d.SessionID)
		}
		report.Profiles = battleProfiles(s.docsFor(caller), battleID)
		if err := s.onRefereeReport(ctx, report); err != nil {
			s.log.Warn("gamesync: could not record completed match", "battle", battleID, "error", err)
		}
	}
}

// isCoopReport tells a Salmon Run report from a versus one by its content:
// only coop carries shift_id or scenario, the same test publishRefereeVerdict
// uses. Its per-player results have their own shape, with report_score a
// map, and the versus completion would add fields the game does not expect.
func isCoopReport(fields *commonpb.MapValue) bool {
	f := fields.GetFields()
	return f["shift_id"] != nil || f["scenario"] != nil
}

// sessionUsers maps the live user sessions of one game session to their users.
func (s *Server) sessionUsers(gameSession string) map[string]string {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := map[string]string{}
	for _, peer := range s.sessions {
		if peer.details != nil && peer.details.SessionID == gameSession {
			out[sessionID(peer.userSession)] = peer.userID
		}
	}
	return out
}

// battleProfiles maps each player of one battle to the player_info their console
// wrote at docs/@@Profile/<battle>/User/<user session>: name, weapon, gear,
// name plate. The battle history reads it per player and shows a nameless
// default player without it.
func battleProfiles(st *store, battleID string) map[string]*commonpb.Value {
	out := map[string]*commonpb.Value{}
	for _, doc := range st.list("docs/@@Profile/" + battleID + "/User") {
		f := doc.GetFields().GetFields()
		uid, info := f["npln_user_id"].GetStringValue(), f["player_info"]
		if uid != "" && info.GetMapValue() != nil {
			out[uid] = proto.Clone(info).(*commonpb.Value)
		}
	}
	return out
}
