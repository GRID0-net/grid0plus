package gamesync

import (
	"context"
	syncpb "github.com/n-popescu/grid0plus/proto/gamesync/v1"
)

// The transport used for a successful write must not change its effects on
// Pia delivery, referee barriers or a reopened Salmon Run session.
func (s *Server) afterWrites(ctx context.Context, caller *session, ops []*syncpb.WriteOperation, changes []*syncpb.DocumentChange) {
	if s.lobbyLog.Load() {
		for _, change := range changes {
			s.logPeerDocument(caller, change.GetDocument(), "committed", "")
		}
	}
	s.rememberPeerStates(caller, changes)
	s.watch.publish(scopeOf(caller), s.normalizeSessionChanges(caller, changes))
	s.publishRefereeVerdicts(ctx, ops)
	s.observeRefereeReports(ctx, ops)
	s.resendKickoffVerdictAfterProfile(caller, ops)
	s.noteAgain(caller, ops)
}
