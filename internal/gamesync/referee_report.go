package gamesync

import (
	"context"
	"strings"

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
		committed, ok := s.docs.snapshot(doc.GetName())
		if !ok || battleID == "" || committed.GetFields().GetFields()["team_result"] == nil ||
			committed.GetFields().GetFields()["personal_result"] == nil {
			continue
		}
		d := caller.details
		report := RefereeReport{
			BattleID: battleID, SessionID: d.SessionID, Mode: d.Mode,
			UserID: caller.userID, Players: append([]string(nil), d.Players...),
			Fields: committed.GetFields(),
		}
		if err := s.onRefereeReport(ctx, report); err != nil {
			s.log.Warn("gamesync: could not record completed match", "battle", battleID, "error", err)
		}
	}
}
