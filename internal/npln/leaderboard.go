package npln

import (
	"context"

	"google.golang.org/protobuf/encoding/protojson"

	leaderboardpb "github.com/n-popescu/grid0plus/proto/leaderboard/v1"
)

// leaderboardService answers BrowseLeaderboard, which Splatoon 3 calls from
// the lobby (21 calls in a week of play), with an empty leaderboard for the
// season it asks about: GRID0+ keeps no rankings, and an empty board is the
// honest answer, the same one Canola and Locker give for their documents.
// The request is logged in full so the season names, and which in-game board
// they belong to, can be read off the log.
//
// TODO(capture-needed): what a real board looks like (scores, ranks,
// holders) and which seasons the game asks for. Change: BrowseLeaderboard.
type leaderboardService struct {
	leaderboardpb.UnimplementedLeaderboardServiceServer
	s *Server
}

func (l *leaderboardService) BrowseLeaderboard(ctx context.Context, req *leaderboardpb.BrowseLeaderboardRequest) (*leaderboardpb.BrowseLeaderboardResponse, error) {
	caller, ok := CallerFrom(ctx)
	if !ok {
		return nil, ErrUnauthenticated
	}
	l.s.log.Info("npln: leaderboard browsed", "user", caller.UserID, "request", protojson.Format(req))
	return &leaderboardpb.BrowseLeaderboardResponse{
		Leaderboard: &leaderboardpb.Leaderboard{Season: req.GetSeason()},
	}, nil
}
