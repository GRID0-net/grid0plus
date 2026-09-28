package toyohr

import (
	"context"
	"errors"
	"sort"
	"strings"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/n-popescu/grid0plus/internal/db"
	toyohrpb "github.com/n-popescu/grid0plus/proto/toyohr/v1"
)

// CreateFestEntry records the team a player picked.
//
// The player is the AUTHENTICATED caller. FestEntry carries a name field the
// client fills in, and taking the player from it would let any console vote as
// anybody — the shape of the bug this repository is built around.
//
// Team choice locks when play opens. Before that, calling this again changes
// the team, which is what a player who changes their mind in the lobby does;
// after it, the fest is under way and letting somebody switch sides mid-fest
// would move a vote that has already been counted into a published ratio.
func (s *Service) CreateFestEntry(
	ctx context.Context, req *toyohrpb.CreateFestEntryRequest,
) (*toyohrpb.FestEntry, error) {
	userID, ok := s.caller(ctx)
	if !ok {
		return nil, status.Error(codes.Unauthenticated, "")
	}

	festID := festIDFromResourceName(req.GetParent())
	if festID == "" {
		return nil, status.Error(codes.InvalidArgument, "no fest named")
	}
	team := strings.TrimSpace(req.GetFestEntry().GetFestTeam())
	if team == "" {
		// A vote for nothing is not a vote. Refused rather than stored, because
		// an empty team would show up in the tally as its own side.
		return nil, status.Error(codes.InvalidArgument, "no team chosen")
	}

	fest, err := s.loadFest(ctx, festID)
	if err != nil {
		return nil, err
	}

	phase := fest.PhaseAt(s.now())
	switch phase {
	case PhaseNone:
		// The fest has not been announced. Nobody should be able to see it, let
		// alone vote in it.
		return nil, status.Error(codes.NotFound, "no such fest")
	case PhaseAnnounced:
	default:
		existing, err := s.store.GetFestEntry(ctx, festID, userID)
		if err == nil {
			// Already voted, and play has started: the entry stands. Answering
			// with it rather than refusing, because a console re-sending its
			// choice on reconnect is normal and an error there reads as the
			// fest being broken.
			return s.festEntryProto(existing), nil
		}
		if !errors.Is(err, db.ErrNotFound) {
			s.log.Error("toyohr: load fest entry", "fest", festID, "error", err)
			return nil, status.Error(codes.Internal, "")
		}
		// No entry, and play has started. Splatoon 3 makes players pick before
		// a fest opens, so this is a console that did not — refused rather than
		// admitted late, because a vote arriving after a ratio was published
		// changes a number the players have already seen.
		s.log.Info("toyohr: refused a fest entry after play opened",
			"fest", festID, "user", userID, "phase", phase)
		return nil, status.Error(codes.FailedPrecondition, "the fest has already started")
	}

	region := strings.ToUpper(strings.TrimSpace(req.GetFestEntry().GetFestRegion()))
	entry := db.FestEntry{
		FestID: festID,
		UserID: userID,
		Team:   team,
		Region: region,
	}
	if err := s.store.UpsertFestEntry(ctx, entry); err != nil {
		s.log.Error("toyohr: save fest entry", "fest", festID, "error", err)
		return nil, status.Error(codes.Internal, "")
	}
	s.log.Info("toyohr: fest entry", "fest", festID, "user", userID,
		"team", team, "region", region)

	return s.festEntryProto(entry), nil
}

// GetFestEntry returns the caller's own entry.
//
// Only their own. A request naming another player's entry is answered with the
// caller's, not refused: the resource name is "…/fests/<id>/festEntry" with no
// user in it, so there is nothing to disagree with — and reading the caller
// from the token is what makes that safe.
func (s *Service) GetFestEntry(
	ctx context.Context, req *toyohrpb.GetFestEntryRequest,
) (*toyohrpb.FestEntry, error) {
	userID, ok := s.caller(ctx)
	if !ok {
		return nil, status.Error(codes.Unauthenticated, "")
	}

	festID := festIDFromResourceName(req.GetName())
	if festID == "" {
		return nil, status.Error(codes.InvalidArgument, "no fest named")
	}

	entry, err := s.store.GetFestEntry(ctx, festID, userID)
	if err != nil {
		if errors.Is(err, db.ErrNotFound) {
			return nil, status.Error(codes.NotFound, "no entry")
		}
		s.log.Error("toyohr: load fest entry", "fest", festID, "error", err)
		return nil, status.Error(codes.Internal, "")
	}
	return s.festEntryProto(entry), nil
}

// GetFestResult reports how a fest stands, or how it ended.
//
// # What is measured, and what is not
//
// The VOTE is real: every entry is a player who picked a side, and the ratio of
// entries per team is a number GRID0+ actually has. That fills the Yobisai
// result, which is the pre-fest vote.
//
// The BATTLE RATIOS are not. A real fest result carries per-category ratios
// derived from battles played, and nothing in GRID0+ records a battle yet —
// GameRecord is unimplemented. Those are reported with is_valid false, which is
// the protocol's own way of saying a section is not available, rather than
// filled with a plausible number. A fabricated ratio would be indistinguishable
// from a measured one to anybody reading it later, including a future version of
// this code.
//
// So the overall verdict is decided on the vote. **That is an editorial choice,
// not a stand-in**: this is one operator's server, a fest here is a fest
// GRID0+ built, and "the side more people picked wins" is a rule that can be
// stated to the players. When GameRecord lands, overallResult is the single
// function that changes.
func (s *Service) GetFestResult(
	ctx context.Context, req *toyohrpb.GetFestResultRequest,
) (*toyohrpb.FestResult, error) {
	if _, ok := s.caller(ctx); !ok {
		return nil, status.Error(codes.Unauthenticated, "")
	}

	festID := festIDFromResourceName(req.GetName())
	if festID == "" {
		// Nextendo's measured behavior for an unrecognized result resource is a
		// successful empty result carrying the requested name. The console uses
		// GetFestResult while reconciling old local fest state; a parse failure
		// must not turn that lookup into a hard RPC failure.
		return &toyohrpb.FestResult{Name: req.GetName()}, nil
	}

	fest, err := s.loadFest(ctx, festID)
	if err != nil {
		return nil, err
	}
	if phase := fest.PhaseAt(s.now()); phase == PhaseNone {
		return nil, status.Error(codes.NotFound, "no such fest")
	}

	entries, err := s.store.ListFestEntries(ctx, festID)
	if err != nil {
		s.log.Error("toyohr: list fest entries", "fest", festID, "error", err)
		return nil, status.Error(codes.Internal, "")
	}

	ratios := voteRatios(entries)
	phase := fest.PhaseAt(s.now())

	out := &toyohrpb.FestResult{
		// The result resource is in the caller's requested collection
		// ("festResults"), not beneath the "fests" resource used by entries.
		Name: req.GetName(),
		// The vote, which is measured. Valid as soon as anybody has entered:
		// an empty tally is a fest nobody joined, and reporting that as invalid
		// would be indistinguishable from the vote not being implemented.
		Yobisai: &toyohrpb.YobisaiResult{
			IsValid:    len(entries) > 0,
			TeamRatios: ratios,
		},
		// Battle ratios. Not measured -- see the doc comment.
		HonsaiMidterm: &toyohrpb.HonsaiMidtermResult{IsValid: false},
		HonsaiFinal:   &toyohrpb.HonsaiFinalResult{IsValid: false},
	}

	// The verdict only exists once play is over. Publishing one mid-fest would
	// have the game announce a winner while the fest is still open.
	if phase == PhaseCounting || phase == PhaseClosed {
		out.Overall = overallResult(entries)
	} else {
		out.Overall = &toyohrpb.OverallResult{IsValid: false}
	}
	return out, nil
}

// voteRatios turns entries into a share per team, largest share first.
//
// The shares sum to 1 across the teams anybody picked. Teams nobody picked are
// absent rather than present at zero: the team names live in the BCAT pack, so
// this layer does not know which teams a fest has and cannot list the empty
// ones without inventing them.
func voteRatios(entries []db.FestEntry) []*toyohrpb.FestTeamRatio {
	if len(entries) == 0 {
		return nil
	}

	counts := map[string]int{}
	for _, e := range entries {
		counts[e.Team]++
	}

	out := make([]*toyohrpb.FestTeamRatio, 0, len(counts))
	for team, n := range counts {
		out = append(out, &toyohrpb.FestTeamRatio{
			FestTeam: team,
			Ratio:    float64(n) / float64(len(entries)),
		})
	}
	// Largest first, team name as the tiebreak. Deterministic, because a map
	// range would reorder the result between two calls and the console has no
	// reason to expect a verdict to move.
	sort.Slice(out, func(i, j int) bool {
		if out[i].Ratio != out[j].Ratio {
			return out[i].Ratio > out[j].Ratio
		}
		return out[i].FestTeam < out[j].FestTeam
	})
	return out
}

// overallResult is the verdict: points per team, most first.
//
// One point per vote, so the side more people picked wins. **This is the single
// function that changes when GRID0+ starts recording battles** — see
// GetFestResult's doc comment for why it is a stated rule rather than a
// placeholder.
func overallResult(entries []db.FestEntry) *toyohrpb.OverallResult {
	if len(entries) == 0 {
		// Nobody entered, so there is no verdict to publish. Invalid rather
		// than a tie: a tie is a result, and this is the absence of one.
		return &toyohrpb.OverallResult{IsValid: false}
	}

	counts := map[string]int64{}
	for _, e := range entries {
		counts[e.Team]++
	}

	points := make([]*toyohrpb.FestTeamPoint, 0, len(counts))
	for team, n := range counts {
		points = append(points, &toyohrpb.FestTeamPoint{FestTeam: team, Points: n})
	}
	sort.Slice(points, func(i, j int) bool {
		if points[i].Points != points[j].Points {
			return points[i].Points > points[j].Points
		}
		return points[i].FestTeam < points[j].FestTeam
	})

	return &toyohrpb.OverallResult{IsValid: true, Points: points}
}

func (s *Service) festEntryProto(e db.FestEntry) *toyohrpb.FestEntry {
	return &toyohrpb.FestEntry{
		Name:       s.tenantPath() + "/fests/" + e.FestID + "/festEntry",
		FestTeam:   e.Team,
		FestRegion: e.Region,
	}
}

// loadFest reads a fest, mapping a miss onto NotFound.
func (s *Service) loadFest(ctx context.Context, festID string) (Fest, error) {
	row, err := s.store.GetFest(ctx, festID)
	if err != nil {
		if errors.Is(err, db.ErrNotFound) {
			return Fest{}, status.Error(codes.NotFound, "no such fest")
		}
		s.log.Error("toyohr: load fest", "fest", festID, "error", err)
		return Fest{}, status.Error(codes.Internal, "")
	}
	if !row.Enabled {
		// A scheduled-but-disabled fest is how the operator holds one ready
		// without announcing it. To a console it does not exist.
		return Fest{}, status.Error(codes.NotFound, "no such fest")
	}
	return festFromRow(row), nil
}
