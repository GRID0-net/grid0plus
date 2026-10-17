package toyohr

import (
	"sort"
	"strings"
	"time"

	"google.golang.org/protobuf/encoding/protowire"
	"google.golang.org/protobuf/proto"

	"github.com/n-popescu/grid0plus/internal/db"
	commonpb "github.com/n-popescu/grid0plus/proto/common"
	toyohrpb "github.com/n-popescu/grid0plus/proto/toyohr/v1"
)

// Final-result weights, in points by rank. The game receives both the totals
// and this map; the server, rather than the client, decides the winner.
//
// The captured map gives Yobisai (Conch Shells) 90/45/0. GRID0+ gives it
// nothing, on the operator's call: GRID0 LAN players never reach GRID0+, so
// their shells could never be counted, and a category only some players can
// score in would decide fests between the two networks. The other weights are
// the captured ones.
var festWeights = map[string][3]int64{
	"Yobisai": {0, 0, 0}, "PlayerCount": {70, 35, 0},
	"Regular": {120, 60, 0}, "Challenge": {120, 60, 0},
	"Tricolor": {180, 90, 0},
}

func festShares(votes []*toyohrpb.FestTeamRatio) map[string]float64 {
	out := make(map[string]float64, len(votes))
	for _, r := range votes {
		out[r.GetFestTeam()] = r.GetRatio()
	}
	return out
}

func rankedShares(shares map[string]float64) []*toyohrpb.FestTeamRatio {
	teams := make([]string, 0, len(shares))
	for team := range shares {
		teams = append(teams, team)
	}
	sort.Slice(teams, func(i, j int) bool {
		if shares[teams[i]] != shares[teams[j]] {
			return shares[teams[i]] > shares[teams[j]]
		}
		return teams[i] < teams[j]
	})
	out := make([]*toyohrpb.FestTeamRatio, 0, len(teams))
	for _, team := range teams {
		out = append(out, &toyohrpb.FestTeamRatio{FestTeam: team, Ratio: displayRatio(shares[team])})
	}
	return out
}

// maxDisplayRatio keeps a share below 100%. Nintendo's published shares are
// always near a third, shown as two digits and two decimals; with a handful
// of players GRID0+ can produce a whole 1.0, and the halftime report then
// showed 0% for every team (2026-10-09, hardware). The ranking is unchanged.
//
// TODO(capture-needed): that the display really overflows at 100 rather than
// rejecting the block for another reason. Change: displayRatio.
const maxDisplayRatio = 0.9999

func displayRatio(r float64) float64 { return min(r, maxDisplayRatio) }

func sharesFromTotals(totals map[string]int64, baseline map[string]float64) map[string]float64 {
	var sum int64
	for _, n := range totals {
		sum += n
	}
	if sum <= 0 {
		return baseline
	}
	out := make(map[string]float64, len(baseline))
	for team := range baseline {
		out[team] = float64(totals[team]) / float64(sum)
	}
	return out
}

// The midpoint is a snapshot at the fest's midpoint, not a live second-half
// tally. Prefer measured ink; use wins if no ink is reported, then the locked
// preliminary vote if no match completed. Later battles cannot change it.
func midpointShares(battles []db.FestBattle, mid time.Time, baseline map[string]float64) map[string]float64 {
	ink, wins := map[string]int64{}, map[string]int64{}
	for _, b := range battles {
		if !b.FinishedAt.Before(mid) {
			continue
		}
		wins[b.WinningTeam]++
		for team, points := range b.Ink {
			ink[team] += points
		}
	}
	if len(ink) > 0 {
		return sharesFromTotals(ink, baseline)
	}
	return sharesFromTotals(wins, baseline)
}

func categoryShares(battles []db.FestBattle, category string, baseline map[string]float64) map[string]float64 {
	counts := map[string]int64{}
	for _, b := range battles {
		mode := strings.ToLower(b.Mode)
		match := false
		switch category {
		case "Regular":
			match = strings.Contains(mode, "regular") && !strings.Contains(mode, "tricolor")
		case "Challenge":
			match = strings.Contains(mode, "challenge")
		case "Tricolor":
			match = strings.Contains(mode, "tricolor")
		}
		if match {
			counts[b.WinningTeam]++
		}
	}
	if len(counts) == 0 {
		// Nobody played this kind of battle: every team has none of it,
		// rather than the vote's shares standing in for games never played.
		none := make(map[string]float64, len(baseline))
		for team := range baseline {
			none[team] = 0
		}
		return none
	}
	return sharesFromTotals(counts, baseline)
}

func festPoints(ranked []*toyohrpb.FestTeamRatio, weights [3]int64, total map[string]int64) {
	// Tied teams share the best rank of their tie group: two teams tied for
	// second both get second place's points, as in Splatoon 3, where a tie for
	// second is possible. A stable alphabetic sort must not give the earlier
	// name more points.
	for i := 0; i < len(ranked); {
		end := i
		for end+1 < len(ranked) && ranked[end+1].GetRatio() == ranked[i].GetRatio() {
			end++
		}
		weight := int64(0)
		if i < len(weights) {
			weight = weights[i]
		}
		// A team with nothing in the category places nowhere: two teams at
		// 0% would otherwise tie for second and take its points for nothing.
		if ranked[i].GetRatio() <= 0 {
			weight = 0
		}
		for j := i; j <= end; j++ {
			total[ranked[j].GetFestTeam()] += weight
		}
		i = end + 1
	}
}

func anyShare(ranked []*toyohrpb.FestTeamRatio) bool {
	for _, r := range ranked {
		if r.GetRatio() > 0 {
			return true
		}
	}
	return false
}

// shownWeights is the points-per-place map sent with a result, from the
// captured weights. The game draws its results screen from each category's
// ranking and this map, giving each listed place its weight: with no Open
// games all three teams sit at 0%, and the screen gave the first-listed team
// Open's 120 points the server never counted (seen on hardware 2026-10-17).
// So a place held by a team at 0% is sent as worth 0, as festPoints scores it.
func shownWeights(rankings map[string][]*toyohrpb.FestTeamRatio) *commonpb.MapValue {
	fields := map[string]*commonpb.Value{}
	for category, values := range festWeights {
		ranked := rankings[category]
		for i, suffix := range []string{"WinPoint", "SecondPoint", "ThirdPoint"} {
			v := values[i]
			if i >= len(ranked) || ranked[i].GetRatio() <= 0 {
				v = 0
			}
			fields[category+suffix] = &commonpb.Value{ValueType: &commonpb.Value_IntegerValue{IntegerValue: v}}
		}
	}
	return &commonpb.MapValue{Fields: fields}
}

func finalFestResult(entries []db.FestEntry, battles []db.FestBattle, baseline map[string]float64, mid time.Time) (*toyohrpb.HonsaiMidtermResult, *toyohrpb.HonsaiFinalResult, *toyohrpb.OverallResult) {
	if len(entries) == 0 {
		return nil, nil, &toyohrpb.OverallResult{IsValid: false}
	}
	midpoint := &toyohrpb.HonsaiMidtermResult{IsValid: true, TeamRatios: rankedShares(midpointShares(battles, mid, baseline))}
	popular := rankedShares(baseline)
	regular := rankedShares(categoryShares(battles, "Regular", baseline))
	challenge := rankedShares(categoryShares(battles, "Challenge", baseline))
	tricolor := rankedShares(categoryShares(battles, "Tricolor", baseline))
	final := &toyohrpb.HonsaiFinalResult{
		IsValid: true, TeamRatios1: popular, TeamRatios2: regular, TeamRatios3: challenge,
	}
	// The captured fourth final ranking occupies protobuf field 5, absent from
	// this generated proto. Unknown fields preserve its wire position and type.
	for _, ratio := range tricolor {
		encoded, err := proto.Marshal(ratio)
		if err != nil {
			continue
		}
		unknown := protowire.AppendTag(final.ProtoReflect().GetUnknown(), 5, protowire.BytesType)
		unknown = protowire.AppendBytes(unknown, encoded)
		final.ProtoReflect().SetUnknown(unknown)
	}
	total := map[string]int64{}
	festPoints(rankedShares(baseline), festWeights["Yobisai"], total)
	festPoints(popular, festWeights["PlayerCount"], total)
	// A battle category nobody played gives nobody points: all teams tie at
	// zero, and a tie for first would otherwise hand every team first place.
	for _, c := range []struct {
		ranked []*toyohrpb.FestTeamRatio
		name   string
	}{{regular, "Regular"}, {challenge, "Challenge"}, {tricolor, "Tricolor"}} {
		if anyShare(c.ranked) {
			festPoints(c.ranked, festWeights[c.name], total)
		}
	}
	points := make([]*toyohrpb.FestTeamPoint, 0, len(total))
	for team, n := range total {
		points = append(points, &toyohrpb.FestTeamPoint{FestTeam: team, Points: n})
	}
	sort.Slice(points, func(i, j int) bool {
		if points[i].Points != points[j].Points {
			return points[i].Points > points[j].Points
		}
		return points[i].FestTeam < points[j].FestTeam
	})
	return midpoint, final, &toyohrpb.OverallResult{IsValid: true, Points: points, Weights: shownWeights(map[string][]*toyohrpb.FestTeamRatio{
		"Yobisai": rankedShares(baseline), "PlayerCount": popular,
		"Regular": regular, "Challenge": challenge, "Tricolor": tricolor,
	})}
}
