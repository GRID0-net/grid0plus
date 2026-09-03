package toyohr

import (
	"context"
	"strconv"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"

	toyohrpb "github.com/n-popescu/switchnet/proto/toyohr/v1"
)

// This file is TODO.md §1.4's backlog: everything besides FestService,
// Schedule, CloudSave.GetSaveRecord and LobbyMessaging (service.go,
// lobby_messaging.go). Each service below gets its own adapter type rather
// than being embedded on *Service directly -- see Service's doc comment in
// service.go for why CanolaServer/LockerServer and
// CoopScenarioServer/ReplayServer cannot both be satisfied by one type.
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

// ---- nn.npln.toyohr.v1.UserScreening -----------------------------------------

type userScreeningService struct {
	*Service
	toyohrpb.UnimplementedUserScreeningServer
}

// GetViolation reports whether an account has an active screening
// violation. GetViolationResponse has no fields at all -- its entire
// content is whether the call succeeds -- so there is nothing to guess at:
// SwitchNet performs no screening, so no account has ever been found in
// violation of anything, and NotFound is that answer, not a placeholder.
func (userScreeningService) GetViolation(
	context.Context, *toyohrpb.GetViolationRequest,
) (*toyohrpb.GetViolationResponse, error) {
	return nil, status.Error(codes.NotFound, "no violation")
}

// SelectSnapshot returns a stored copy of a piece of user content (a
// screen name, most likely, given StringValue/Language) for a screener to
// review. SwitchNet takes no such snapshots, so there is nothing to select.
func (userScreeningService) SelectSnapshot(
	context.Context, *toyohrpb.SelectSnapshotRequest,
) (*toyohrpb.SelectSnapshotResponse, error) {
	return nil, status.Error(codes.NotFound, "no snapshot")
}

// CreateReport accepts a player's report and logs it for the operator.
//
// This is deliberately the one write in this file that is not left
// Unimplemented: a report is data the caller is handing over, not data
// SwitchNet has to have already had, and there is no undocumented shape to
// get wrong -- every field is passed straight through to the log. What is
// NOT done is anything that would claim moderation actually happened
// (banning, hiding content): SwitchNet has no screening system, so a report
// is retained only as a log line an operator can read, not acted on.
//
// TODO(capture-needed): the resource-name shape returned in Name. No
// capture shows what a real CreateReport response names a new report as;
// this uses req.Parent + "/reports/<unix-nano timestamp>" as a defensible
// placeholder, following the same tenants/<t>/... convention the rest of
// this package uses elsewhere, not a confirmed one. Change: this function.
func (u userScreeningService) CreateReport(
	_ context.Context, req *toyohrpb.CreateReportRequest,
) (*toyohrpb.Report, error) {
	in := req.GetReport()
	if in == nil {
		return nil, status.Error(codes.InvalidArgument, "no report given")
	}

	now := u.now()
	out := &toyohrpb.Report{
		Name:                req.GetParent() + "/reports/" + strconv.FormatInt(now.UnixNano(), 10),
		Category:            in.GetCategory(),
		Reason:              in.GetReason(),
		LanguageCode:        in.GetLanguageCode(),
		CreateTime:          timestamppb.New(now),
		ScreeningTarget:     in.GetScreeningTarget(),
		Weight:              in.GetWeight(),
		Context:             in.GetContext(),
		ScreeningTargetType: in.GetScreeningTargetType(),
	}
	u.log.Info("toyohr: user screening report received",
		"category", out.Category, "target", out.ScreeningTarget, "target_type", out.ScreeningTargetType,
		"reason", out.Reason)
	return out, nil
}

// ---- nn.npln.toyohr.v1.GameRecord --------------------------------------------
//
// CreateBankaraChallenge, InitializeAttributes and CreateFestPowerMeasurement
// are all writes that report real battle outcomes and rank/power state --
// deeply Nintendo-internal rating data (InitializeAttributesRequest alone
// carries per-mode X Power seed values for Area/Lift/Goal/Clam) with no
// defensible default. Inventing initial values here is exactly the
// plausible-looking-Nintendo-internal CLAUDE.md's first rule forbids, so
// all three are left entirely to the embedded Unimplemented default.
type gameRecordService struct {
	*Service
	toyohrpb.UnimplementedGameRecordServer
}
