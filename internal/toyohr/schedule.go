package toyohr

import (
	"context"

	toyohrpb "github.com/n-popescu/switchnet/proto/toyohr/v1"
)

// SelectVsParams, SelectVsSchedules, SelectCoopSchedules,
// SelectSeasonSchedules and SelectLeagueSchedules answer "there is currently
// no schedule", the same "minimum that does not crash the client" ROADMAP
// §2.2 already plans for hydro and leaderboard.
//
// TODO(capture-needed): the map/mode/weapon rotation content itself. Unlike a
// fest, which the operator authors and SaveFest validates, a normal Vs/Coop
// rotation is Nintendo's own game design data -- there is no dump or capture
// this server has of it, and CLAUDE.md is explicit that a genuinely
// undocumented shape must not be filled with an invented-but-plausible
// Nintendo internal. An empty schedule list is honest: the game reads it as
// "nothing scheduled right now" rather than crashing on a malformed one,
// which is strictly better than a fabricated rotation that is wrong in a way
// nothing here would ever catch. A capture of a real server's response would
// settle what a populated one looks like, or an operator-authored table in
// the same spirit as fest.go's SaveFest, if the operator decides fidelity to
// Nintendo's own rotations does not matter for a private network.
// Change: these five functions.

// SelectVsParams answers the current turf-war/ranked/league mode parameters.
func (s *Service) SelectVsParams(
	_ context.Context, _ *toyohrpb.SelectVsParamsRequest,
) (*toyohrpb.SelectVsParamsResponse, error) {
	return &toyohrpb.SelectVsParamsResponse{}, nil
}

// SelectVsSchedules answers the Vs (turf war / ranked / league) rotation.
func (s *Service) SelectVsSchedules(
	_ context.Context, _ *toyohrpb.SelectVsSchedulesRequest,
) (*toyohrpb.SelectVsSchedulesResponse, error) {
	return &toyohrpb.SelectVsSchedulesResponse{}, nil
}

// SelectCoopSchedules answers the Salmon Run rotation.
func (s *Service) SelectCoopSchedules(
	_ context.Context, _ *toyohrpb.SelectCoopSchedulesRequest,
) (*toyohrpb.SelectCoopSchedulesResponse, error) {
	return &toyohrpb.SelectCoopSchedulesResponse{}, nil
}

// SelectSeasonSchedules answers the current ranked/league season.
func (s *Service) SelectSeasonSchedules(
	_ context.Context, _ *toyohrpb.SelectSeasonSchedulesRequest,
) (*toyohrpb.SelectSeasonSchedulesResponse, error) {
	return &toyohrpb.SelectSeasonSchedulesResponse{}, nil
}

// SelectLeagueSchedules answers the current league rotation.
func (s *Service) SelectLeagueSchedules(
	_ context.Context, _ *toyohrpb.SelectLeagueSchedulesRequest,
) (*toyohrpb.SelectLeagueSchedulesResponse, error) {
	return &toyohrpb.SelectLeagueSchedulesResponse{}, nil
}
