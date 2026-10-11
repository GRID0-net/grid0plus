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

// IssueMatchmakingIdToken mints the session tokens a squad leader asks for on
// behalf of the squad: one MatchedUserSession per user, the same entry
// JoinGameSession hands each player for itself.
//
// Fail-closed on identity, as everywhere else: every user named must be the
// caller, or a teammate who delegated to the caller with a token this server
// signed (squadMembers); and every one of them must already be a player of
// the session. Anything else refuses the whole request, rather than issuing a
// partial set -- the half-filled lobby matchedUserSessions' comment
// describes.
//
// TODO(capture-needed): when Splatoon 3 calls this. The game links the stub
// (GameSessionService ctor at 0x71005ec2d0, method at 0x71005f0164) but no
// hardware session has called it yet, and nothing shows whether it expects
// entries for include_id_token_users only or for every user. This answers
// include_id_token_users when given, otherwise every user. Change: this
// function.
func (g *gameSessionService) IssueMatchmakingIdToken(
	ctx context.Context, req *mmpb.IssueMatchmakingIdTokenRequest,
) (*mmpb.IssueMatchmakingIdTokenResponse, error) {
	caller, ok := CallerFrom(ctx)
	if !ok {
		return nil, ErrUnauthenticated
	}
	id := lastSegment(req.GetGameSession())
	sess, found := g.s.queue.Session(id)
	if !found {
		return nil, ErrNotFound
	}

	vouched := map[string]bool{caller.UserID: true}
	for _, m := range g.s.squadMembers(caller.UserID, req.GetUserDelegationTokens()) {
		vouched[m] = true
	}
	resolve := func(name string) string {
		u := lastSegment(name)
		if u == "" || u == "current" {
			return caller.UserID
		}
		return u
	}
	users := req.GetIncludeIdTokenUsers()
	if len(users) == 0 {
		users = req.GetUsers()
	}
	if len(users) == 0 {
		users = []string{caller.UserID}
	}

	var out []*mmpb.MatchedUserSession
	seen := map[string]bool{}
	for _, name := range users {
		u := resolve(name)
		if seen[u] {
			continue
		}
		seen[u] = true
		if !vouched[u] {
			g.s.log.Warn("npln: id token asked for a user who did not delegate to the caller",
				"caller", caller.UserID, "user", u, "session", id)
			return nil, ErrPermissionDenied
		}
		if !g.s.queue.IsPlayer(id, u) {
			g.s.log.Warn("npln: id token asked for a user outside the session",
				"caller", caller.UserID, "user", u, "session", id)
			return nil, ErrNotFound
		}
		m, err := g.s.matchedUserSessions(sess, u, nil)
		if err != nil {
			return nil, err
		}
		out = append(out, m...)
	}
	g.s.log.Info("npln: matchmaking id tokens issued", "caller", caller.UserID,
		"session", id, "users", len(out))
	return &mmpb.IssueMatchmakingIdTokenResponse{
		MatchedUserSessions: out,
		GameSession:         g.s.sessionProto(sess),
	}, nil
}
