package rendezvous

import (
	"fmt"
	"slices"
	"strconv"
	"time"

	"github.com/n-popescu/grid0plus/internal/nex"
)

// Ranking (protocol 112): leaderboards. Scores are kept as records and
// ranked in Go on each request -- a private server's leaderboard is a few
// dozen rows.
// https://github.com/kinnay/NintendoClients/wiki/Ranking-Protocol
const ProtocolRanking = 0x70

// Record kinds.
const (
	KindRankingScore  = "ranking_score"
	KindRankingCommon = "ranking_common"
)

// RankingScore is one uploaded score.
type RankingScore struct {
	PID      uint64 `json:"pid"`
	UniqueID uint64 `json:"unique_id"`
	Category uint32 `json:"category"`
	Score    uint32 `json:"score"`
	OrderBy  uint8  `json:"order_by"`
	Groups   []byte `json:"groups"`
	Param    uint64 `json:"param"`
	At       int64  `json:"at"`
}

type rankingCommon struct {
	PID      uint64 `json:"pid"`
	UniqueID uint64 `json:"unique_id"`
	Data     []byte `json:"data"`
}

// Ranking modes (Pretendo's names).
const (
	rankRange       = 0
	rankNear        = 1
	rankFriendRange = 2
	rankFriendNear  = 3
	rankUser        = 4
)

// rankingSinceTime is 2000-01-01 00:00:00, which Pretendo records the real
// server sending as a RankingResult's sinceTime.
const rankingSinceTime nex.DateTime = 0x1F40420000

func (s *Server) registerRanking() {
	const p, n = ProtocolRanking, "Ranking"
	s.Handle(p, n, 1, "UploadScore", s.uploadScore)
	s.Handle(p, n, 2, "DeleteScore", s.deleteScore)
	s.Handle(p, n, 3, "DeleteAllScores", s.deleteAllScores)
	s.Handle(p, n, 4, "UploadCommonData", s.uploadCommonData)
	s.Handle(p, n, 5, "DeleteCommonData", s.deleteCommonData)
	s.Handle(p, n, 6, "GetCommonData", s.getCommonData)
	s.Handle(p, n, 7, "ChangeAttributes", s.changeAttributes(false))
	s.Handle(p, n, 8, "ChangeAllAttributes", s.changeAttributes(true))
	s.Handle(p, n, 9, "GetRanking", s.getRanking)
	s.Handle(p, n, 10, "GetApproxOrder", s.getApproxOrder)
	s.Handle(p, n, 11, "GetStats", s.getStats)
	s.Handle(p, n, 12, "GetRankingByPIDList", s.getRankingByList(true))
	s.Handle(p, n, 13, "GetRankingByUniqueIdList", s.getRankingByList(false))
	s.Handle(p, n, 14, "GetCachedTopXRanking", s.getCachedTopX)
	s.Handle(p, n, 15, "GetCachedTopXRankings", s.getCachedTopXs)
}

func scoreKey(category uint32, uniqueID, pid uint64) string {
	return fmt.Sprintf("%d:%d:%d", category, uniqueID, pid)
}

// uploadScore: RankingScoreData{u32 category, u32 score, u8 orderBy, u8
// updateMode, List<u8> groups, u64 param} | u64 uniqueId.
func (s *Server) uploadScore(c *nex.Conn, req *nex.Request) *nex.Response {
	r := nex.NewReader(req.Body)
	var sc RankingScore
	var updateMode uint8
	r.Struct(func(r *nex.Reader, _ uint8) {
		sc.Category = r.U32()
		sc.Score = r.U32()
		sc.OrderBy = r.U8()
		updateMode = r.U8()
		sc.Groups = nex.ReadList(r, (*nex.Reader).U8)
		sc.Param = r.U64()
	})
	sc.UniqueID = r.U64()
	if r.Err() != nil {
		return req.Error(nex.ResultInvalidArgument)
	}
	sc.PID, sc.At = c.PID, s.now().Unix()
	key := scoreKey(sc.Category, sc.UniqueID, c.PID)

	// TODO(capture-needed): the meaning of orderBy and updateMode. Read here
	// as: orderBy 0 ranks higher scores first, anything else lower first;
	// updateMode 0 keeps the better of the old and new score, anything else
	// always replaces. A capture of a real ranking upload and the ranking read
	// back would settle both; better() and this branch are the code to change.
	var old RankingScore
	if _, err := s.Records().Get(KindRankingScore, key, &old); err == nil && updateMode == 0 && !better(sc, old) {
		return req.Success(nil)
	}
	if err := s.Records().Put(KindRankingScore, key, UserIDForPID(c.PID), sc); err != nil {
		s.log.Error("nex: store score", "error", err)
		return req.Error(nex.ResultNotImplemented)
	}
	return req.Success(nil)
}

func better(a, b RankingScore) bool {
	if a.OrderBy == 0 {
		return a.Score > b.Score
	}
	return a.Score < b.Score
}

func (s *Server) myScores(pid uint64) []RankingScore {
	all, _ := List[RankingScore](s.Records(), KindRankingScore, UserIDForPID(pid))
	return all
}

// deleteScore: u32 category | u64 uniqueId.
func (s *Server) deleteScore(c *nex.Conn, req *nex.Request) *nex.Response {
	r := nex.NewReader(req.Body)
	category, uid := r.U32(), r.U64()
	if r.Err() != nil {
		return req.Error(nex.ResultInvalidArgument)
	}
	_ = s.Records().Delete(KindRankingScore, scoreKey(category, uid, c.PID))
	return req.Success(nil)
}

// deleteAllScores: u64 uniqueId.
func (s *Server) deleteAllScores(c *nex.Conn, req *nex.Request) *nex.Response {
	uid := nex.NewReader(req.Body).U64()
	for _, sc := range s.myScores(c.PID) {
		if sc.UniqueID == uid {
			_ = s.Records().Delete(KindRankingScore, scoreKey(sc.Category, sc.UniqueID, c.PID))
		}
	}
	return req.Success(nil)
}

func commonKey(uid, pid uint64) string {
	return strconv.FormatUint(uid, 10) + ":" + strconv.FormatUint(pid, 10)
}

// uploadCommonData: Buffer | u64 uniqueId. The per-player blob shown beside
// a ranking entry.
func (s *Server) uploadCommonData(c *nex.Conn, req *nex.Request) *nex.Response {
	r := nex.NewReader(req.Body)
	data, uid := r.Buffer(), r.U64()
	if r.Err() != nil {
		return req.Error(nex.ResultInvalidArgument)
	}
	if err := s.Records().Put(KindRankingCommon, commonKey(uid, c.PID), UserIDForPID(c.PID),
		rankingCommon{PID: c.PID, UniqueID: uid, Data: data}); err != nil {
		return req.Error(nex.ResultNotImplemented)
	}
	return req.Success(nil)
}

func (s *Server) deleteCommonData(c *nex.Conn, req *nex.Request) *nex.Response {
	uid := nex.NewReader(req.Body).U64()
	_ = s.Records().Delete(KindRankingCommon, commonKey(uid, c.PID))
	return req.Success(nil)
}

// getCommonData: u64 uniqueId -> Buffer, or Ranking::NotFound.
func (s *Server) getCommonData(c *nex.Conn, req *nex.Request) *nex.Response {
	uid := nex.NewReader(req.Body).U64()
	var cd rankingCommon
	if _, err := s.Records().Get(KindRankingCommon, commonKey(uid, c.PID), &cd); err != nil {
		return req.Error(nex.ResultRankingNotFound)
	}
	w := nex.NewWriter()
	w.Buffer(cd.Data)
	return req.Success(w.Bytes())
}

// changeAttributes: (7) u32 category | ChangeParam | u64 uniqueId,
// (8) ChangeParam | u64 uniqueId. ChangeParam{u8 flags, List<u8> groups, u64 param};
// flag 0x1 replaces the groups, 0x2 the param.
func (s *Server) changeAttributes(all bool) Method {
	return func(c *nex.Conn, req *nex.Request) *nex.Response {
		r := nex.NewReader(req.Body)
		var category uint32
		if !all {
			category = r.U32()
		}
		var flags uint8
		var groups []byte
		var param uint64
		r.Struct(func(r *nex.Reader, _ uint8) {
			flags = r.U8()
			groups = nex.ReadList(r, (*nex.Reader).U8)
			param = r.U64()
		})
		uid := r.U64()
		if r.Err() != nil {
			return req.Error(nex.ResultInvalidArgument)
		}
		for _, sc := range s.myScores(c.PID) {
			if sc.UniqueID != uid || (!all && sc.Category != category) {
				continue
			}
			if flags&0x1 != 0 {
				sc.Groups = groups
			}
			if flags&0x2 != 0 {
				sc.Param = param
			}
			_ = s.Records().Put(KindRankingScore, scoreKey(sc.Category, sc.UniqueID, sc.PID), UserIDForPID(sc.PID), sc)
		}
		return req.Success(nil)
	}
}

// orderParam is RankingOrderParam.
type orderParam struct {
	calc       uint8
	groupIndex uint8
	groupNum   uint8
	timeScope  uint8
	offset     uint32
	length     uint8
}

func readOrderParam(r *nex.Reader) orderParam {
	var o orderParam
	r.Struct(func(r *nex.Reader, _ uint8) {
		o.calc = r.U8()
		o.groupIndex = r.U8()
		o.groupNum = r.U8()
		o.timeScope = r.U8()
		o.offset = r.U32()
		o.length = r.U8()
	})
	return o
}

type ranked struct {
	RankingScore
	order uint32
}

// leaderboard ranks a category's scores. A group filter (groupIndex <
// len(groups)) keeps only scores whose group at that index is groupNum;
// 255 means no filter.
func (s *Server) leaderboard(category uint32, o orderParam) []ranked {
	all, _ := List[RankingScore](s.Records(), KindRankingScore, "")
	var in []RankingScore
	for _, sc := range all {
		if sc.Category != category {
			continue
		}
		if o.groupIndex != 255 && int(o.groupIndex) < 4 {
			if int(o.groupIndex) >= len(sc.Groups) || sc.Groups[o.groupIndex] != o.groupNum {
				continue
			}
		}
		in = append(in, sc)
	}
	slices.SortStableFunc(in, func(a, b RankingScore) int {
		switch {
		case better(a, b):
			return -1
		case better(b, a):
			return 1
		}
		return int(a.At - b.At)
	})
	out := make([]ranked, len(in))
	for i, sc := range in {
		order := uint32(i + 1)
		if o.calc == 0 && i > 0 && sc.Score == in[i-1].Score {
			order = out[i-1].order // standard competition ranking: 1224
		}
		out[i] = ranked{RankingScore: sc, order: order}
	}
	return out
}

func (s *Server) commonData(uid, pid uint64) []byte {
	var cd rankingCommon
	if _, err := s.Records().Get(KindRankingCommon, commonKey(uid, pid), &cd); err != nil {
		return nil
	}
	return cd.Data
}

func (s *Server) writeRankData(w *nex.Writer, e ranked) {
	w.Struct(0, func(w *nex.Writer) {
		w.PID(e.PID)
		w.U64(e.UniqueID)
		w.U32(e.order)
		w.U32(e.Category)
		w.U32(e.Score)
		nex.WriteList(w, e.Groups, (*nex.Writer).U8)
		w.U64(e.Param)
		w.Buffer(s.commonData(e.UniqueID, e.PID))
	})
}

func (s *Server) writeRankingResult(w *nex.Writer, list []ranked, total int) {
	w.Struct(0, func(w *nex.Writer) {
		nex.WriteList(w, list, s.writeRankData)
		w.U32(uint32(total))
		w.DateTime(rankingSinceTime)
	})
}

func window(board []ranked, offset uint32, length uint8) []ranked {
	if int(offset) >= len(board) {
		return nil
	}
	board = board[offset:]
	if length > 0 && int(length) < len(board) {
		board = board[:length]
	}
	return board
}

// near is the window of board around pid's entry.
func near(board []ranked, pid uint64, length uint8) []ranked {
	for i, e := range board {
		if e.PID == pid {
			half := int(length) / 2
			start := max(0, i-half)
			return window(board, uint32(start), length)
		}
	}
	return nil
}

// getRanking: u8 mode | u32 category | RankingOrderParam | u64 uniqueId |
// PID -> RankingResult, or Ranking::NotFound when there is nothing to show
// (what Pretendo records the real server answering).
func (s *Server) getRanking(c *nex.Conn, req *nex.Request) *nex.Response {
	r := nex.NewReader(req.Body)
	mode, category := r.U8(), r.U32()
	o := readOrderParam(r)
	_ = r.U64()
	pid := r.PID()
	if r.Err() != nil {
		return req.Error(nex.ResultInvalidArgument)
	}
	if pid == 0 {
		pid = c.PID
	}
	board := s.leaderboard(category, o)
	total := len(board)
	if mode == rankFriendRange || mode == rankFriendNear {
		friends := s.friendsOf(pid)
		board = slices.DeleteFunc(board, func(e ranked) bool { return e.PID != pid && !friends[e.PID] })
		total = len(board)
	}
	var list []ranked
	switch mode {
	case rankRange, rankFriendRange:
		list = window(board, o.offset, o.length)
	case rankNear, rankFriendNear:
		list = near(board, pid, o.length)
	case rankUser:
		list = near(board, pid, 1)
	default:
		return req.Error(nex.ResultInvalidArgument)
	}
	if len(list) == 0 {
		return req.Error(nex.ResultRankingNotFound)
	}
	w := nex.NewWriter()
	s.writeRankingResult(w, list, total)
	return req.Success(w.Bytes())
}

// getApproxOrder: u32 category | OrderParam | u32 score | u64 | PID -> u32:
// where a score would place.
func (s *Server) getApproxOrder(c *nex.Conn, req *nex.Request) *nex.Response {
	r := nex.NewReader(req.Body)
	category := r.U32()
	o := readOrderParam(r)
	score := r.U32()
	if r.Err() != nil {
		return req.Error(nex.ResultInvalidArgument)
	}
	order := uint32(1)
	for _, e := range s.leaderboard(category, o) {
		if better(e.RankingScore, RankingScore{Score: score, OrderBy: e.OrderBy}) {
			order++
		}
	}
	w := nex.NewWriter()
	w.U32(order)
	return req.Success(w.Bytes())
}

// getStats: u32 category | OrderParam | u32 flags -> RankingStats{List<double>}
// with five entries: count, sum, min, max, average.
func (s *Server) getStats(c *nex.Conn, req *nex.Request) *nex.Response {
	r := nex.NewReader(req.Body)
	category := r.U32()
	o := readOrderParam(r)
	flags := r.U32()
	if r.Err() != nil {
		return req.Error(nex.ResultInvalidArgument)
	}
	board := s.leaderboard(category, o)
	stats := make([]float64, 5)
	if len(board) > 0 {
		lo, hi, sum := float64(board[0].Score), float64(board[0].Score), 0.0
		for _, e := range board {
			v := float64(e.Score)
			sum += v
			lo, hi = min(lo, v), max(hi, v)
		}
		vals := []float64{float64(len(board)), sum, lo, hi, sum / float64(len(board))}
		for i := range stats {
			if flags&(1<<i) != 0 {
				stats[i] = vals[i]
			}
		}
	}
	w := nex.NewWriter()
	w.Struct(0, func(w *nex.Writer) { nex.WriteList(w, stats, (*nex.Writer).F64) })
	return req.Success(w.Bytes())
}

// getRankingByList: (12) List<PID> | u8 mode | u32 category | OrderParam | u64,
// (13) List<u64 uniqueId> | ... -> RankingResult.
func (s *Server) getRankingByList(byPID bool) Method {
	return func(c *nex.Conn, req *nex.Request) *nex.Response {
		r := nex.NewReader(req.Body)
		ids := nex.ReadList(r, (*nex.Reader).U64)
		_ = r.U8()
		category := r.U32()
		o := readOrderParam(r)
		_ = r.U64()
		if r.Err() != nil {
			return req.Error(nex.ResultInvalidArgument)
		}
		board := s.leaderboard(category, o)
		var list []ranked
		for _, e := range board {
			id := e.UniqueID
			if byPID {
				id = e.PID
			}
			if slices.Contains(ids, id) {
				list = append(list, e)
			}
		}
		if len(list) == 0 {
			return req.Error(nex.ResultRankingNotFound)
		}
		w := nex.NewWriter()
		s.writeRankingResult(w, list, len(board))
		return req.Success(w.Bytes())
	}
}

// cachedTopX writes RankingCachedResult: RankingResult, then {DateTime
// created, DateTime expires, u8 maxLength}.
func (s *Server) writeCachedTopX(w *nex.Writer, category uint32, o orderParam) {
	board := s.leaderboard(category, o)
	list := window(board, o.offset, o.length)
	s.writeRankingResult(w, list, len(board))
	now := s.now()
	w.Struct(0, func(w *nex.Writer) {
		w.DateTime(nex.NewDateTime(now))
		w.DateTime(nex.NewDateTime(now.Add(5 * time.Minute)))
		w.U8(o.length)
	})
}

func (s *Server) getCachedTopX(c *nex.Conn, req *nex.Request) *nex.Response {
	r := nex.NewReader(req.Body)
	category := r.U32()
	o := readOrderParam(r)
	if r.Err() != nil {
		return req.Error(nex.ResultInvalidArgument)
	}
	w := nex.NewWriter()
	s.writeCachedTopX(w, category, o)
	return req.Success(w.Bytes())
}

func (s *Server) getCachedTopXs(c *nex.Conn, req *nex.Request) *nex.Response {
	r := nex.NewReader(req.Body)
	cats := nex.ReadList(r, (*nex.Reader).U32)
	params := nex.ReadList(r, readOrderParam)
	if r.Err() != nil || len(cats) != len(params) {
		return req.Error(nex.ResultInvalidArgument)
	}
	w := nex.NewWriter()
	w.U32(uint32(len(cats)))
	for i := range cats {
		s.writeCachedTopX(w, cats[i], params[i])
	}
	return req.Success(w.Bytes())
}
