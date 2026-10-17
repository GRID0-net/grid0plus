package toyohr

import (
	"context"
	"errors"
	"sort"
	"strings"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/n-popescu/grid0plus/internal/db"
	commonpb "github.com/n-popescu/grid0plus/proto/common"
	toyohrpb "github.com/n-popescu/grid0plus/proto/toyohr/v1"
)

// CreateFestEntry records the team a player picked.
//
// The player is the AUTHENTICATED caller. FestEntry carries a name field the
// client fills in, and taking the player from it would let any console vote as
// anybody — the shape of the bug this repository is built around.
//
// The first pick is final, as it is in Splatoon 3 itself: once a team is
// chosen -- from this console, another one on the same account, or the
// Discord bot's vote post -- a later choice gets that entry back rather than
// replacing it. Back, not refused: a refusal is a communication error, and a
// console re-sending its choice on reconnect is normal. A player with no
// entry can still pick one while play is open, as the game expects; only
// after play ends is a new entry refused.
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

	region := strings.ToUpper(strings.TrimSpace(req.GetFestEntry().GetFestRegion()))
	phase := fest.PhaseAt(s.now())
	switch phase {
	case PhaseNone:
		// The fest has not been announced. Nobody should be able to see it, let
		// alone vote in it.
		return nil, status.Error(codes.NotFound, "no such fest")
	case PhaseAnnounced, PhaseFirstHalf, PhaseSecondHalf:
		// Splatoon 3 lets a player who skipped the announcement pick a team
		// when they first open the game during the fest, and Nextendo's server
		// accepts entries through both halves; a refusal here is a
		// communication error that locks them out of the whole fest. The late
		// vote does move the vote ratio, which is the lesser harm.
	default:
		existing, err := s.store.GetFestEntry(ctx, festID, userID)
		if err == nil {
			return s.festEntryProto(existing), nil
		}
		if !errors.Is(err, db.ErrNotFound) {
			s.log.Error("toyohr: load fest entry", "fest", festID, "error", err)
			return nil, status.Error(codes.Internal, "")
		}
		s.log.Info("toyohr: refused a fest entry after play ended",
			"fest", festID, "user", userID, "phase", phase)
		return nil, status.Error(codes.FailedPrecondition, "the fest is over")
	}

	entry, created, err := s.store.CreateFestEntry(ctx, db.FestEntry{
		FestID: festID, UserID: userID, Team: team, Region: region,
	})
	if err != nil {
		s.log.Error("toyohr: save fest entry", "fest", festID, "error", err)
		return nil, status.Error(codes.Internal, "")
	}
	if !created {
		if entry.Team != team {
			s.log.Info("toyohr: kept a player's first team", "fest", festID, "user", userID,
				"team", entry.Team, "asked", team)
		} else if entry.Region == "" && region != "" {
			// Picked from the Discord bot, which knows no region; the console
			// confirming the same team is where the region comes from.
			entry.Region = region
			if err := s.store.UpsertFestEntry(ctx, entry); err != nil {
				s.log.Error("toyohr: save fest entry region", "fest", festID, "error", err)
				return nil, status.Error(codes.Internal, "")
			}
		}
		return s.festEntryProto(entry), nil
	}
	s.log.Info("toyohr: fest entry", "fest", festID, "user", userID,
		"team", team, "region", region)
	if s.onEntry != nil {
		s.onEntry(ctx, entry)
	}
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
// Completed gamesync referee reports now supply wins by mode and paint points.
// The midpoint uses those measured paint points before the midpoint, falling
// back to wins and then the locked vote if no usable reports exist. The final
// ranks each battle category from recorded wins, with the vote as the stated
// fallback for an unplayed category.
//
// Nintendo's precise clout formula is unknown; these are GRID0+'s explicit
// scoring rules, while the point weights and wire structure come from capture.
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
	battles, err := s.store.ListFestBattles(ctx, festID)
	if err != nil {
		s.log.Error("toyohr: list fest battles", "fest", festID, "error", err)
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
	}

	// The verdict only exists once play is over. Publishing one mid-fest would
	// have the game announce a winner while the fest is still open.
	if phase == PhaseClosed {
		out.HonsaiMidterm, out.HonsaiFinal, out.Overall = finalFestResult(entries, battles, festShares(ratios), fest.Timetable().Mid)
		out.Unk = int32(len(ratios))
	} else if phase == PhaseFirstHalf || phase == PhaseSecondHalf || phase == PhaseCounting {
		// The live-fest capture omits the unplayed phases and carries an
		// overall envelope with an empty weights map. A present-but-invalid
		// midterm/final has a different wire shape and can redirect the game's
		// result state machine before it loads the fest news.
		out.Overall = &toyohrpb.OverallResult{Weights: &commonpb.MapValue{}}
		out.Unk = int32(len(ratios))
		if phase != PhaseFirstHalf && len(entries) > 0 {
			// The captured second half has a valid midpoint, ranked by share,
			// and no final result. Keep the canonical Yobisai order above and
			// rank a separate midpoint snapshot from pre-midpoint reports.
			midterm := rankedShares(midpointShares(battles, fest.Timetable().Mid, festShares(ratios)))
			out.HonsaiMidterm = &toyohrpb.HonsaiMidtermResult{
				IsValid:    true,
				TeamRatios: midterm,
			}
		}
	} else {
		out.HonsaiMidterm = &toyohrpb.HonsaiMidtermResult{IsValid: false}
		out.HonsaiFinal = &toyohrpb.HonsaiFinalResult{IsValid: false}
		out.Overall = &toyohrpb.OverallResult{IsValid: false}
	}
	return out, nil
}

// voteRatios turns entries into a share per team.
//
// S3's three-team live-fest response includes Alpha, Bravo and Charlie even
// when a side has no votes. These are protocol-side identifiers rather than
// the localized team names in the BCAT pack. Keep any noncanonical side a
// client submitted too, so its measured votes are not silently discarded.
func voteRatios(entries []db.FestEntry) []*toyohrpb.FestTeamRatio {
	if len(entries) == 0 {
		return nil
	}

	counts := map[string]int{}
	for _, e := range entries {
		counts[e.Team]++
	}

	teams := []string{"Alpha", "Bravo", "Charlie"}
	for team := range counts {
		if team != "Alpha" && team != "Bravo" && team != "Charlie" {
			teams = append(teams, team)
		}
	}
	if len(teams) > 3 {
		sort.Strings(teams[3:])
	}
	out := make([]*toyohrpb.FestTeamRatio, 0, len(teams))
	for _, team := range teams {
		out = append(out, &toyohrpb.FestTeamRatio{
			FestTeam: team,
			Ratio:    displayRatio(float64(counts[team]) / float64(len(entries))),
		})
	}
	return out
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
