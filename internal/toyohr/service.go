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
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/n-popescu/switchnet/internal/db"
	toyohrpb "github.com/n-popescu/switchnet/proto/toyohr/v1"
)

// Service implements the Splatoon 3 services.
type Service struct {
	toyohrpb.UnimplementedFestServiceServer

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
}

// Options configures the Splatoon 3 service set.
type Options struct {
	Store  db.Store
	Log    *slog.Logger
	Tenant string

	// CallerID resolves the authenticated player behind an RPC. See the field
	// comment; leaving it nil makes every player-scoped handler refuse.
	CallerID func(context.Context) (string, bool)

	Now func() time.Time
}

// NewService builds the Splatoon 3 service set.
func NewService(opts Options) *Service {
	if opts.Now == nil {
		opts.Now = time.Now
	}
	return &Service{
		store:    opts.Store,
		log:      opts.Log,
		tenant:   opts.Tenant,
		now:      opts.Now,
		callerID: opts.CallerID,
	}
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

// Register attaches the services to a gRPC server.
func (s *Service) Register(srv *grpc.Server) {
	toyohrpb.RegisterFestServiceServer(srv, s)
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
func (s *Service) SelectFestSchedule(
	ctx context.Context, req *toyohrpb.SelectFestScheduleRequest,
) (*toyohrpb.SelectFestScheduleResponse, error) {
	region := strings.ToUpper(strings.TrimSpace(req.GetRegion()))

	fest, ok, err := s.currentFest(ctx, region)
	if err != nil {
		return nil, err
	}
	if !ok {
		// No fest is running. An empty schedule is the honest answer, and it is
		// what the game sees between fests on a real server.
		return &toyohrpb.SelectFestScheduleResponse{}, nil
	}

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
		Target:      target,
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
	}
}

// GetFestDecryptionKey hands the console the keys that open a fest's BCAT packs.
//
// The keys are SwitchNet's own: the pack was encrypted with them. Nothing here
// is Nintendo's, and nothing here decrypts anything of Nintendo's -- a fest
// SwitchNet serves is a fest SwitchNet built.
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
	return out, nil
}

// assignPackKeys fills the key fields from an ordered list.
//
// A key that is absent is left absent rather than filled with an empty string.
// Nintendo omits the key for a phase that has not happened yet, and an empty
// string is a key the console would try to use.
func assignPackKeys(dst *toyohrpb.FestDecryptionKey, keys []string) {
	// The seven slots the message declares, in order. The reference
	// implementation this protocol knowledge came from stops at three, on the
	// grounds that the game never asks for more -- but the schema has seven and
	// the request carries a show_all_keys flag, so that is a property of the
	// phases they tested rather than of the protocol. All seven are filled when
	// they are supplied.
	set := []func(string){
		func(v string) { dst.NoticeKey = v },
		func(v string) { dst.StartKey = v },
		func(v string) { dst.ResultWinningTeamKey = v },
		func(v string) { dst.TeamAlphaKey = v },
		func(v string) { dst.TeamBravoKey = v },
		func(v string) { dst.TeamCharlieKey = v },
	}
	for i, k := range keys {
		if i >= len(set) || k == "" {
			continue
		}
		set[i](k)
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
		if p == "fests" && i+1 < len(parts) {
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
		if p := f.PhaseAt(now); p == PhaseNone || p == PhaseClosed {
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
		if len(k) != 32 {
			return fmt.Errorf("pack key %d is %d characters, want 32 (a 16-byte AES-128 key in hex)", i, len(k))
		}
	}
	return s.store.UpsertFest(ctx, RowFromFest(f, enabled, packKeys))
}
