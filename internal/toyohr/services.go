package toyohr

import (
	"google.golang.org/grpc"

	toyohrpb "github.com/n-popescu/switchnet/proto/toyohr/v1"
)

// Register attaches every Splatoon 3 service to a gRPC server.
//
// **All of them, including the ones with no handlers yet**, and that is the
// point. An RPC on a registered service answers Unimplemented and
// internal/npln's interceptor logs it once; an RPC on a service that was never
// registered fails the same way with no log line. ROADMAP §2.4 says to let the
// console drive the order of this backlog — which only works if the console's
// asking is recorded.
//
// One type per service rather than one type implementing all of them, because
// Locker and Canola both declare IkasuDocument and SelectDocuments with
// identical signatures: a struct embedding both Unimplemented servers has an
// ambiguous promoted method and satisfies neither interface. The compiler says
// so, but only for the pair that happens to clash, so it is worth stating.
func (s *Service) Register(srv *grpc.Server) {
	toyohrpb.RegisterFestServiceServer(srv, s)
	toyohrpb.RegisterScheduleServer(srv, &scheduleService{s: s})
	toyohrpb.RegisterLobbyMessagingServer(srv, &lobbyMessagingService{s: s})
	toyohrpb.RegisterUserScreeningServer(srv, &userScreeningService{s: s})

	// No handlers yet. Registered so that a console asking is logged, which is
	// how the order of the rest of this backlog gets decided.
	toyohrpb.RegisterGameRecordServer(srv, &gameRecordService{})
	toyohrpb.RegisterCoopScenarioServer(srv, &coopScenarioService{})
	toyohrpb.RegisterLockerServer(srv, &lockerService{})
	toyohrpb.RegisterCanolaServer(srv, &canolaService{})
	toyohrpb.RegisterCloudSaveServer(srv, &cloudSaveService{})
	toyohrpb.RegisterReplayServer(srv, &replayService{})
}

// The services with no handlers yet.
//
// Each carries a note on what implementing it would need, so that whoever picks
// one up from the log knows what they are taking on before they start.

// gameRecordService: player records, tags, and Bankara challenges.
//
// **This is the one that matters most**, because it is where battle results
// would come from — and battle results are the missing half of a Splatfest
// verdict. See GetFestResult: the vote is measured and the battle ratios are
// not, precisely because nothing writes them.
type gameRecordService struct {
	toyohrpb.UnimplementedGameRecordServer
}

// coopScenarioService: Salmon Run scenario codes. Needs a document store and a
// code namespace shared with Replay's.
type coopScenarioService struct {
	toyohrpb.UnimplementedCoopScenarioServer
}

// lockerService: the player's locker. A document store keyed per player.
type lockerService struct {
	toyohrpb.UnimplementedLockerServer
}

// canolaService: the same document shape as Locker, on its own service.
type canolaService struct {
	toyohrpb.UnimplementedCanolaServer
}

// cloudSaveService: save data upload and download.
//
// Worth being deliberate about rather than implementing because it is next in a
// list: this one holds the player's actual save, so a bug here loses progress
// rather than degrading a feature.
type cloudSaveService struct {
	toyohrpb.UnimplementedCloudSaveServer
}

// replayService: replay upload and replay codes.
type replayService struct {
	toyohrpb.UnimplementedReplayServer
}
