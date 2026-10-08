package gamesync

import (
	"context"
	"testing"

	commonpb "github.com/n-popescu/grid0plus/proto/common"
	syncpb "github.com/n-popescu/grid0plus/proto/gamesync/v1"
)

func TestOnlyCommittedFinalRefereeReportsReachTheBattleRecorder(t *testing.T) {
	h := newHarness(t)
	alice := h.session("token-alice")
	h.srv.mu.Lock()
	for _, sess := range h.srv.sessions {
		sess.details = &SessionDetails{
			UserID: "alice", SessionID: "room-1", Mode: "fest_match_regular_normal_config",
			Players: []string{"alice", "bob"},
		}
	}
	h.srv.mu.Unlock()
	var reports []RefereeReport
	h.srv.onRefereeReport = func(_ context.Context, r RefereeReport) error {
		reports = append(reports, r)
		return nil
	}
	write := func(name string, fields map[string]*commonpb.Value) {
		t.Helper()
		_, err := h.client.WriteDocuments(alice, &syncpb.WriteDocumentsRequest{
			WriteOperations: []*syncpb.WriteOperation{merge(doc(name, fields))},
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	write("docs/@@RefereeRequest/20260905T010000_room-1", map[string]*commonpb.Value{
		"team_result": intValue(1), "personal_result": intValue(2),
	})
	write("docs/@@RefereeReport/20260905T010000_room-1/User/alice", map[string]*commonpb.Value{
		"team_result": intValue(1),
	})
	if len(reports) != 0 {
		t.Fatalf("kickoff or incomplete report counted as a completed battle: %v", reports)
	}
	write("docs/@@RefereeReport/20260905T010000_room-1/User/alice", map[string]*commonpb.Value{
		"personal_result": intValue(2),
	})
	// A client-supplied battle name must not move a completed report outside
	// the game session established by its token.
	write("docs/@@RefereeReport/20260905T010000_other-room/User/alice", map[string]*commonpb.Value{
		"team_result": intValue(1), "personal_result": intValue(2),
	})
	if len(reports) != 1 || reports[0].BattleID != "20260905T010000_room-1" ||
		reports[0].UserID != "alice" || len(reports[0].Players) != 2 {
		t.Fatalf("authenticated report callback = %v", reports)
	}
}
