package toyohr

import (
	"context"
	"errors"
	"strings"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/n-popescu/switchnet/internal/db"
	"github.com/n-popescu/switchnet/internal/rpcauth"
	toyohrpb "github.com/n-popescu/switchnet/proto/toyohr/v1"
)

// CreateFestEntry records the caller's team choice for a fest.
//
// The choice is permanent: Splatoon 3's own Splatfest rules do not let a
// player switch teams after picking, which is why a second call for the
// same fest returns AlreadyExists rather than overwriting the first --
// db.Store.CreateFestEntry enforces this the same way, so there is one place
// the rule lives, not two that could disagree.
//
// SwitchNet cannot validate fest_team against the fest's real team names:
// those live inside the BCAT pack the console fetches separately (see
// internal/bcat's package comment), not in anything this server stores. Any
// non-empty string is accepted, the same boundary GetFestDecryptionKey's own
// package doc already draws.
func (s *Service) CreateFestEntry(
	ctx context.Context, req *toyohrpb.CreateFestEntryRequest,
) (*toyohrpb.FestEntry, error) {
	festID := festIDFromResourceName(req.GetParent())
	if festID == "" {
		return nil, status.Error(codes.InvalidArgument, "no fest named")
	}
	team := strings.TrimSpace(req.GetFestEntry().GetFestTeam())
	if team == "" {
		return nil, status.Error(codes.InvalidArgument, "no fest_team given")
	}
	region := strings.ToUpper(strings.TrimSpace(req.GetFestEntry().GetFestRegion()))

	caller, ok := rpcauth.From(ctx)
	if !ok {
		return nil, status.Error(codes.Unauthenticated, "")
	}

	if _, err := s.store.GetFest(ctx, festID); err != nil {
		if errors.Is(err, db.ErrNotFound) {
			return nil, status.Error(codes.NotFound, "no such fest")
		}
		s.log.Error("toyohr: load fest", "fest", festID, "error", err)
		return nil, status.Error(codes.Internal, "")
	}

	entry := db.FestEntry{
		FestID: festID, UserID: caller.UserID,
		FestTeam: team, FestRegion: region, CreatedAt: s.now(),
	}
	if err := s.store.CreateFestEntry(ctx, entry); err != nil {
		if errors.Is(err, db.ErrConflict) {
			return nil, status.Error(codes.AlreadyExists, "already entered this fest")
		}
		s.log.Error("toyohr: create fest entry",
			"fest", festID, "user", caller.UserID, "error", err)
		return nil, status.Error(codes.Internal, "")
	}

	s.log.Info("toyohr: fest entry created", "fest", festID, "user", caller.UserID, "team", team)
	return s.festEntryProto(entry), nil
}

// GetFestEntry returns the caller's own entry for a fest.
//
// Self-only, the same discipline internal/baas's requireSelf enforces on
// every user-scoped HTTP endpoint: a team choice is the kind of thing a
// console should not be able to read for an account that is not its own,
// before results are public.
func (s *Service) GetFestEntry(
	ctx context.Context, req *toyohrpb.GetFestEntryRequest,
) (*toyohrpb.FestEntry, error) {
	festID, userID := festEntryIDsFromResourceName(req.GetName())
	if festID == "" || userID == "" {
		return nil, status.Error(codes.InvalidArgument, "no fest entry named")
	}

	caller, ok := rpcauth.From(ctx)
	if !ok {
		return nil, status.Error(codes.Unauthenticated, "")
	}
	if !strings.EqualFold(userID, caller.UserID) {
		return nil, status.Error(codes.PermissionDenied, "")
	}

	entry, err := s.store.GetFestEntry(ctx, festID, userID)
	if err != nil {
		if errors.Is(err, db.ErrNotFound) {
			return nil, status.Error(codes.NotFound, "no such fest entry")
		}
		s.log.Error("toyohr: load fest entry", "fest", festID, "user", userID, "error", err)
		return nil, status.Error(codes.Internal, "")
	}
	return s.festEntryProto(entry), nil
}

// festEntryProto renders a stored entry as the resource message.
//
// TODO(capture-needed): the "entries/<userID>" resource-name shape below
// follows the same "tenants/<t>/fests/<ID>/..." pattern GetFestDecryptionKey
// already uses (confirmed against a real seed folder's summary.yml for the
// fest id/resource/revision, not for this specific sub-path), but no capture
// shows what CreateFestEntry's response actually names a newly-created entry,
// or whether the console parses this field at all rather than only checking
// the RPC succeeded. Change: this function and festEntryIDsFromResourceName.
func (s *Service) festEntryProto(e db.FestEntry) *toyohrpb.FestEntry {
	return &toyohrpb.FestEntry{
		Name:       s.tenantPath() + "/fests/" + e.FestID + "/entries/" + e.UserID,
		FestTeam:   e.FestTeam,
		FestRegion: e.FestRegion,
	}
}

// festEntryIDsFromResourceName pulls the fest and account ids out of a fest
// entry's resource name -- see festEntryProto for the shape it parses back.
func festEntryIDsFromResourceName(name string) (festID, userID string) {
	parts := strings.Split(name, "/")
	for i, p := range parts {
		switch {
		case p == "fests" && i+1 < len(parts):
			festID = strings.ToUpper(parts[i+1])
		case p == "entries" && i+1 < len(parts):
			userID = parts[i+1]
		}
	}
	return festID, userID
}

// GetFestResult answers whether a fest has concluded, and how.
//
// TODO(capture-needed): every sub-result below reports IsValid: false rather
// than a computed ratio or point total. SwitchNet has no real battle-outcome
// data to compute one from -- npln_fest_entries records who picked which
// team, not who won, and GameRecord (TODO.md §1.4) is not implemented -- so
// a number here would be either fabricated, or, if derived from entry counts
// alone, actively misleading: a lopsided sign-up split is not a lopsided
// battle result, which is what these fields represent in a real Splatfest.
// IsValid exists in the proto specifically to say "not computed yet" without
// forcing a number, which is why using it is the defensible reading rather
// than an invented one. What a capture would settle: whether the console
// reads an all-invalid result as "not concluded" (a fest keeps running, the
// same as returning Unimplemented did before this function existed) or as a
// valid empty/tied outcome that would need IsValid: true after all. Change:
// this function.
func (s *Service) GetFestResult(
	ctx context.Context, req *toyohrpb.GetFestResultRequest,
) (*toyohrpb.FestResult, error) {
	festID := festIDFromResourceName(req.GetName())
	if festID == "" {
		return nil, status.Error(codes.InvalidArgument, "no fest named")
	}

	if _, err := s.store.GetFest(ctx, festID); err != nil {
		if errors.Is(err, db.ErrNotFound) {
			return nil, status.Error(codes.NotFound, "no such fest")
		}
		s.log.Error("toyohr: load fest", "fest", festID, "error", err)
		return nil, status.Error(codes.Internal, "")
	}

	return &toyohrpb.FestResult{
		Name:          s.tenantPath() + "/fests/" + festID + "/result",
		Yobisai:       &toyohrpb.YobisaiResult{IsValid: false},
		HonsaiMidterm: &toyohrpb.HonsaiMidtermResult{IsValid: false},
		HonsaiFinal:   &toyohrpb.HonsaiFinalResult{IsValid: false},
		Overall:       &toyohrpb.OverallResult{IsValid: false},
	}, nil
}
