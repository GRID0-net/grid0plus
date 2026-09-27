package splatoon2

import (
	"fmt"
	"slices"
	"strconv"

	"github.com/n-popescu/grid0plus/internal/nex"
	"github.com/n-popescu/grid0plus/internal/rendezvous"
)

// Splatoon 2's Ranking methods (16-26): Splatfest, league battles, X Power
// and event matches. https://github.com/kinnay/NintendoClients/wiki/Ranking-Protocol-(Splatoon-2)
//
// Everything is kept as NEX records and computed on request. Two teams per
// Splatfest, as in the game.

const (
	kindFestVote    = "fest_vote"
	kindFestScore   = "fest_score"
	kindLeague      = "league"
	kindXPower      = "xpower"
	kindEventResult = "event_match_result"
)

func (s *Server) registerRanking() {
	const p, n = rendezvous.ProtocolRanking, "Ranking"
	s.Handle(p, n, 16, "UploadLeaguePoint", s.uploadLeaguePoint)
	s.Handle(p, n, 17, "GetLeagueResult", s.getLeagueResult)
	s.Handle(p, n, 18, "GetFestivalResult", s.getFestivalResult)
	s.Handle(p, n, 19, "UploadFestivalVote", s.uploadFestivalVote)
	s.Handle(p, n, 20, "UploadFestivalScore", s.uploadFestivalScore)
	s.Handle(p, n, 21, "DeleteFestival", s.deleteFestival)
	s.Handle(p, n, 22, "UploadXPower", s.storeRaw(kindXPower))
	s.Handle(p, n, 23, "GetXPowerRanking", s.getXPowerRanking)
	s.Handle(p, n, 24, "UploadEventMatchResult", s.storeRaw(kindEventResult))
	s.Handle(p, n, 25, "GetEventMatchResult", s.getEventMatchResult)
}

// ---- Splatfest ----------------------------------------------------------

type festVote struct {
	FestivalID uint32 `json:"festival_id"`
	Team       uint8  `json:"team"`
	PID        uint64 `json:"pid"`
}

type festScore struct {
	FestivalID uint32 `json:"festival_id"`
	Team       uint8  `json:"team"`
	PID        uint64 `json:"pid"`
	UniqueID   uint64 `json:"unique_id"`
	Region     uint8  `json:"region"`
	Total      uint64 `json:"total"`
	Battles    uint32 `json:"battles"`
	Wins       uint32 `json:"wins"`
}

func festKey(fid uint32, pid uint64) string { return fmt.Sprintf("%d:%d", fid, pid) }

// uploadFestivalVote: FestivalUploadVoteParam{u32 festivalId, u8 themeId}.
//
// Kinnay documents this as returning nothing. NextendoNetwork answered it
// with an empty list instead, reading it as a query it saw fired at the
// start of an 8-player fest match (facts only); the documented shape is
// used here, and both are answers the game accepted where NotImplemented
// was 2306-0103.
func (s *Server) uploadFestivalVote(c *nex.Conn, req *nex.Request) *nex.Response {
	r := nex.NewReader(req.Body)
	var v festVote
	r.Struct(func(r *nex.Reader, _ uint8) { v.FestivalID, v.Team = r.U32(), r.U8() })
	if r.Err() != nil {
		return req.Error(nex.ResultInvalidArgument)
	}
	v.PID = c.PID
	if err := s.Records().Put(kindFestVote, festKey(v.FestivalID, c.PID), rendezvous.UserIDForPID(c.PID), v); err != nil {
		return req.Error(nex.ResultNotImplemented)
	}
	s.log.Info("s2: splatfest vote", "pid", c.PID, "festival", v.FestivalID, "team", v.Team)
	return req.Success(nil)
}

// uploadFestivalScore: FestivalUploadScoreParam{u64 uniqueId, u8 region,
// u32 festivalId, u8 teamId, u32 score, u32 battleGatheringId, u32
// battleResult, qBuffer}. One per fest battle; accumulated per player.
func (s *Server) uploadFestivalScore(c *nex.Conn, req *nex.Request) *nex.Response {
	r := nex.NewReader(req.Body)
	var in festScore
	var score, result uint32
	r.Struct(func(r *nex.Reader, _ uint8) {
		in.UniqueID = r.U64()
		in.Region = r.U8()
		in.FestivalID = r.U32()
		in.Team = r.U8()
		score = r.U32()
		_ = r.U32() // battle gathering id
		result = r.U32()
		_ = r.QBuffer()
	})
	if r.Err() != nil {
		return req.Error(nex.ResultInvalidArgument)
	}
	key := festKey(in.FestivalID, c.PID)
	var cur festScore
	if _, err := s.Records().Get(kindFestScore, key, &cur); err != nil {
		cur = in
	}
	cur.PID, cur.Team, cur.UniqueID, cur.Region = c.PID, in.Team, in.UniqueID, in.Region
	cur.Total += uint64(score)
	cur.Battles++
	// TODO(capture-needed): what battleResult encodes. Read as 1 = win.
	if result == 1 {
		cur.Wins++
	}
	if err := s.Records().Put(kindFestScore, key, rendezvous.UserIDForPID(c.PID), cur); err != nil {
		return req.Error(nex.ResultNotImplemented)
	}
	return req.Success(nil)
}

// FestivalTally is a Splatfest's result so far.
type FestivalTally struct {
	FestivalID   uint32
	Participants [2]uint32 // votes per team
	Scores       [2]uint64 // uploaded score per team
	Battles      [2]uint32
	Wins         [2]uint32
}

// TallyFestival adds up a Splatfest's votes and scores.
func (s *Server) TallyFestival(fid uint32) FestivalTally {
	t := FestivalTally{FestivalID: fid}
	votes, _ := rendezvous.List[festVote](s.Records(), kindFestVote, "")
	for _, v := range votes {
		if v.FestivalID == fid && v.Team < 2 {
			t.Participants[v.Team]++
		}
	}
	scores, _ := rendezvous.List[festScore](s.Records(), kindFestScore, "")
	for _, sc := range scores {
		if sc.FestivalID == fid && sc.Team < 2 {
			t.Scores[sc.Team] += sc.Total
			t.Battles[sc.Team] += sc.Battles
			t.Wins[sc.Team] += sc.Wins
		}
	}
	return t
}

// getFestivalResult: u32 festivalId -> FestivalResult{u32 festivalId, u32
// numParticipants, List<u32> teamParticipants, List<u32> teamScores,
// DateTime updated}, revision 1 adding {u8, List<u64> team scores}.
//
// Written as revision 1: its extra fields sit inside the structure's own
// length, so a reader expecting revision 0 skips them, while one expecting
// revision 1 would fail on a revision-0 body.
//
// TODO(capture-needed): what a team's score is -- here, the sum of the
// scores its players uploaded -- and what the revision-1 byte means (sent as
// 0). A capture of a real result would settle both; this function is the one
// to change.
func (s *Server) getFestivalResult(c *nex.Conn, req *nex.Request) *nex.Response {
	fid := nex.NewReader(req.Body).U32()
	t := s.TallyFestival(fid)
	w := nex.NewWriter()
	w.Struct(1, func(w *nex.Writer) {
		w.U32(fid)
		w.U32(t.Participants[0] + t.Participants[1])
		nex.WriteList(w, t.Participants[:], (*nex.Writer).U32)
		nex.WriteList(w, []uint32{clamp32(t.Scores[0]), clamp32(t.Scores[1])}, (*nex.Writer).U32)
		w.DateTime(nex.NewDateTime(s.Now()))
		w.U8(0)
		nex.WriteList(w, t.Scores[:], (*nex.Writer).U64)
	})
	return req.Success(w.Bytes())
}

func clamp32(v uint64) uint32 {
	if v > 0xFFFFFFFF {
		return 0xFFFFFFFF
	}
	return uint32(v)
}

// deleteFestival: u32 festivalId. Removes the caller's own vote and scores
// for it -- never anyone else's.
func (s *Server) deleteFestival(c *nex.Conn, req *nex.Request) *nex.Response {
	fid := nex.NewReader(req.Body).U32()
	_ = s.Records().Delete(kindFestVote, festKey(fid, c.PID))
	_ = s.Records().Delete(kindFestScore, festKey(fid, c.PID))
	return req.Success(nil)
}

// ---- league battles -----------------------------------------------------

// LeaguePlayer is one member of a league team.
type LeaguePlayer struct {
	PID    uint64 `json:"pid"`
	Weapon int32  `json:"weapon"`
}

// LeagueEntry is a league team's best result in one league.
type LeagueEntry struct {
	TagID      uint64                  `json:"tag_id"`
	Region     uint8                   `json:"region"`
	LeagueID   string                  `json:"league_id"`
	Point      uint32                  `json:"point"`
	Members    map[uint64]LeaguePlayer `json:"members"`
	AppBuffer  []byte                  `json:"app_buffer"`
	MatchCount uint32                  `json:"match_count"`
}

func readLeaguePointInfo(r *nex.Reader) LeagueEntry {
	e := LeagueEntry{Members: map[uint64]LeaguePlayer{}}
	r.Struct(func(r *nex.Reader, _ uint8) {
		e.TagID = r.U64()
		e.Region = r.U8()
		e.LeagueID = r.String()
		e.Point = r.U32()
		n := r.U32()
		for i := uint32(0); i < n && r.Err() == nil && i < 16; i++ {
			k := r.U64()
			var p LeaguePlayer
			r.Struct(func(r *nex.Reader, _ uint8) { p.PID, p.Weapon = r.PID(), r.S32() })
			e.Members[k] = p
		}
		e.AppBuffer = r.QBuffer()
	})
	return e
}

func writeLeaguePointInfo(w *nex.Writer, e LeagueEntry) {
	w.Struct(0, func(w *nex.Writer) {
		w.U64(e.TagID)
		w.U8(e.Region)
		w.String(e.LeagueID)
		w.U32(e.Point)
		keys := make([]uint64, 0, len(e.Members))
		for k := range e.Members {
			keys = append(keys, k)
		}
		slices.Sort(keys)
		w.U32(uint32(len(keys)))
		for _, k := range keys {
			w.U64(k)
			m := e.Members[k]
			w.Struct(0, func(w *nex.Writer) { w.PID(m.PID); w.S32(m.Weapon) })
		}
		w.QBuffer(e.AppBuffer)
	})
}

func leagueKey(leagueID string, tag uint64) string {
	return leagueID + ":" + strconv.FormatUint(tag, 10)
}

// uploadLeaguePoint: LeaguePointInfo. A team's result keeps its best point.
func (s *Server) uploadLeaguePoint(c *nex.Conn, req *nex.Request) *nex.Response {
	r := nex.NewReader(req.Body)
	in := readLeaguePointInfo(r)
	if r.Err() != nil || in.LeagueID == "" {
		return req.Error(nex.ResultInvalidArgument)
	}
	key := leagueKey(in.LeagueID, in.TagID)
	var cur LeagueEntry
	if _, err := s.Records().Get(kindLeague, key, &cur); err == nil {
		in.MatchCount = cur.MatchCount
		if cur.Point > in.Point {
			in.Point = cur.Point
		}
	}
	in.MatchCount++
	if err := s.Records().Put(kindLeague, key, rendezvous.UserIDForPID(c.PID), in); err != nil {
		return req.Error(nex.ResultNotImplemented)
	}
	return req.Success(nil)
}

// LeagueStandings ranks one league's teams, best point first.
func (s *Server) LeagueStandings(leagueID string) []LeagueEntry {
	all, _ := rendezvous.List[LeagueEntry](s.Records(), kindLeague, "")
	var out []LeagueEntry
	for _, e := range all {
		if e.LeagueID == leagueID {
			out = append(out, e)
		}
	}
	slices.SortStableFunc(out, func(a, b LeagueEntry) int { return int(b.Point) - int(a.Point) })
	return out
}

// getLeagueResult: String leagueId | u64 tagId -> LeagueResult{LeaguePointInfo,
// u8 status, u32 specificRank, u8 rankRatio, u32 tagNum, u32 matchCount}, or
// Ranking::NotFound for a team with no result in that league.
//
// TODO(capture-needed): what status and rankRatio mean. Sent as 0 and as the
// team's percentile (1 = top). A capture of a real result settles both; this
// function is the one to change.
func (s *Server) getLeagueResult(c *nex.Conn, req *nex.Request) *nex.Response {
	r := nex.NewReader(req.Body)
	leagueID, tag := r.String(), r.U64()
	if r.Err() != nil {
		return req.Error(nex.ResultInvalidArgument)
	}
	standings := s.LeagueStandings(leagueID)
	idx := slices.IndexFunc(standings, func(e LeagueEntry) bool { return e.TagID == tag })
	if idx < 0 {
		return req.Error(nex.ResultRankingNotFound)
	}
	e := standings[idx]
	rank := uint32(idx + 1)
	w := nex.NewWriter()
	w.Struct(0, func(w *nex.Writer) {
		writeLeaguePointInfo(w, e)
		w.U8(0)
		w.U32(rank)
		w.U8(uint8(max(1, rank*100/uint32(len(standings)))))
		w.U32(uint32(len(standings)))
		w.U32(e.MatchCount)
	})
	return req.Success(w.Bytes())
}

// ---- X Power and event matches ------------------------------------------

type rawUpload struct {
	PID  uint64 `json:"pid"`
	Body []byte `json:"body"`
	At   int64  `json:"at"`
}

// storeRaw keeps an upload whose shape is not documented, whole, and
// acknowledges it.
//
// TODO(capture-needed): UploadXPower (22) and UploadEventMatchResult (24)
// have no documented request shape. They are read as uploads -- void, like
// every other Upload* method on this protocol -- and kept byte for byte, so
// that once their shape is known the history can be decoded rather than
// lost. A capture of either request settles its fields.
func (s *Server) storeRaw(kind string) rendezvous.Method {
	return func(c *nex.Conn, req *nex.Request) *nex.Response {
		key := fmt.Sprintf("%d:%d", c.PID, s.Now().UnixNano())
		if err := s.Records().Put(kind, key, rendezvous.UserIDForPID(c.PID),
			rawUpload{PID: c.PID, Body: req.Body, At: s.Now().Unix()}); err != nil {
			return req.Error(nex.ResultNotImplemented)
		}
		return req.Success(nil)
	}
}

// getXPowerRanking answers Ranking::NotFound -- the protocol's own "no
// ranking to show" -- until the response shape is known.
//
// TODO(capture-needed): GetXPowerRanking's request and response shapes.
func (s *Server) getXPowerRanking(c *nex.Conn, req *nex.Request) *nex.Response {
	return req.Error(nex.ResultRankingNotFound)
}

// getEventMatchResult answers an empty list: the game calls it about every
// six minutes while a Splatfest is running, and NextendoNetwork found an
// empty list accepted where NotImplemented raised 2306-0103 (facts only).
//
// TODO(capture-needed): the element type, and what a populated answer
// changes in the game.
func (s *Server) getEventMatchResult(c *nex.Conn, req *nex.Request) *nex.Response {
	w := nex.NewWriter()
	w.U32(0)
	return req.Success(w.Bytes())
}
