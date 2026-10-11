package toyohr

import (
	"context"
	"crypto/rand"
	"strings"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/types/known/timestamppb"

	commonpb "github.com/n-popescu/grid0plus/proto/common"
	toyohrpb "github.com/n-popescu/grid0plus/proto/toyohr/v1"
	ugcpb "github.com/n-popescu/grid0plus/proto/ugcstore/v1"
)

// This file is TODO.md §1.4's backlog: everything besides FestService,
// Schedule, CloudSave.GetSaveRecord and LobbyMessaging/UserScreening
// (service.go, schedule.go, cloud_save.go, messaging.go). Each service below
// gets its own adapter type rather than being embedded on *Service directly
// -- see Service's doc comment in service.go for why CanolaServer/LockerServer
// and CoopScenarioServer/ReplayServer cannot both be satisfied by one type.
//
// The rule applied throughout: a lookup or a query answers honestly with
// what GRID0+ actually has. A write with an unknown validation or
// storage contract (CreateBankaraChallenge) is left to the embedded
// Unimplemented default instead of guessed at -- the same boundary
// CloudSave's own five unimplemented RPCs already draw, for the same
// reason: implementing "write" convincingly would mean inventing a shape
// nothing here can check against a capture.

// ---- nn.npln.toyohr.v1.Canola and nn.npln.toyohr.v1.Locker -----------------
//
// Both declare the identical IkasuDocumentRequest/SelectDocumentsRequest
// shapes (proto/toyohr/v1/common.proto), backed by the same
// nn.npln.ugcstore.v1.Document the wiki's Server-List places under a
// separate Ugcstore/Screening pair of services GRID0+ does not
// implement. A select lists what this server's players have published (see
// plaza); "Ikasu" on one of them is acknowledged, see ikasu.

type canolaService struct {
	*Service
	toyohrpb.UnimplementedCanolaServer
}

func (s canolaService) SelectDocuments(
	ctx context.Context, req *toyohrpb.SelectDocumentsRequest,
) (*toyohrpb.SelectDocumentsResponse, error) {
	s.logSelect("canola", req)
	return s.plaza(ctx, "canola/deckPosts", plazaDecks)
}

// How many published posts the plaza is sent at once. NextendoNetwork/
// splatoon-3 measured Nintendo's own answer at launch (facts only, none of
// its code used): 31 locker posts and 6 card-game decks, each a document
// under services/toyohr/interfaces/<service>/<collection>/<uuid> carrying
// only what was published -- no identity.
const (
	plazaLockers = 31
	plazaDecks   = 6
)

// plaza answers SelectDocuments with what players on this server have
// published to collection: written through Ugcstore/CommitDocuments, then
// announced by RegisterDocument. Before this, both services answered
// nothing and the plaza showed no player's locker or deck.
//
// TODO(capture-needed): requests that name specific players (Nextendo's
// notes describe friend name plates asked for by id) may want those
// players' documents rather than the plaza set. The request is logged by
// logSelect; see docs/CAPTURES/README.md. Change: this function.
func (s *Service) plaza(
	ctx context.Context, collection string, limit int,
) (*toyohrpb.SelectDocumentsResponse, error) {
	if s.docs == nil {
		return &toyohrpb.SelectDocumentsResponse{}, nil
	}
	docs, err := s.docs.Published(ctx, "services/toyohr/interfaces/"+collection, limit)
	if err != nil {
		s.log.Error("toyohr: list published posts", "collection", collection, "error", err)
		return nil, status.Error(codes.Internal, "internal error")
	}
	return &toyohrpb.SelectDocumentsResponse{Documents: docs}, nil
}

// ikasu answers IkasuDocument: a player giving a plaza post a "fresh!"
// (イカす, Splatoon's word for a like). Its response is an empty message, so
// acknowledging a post that exists invents nothing; before the plaza showed
// real posts this answered NotFound for everything, which a console showing
// a real post would report as an error.
//
// TODO(capture-needed): whether a like is ever shown back -- a count on the
// post, a notice to its owner -- and so needs keeping. Nothing the game
// reads back has been seen carrying one; this only logs it. See
// docs/CAPTURES/README.md. Change: this function.
func (s *Service) ikasu(
	ctx context.Context, collection string, req *toyohrpb.IkasuDocumentRequest,
) (*toyohrpb.IkasuDocumentResponse, error) {
	uid, ok := s.caller(ctx)
	if !ok {
		return nil, status.Error(codes.Unauthenticated, "unauthenticated")
	}
	if s.docs == nil {
		return nil, status.Error(codes.NotFound, "no such document")
	}
	found, err := s.docs.IsPublished(ctx, "services/toyohr/interfaces/"+collection, req.GetName())
	if err != nil {
		s.log.Error("toyohr: ikasu lookup", "document", req.GetName(), "error", err)
		return nil, status.Error(codes.Internal, "internal error")
	}
	if !found {
		s.log.Warn("toyohr: ikasu for an unknown post", "user", uid, "document", req.GetName())
		return nil, status.Error(codes.NotFound, "no such document")
	}
	s.log.Info("toyohr: ikasu", "user", uid, "document", req.GetName())
	return &toyohrpb.IkasuDocumentResponse{}, nil
}

// logSelect records what a console asks SelectDocuments for. Nothing
// documents the request's type and params; the console's own requests are
// the cheapest way to learn which posts it wants listed.
func (s *Service) logSelect(service string, req *toyohrpb.SelectDocumentsRequest) {
	params, _ := protojson.Marshal(req.GetParams())
	s.log.Info("toyohr: select documents", "service", service,
		"type", req.GetType(), "tenant", req.GetTenant(), "params", string(params))
}

func (s canolaService) IkasuDocument(
	ctx context.Context, req *toyohrpb.IkasuDocumentRequest,
) (*toyohrpb.IkasuDocumentResponse, error) {
	return s.ikasu(ctx, "canola/deckPosts", req)
}

type lockerService struct {
	*Service
	toyohrpb.UnimplementedLockerServer
}

func (s lockerService) SelectDocuments(
	ctx context.Context, req *toyohrpb.SelectDocumentsRequest,
) (*toyohrpb.SelectDocumentsResponse, error) {
	s.logSelect("locker", req)
	return s.plaza(ctx, "locker/lockerPosts", plazaLockers)
}

func (s lockerService) IkasuDocument(
	ctx context.Context, req *toyohrpb.IkasuDocumentRequest,
) (*toyohrpb.IkasuDocumentResponse, error) {
	return s.ikasu(ctx, "locker/lockerPosts", req)
}

// RegisterDocument on Canola (a drawing) and Locker (a locker) is the last
// step of publishing one: the console uploads the document to Ugcstore, then
// registers it. Both answers are empty messages, and NextendoNetwork records
// Nintendo's own answer to Canola's as exactly that, an empty payload (facts
// only, none of their code used) -- so acknowledging invents nothing. Left
// Unimplemented, the game reports a communication error at the end of
// posting.
//
// This only acknowledges. The document itself is already stored by
// Ugcstore/CommitDocuments, and SelectDocuments shows it to other players.
func (s canolaService) RegisterDocument(
	ctx context.Context, req *toyohrpb.CanolaRegisterDocumentRequest,
) (*toyohrpb.CanolaRegisterDocumentResponse, error) {
	uid, ok := s.caller(ctx)
	if !ok {
		return nil, status.Error(codes.Unauthenticated, "unauthenticated")
	}
	s.log.Info("toyohr: drawing registered", "user", uid, "document", req.GetName())
	return &toyohrpb.CanolaRegisterDocumentResponse{}, nil
}

func (s lockerService) RegisterDocument(
	ctx context.Context, req *toyohrpb.LockerRegisterDocumentRequest,
) (*toyohrpb.LockerRegisterDocumentResponse, error) {
	uid, ok := s.caller(ctx)
	if !ok {
		return nil, status.Error(codes.Unauthenticated, "unauthenticated")
	}
	s.log.Info("toyohr: locker registered", "user", uid, "document", req.GetName())
	return &toyohrpb.LockerRegisterDocumentResponse{}, nil
}

// ---- nn.npln.toyohr.v1.CoopScenario -----------------------------------------

type coopScenarioService struct {
	*Service
	toyohrpb.UnimplementedCoopScenarioServer
}

// RegisterDocument shares a Salmon Run job as a scenario code. The console
// names the job's document; the code maps to it in Ugcstore under
// grid0/coopScenarioCodes/<code>, and ResolveCoopScenarioCode hands that
// document back to whoever types the code in.
//
// TODO(capture-needed): the code's exact alphabet and the shape of
// CoopScenarioCode.name. This answers tenants/<t>/coopScenarioCodes/<code>
// (the game carries the "/coopScenarioCodes" segment) with a 16-character
// code starting with S, like the codes players share. Change: newShareCode
// and shareCodeFrom.
func (s coopScenarioService) RegisterDocument(
	ctx context.Context, req *toyohrpb.CoopScenarioRegisterDocumentRequest,
) (*toyohrpb.CoopScenarioRegisterDocumentResponse, error) {
	uid, ok := s.caller(ctx)
	if !ok {
		return nil, status.Error(codes.Unauthenticated, "unauthenticated")
	}
	if s.docs == nil || req.GetName() == "" {
		return nil, status.Error(codes.FailedPrecondition, "scenario codes are not available")
	}
	code, err := newShareCode('S')
	if err != nil {
		return nil, status.Error(codes.Internal, "internal error")
	}
	if _, err := s.docs.Put(ctx, uid, shareCodeDoc("coopScenarioCodes", code), &commonpb.MapValue{Fields: map[string]*commonpb.Value{
		"document": {ValueType: &commonpb.Value_StringValue{StringValue: req.GetName()}},
	}}); err != nil {
		return nil, err
	}
	s.log.Info("toyohr: scenario code registered", "user", uid, "code", code, "document", req.GetName())
	return &toyohrpb.CoopScenarioRegisterDocumentResponse{
		Code: &toyohrpb.CoopScenarioCode{Name: s.tenantPath() + "/coopScenarioCodes/" + code},
	}, nil
}

// ResolveCoopScenarioCode looks up a shared Salmon Run scenario by its code.
// A code this server never issued is NotFound.
func (s coopScenarioService) ResolveCoopScenarioCode(
	ctx context.Context, req *toyohrpb.ResolveCoopScenarioCodeRequest,
) (*toyohrpb.ResolveCoopScenarioCodeResponse, error) {
	doc, err := s.resolveShareCode(ctx, "coopScenarioCodes", req.GetName())
	if err != nil {
		return nil, err
	}
	return &toyohrpb.ResolveCoopScenarioCodeResponse{Document: doc}, nil
}

// resolveShareCode follows a shared code to the document it was registered
// for. A code this server never issued is NotFound.
func (s *Service) resolveShareCode(ctx context.Context, collection, name string) (*ugcpb.Document, error) {
	code := shareCodeFrom(name)
	if s.docs == nil || code == "" {
		return nil, status.Error(codes.NotFound, "no such code")
	}
	link, err := s.docs.GetDocument(ctx, &ugcpb.GetDocumentRequest{Name: shareCodeDoc(collection, code)})
	if err != nil {
		s.log.Info("toyohr: unknown share code", "collection", collection, "code", code)
		return nil, status.Error(codes.NotFound, "no such code")
	}
	target := link.GetFields().GetFields()["document"].GetStringValue()
	doc, err := s.docs.GetDocument(ctx, &ugcpb.GetDocumentRequest{Name: target})
	if err != nil {
		return nil, status.Error(codes.NotFound, "no such code")
	}
	return doc, nil
}

const scenarioCodeAlphabet = "0123456789ABCDEFGHJKLMNPQRSTUVWXY"

// newShareCode makes a 16-character code: prefix ('S' for a Salmon Run
// scenario, 'R' for a replay, like the codes players share) and 15 random
// characters.
func newShareCode(prefix byte) (string, error) {
	b := make([]byte, 15)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	out := []byte{prefix}
	for _, c := range b {
		out = append(out, scenarioCodeAlphabet[int(c)%len(scenarioCodeAlphabet)])
	}
	return string(out), nil
}

// shareCodeFrom takes a code from a resource name or as typed, with or
// without dashes, in any case.
func shareCodeFrom(name string) string {
	if i := strings.LastIndex(name, "/"); i >= 0 {
		name = name[i+1:]
	}
	code := strings.ToUpper(strings.ReplaceAll(name, "-", ""))
	if len(code) != 16 || strings.Trim(code, scenarioCodeAlphabet) != "" {
		return ""
	}
	return code
}

func shareCodeDoc(collection, code string) string {
	return "tenants/current/documents/grid0/" + collection + "/" + code
}

// ---- nn.npln.toyohr.v1.Replay ------------------------------------------------

type replayService struct {
	*Service
	toyohrpb.UnimplementedReplayServer
}

// RegisterDocument shares an uploaded replay as a replay code, the same way
// CoopScenario shares a job: the console names the replay's document (its
// bytes are the document's Ugcstore attachment), and the code maps to it
// under grid0/replayCodes/<code>. The game carries the "/replayCodes"
// segment beside "/coopScenarioCodes", and the two services' messages have
// the same shape.
//
// TODO(capture-needed): the replay code's exact alphabet and
// ReplayCode.name. Same reading as CoopScenario's, with codes starting with
// R like the codes players share. Change: newShareCode and shareCodeFrom.
func (s replayService) RegisterDocument(
	ctx context.Context, req *toyohrpb.ReplayRegisterDocumentRequest,
) (*toyohrpb.ReplayRegisterDocumentResponse, error) {
	uid, ok := s.caller(ctx)
	if !ok {
		return nil, status.Error(codes.Unauthenticated, "unauthenticated")
	}
	if s.docs == nil || req.GetName() == "" {
		return nil, status.Error(codes.FailedPrecondition, "replay codes are not available")
	}
	code, err := newShareCode('R')
	if err != nil {
		return nil, status.Error(codes.Internal, "internal error")
	}
	if _, err := s.docs.Put(ctx, uid, shareCodeDoc("replayCodes", code), &commonpb.MapValue{Fields: map[string]*commonpb.Value{
		"document": {ValueType: &commonpb.Value_StringValue{StringValue: req.GetName()}},
	}}); err != nil {
		return nil, err
	}
	s.log.Info("toyohr: replay code registered", "user", uid, "code", code, "document", req.GetName())
	return &toyohrpb.ReplayRegisterDocumentResponse{
		Code: &toyohrpb.ReplayCode{Name: s.tenantPath() + "/replayCodes/" + code},
	}, nil
}

// ResolveReplayCode hands the replay's document back to whoever types the
// code in; the console then downloads its attachment.
func (s replayService) ResolveReplayCode(
	ctx context.Context, req *toyohrpb.ResolveReplayCodeRequest,
) (*toyohrpb.ResolveReplayCodeResponse, error) {
	doc, err := s.resolveShareCode(ctx, "replayCodes", req.GetName())
	if err != nil {
		return nil, err
	}
	return &toyohrpb.ResolveReplayCodeResponse{Document: doc}, nil
}

// FulfillReservations reports which of a player's replay reservations are
// ready to download. A reservation is made outside the game (a code
// entered in the phone app), which GRID0+ has no way to receive, so the
// honest answer is an empty list -- not an error, the same
// distinction ListDeviceAccounts (empty, not NotFound) draws against
// GetDeviceAccount (NotFound) for a single missing one.
func (s replayService) FulfillReservations(
	_ context.Context, _ *toyohrpb.FulfillReservationsRequest,
) (*toyohrpb.FulfillReservationsResponse, error) {
	return &toyohrpb.FulfillReservationsResponse{Timestamp: timestamppb.New(s.now())}, nil
}

// ---- nn.npln.toyohr.v1.GameRecord --------------------------------------------
//
// CreateBankaraChallenge is a write that reports real battle outcomes and
// rank state -- deeply Nintendo-internal rating data with no defensible
// default -- so it is left to the embedded Unimplemented default.
// CreateFestPowerMeasurement is implemented (game_record.go) now that a
// capture settles its shape. InitializeAttributes is implemented (game_record.go):
// the game's own parser keeps its defaults for any absent key, so that answer
// needs no invented value, only what the request itself carries.
// InitializeTag, GetTag and SelectTags are also implemented (game_record.go):
// unlike CreateBankaraChallenge, their response
// messages are all "[UNKNOWN]" upstream and generate with zero fields, so
// answering them invents nothing either.
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
