package npln

import (
	"context"
	"time"

	"google.golang.org/protobuf/types/known/durationpb"

	mmpb "github.com/n-popescu/grid0plus/proto/matchmaking/v1"
)

// delegationTokenIssuer marks a delegation token. authenticate accepts only
// an access token's issuer, so this token can never act as a bearer: it names
// one player and is handed to another, and accepting it as a bearer would let
// the holder act as the player it names.
const delegationTokenIssuer = "npln-delegation"

// delegationTokenTTL is how long a delegation stays good. An hour, like a
// session token: long enough for a squad to queue together, no longer.
const delegationTokenTTL = time.Hour

// IssueUserDelegationToken lets the caller authorise a teammate to act for
// them in matchmaking -- the delegator names a mandatary, and the answer is a
// signed token saying so.
//
// Splatoon 3 links and calls it: NextendoNetwork/splatoon-3 records a real
// capture (2026-08-07; facts only, none of its code used) of a squad queuing
// together, where the request carries delegator "tenants/current/users/current",
// a concrete mandatary and match attributes, and the answer echoes them in a
// UserDelegationDetail with a signed token and a TTL. Answered Unimplemented,
// the squad's matchmaking failed outright.
//
// The delegator is always the authenticated caller, whatever the request
// names: a delegation is the caller vouching for itself, never for somebody
// else. The mandatary must be a real account.
//
// The leader's ticket then carries the token (squadMembers). The token is
// this server's own format; nothing shows the client reading inside it.
func (g *gameSessionService) IssueUserDelegationToken(
	ctx context.Context, req *mmpb.IssueUserDelegationTokenRequest,
) (*mmpb.IssueUserDelegationTokenResponse, error) {
	caller, ok := CallerFrom(ctx)
	if !ok {
		return nil, ErrUnauthenticated
	}

	if named := lastSegment(req.GetDelegatorUser()); named != "" && named != "current" && named != caller.UserID {
		g.s.log.Warn("npln: delegation naming somebody else as delegator",
			"caller", caller.UserID, "named", named)
		return nil, ErrPermissionDenied
	}
	mandatary := lastSegment(req.GetMandataryUser())
	if mandatary == "" || mandatary == "current" || mandatary == caller.UserID {
		return nil, errMalformed(g.s.env)
	}
	if u, err := g.s.store.GetUser(ctx, mandatary); err != nil || u.Deleted {
		return nil, ErrNotFound
	}

	now := g.s.now()
	token, err := g.s.issuer.Sign(nplnClaims{
		Subject:  caller.UserID,
		Issuer:   delegationTokenIssuer,
		Audience: mandatary,
		IssuedAt: now.Unix(),
		Expires:  now.Add(delegationTokenTTL).Unix(),
	})
	if err != nil {
		g.s.log.Error("npln: sign delegation token", "user", caller.UserID, "error", err)
		return nil, errInternal()
	}

	g.s.log.Info("npln: delegation issued", "delegator", caller.UserID, "mandatary", mandatary)
	return &mmpb.IssueUserDelegationTokenResponse{
		UserDelegationDetail: &mmpb.UserDelegationDetail{
			DelegatorUser:       g.s.userResourceName(caller.UserID),
			MandataryUser:       g.s.userResourceName(mandatary),
			Attributes:          req.GetAttributes(),
			DelegationActions:   req.GetDelegationActions(),
			UserDelegationToken: token,
			Ttl:                 durationpb.New(delegationTokenTTL),
		},
	}, nil
}

// squadMembers returns the players a squad ticket may queue together with
// leader: one per delegation token that this server signed, that is still
// valid, and that a member issued naming leader as its mandatary.
// IssueUserDelegationToken signs the member as subject and the leader as
// audience, and a member calls it before the leader creates the ticket
// (hardware, 2026-10-03: a Salmon Run room's guest delegated to its host,
// whose ticket then carried the token). Anything else is dropped rather than
// failing the ticket: the leader still queues, alone.
func (s *Server) squadMembers(leader string, tokens []string) []string {
	var out []string
	seen := map[string]bool{leader: true}
	for _, raw := range tokens {
		var c nplnClaims
		if err := s.tokens.Verify(raw, &c); err != nil {
			s.log.Warn("npln: squad ticket carried an invalid delegation", "leader", leader, "error", err)
			continue
		}
		if c.Issuer != delegationTokenIssuer || c.Audience != leader || c.Subject == "" || seen[c.Subject] {
			s.log.Warn("npln: squad ticket carried a delegation not made to its leader",
				"leader", leader, "delegator", c.Subject, "mandatary", c.Audience)
			continue
		}
		seen[c.Subject] = true
		out = append(out, c.Subject)
	}
	return out
}
