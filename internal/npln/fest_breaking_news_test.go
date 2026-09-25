package npln

import (
	"testing"

	"google.golang.org/protobuf/types/known/emptypb"
)

// TestFestBreakingNewsAnswersAnEmptyMessageNotUnimplemented: the game calls
// this while a fest is open, and Unimplemented is a hard error in-game.
// Nintendo answered one empty message during a real Splatfest.
func TestFestBreakingNewsAnswersAnEmptyMessageNotUnimplemented(t *testing.T) {
	const uid = "news000000000001"
	h := newHarness(t)
	h.createUser(uid, "device-news-1")
	ctx := h.ctx(h.sessionFor(t, uid, "device-news-1"), "")

	out := &emptypb.Empty{}
	if err := h.conn.Invoke(ctx, "/nn.npln.toyohr.v1.FestService/SelectFestBreakingNews",
		&emptypb.Empty{}, out); err != nil {
		t.Fatalf("SelectFestBreakingNews = %v, want an empty message", err)
	}
}
