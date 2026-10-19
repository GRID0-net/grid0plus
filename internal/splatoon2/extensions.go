package splatoon2

import (
	"context"
	"encoding/binary"
	"fmt"
	"hash/fnv"
	"slices"
	"strconv"
	"time"

	"github.com/n-popescu/grid0plus/internal/db"
	"github.com/n-popescu/grid0plus/internal/nex"
	"github.com/n-popescu/grid0plus/internal/rendezvous"
)

// Splatoon 2's additions to Utility and MatchmakeExtension, and what it
// records when a match starts.

const kindCurrentUser = "current_user"

func (s *Server) registerExtensions() {
	s.Handle(rendezvous.ProtocolUtility, "Utility", 9, "AcquireTagId", s.acquireTagID)
	s.Handle(rendezvous.ProtocolUtility, "Utility", 10, "UpdateCurrentUser", s.updateCurrentUser)
	s.Handle(rendezvous.ProtocolMatchmakeExtension, "MatchmakeExtension", 60,
		"CustomGetSimplePlayingSession", s.customGetSimplePlayingSession)
	s.Matchmaker().OnMatchStart(s.recordMatch)
}

// TagID is a league team's id: the same for the same members every time,
// whatever order they are listed in, so a team's results follow it from
// one session to the next without the server storing anything. FNV-1a over
// the sorted unique ids, kept positive and non-zero.
func TagID(uniqueIDs []uint64) uint64 {
	ids := slices.Clone(uniqueIDs)
	slices.Sort(ids)
	ids = slices.Compact(ids)
	h := fnv.New64a()
	var b [8]byte
	for _, id := range ids {
		binary.LittleEndian.PutUint64(b[:], id)
		h.Write(b[:])
	}
	return h.Sum64()&0x7FFF_FFFF_FFFF_FFFF | 1
}

// acquireTagID: List<u64> uniqueIds -> u64 tagId (Kinnay, Utility Protocol
// (Splatoon 2)). The id a league team's results are filed under.
func (s *Server) acquireTagID(c *nex.Conn, req *nex.Request) *nex.Response {
	r := nex.NewReader(req.Body)
	ids := nex.ReadList(r, (*nex.Reader).U64)
	if r.Err() != nil || len(ids) == 0 {
		return req.Error(nex.ResultInvalidArgument)
	}
	w := nex.NewWriter()
	w.U64(TagID(ids))
	return req.Success(w.Bytes())
}

type currentUser struct {
	UniqueID uint64 `json:"unique_id"`
	Region   uint8  `json:"region"`
	At       int64  `json:"at"`
}

// updateCurrentUser: UpdateCurrentUserParam{UniqueIdInfo, u8 region} -> nothing.
//
// Its answer gates Splatoon 2 going network-ready (AcquireNexUniqueID ->
// UpdateCurrentUser -> network ready), and the answer is an EMPTY success:
// Nintendo's reply is zero bytes.
//
// The unique id comes from the game's save data, so it may be one this
// server never handed out -- a save from before GRID0+. Such an id is
// adopted for the caller. One that belongs to someone else is not, and is
// logged; the call still succeeds, because failing it would only stop the
// player going online, and nothing is filed under an id nobody owns.
func (s *Server) updateCurrentUser(c *nex.Conn, req *nex.Request) *nex.Response {
	r := nex.NewReader(req.Body)
	var u rendezvous.UniqueID
	var region uint8
	r.Struct(func(r *nex.Reader, _ uint8) {
		u = rendezvous.ReadUniqueIDInfo(r)
		region = r.U8()
	})
	if r.Err() != nil {
		return req.Error(nex.ResultInvalidArgument)
	}
	if u.ID != 0 {
		if err := s.ClaimUniqueID(c.PID, u); err != nil {
			s.adoptUniqueID(c.PID, u)
		}
	}
	_ = s.Records().Put(kindCurrentUser, strconv.FormatUint(c.PID, 10), rendezvous.UserIDForPID(c.PID),
		currentUser{UniqueID: u.ID, Region: region, At: s.Now().Unix()})
	return req.Success(nil)
}

func (s *Server) adoptUniqueID(pid uint64, u rendezvous.UniqueID) {
	var existing rendezvous.UniqueID
	key := strconv.FormatUint(u.ID, 10)
	if rec, err := s.Records().Get(rendezvous.KindUniqueID, key, &existing); err == nil {
		if rec.Owner != rendezvous.UserIDForPID(pid) {
			s.log.Warn("s2: player presented another player's unique id", "pid", pid, "unique_id", u.ID)
		}
		return
	}
	u.Associated = true
	if err := s.Records().Put(rendezvous.KindUniqueID, key, rendezvous.UserIDForPID(pid), u); err != nil {
		s.log.Error("s2: adopt unique id", "pid", pid, "error", err)
	}
}

// customGetSimplePlayingSession: List<PID> | u8 | u8 -> List<SimplePlayingSession>.
// The shape Kinnay documents for Mario Kart 8 Deluxe's method 60; Splatoon 2
// uses the same id. Which lobby each of the caller's friends is in: how a
// friend-join finds its target.
//
// An empty list is what this answers whenever none of the caller's friends is in
// a lobby; when one is, the lobby is named, which is what makes joining a friend
// possible.
func (s *Server) customGetSimplePlayingSession(c *nex.Conn, req *nex.Request) *nex.Response {
	r := nex.NewReader(req.Body)
	pids := nex.ReadList(r, (*nex.Reader).PID)
	_, _ = r.U8(), r.U8()
	if r.Err() != nil {
		return req.Error(nex.ResultInvalidArgument)
	}
	w := nex.NewWriter()
	rendezvous.WriteSimplePlayingSessions(w, s.Matchmaker().SimplePlayingSessions(c.PID, pids, false))
	return req.Success(w.Bytes())
}

// recordMatch files a lobby that became a match in GRID0+'s match
// history, beside Splatoon 3's: the dashboard shows both.
func (s *Server) recordMatch(g rendezvous.GatheringInfo) {
	if s.store == nil {
		return
	}
	players := make([]string, 0, len(g.Participants))
	for _, p := range g.Participants {
		players = append(players, rendezvous.UserIDForPID(p))
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	m := db.Match{
		ID:         fmt.Sprintf("s2-%d-%d", g.ID, s.Now().Unix()),
		Tenant:     Tenant,
		Mode:       GameModeName(g.GameMode),
		HostUserID: rendezvous.UserIDForPID(g.Host),
		PlayerIDs:  players,
		CreatedAt:  s.Now().UTC(),
	}
	if err := s.store.CreateMatch(ctx, m); err != nil {
		s.log.Error("s2: record match", "gid", g.ID, "error", err)
	}
}

// Tenant is what Splatoon 2's matches are filed under in the match history,
// in the column Splatoon 3's NPLN tenant id uses.
const Tenant = "splatoon2"

// GameModeName names a MatchmakeSession game mode for people.
//
// UNCONFIRMED: Splatoon 2's game-mode numbering. Nothing public
// maps it, so the number is shown as-is; a capture of each mode's
// AutoMatchmake (or RE of the game's matchmaking setup) would settle it,
// and this is the one function to change.
func GameModeName(mode uint32) string { return "mode " + strconv.FormatUint(uint64(mode), 10) }
