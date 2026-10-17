// Package toyohr implements Splatoon 3's own NPLN services
// (nn.npln.toyohr.v1.*), starting with Splatfests.
//
// Reference: https://github.com/kinnay/NintendoClients/wiki/NPLN-Servers
// Definitions: github.com/kinnay/NPLN-Protocols, generated into proto/
package toyohr

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/n-popescu/grid0plus/internal/bcat"
	"github.com/n-popescu/grid0plus/internal/db"
	"github.com/n-popescu/grid0plus/internal/splatdiag"
	"github.com/n-popescu/grid0plus/internal/ugcstore"
	commonpb "github.com/n-popescu/grid0plus/proto/common"
	toyohrpb "github.com/n-popescu/grid0plus/proto/toyohr/v1"
)

// Service implements the Splatoon 3 services.
//
// Canola, CoopScenario, GameRecord, Locker, Replay and UserScreening are
// deliberately NOT embedded here: CanolaServer and LockerServer declare the
// identical IkasuDocument/SelectDocuments/RegisterDocument method names (so
// both Unimplemented types promoted onto one struct would make every one of
// those an ambiguous selector), and CoopScenarioServer/ReplayServer both
// declare RegisterDocument with different signatures (which Go cannot
// satisfy with one method, ambiguous or not -- there is no overloading).
// Each gets its own small adapter type in backlog.go instead, the same
// shape internal/npln/services.go already uses for its own several
// interfaces on one *Server. See backlog.go's package comment for what is
// and is not implemented there.
type Service struct {
	toyohrpb.UnimplementedFestServiceServer
	toyohrpb.UnimplementedCloudSaveServer

	store  db.Store
	log    *slog.Logger
	tenant string
	now    func() time.Time

	// callerID resolves the authenticated player behind an RPC.
	//
	// A function rather than an import, because the identity lives in
	// internal/npln's interceptor and internal/npln registers this service --
	// importing it back would be a cycle. It returns only the user id, which is
	// all any handler here needs.
	//
	// Nil is legitimate and fail-closed: the dashboard builds a Service to
	// schedule fests and never serves an RPC through it, so a handler reached
	// without a resolver refuses rather than guessing.
	callerID func(context.Context) (string, bool)

	// rotation is the operator's configured stage and rule rotation. Empty
	// means the schedule handlers answer with nothing, which is a state the
	// game has a reading for.
	rotation Rotation

	// docs is where GameRecord documents are created. See Options.Documents.
	docs       *ugcstore.Service
	diagnostic *splatdiag.Bundle

	// messages delivers lobby messages between connected players. In memory:
	// a lobby message is worth nothing to a player who was not connected.
	messages *messageHub

	// onEntry is told about each new fest entry. See Options.OnEntry.
	onEntry func(context.Context, db.FestEntry)

	// noRotation remembers which schedule kinds have already been reported as
	// unconfigured, so a console asking on a timer does not fill the log.
	noRotation sync.Map
}

// Options configures the Splatoon 3 service set.
type Options struct {
	Store  db.Store
	Log    *slog.Logger
	Tenant string

	// CallerID resolves the authenticated player behind an RPC. See the field
	// comment; leaving it nil makes every player-scoped handler refuse.
	CallerID func(context.Context) (string, bool)

	// Rotation is the stage and rule rotation to serve. Empty is legitimate.
	Rotation Rotation

	// Documents is the Ugcstore the console reads its GameRecord documents
	// from. GameRecord/InitializeAttributes creates them there; nil leaves
	// that RPC unimplemented.
	Documents  *ugcstore.Service
	Diagnostic *splatdiag.Bundle

	// OnEntry is called once for every new fest entry a console makes, after
	// it is stored: the Discord bot gives the player the team's role from it.
	// Optional. It must not block; it runs on the console's request.
	OnEntry func(context.Context, db.FestEntry)

	Now func() time.Time
}

// NewService builds the Splatoon 3 service set.
func NewService(opts Options) *Service {
	if opts.Now == nil {
		opts.Now = time.Now
	}
	return &Service{
		store:      opts.Store,
		log:        opts.Log,
		tenant:     opts.Tenant,
		now:        opts.Now,
		callerID:   opts.CallerID,
		rotation:   opts.Rotation,
		docs:       opts.Documents,
		diagnostic: opts.Diagnostic,
		messages:   newMessageHub(),
		onEntry:    opts.OnEntry,
	}
}

// Register attaches every Splatoon 3 service to a gRPC server.
//
// **All of them, including the ones with no handlers yet**, and that is the
// point. An RPC on a registered service answers Unimplemented and
// internal/npln's interceptor logs it once; an RPC on a service that was
// never registered fails the same way with no log line. ROADMAP §2.4 says to
// let the console drive the order of this backlog -- which only works if the
// console's asking is recorded. See backlog.go's package comment for what is
// and is not implemented there, and Service's own doc comment above for why
// several of these get their own adapter type instead of being embedded here
// directly.
func (s *Service) Register(srv *grpc.Server) {
	registerFestService(srv, s)
	registerCloudSave(srv, s)
	toyohrpb.RegisterScheduleServer(srv, &scheduleService{s: s})
	toyohrpb.RegisterLobbyMessagingServer(srv, &lobbyMessagingService{s: s})
	toyohrpb.RegisterUserScreeningServer(srv, &userScreeningService{s: s})
	toyohrpb.RegisterCanolaServer(srv, canolaService{Service: s})
	toyohrpb.RegisterLockerServer(srv, lockerService{Service: s})
	toyohrpb.RegisterCoopScenarioServer(srv, coopScenarioService{Service: s})
	toyohrpb.RegisterReplayServer(srv, replayService{Service: s})
	toyohrpb.RegisterGameRecordServer(srv, gameRecordService{Service: s})
}

// caller returns the authenticated player's id, or false.
//
// Fail-closed: no resolver, or a resolver that cannot name a player, means the
// handler refuses. There is no default player and no fallback -- that is the
// bug this repository is shaped around.
func (s *Service) caller(ctx context.Context) (string, bool) {
	if s.callerID == nil {
		return "", false
	}
	id, ok := s.callerID(ctx)
	if !ok || id == "" {
		return "", false
	}
	return id, true
}

// tenantPath is the resource-name prefix every fest resource hangs off.
func (s *Service) tenantPath() string { return "tenants/" + s.tenant }

// SelectFestSchedule announces the fest a region should be showing.
//
// The response carries only the fest's MECHANICS: which pack to load, its
// revision, the five phase times, and the publication window. Every piece of
// content -- team names, banners, idol dialogue -- is inside the BCAT pack this
// names, which the console fetches separately.
//
// That split is why the two must agree. If the announced fest id, resource or
// revision does not match what the console's BCAT cache actually holds, the
// game asks BCAT for a resource digest it cannot find and reports BcatInvalid,
// which reaches the player as a generic communication error.
//
// Both preserved working hall captures answer with an empty protobuf when no
// fest is configured. A one-account hardware probe of that response also
// eliminated the BCAT 2122-1058 boot error without changing the console.
// An earlier empty-response crash happened with an older set of responses and
// does not establish that the empty no-fest response itself caused it.
func (s *Service) SelectFestSchedule(
	ctx context.Context, req *toyohrpb.SelectFestScheduleRequest,
) (*toyohrpb.SelectFestScheduleResponse, error) {
	region := strings.ToUpper(strings.TrimSpace(req.GetRegion()))

	fest, ok, err := s.currentFest(ctx, region)
	if err != nil {
		return nil, err
	}
	if !ok {
		return &toyohrpb.SelectFestScheduleResponse{}, nil
	}
	s.log.Info("toyohr: announced fest schedule", "fest", fest.ID,
		"phase", fest.PhaseAt(s.now()), "region", region,
		"requested_target", req.GetTarget(), "target", scheduleTarget(req.GetTarget()))

	return &toyohrpb.SelectFestScheduleResponse{
		Schedule: s.scheduleProto(fest, region, req.GetTarget()),
	}, nil
}

// scheduleProto renders a fest as the schedule message.
func (s *Service) scheduleProto(f Fest, region, target string) *toyohrpb.FestSchedule {
	tt := f.Timetable()
	pubStart, pubEnd := f.PublishWindow()

	publishments := make(map[string]*toyohrpb.FestPublishment, len(f.Regions))
	for _, r := range f.Regions {
		publishments[r] = &toyohrpb.FestPublishment{
			StartTime: timestamppb.New(pubStart),
			EndTime:   timestamppb.New(pubEnd),
		}
	}

	return &toyohrpb.FestSchedule{
		Name: s.tenantPath() + "/festSchedules/" + f.ID,
		Fest: s.tenantPath() + "/fests/" + f.ID,
		// Every region the fest runs in, not just the caller's: the response
		// carries the whole publication map, and a console comparing its own
		// region against a one-entry map would find nothing for its neighbours.
		FestRegions: append([]string(nil), f.Regions...),
		Target:      scheduleTarget(target),
		// The BCAT pack, and the revision of it. These two plus the fest id are
		// the three things that must match the console's cache.
		FestGameData:         f.Resource,
		FestGameDataRevision: f.Revision,
		Timetable: &toyohrpb.FestTimetable{
			OpenTime:  timestamppb.New(tt.Open),
			StartTime: timestamppb.New(tt.Start),
			MidTime:   timestamppb.New(tt.Mid),
			EndTime:   timestamppb.New(tt.End),
			CloseTime: timestamppb.New(tt.Close),
		},
		FestPublishments: publishments,
		// The captured fest schedule carries this attribute even before play
		// starts. Without it, our announcement has a different wire shape from
		// the one Splatoon 3 accepts for team selection.
		Attributes: &commonpb.MapValue{Fields: map[string]*commonpb.Value{
			"TricolorStages": {ValueType: &commonpb.Value_ArrayValue{ArrayValue: &commonpb.ArrayValue{
				Values: []*commonpb.Value{intValue(2), intValue(12)},
			}}},
		}},
	}
}

// GetFestDecryptionKey hands the console the keys that open a fest's BCAT packs.
//
// The keys are GRID0+'s own: the pack was encrypted with them. Nothing here
// is Nintendo's, and nothing here decrypts anything of Nintendo's -- a fest
// GRID0+ serves is a fest GRID0+ built.
//
// The keys are returned POSITIONALLY, in the order the message declares them:
// notice, start, result, then the three per-team keys. A key in the wrong slot
// is handed to the console for the wrong phase, and the pack fails to open with
// nothing to say which phase was wrong.
func (s *Service) GetFestDecryptionKey(
	ctx context.Context, req *toyohrpb.GetFestDecryptionKeyRequest,
) (*toyohrpb.FestDecryptionKey, error) {
	id := festIDFromResourceName(req.GetName())
	if id == "" {
		return nil, status.Error(codes.InvalidArgument, "no fest named")
	}

	row, err := s.store.GetFest(ctx, id)
	if err != nil {
		if errors.Is(err, db.ErrNotFound) {
			return nil, status.Error(codes.NotFound, "no such fest")
		}
		s.log.Error("toyohr: load fest", "fest", id, "error", err)
		return nil, status.Error(codes.Internal, "")
	}

	out := &toyohrpb.FestDecryptionKey{
		Name: s.tenantPath() + "/fests/" + id + "/decryptionKey",
	}
	assignPackKeys(out, row.PackKeys)
	// The result field carries the key for the winning wa/wb/wc archive,
	// not all three outcome archives. Retain these before phase filtering.
	outcomeKeys := map[string]string{
		"Alpha": out.TeamAlphaKey, "Bravo": out.TeamBravoKey, "Charlie": out.TeamCharlieKey,
	}
	// Nextendo's working active-fest capture carries only name, notice and
	// start (fields 1-3). Publishing the stored team keys on an ordinary
	// request added fields 5-7 before the result phase. Keep those optional
	// slots for explicit show_all_keys requests after results, not game startup.
	if !req.GetShowAllKeys() {
		out.TeamAlphaKey = ""
		out.TeamBravoKey = ""
		out.TeamCharlieKey = ""
	}
	if f := festFromRow(row); f.PhaseAt(s.now()) == PhaseClosed {
		entries, err := s.store.ListFestEntries(ctx, id)
		if err != nil {
			return nil, status.Error(codes.Internal, "")
		}
		battles, err := s.store.ListFestBattles(ctx, id)
		if err != nil {
			return nil, status.Error(codes.Internal, "")
		}
		_, _, overall := finalFestResult(entries, battles, festShares(voteRatios(entries)), f.Timetable().Mid)
		if overall.GetIsValid() && len(overall.GetPoints()) > 0 {
			// The winner in the result-pack key must be the same team as the
			// one awarded the most points in GetFestResult.
			out.WinningTeam = overall.GetPoints()[0].GetFestTeam()
			if out.ResultWinningTeamKey == "" {
				out.ResultWinningTeamKey = outcomeKeys[out.WinningTeam]
			}
		}
	} else {
		// A result key and winner during an active fest make the game read
		// the victory pack before the result window opens.
		out.ResultWinningTeamKey = ""
		out.TeamAlphaKey = ""
		out.TeamBravoKey = ""
		out.TeamCharlieKey = ""
	}
	s.log.Info("toyohr: fest key response", "fest", id, "show_all_keys", req.GetShowAllKeys(),
		"notice", out.NoticeKey != "", "start", out.StartKey != "",
		"result", out.ResultWinningTeamKey != "", "team_keys", out.TeamAlphaKey != "" || out.TeamBravoKey != "" || out.TeamCharlieKey != "")
	return out, nil
}

// assignPackKeys fills the key fields from an ordered list.
//
// The order is bcat.KeyFields, which is the order the message declares its
// fields in. A key that is absent is left absent rather than filled with an
// empty string: Nintendo omits the key for a phase that has not happened yet,
// and an empty string is a key the console would try to use.
//
// All supplied slots remain available to explicit show_all_keys requests.
// Ordinary requests are filtered by GetFestDecryptionKey to match the captured
// phase response rather than treating schema availability as a publish rule.
func assignPackKeys(dst *toyohrpb.FestDecryptionKey, keys []string) {
	set := map[string]func(string){
		bcat.KeyNotice:            func(v string) { dst.NoticeKey = v },
		bcat.KeyStart:             func(v string) { dst.StartKey = v },
		bcat.KeyResultWinningTeam: func(v string) { dst.ResultWinningTeamKey = v },
		bcat.KeyTeamAlpha:         func(v string) { dst.TeamAlphaKey = v },
		bcat.KeyTeamBravo:         func(v string) { dst.TeamBravoKey = v },
		bcat.KeyTeamCharlie:       func(v string) { dst.TeamCharlieKey = v },
	}
	for i, k := range keys {
		if i >= len(bcat.KeyFields) || k == "" {
			continue
		}
		set[bcat.KeyFields[i]](k)
	}
}

// festIDFromResourceName pulls the fest id out of a resource name.
//
// The console addresses a fest as "tenants/<t>/fests/<ID>/decryptionKey". Only
// the id is used, so a caller naming another tenant cannot reach across: the
// lookup is by id against this server's own store either way.
func festIDFromResourceName(name string) string {
	parts := strings.Split(name, "/")
	for i, p := range parts {
		// Entry and decryption-key resources use "fests"; result resources
		// use the separate "festResults" collection. The console asks for the
		// latter directly, so treating it as an unknown path turns a normal
		// result lookup into InvalidArgument and aborts the hall flow.
		if (p == "fests" || p == "festResults") && i+1 < len(parts) {
			return strings.ToUpper(parts[i+1])
		}
	}
	// A bare id is accepted too: it is unambiguous, and refusing it would mean
	// the only way to ask is a path whose exact shape is not yet confirmed
	// against a capture.
	if !strings.Contains(name, "/") {
		return strings.ToUpper(name)
	}
	return ""
}

// lastSegment returns the trailing path component of a resource name, or the
// whole string if it has none -- the same lenient parsing
// internal/npln's own lastSegment does, for the same reason: a console
// echoing back whatever name this server minted (or a bare id) should not
// have to match a prefix nobody has confirmed against a capture. Unlike
// festIDFromResourceName, this does not uppercase: ids outside the fest
// family (a user id, a lobby id) are not fest ids.
func lastSegment(name string) string {
	name = strings.TrimSuffix(strings.TrimSpace(name), "/")
	if i := strings.LastIndex(name, "/"); i >= 0 {
		return name[i+1:]
	}
	return name
}

// currentFest returns the fest a region should be showing, if any.
//
// "Current" spans from the announcement to the close, not just play: a fest is
// visible in the lobby for a week before it starts and its results stay up for
// two hours after it ends.
//
// When two fests overlap -- which the operator can create by mistake -- the one
// that started most recently wins, and the overlap is logged. Announcing two
// fests to one console is not a state the game has a reading for.
func (s *Service) currentFest(ctx context.Context, region string) (Fest, bool, error) {
	rows, err := s.store.ListFests(ctx)
	if err != nil {
		s.log.Error("toyohr: list fests", "error", err)
		return Fest{}, false, status.Error(codes.Internal, "")
	}

	now := s.now()
	var chosen Fest
	var found int

	for _, row := range rows {
		if !row.Enabled {
			continue
		}
		f := festFromRow(row)
		if region != "" && !f.publishedIn(region) {
			continue
		}
		// A closed fest is one whose results are out, and the game shows them
		// from this schedule, so it stays announced until its publish window
		// ends. Dropping it at close hid the results entirely (2026-10-09).
		if _, pubEnd := f.PublishWindow(); f.PhaseAt(now) == PhaseNone || !now.Before(pubEnd) {
			continue
		}
		found++
		if chosen.ID == "" || f.Start.After(chosen.Start) {
			chosen = f
		}
	}

	if found > 1 {
		s.log.Warn("toyohr: more than one fest is live at once; announcing the most recent",
			"count", found, "announcing", chosen.ID, "region", region)
	}
	return chosen, chosen.ID != "", nil
}

func (f Fest) publishedIn(region string) bool {
	for _, r := range f.Regions {
		if strings.EqualFold(r, region) {
			return true
		}
	}
	return false
}

// festFromRow converts a stored row into the domain type.
func festFromRow(r db.Fest) Fest {
	return Fest{
		ID:       r.ID,
		Resource: r.Resource,
		Revision: r.Revision,
		Start:    r.StartAt.UTC(),
		End:      r.EndAt.UTC(),
		Regions:  append([]string(nil), r.Regions...),
	}
}

// RowFromFest converts a fest into a storable row.
func RowFromFest(f Fest, enabled bool, packKeys []string) db.Fest {
	return db.Fest{
		ID:       f.ID,
		Resource: f.Resource,
		Revision: f.Revision,
		StartAt:  f.Start.UTC(),
		EndAt:    f.End,
		Regions:  append([]string(nil), f.Regions...),
		PackKeys: append([]string(nil), packKeys...),
		Enabled:  enabled,
	}
}

// SaveFest validates a fest and stores it.
//
// Validation happens here rather than in the store because the store is
// deliberately free of business logic -- and because the reasons a fest is
// invalid (slot alignment, resource-name case) need explaining, which is not a
// constraint a column can carry.
func (s *Service) SaveFest(ctx context.Context, f Fest, enabled bool, packKeys []string) error {
	if err := f.Validate(); err != nil {
		return err
	}
	for i, k := range packKeys {
		// An EMPTY key is legitimate and means "this pack does not exist", which
		// is how the remaining keys stay in their declared positions --
		// assignPackKeys returns them positionally and skips the empty ones. A
		// two-team fest has no wc, and requiring 32 characters for its slot
		// would mean either inventing a key for a pack nobody built or shifting
		// every later key by one, which hands the console the wrong key for the
		// wrong phase.
		if k == "" {
			continue
		}
		if len(k) != 32 {
			return fmt.Errorf("pack key %d is %d characters, want 32 (a 16-byte AES-128 key in hex) or empty for a pack that does not exist", i, len(k))
		}
	}
	return s.store.UpsertFest(ctx, RowFromFest(f, enabled, packKeys))
}
