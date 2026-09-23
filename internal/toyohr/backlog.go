package toyohr

import (
	"context"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"

	toyohrpb "github.com/n-popescu/switchnet/proto/toyohr/v1"
)

// This file is TODO.md §1.4's backlog: everything besides FestService,
// Schedule, CloudSave.GetSaveRecord and LobbyMessaging/UserScreening
// (service.go, schedule.go, cloud_save.go, messaging.go). Each service below
// gets its own adapter type rather than being embedded on *Service directly
// -- see Service's doc comment in service.go for why CanolaServer/LockerServer
// and CoopScenarioServer/ReplayServer cannot both be satisfied by one type.
//
// The rule applied throughout: a lookup or a query answers honestly with
// what SwitchNet actually has, which for every one of these is nothing --
// there is no internal/ugcstore, no CoopScenario/Replay code was ever
// registered, and no violation or screening snapshot was ever produced. A
// write with an unknown validation or storage contract (RegisterDocument on
// four different services, GameRecord's three RPCs) is left to the embedded
// Unimplemented default instead of guessed at -- the same boundary
// CloudSave's own five unimplemented RPCs already draw, for the same
// reason: implementing "write" convincingly would mean inventing a shape
// nothing here can check against a capture.

// ---- nn.npln.toyohr.v1.Canola and nn.npln.toyohr.v1.Locker -----------------
//
// Both declare the identical IkasuDocumentRequest/SelectDocumentsRequest
// shapes (proto/toyohr/v1/common.proto), backed by the same
// nn.npln.ugcstore.v1.Document the wiki's Server-List places under a
// separate Ugcstore/Screening pair of services SwitchNet does not
// implement. Without that store there is no document either service could
// honestly return, so both get the same two answers: a select finds
// nothing, and activating ("Ikasu") one specific document that was never
// registered is NotFound.

type canolaService struct {
	*Service
	toyohrpb.UnimplementedCanolaServer
}

func (canolaService) SelectDocuments(
	context.Context, *toyohrpb.SelectDocumentsRequest,
) (*toyohrpb.SelectDocumentsResponse, error) {
	return &toyohrpb.SelectDocumentsResponse{}, nil
}

func (canolaService) IkasuDocument(
	context.Context, *toyohrpb.IkasuDocumentRequest,
) (*toyohrpb.IkasuDocumentResponse, error) {
	return nil, status.Error(codes.NotFound, "no such document")
}

type lockerService struct {
	*Service
	toyohrpb.UnimplementedLockerServer
}

func (lockerService) SelectDocuments(
	context.Context, *toyohrpb.SelectDocumentsRequest,
) (*toyohrpb.SelectDocumentsResponse, error) {
	return &toyohrpb.SelectDocumentsResponse{}, nil
}

func (lockerService) IkasuDocument(
	context.Context, *toyohrpb.IkasuDocumentRequest,
) (*toyohrpb.IkasuDocumentResponse, error) {
	return nil, status.Error(codes.NotFound, "no such document")
}

// ---- nn.npln.toyohr.v1.CoopScenario -----------------------------------------

type coopScenarioService struct {
	*Service
	toyohrpb.UnimplementedCoopScenarioServer
}

// ResolveCoopScenarioCode looks up a shared Salmon Run scenario by its
// share code. RegisterDocument -- what would create one -- is left to the
// embedded Unimplemented default, so no code this server did not create can
// ever exist to resolve: NotFound is the honest answer for every code,
// exactly the way CloudSave.GetSaveRecord's NotFound is correct rather than
// a stub for an account with no cloud save.
func (coopScenarioService) ResolveCoopScenarioCode(
	context.Context, *toyohrpb.ResolveCoopScenarioCodeRequest,
) (*toyohrpb.ResolveCoopScenarioCodeResponse, error) {
	return nil, status.Error(codes.NotFound, "no such scenario code")
}

// ---- nn.npln.toyohr.v1.Replay ------------------------------------------------

type replayService struct {
	*Service
	toyohrpb.UnimplementedReplayServer
}

// ResolveReplayCode is CoopScenario's ResolveCoopScenarioCode, for replays:
// RegisterDocument (unimplemented) is the only thing that could create a
// code, so no code exists to resolve.
func (replayService) ResolveReplayCode(
	context.Context, *toyohrpb.ResolveReplayCodeRequest,
) (*toyohrpb.ResolveReplayCodeResponse, error) {
	return nil, status.Error(codes.NotFound, "no such replay code")
}

// FulfillReservations reports which of a player's replay reservations are
// ready to download. Since RegisterDocument never runs, nothing is ever
// reserved, so the honest answer is an empty list -- not an error, the same
// distinction ListDeviceAccounts (empty, not NotFound) draws against
// GetDeviceAccount (NotFound) for a single missing one.
func (s replayService) FulfillReservations(
	_ context.Context, _ *toyohrpb.FulfillReservationsRequest,
) (*toyohrpb.FulfillReservationsResponse, error) {
	return &toyohrpb.FulfillReservationsResponse{Timestamp: timestamppb.New(s.now())}, nil
}

// ---- nn.npln.toyohr.v1.GameRecord --------------------------------------------
//
// CreateBankaraChallenge and CreateFestPowerMeasurement are writes that report
// real battle outcomes and rank/power state -- deeply Nintendo-internal rating
// data with no defensible default -- so both are left to the embedded
// Unimplemented default. InitializeAttributes is implemented (game_record.go):
// the game's own parser keeps its defaults for any absent key, so that answer
// needs no invented value, only what the request itself carries.
//
// The real GameRecord service has more RPCs than this file, this package's
// proto, and kinnay's public NPLN-Protocols repo all know about. A
// 2026-09-21 decompile of Splatoon 3 v11.3.0's GameRecord client-stub
// constructor (the code that builds one method-descriptor slot per RPC the
// service defines -- present regardless of which methods the game's own
// logic goes on to call, so this proves the methods are real, not that
// Splatoon 3 invokes all of them) recovered five more full method paths,
// none of them documented anywhere before tonight:
//
//	/nn.npln.toyohr.v1.GameRecord/PrepareTag                       (kinnay's proto already has this as a commented-out "rpc PrepareTag(?) returns (?);" placeholder -- the name was suspected, now confirmed real)
//	/nn.npln.toyohr.v1.GameRecord/ResetUdemae
//	/nn.npln.toyohr.v1.GameRecord/PrepareBankaraPowerMeasurement
//	/nn.npln.toyohr.v1.GameRecord/PrepareWeaponPowerMeasurement
//	/nn.npln.toyohr.v1.GameRecord/PrepareLeaguePowerMeasurement
//
// The "Prepare<Mode>PowerMeasurement" naming, alongside the already-known
// CreateFestPowerMeasurement, suggests a Prepare-then-Create pattern per
// game mode (Fest, Bankara/Anarchy, Weapon, League) -- but only Fest's
// Create-side method path was found, so whether Bankara/Weapon/League
// finalize through CreateBankaraChallenge, a not-yet-found method, or no
// separate finalize call at all is still unknown. ResetUdemae plausibly
// fires on a rank reset/season boundary. None of this settles field-level
// request/response shapes -- LITE_RUNTIME strips the descriptor metadata
// that would give those directly, unlike the method path itself, which has
// to survive because gRPC dispatches over it at the HTTP/2 transport
// level. Same CLAUDE.md rule as above applies just as hard to these: real
// names, not real defaults to implement against.
type gameRecordService struct {
	*Service
	toyohrpb.UnimplementedGameRecordServer
}
