package splatoon2

import (
	"encoding/json"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/n-popescu/switchnet/internal/rendezvous"
)

// What the dashboard's Splatoon 2 tabs read beyond the live status: what the
// game has stored here. Each is read when its tab opens rather than on the
// status poll, since each walks a whole kind of record.

type adminFest struct {
	FestivalID uint32    `json:"festivalId"`
	Votes      [2]uint32 `json:"votes"`
	Scores     [2]uint64 `json:"scores"`
	Battles    [2]uint32 `json:"battles"`
	Wins       [2]uint32 `json:"wins"`
}

// adminFests is every Splatfest the game has voted or scored in, newest id
// first, each with its tally.
func (s *Server) adminFests() []adminFest {
	ids := map[uint32]bool{}
	votes, _ := rendezvous.List[festVote](s.Records(), kindFestVote, "")
	for _, v := range votes {
		ids[v.FestivalID] = true
	}
	scores, _ := rendezvous.List[festScore](s.Records(), kindFestScore, "")
	for _, sc := range scores {
		ids[sc.FestivalID] = true
	}
	out := []adminFest{}
	for fid := range ids {
		t := s.TallyFestival(fid)
		out = append(out, adminFest{FestivalID: fid, Votes: t.Participants, Scores: t.Scores,
			Battles: t.Battles, Wins: t.Wins})
	}
	slices.SortFunc(out, func(a, b adminFest) int { return int(b.FestivalID) - int(a.FestivalID) })
	return out
}

type adminLeagueMember struct {
	UserID string `json:"userId"`
	Weapon int32  `json:"weapon"`
}

type adminLeagueTeam struct {
	TagID      uint64              `json:"tagId,string"`
	Region     uint8               `json:"region"`
	Point      uint32              `json:"point"`
	MatchCount uint32              `json:"matchCount"`
	Members    []adminLeagueMember `json:"members"`
}

type adminLeague struct {
	LeagueID string            `json:"leagueId"`
	Teams    []adminLeagueTeam `json:"teams"`
}

// adminLeagues is every league with a result, newest id first (a league id
// starts with its date), each team ranked as GetLeagueResult ranks it.
func (s *Server) adminLeagues() []adminLeague {
	all, _ := rendezvous.List[LeagueEntry](s.Records(), kindLeague, "")
	seen := map[string]bool{}
	var ids []string
	for _, e := range all {
		if !seen[e.LeagueID] {
			seen[e.LeagueID] = true
			ids = append(ids, e.LeagueID)
		}
	}
	slices.SortFunc(ids, func(a, b string) int { return strings.Compare(b, a) })
	out := []adminLeague{}
	for _, id := range ids {
		l := adminLeague{LeagueID: id, Teams: []adminLeagueTeam{}}
		for _, e := range s.LeagueStandings(id) {
			t := adminLeagueTeam{TagID: e.TagID, Region: e.Region, Point: e.Point, MatchCount: e.MatchCount,
				Members: []adminLeagueMember{}}
			for pid, m := range e.Members {
				t.Members = append(t.Members, adminLeagueMember{UserID: rendezvous.UserIDForPID(pid), Weapon: m.Weapon})
			}
			slices.SortFunc(t.Members, func(a, b adminLeagueMember) int { return strings.Compare(a.UserID, b.UserID) })
			l.Teams = append(l.Teams, t)
		}
		out = append(out, l)
	}
	return out
}

type adminPost struct {
	DataID     uint64 `json:"dataId,string"`
	UserID     string `json:"userId"`
	FestivalID uint32 `json:"festivalId"`
	Region     uint8  `json:"region"`
	PostType   uint8  `json:"postType"`
	Theme      uint8  `json:"theme"`
	Language   string `json:"language"`
	Rating     uint32 `json:"rating"`
	Hidden     bool   `json:"hidden"`
	Bytes      int    `json:"bytes"`
}

// adminPosts is every plaza post, most recently changed first. The image
// itself is not sent: it is the game's own encoding, and the page has no way
// to draw it.
func (s *Server) adminPosts() []adminPost {
	posts, _ := rendezvous.List[CoconutPost](s.Records(), kindCoconut, "")
	out := []adminPost{}
	for _, p := range posts {
		out = append(out, adminPost{DataID: p.DataID, UserID: rendezvous.UserIDForPID(p.PID), FestivalID: p.FestivalID,
			Region: p.Region, PostType: p.PostType, Theme: p.Theme, Language: p.Language, Rating: p.Rating,
			Hidden: p.Hidden, Bytes: len(p.Binary)})
	}
	return out
}

// setPostHidden hides a post from everyone but its author, or shows it
// again: what a report does, and the operator's way to undo one.
func (s *Server) setPostHidden(id uint64, hidden bool) bool {
	var p CoconutPost
	key := uint64Key(id)
	rec, err := s.Records().Get(kindCoconut, key, &p)
	if err != nil {
		return false
	}
	p.Hidden = hidden
	return s.Records().Put(kindCoconut, key, rec.Owner, p) == nil
}

type adminOrder struct {
	UserID string    `json:"userId"`
	Order  GearOrder `json:"order"`
}

// adminOrders is every order Annie is holding, connected or not.
func (s *Server) adminOrders() []adminOrder {
	orders, _ := s.Records().ListRaw(kindGearOrder)
	out := []adminOrder{}
	for _, rec := range orders {
		var o GearOrder
		if json.Unmarshal(rec.Value, &o) == nil {
			out = append(out, adminOrder{UserID: rec.Owner, Order: o})
		}
	}
	slices.SortFunc(out, func(a, b adminOrder) int { return strings.Compare(a.UserID, b.UserID) })
	return out
}

type adminUpload struct {
	Kind   string    `json:"kind"`
	UserID string    `json:"userId"`
	At     time.Time `json:"at"`
	Bytes  int       `json:"bytes"`
}

// adminUploads is what the game uploaded that this server keeps without
// reading -- X Power, event match results, Octo Expansion clear times --
// newest first: that they arrive, and from whom, is what can be shown until
// their shapes are known.
func (s *Server) adminUploads() []adminUpload {
	out := []adminUpload{}
	for _, kind := range []string{kindXPower, kindEventResult, kindTimeAttack} {
		ups, _ := rendezvous.List[rawUpload](s.Records(), kind, "")
		for _, u := range ups {
			out = append(out, adminUpload{Kind: kind, UserID: rendezvous.UserIDForPID(u.PID),
				At: time.Unix(u.At, 0).UTC(), Bytes: len(u.Body)})
		}
	}
	slices.SortFunc(out, func(a, b adminUpload) int { return b.At.Compare(a.At) })
	return out
}

func (s *Server) registerAdminRecords(mux *http.ServeMux, guard func(http.HandlerFunc) http.HandlerFunc) {
	get := func(path, key string, f func() any) {
		mux.HandleFunc("GET "+path, guard(func(w http.ResponseWriter, r *http.Request) {
			writeJSON(w, http.StatusOK, map[string]any{key: f()})
		}))
	}
	get("/splatoon2/fests", "fests", func() any { return s.adminFests() })
	get("/splatoon2/leagues", "leagues", func() any { return s.adminLeagues() })
	get("/splatoon2/plaza", "posts", func() any { return s.adminPosts() })
	get("/splatoon2/gear-orders", "orders", func() any { return s.adminOrders() })
	get("/splatoon2/uploads", "uploads", func() any { return s.adminUploads() })

	mux.HandleFunc("POST /splatoon2/plaza/{id}/hidden", guard(func(w http.ResponseWriter, r *http.Request) {
		id, err := strconv.ParseUint(r.PathValue("id"), 10, 64)
		var body struct {
			Hidden bool `json:"hidden"`
		}
		if err != nil || json.NewDecoder(http.MaxBytesReader(w, r.Body, 1024)).Decode(&body) != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "want /plaza/<data id>/hidden and {hidden}"})
			return
		}
		if !s.setPostHidden(id, body.Hidden) {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "no such post"})
			return
		}
		s.log.Info("s2: plaza post visibility set by the operator", "data_id", id, "hidden", body.Hidden)
		writeJSON(w, http.StatusOK, map[string]any{"posts": s.adminPosts()})
	}))
}
