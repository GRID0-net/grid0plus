package splatoon2

import (
	"crypto/rand"
	"encoding/binary"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"

	"github.com/n-popescu/grid0plus/internal/nex"
	"github.com/n-popescu/grid0plus/internal/rendezvous"
)

// DataStore (protocol 115): Splatoon 2's match results, Annie's gear orders,
// Splatfest plaza posts ("Coconut") and play logs.
// https://github.com/kinnay/NintendoClients/wiki/Data-Store-Protocol-(Splatoon-2)
//
// This is the part NextendoNetwork's server leaves as a stub -- every method
// answered with an empty success, which its README says is what keeps
// matchmaking from being complete (facts only). Here the methods Kinnay
// documents are answered in their documented shapes and what they upload is
// kept; the empty success remains only as the fallback for the methods
// nobody has documented, because that is the answer the game is known to
// accept during its online bring-up.
const ProtocolDataStore = 0x73

const (
	kindMatchResult = "match_result"
	kindGearOrder   = "gear_order"
	kindTimeAttack  = "time_attack"
	kindCoconut     = "coconut_post"
	kindPlayLog     = "play_log"
)

func (s *Server) registerDataStore() {
	const p, n = ProtocolDataStore, "DataStore"
	for id, name := range map[uint32]string{
		51: "UploadRegularMatchResult", 52: "UploadGachiMatchResult",
		53: "UploadLeagueMatchResult", 54: "UploadFesMatchResult",
		59: "UploadFesMatchResultV2", 60: "UploadXMatchResult",
		61: "UploadRegularMatchResultV2", 62: "UploadGachiMatchResultV2",
		63: "UploadLeagueMatchResultV2", 64: "UploadFesMatchResultV3",
		65: "UploadXMatchResultV2", 68: "UploadFesMatchResultV4",
		69: "UploadCoopResult",
	} {
		s.Handle(p, n, id, name, s.uploadMatchResult(name))
	}
	s.Handle(p, n, 47, "CoconutRegisterMeta", s.coconutRegister(false))
	s.Handle(p, n, 48, "CoconutRatePost", s.coconutRate)
	s.Handle(p, n, 49, "CoconutGetObjectInfos", s.coconutGet)
	s.Handle(p, n, 50, "CoconutReportViolation", s.coconutReport)
	s.Handle(p, n, 55, "GetOrderedGear", s.getOrderedGear)
	s.Handle(p, n, 56, "PurchaseGear", s.purchaseGear)
	s.Handle(p, n, 57, "UploadTimeAttack", s.uploadTimeAttack)
	s.Handle(p, n, 58, "CoconutRegisterMetaByParam", s.coconutRegister(true))
	s.Handle(p, n, 66, "PreparePostPlayLog", s.preparePostPlayLog)
	s.Handle(p, n, 67, "PrepareGetPlayLog", s.prepareGetPlayLog)
	s.HandleFallback(p, n, s.dataStoreFallback)
}

// dataStoreFallback answers an undocumented DataStore method with an empty
// success, logged so that it can be implemented properly once a capture
// shows what it is. NextendoNetwork's server answers every DataStore method
// this way and Splatoon 2 proceeds through its bring-up (facts only);
// NotImplemented is not known to be survivable.
func (s *Server) dataStoreFallback(c *nex.Conn, req *nex.Request) *nex.Response {
	s.log.Warn("s2: DataStore method answered with the empty-success fallback",
		"pid", c.PID, "method", req.Method, "body_len", len(req.Body))
	return req.Success(nil)
}

func uint64Key(v uint64) string { return strconv.FormatUint(v, 10) }

func newDataID() uint64 {
	var b [8]byte
	_, _ = rand.Read(b[:])
	return binary.LittleEndian.Uint64(b[:])&0x7FFF_FFFF_FFFF_FFFF | 1
}

// ---- match results -------------------------------------------------------

// MatchResult is one uploaded result, with the few fields every Calico
// stats structure starts with decoded for the dashboard. The body is kept
// whole: the V2+ structures are not documented past that common start.
type MatchResult struct {
	PID      uint64 `json:"pid"`
	Method   string `json:"method"`
	GameMode uint32 `json:"game_mode"`
	Rule     int32  `json:"rule"`
	Result   uint8  `json:"result"`
	Stage    int32  `json:"stage"`
	At       int64  `json:"at"`
	Body     []byte `json:"body"`
}

// uploadMatchResult keeps a result. Every variant is a Calico*Stats
// structure whose first level is CalicoStatsBase: u32 gameMode, s32 rule,
// u8 result, s32 stage, ... (UploadCoopResult is read the same way; a
// field that does not decode is simply left zero).
func (s *Server) uploadMatchResult(method string) rendezvous.Method {
	return func(c *nex.Conn, req *nex.Request) *nex.Response {
		res := MatchResult{PID: c.PID, Method: method, At: s.Now().Unix(), Body: req.Body}
		r := nex.NewReader(req.Body)
		r.Struct(func(r *nex.Reader, _ uint8) {
			res.GameMode, res.Rule, res.Result, res.Stage = r.U32(), r.S32(), r.U8(), r.S32()
		})
		key := fmt.Sprintf("%d:%d", c.PID, s.Now().UnixNano())
		if err := s.Records().Put(kindMatchResult, key, rendezvous.UserIDForPID(c.PID), res); err != nil {
			return req.Error(nex.ResultNotImplemented)
		}
		return req.Success(nil)
	}
}

// RecentResults are the latest uploaded match results, newest first.
func (s *Server) RecentResults(limit int) []MatchResult {
	all, _ := rendezvous.List[MatchResult](s.Records(), kindMatchResult, "")
	if len(all) > limit {
		all = all[:limit]
	}
	return all
}

// ---- Annie's gear orders --------------------------------------------------

// GearOrder is OrderedInfo: a piece of gear ordered for a player. On
// Nintendo's service an order came from the SplatNet 2 app; here the
// operator places it from the dashboard.
type GearOrder struct {
	Kind  int32 `json:"kind"`
	ID    int32 `json:"id"`
	Skill int32 `json:"skill"`
	Price int32 `json:"price"`
}

func writeOrderedInfo(w *nex.Writer, o GearOrder) {
	w.Struct(0, func(w *nex.Writer) { w.S32(o.Kind); w.S32(o.ID); w.S32(o.Skill); w.S32(o.Price) })
}

// PlaceGearOrder orders gear for a player, replacing any open order.
func (s *Server) PlaceGearOrder(pid uint64, o GearOrder) error {
	return s.Records().Put(kindGearOrder, uint64Key(pid), rendezvous.UserIDForPID(pid), o)
}

// GearOrderOf returns a player's open order.
func (s *Server) GearOrderOf(pid uint64) (GearOrder, bool) {
	var o GearOrder
	_, err := s.Records().Get(kindGearOrder, strconv.FormatUint(pid, 10), &o)
	return o, err == nil
}

// getOrderedGear: no parameters -> OrderedInfo, or DataStore::NotFound when
// nothing is on order.
//
// TODO(capture-needed): how Nintendo answered "nothing ordered". NotFound is
// this protocol's documented "nothing here"; a capture of a player with no
// order would settle it, and this function is the one to change.
func (s *Server) getOrderedGear(c *nex.Conn, req *nex.Request) *nex.Response {
	o, ok := s.GearOrderOf(c.PID)
	if !ok {
		return req.Error(nex.ResultDataStoreNotFound)
	}
	w := nex.NewWriter()
	writeOrderedInfo(w, o)
	return req.Success(w.Bytes())
}

// purchaseGear: OrderedInfo. The player picked up their order in the game.
func (s *Server) purchaseGear(c *nex.Conn, req *nex.Request) *nex.Response {
	_ = s.Records().Delete(kindGearOrder, strconv.FormatUint(c.PID, 10))
	s.log.Info("s2: gear order collected", "pid", c.PID)
	return req.Success(nil)
}

// uploadTimeAttack: TimeAttackInfo (Octo Expansion clear times). Kept whole,
// one per player.
func (s *Server) uploadTimeAttack(c *nex.Conn, req *nex.Request) *nex.Response {
	if err := s.Records().Put(kindTimeAttack, strconv.FormatUint(c.PID, 10), rendezvous.UserIDForPID(c.PID),
		rawUpload{PID: c.PID, Body: req.Body, At: s.Now().Unix()}); err != nil {
		return req.Error(nex.ResultNotImplemented)
	}
	return req.Success(nil)
}

// ---- Splatfest plaza posts ("Coconut") -------------------------------------

// CoconutPost is a post a player shared from the plaza during a Splatfest.
type CoconutPost struct {
	DataID     uint64 `json:"data_id"`
	TweetID    uint64 `json:"tweet_id"`
	Region     uint8  `json:"region"`
	PostType   uint8  `json:"post_type"`
	Theme      uint8  `json:"theme"`
	FestivalID uint32 `json:"festival_id"`
	Language   string `json:"language"`
	Binary     []byte `json:"binary"`
	PID        uint64 `json:"pid"`
	UniqueID   uint64 `json:"unique_id"`
	URL        string `json:"url"`
	Rating     uint32 `json:"rating"`
	Hidden     bool   `json:"hidden"`
}

func readCoconutMeta(r *nex.Reader) CoconutPost {
	var p CoconutPost
	r.Struct(func(r *nex.Reader, _ uint8) {
		p.DataID = r.U64()
		p.TweetID = r.U64()
		p.Region = r.U8()
		p.PostType = r.U8()
		p.Theme = r.U8()
		p.FestivalID = r.U32()
		p.Language = r.String()
		p.Binary = r.QBuffer()
	})
	return p
}

func writeCoconutMeta(w *nex.Writer, p CoconutPost) {
	w.Struct(0, func(w *nex.Writer) {
		w.U64(p.DataID)
		w.U64(p.TweetID)
		w.U8(p.Region)
		w.U8(p.PostType)
		w.U8(p.Theme)
		w.U32(p.FestivalID)
		w.String(p.Language)
		w.QBuffer(p.Binary)
	})
}

// coconutRegister: (47) CoconutMeta | String url, (58) CoconutMeta | String
// snsName | String postId. The server assigns the data id when the console
// does not.
func (s *Server) coconutRegister(byParam bool) rendezvous.Method {
	return func(c *nex.Conn, req *nex.Request) *nex.Response {
		r := nex.NewReader(req.Body)
		p := readCoconutMeta(r)
		if byParam {
			sns, post := r.String(), r.String()
			p.URL = sns + ":" + post
		} else {
			p.URL = r.String()
		}
		if r.Err() != nil {
			return req.Error(nex.ResultInvalidArgument)
		}
		if p.DataID == 0 {
			p.DataID = newDataID()
		}
		p.PID = c.PID
		if cu, ok := s.currentUser(c.PID); ok {
			p.UniqueID = cu.UniqueID
		}
		if err := s.Records().Put(kindCoconut, strconv.FormatUint(p.DataID, 10), rendezvous.UserIDForPID(c.PID), p); err != nil {
			return req.Error(nex.ResultNotImplemented)
		}
		return req.Success(nil)
	}
}

func (s *Server) currentUser(pid uint64) (currentUser, bool) {
	var cu currentUser
	_, err := s.Records().Get(kindCurrentUser, strconv.FormatUint(pid, 10), &cu)
	return cu, err == nil
}

// coconutRate: u64 dataId.
func (s *Server) coconutRate(c *nex.Conn, req *nex.Request) *nex.Response {
	id := nex.NewReader(req.Body).U64()
	var p CoconutPost
	key := strconv.FormatUint(id, 10)
	if rec, err := s.Records().Get(kindCoconut, key, &p); err == nil {
		p.Rating++
		_ = s.Records().Put(kindCoconut, key, rec.Owner, p)
	}
	return req.Success(nil)
}

// coconutReport: CoconutViolation{u64 dataId, String category, String
// reason}. A reported post is hidden from everyone but its author, and
// logged for the operator.
func (s *Server) coconutReport(c *nex.Conn, req *nex.Request) *nex.Response {
	r := nex.NewReader(req.Body)
	var id uint64
	var category, reason string
	r.Struct(func(r *nex.Reader, _ uint8) { id, category, reason = r.U64(), r.String(), r.String() })
	var p CoconutPost
	key := strconv.FormatUint(id, 10)
	if rec, err := s.Records().Get(kindCoconut, key, &p); err == nil {
		p.Hidden = true
		_ = s.Records().Put(kindCoconut, key, rec.Owner, p)
	}
	s.log.Warn("s2: plaza post reported", "reporter", c.PID, "data_id", id, "category", category, "reason", reason)
	return req.Success(nil)
}

// coconutGet: CoconutGetParam{List<u64> uniqueIds, u8 getType, u8 region,
// u32 festivalId} -> List<CoconutGetInfo{DataStoreReqGetInfo, CoconutMeta}> |
// List<Result>.
//
// The posts of that Splatfest -- by the given players when the list is not
// empty -- newest first. The get info points at this server, which serves
// the post's own binary data back.
//
// TODO(capture-needed): what getType selects, and what the get info's URL
// is expected to serve (read here as the post's binary data). A capture of
// a real answer settles both; this function and ServeHTTP are what change.
func (s *Server) coconutGet(c *nex.Conn, req *nex.Request) *nex.Response {
	r := nex.NewReader(req.Body)
	var ids []uint64
	var fid uint32
	r.Struct(func(r *nex.Reader, _ uint8) {
		ids = nex.ReadList(r, (*nex.Reader).U64)
		_, _ = r.U8(), r.U8()
		fid = r.U32()
	})
	if r.Err() != nil {
		return req.Error(nex.ResultInvalidArgument)
	}
	all, _ := rendezvous.List[CoconutPost](s.Records(), kindCoconut, "")
	var out []CoconutPost
	for _, p := range all {
		if p.FestivalID != fid || (p.Hidden && p.PID != c.PID) {
			continue
		}
		if len(ids) > 0 && !containsU64(ids, p.UniqueID) {
			continue
		}
		out = append(out, p)
		if len(out) == 100 {
			break
		}
	}
	w := nex.NewWriter()
	nex.WriteList(w, out, func(w *nex.Writer, p CoconutPost) {
		w.Struct(0, func(w *nex.Writer) {
			writeReqGetInfo(w, s.objectURL("coconut", p.DataID), uint32(len(p.Binary)), p.DataID)
			writeCoconutMeta(w, p)
		})
	})
	w.U32(uint32(len(out)))
	for range out {
		w.Result(nex.ResultSuccess)
	}
	return req.Success(w.Bytes())
}

func containsU64(list []uint64, v uint64) bool {
	for _, x := range list {
		if x == v {
			return true
		}
	}
	return false
}

func writeKeyValues(w *nex.Writer, kv [][2]string) {
	nex.WriteList(w, kv, func(w *nex.Writer, p [2]string) {
		w.Struct(0, func(w *nex.Writer) { w.String(p[0]); w.String(p[1]) })
	})
}

// writeReqGetInfo: DataStoreReqGetInfo{String url, List<KeyValue> headers,
// u32 size, Buffer rootCaCert, u64 dataId}. No root CA: the console's own
// trust store already trusts this server, which is the same trust every
// other GRID0+ host relies on.
func writeReqGetInfo(w *nex.Writer, url string, size uint32, dataID uint64) {
	w.Struct(0, func(w *nex.Writer) {
		w.String(url)
		writeKeyValues(w, nil)
		w.U32(size)
		w.Buffer(nil)
		w.U64(dataID)
	})
}

// objectURL is where this server serves or accepts a DataStore object: the
// game server's own hostname, which the console already resolves here and
// trusts, and which nginx already routes to cmd/nex.
func (s *Server) objectURL(kind string, id uint64) string {
	return fmt.Sprintf("https://%s/datastore/%s/%d", Host, kind, id)
}

// ---- play logs ---------------------------------------------------------------

type playLog struct {
	PID      uint64 `json:"pid"`
	Prepared int64  `json:"prepared"`
	Uploaded bool   `json:"uploaded"`
	Size     int    `json:"size"`
}

// preparePostPlayLog: PlayLogPreparePostParam -> DataStoreReqPostInfo{u64
// dataId, String url, List<KeyValue> headers, List<KeyValue> formFields,
// Buffer rootCaCert}. The console then uploads the log to that URL.
//
// Play logs are the game's own record of a battle for Nintendo's use. The
// upload is accepted and its size noted; the content is not kept.
func (s *Server) preparePostPlayLog(c *nex.Conn, req *nex.Request) *nex.Response {
	id := newDataID()
	if err := s.Records().Put(kindPlayLog, strconv.FormatUint(id, 10), rendezvous.UserIDForPID(c.PID),
		playLog{PID: c.PID, Prepared: s.Now().Unix()}); err != nil {
		return req.Error(nex.ResultNotImplemented)
	}
	w := nex.NewWriter()
	w.Struct(0, func(w *nex.Writer) {
		w.U64(id)
		w.String(s.objectURL("playlog", id))
		writeKeyValues(w, nil)
		writeKeyValues(w, nil)
		w.Buffer(nil)
	})
	return req.Success(w.Bytes())
}

// prepareGetPlayLog answers DataStore::NotFound: no play log is ever kept
// to be read back.
//
// TODO(capture-needed): what the game asks for here and what it does with
// it (PlayLogPrepareGetParam's fields are unnamed on Kinnay's wiki).
func (s *Server) prepareGetPlayLog(c *nex.Conn, req *nex.Request) *nex.Response {
	return req.Error(nex.ResultDataStoreNotFound)
}

// maxObjectUpload bounds one DataStore upload.
const maxObjectUpload = 4 << 20

// serveObjects serves the DataStore object URLs handed out above:
//
//	POST /datastore/playlog/<id>   a play log upload, for an id this server prepared
//	GET  /datastore/coconut/<id>   a plaza post's binary data
//
// Anything else is 404. No request here names a player or changes anything a
// player sees except by completing an upload the server itself asked for.
func (s *Server) serveObjects(w http.ResponseWriter, r *http.Request) {
	rest, ok := strings.CutPrefix(r.URL.Path, "/datastore/")
	kind, idStr, _ := strings.Cut(rest, "/")
	id, err := strconv.ParseUint(idStr, 10, 64)
	if !ok || err != nil {
		http.NotFound(w, r)
		return
	}
	switch {
	case kind == "playlog" && r.Method == http.MethodPost || kind == "playlog" && r.Method == http.MethodPut:
		var pl playLog
		key := strconv.FormatUint(id, 10)
		rec, err := s.Records().Get(kindPlayLog, key, &pl)
		if err != nil || pl.Uploaded {
			http.NotFound(w, r)
			return
		}
		n, _ := io.Copy(io.Discard, http.MaxBytesReader(w, r.Body, maxObjectUpload))
		pl.Uploaded, pl.Size = true, int(n)
		_ = s.Records().Put(kindPlayLog, key, rec.Owner, pl)
		w.WriteHeader(http.StatusOK)
	case kind == "coconut" && r.Method == http.MethodGet:
		var p CoconutPost
		if _, err := s.Records().Get(kindCoconut, strconv.FormatUint(id, 10), &p); err != nil || p.Hidden {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/octet-stream")
		_, _ = w.Write(p.Binary)
	default:
		http.NotFound(w, r)
	}
}
