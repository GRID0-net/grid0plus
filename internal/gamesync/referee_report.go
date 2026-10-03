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
		if !ok || battleID == "" || committed.GetFields().GetFields()["team_result"] == nil ||
			committed.GetFields().GetFields()["personal_result"] == nil {
			continue
		}
		d := caller.details
		// The consumers see the personal results as the verdict scores them
		// (deemed_result filled in, a disconnect scored as a loss), not the
		// console's raw report, so the battle history shows the same outcome
		// the game was told.
		fields := proto.Clone(committed.GetFields()).(*commonpb.MapValue)
		fields.Fields["personal_result"] = completePersonalResults(
			fields.Fields["personal_result"], fields.Fields["team_result"])
		report := RefereeReport{
			BattleID: battleID, SessionID: d.SessionID, Mode: d.Mode,
			UserID: caller.userID, Players: append([]string(nil), d.Players...),
			Fields: fields,
		}
		if err := s.onRefereeReport(ctx, report); err != nil {
			s.log.Warn("gamesync: could not record completed match", "battle", battleID, "error", err)
		}
	}
}
