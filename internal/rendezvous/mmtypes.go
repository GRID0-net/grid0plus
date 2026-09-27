package rendezvous

import (
	"github.com/n-popescu/grid0plus/internal/nex"
)

// The matchmaking structures, NEX 4.0 layout.
// https://github.com/kinnay/NintendoClients/wiki/Match-Making-Types
// Field order cross-checked against NintendoClients' matchmaking.py.

// Gathering flags (Pretendo's nex-protocols-go names them).
const (
	// FlagMigrateOwner: when the owner leaves, pick a new one rather than
	// deleting the gathering.
	FlagMigrateOwner = 0x10
)

// Participation policy 98: only the owner's friends may join.
const policyFriendsOnly = 98

// Gathering is the base of every gathering.
type Gathering struct {
	ID                  uint32
	Owner               uint64
	Host                uint64
	MinParticipants     uint16
	MaxParticipants     uint16
	ParticipationPolicy uint32
	PolicyArgument      uint32
	Flags               uint32
	State               uint32
	Description         string
}

// Param is one MatchmakeParam entry. A slice, not a map: the order the
// console sent is the order it gets back.
type Param struct {
	Key   string
	Value nex.Variant
}

// MatchmakeSession is a gathering players are matched into.
type MatchmakeSession struct {
	Gathering
	GameMode              uint32
	Attribs               []uint32
	OpenParticipation     bool
	MatchmakeSystemType   uint32
	ApplicationBuffer     []byte
	ParticipationCount    uint32
	ProgressScore         uint8
	SessionKey            []byte
	Option                uint32
	Params                []Param
	StartedTime           nex.DateTime
	UserPassword          string
	ReferGID              uint32
	UserPasswordEnabled   bool
	SystemPasswordEnabled bool
	Codeword              string
}

func writeGathering(w *nex.Writer, g Gathering) {
	w.Struct(0, func(w *nex.Writer) {
		w.U32(g.ID)
		w.PID(g.Owner)
		w.PID(g.Host)
		w.U16(g.MinParticipants)
		w.U16(g.MaxParticipants)
		w.U32(g.ParticipationPolicy)
		w.U32(g.PolicyArgument)
		w.U32(g.Flags)
		w.U32(g.State)
		w.String(g.Description)
	})
}

func readGathering(r *nex.Reader) Gathering {
	var g Gathering
	r.Struct(func(r *nex.Reader, _ uint8) {
		g.ID = r.U32()
		g.Owner = r.PID()
		g.Host = r.PID()
		g.MinParticipants = r.U16()
		g.MaxParticipants = r.U16()
		g.ParticipationPolicy = r.U32()
		g.PolicyArgument = r.U32()
		g.Flags = r.U32()
		g.State = r.U32()
		g.Description = r.String()
	})
	return g
}

func writeParams(w *nex.Writer, params []Param) {
	w.Struct(0, func(w *nex.Writer) {
		nex.WriteList(w, params, func(w *nex.Writer, p Param) {
			w.String(p.Key)
			w.Variant(p.Value)
		})
	})
}

func readParams(r *nex.Reader) []Param {
	var out []Param
	r.Struct(func(r *nex.Reader, _ uint8) {
		out = nex.ReadList(r, func(r *nex.Reader) Param {
			return Param{Key: r.String(), Value: r.Variant()}
		})
	})
	return out
}

// WriteMatchmakeSession writes both structure levels.
func WriteMatchmakeSession(w *nex.Writer, s *MatchmakeSession) {
	writeGathering(w, s.Gathering)
	w.Struct(0, func(w *nex.Writer) {
		w.U32(s.GameMode)
		nex.WriteList(w, s.Attribs, (*nex.Writer).U32)
		w.Bool(s.OpenParticipation)
		w.U32(s.MatchmakeSystemType)
		w.Buffer(s.ApplicationBuffer)
		w.U32(s.ParticipationCount)
		w.U8(s.ProgressScore)
		w.Buffer(s.SessionKey)
		w.U32(s.Option)
		writeParams(w, s.Params)
		w.DateTime(s.StartedTime)
		w.String(s.UserPassword)
		w.U32(s.ReferGID)
		w.Bool(s.UserPasswordEnabled)
		w.Bool(s.SystemPasswordEnabled)
		w.String(s.Codeword)
	})
}

// ReadMatchmakeSession reads both structure levels.
func ReadMatchmakeSession(r *nex.Reader) *MatchmakeSession {
	s := &MatchmakeSession{Gathering: readGathering(r)}
	r.Struct(func(r *nex.Reader, _ uint8) {
		s.GameMode = r.U32()
		s.Attribs = nex.ReadList(r, (*nex.Reader).U32)
		s.OpenParticipation = r.Bool()
		s.MatchmakeSystemType = r.U32()
		s.ApplicationBuffer = r.Buffer()
		s.ParticipationCount = r.U32()
		s.ProgressScore = r.U8()
		s.SessionKey = r.Buffer()
		s.Option = r.U32()
		s.Params = readParams(r)
		s.StartedTime = r.DateTime()
		s.UserPassword = r.String()
		s.ReferGID = r.U32()
		s.UserPasswordEnabled = r.Bool()
		s.SystemPasswordEnabled = r.Bool()
		s.Codeword = r.String()
	})
	return s
}

// writeSessionHolder writes a MatchmakeSession as AnyDataHolder<Gathering>.
func writeSessionHolder(w *nex.Writer, s *MatchmakeSession) {
	w.AnyData("MatchmakeSession", func(w *nex.Writer) { WriteMatchmakeSession(w, s) })
}

// readSessionHolder reads AnyDataHolder<Gathering>, which must hold a
// MatchmakeSession: the only gathering kind a Switch title matchmakes with.
func readSessionHolder(r *nex.Reader) *MatchmakeSession {
	name, obj := r.AnyData()
	if r.Err() != nil {
		return nil
	}
	if name != "MatchmakeSession" {
		r.Fail(errNotASession(name))
		return nil
	}
	s := ReadMatchmakeSession(obj)
	if obj.Err() != nil {
		r.Fail(obj.Err())
		return nil
	}
	return s
}

type errNotASession string

func (e errNotASession) Error() string {
	return "rendezvous: gathering holder carries a " + string(e) + ", not a MatchmakeSession"
}

// SearchCriteria is MatchmakeSessionSearchCriteria. Every numeric filter is a
// string: "" for any, "n" for exactly n, "a,b" for a range.
type SearchCriteria struct {
	Attribs                []string
	GameMode               string
	MinParticipants        string
	MaxParticipants        string
	MatchmakeSystemType    string
	VacantOnly             bool
	ExcludeLocked          bool
	ExcludeNonHostPID      bool
	SelectionMethod        uint32
	VacantParticipants     uint16
	ExcludeUserPassword    bool
	ExcludeSystemPassword  bool
	ReferGID               uint32
	Codeword               string
	ResultOffset, ResultSz uint32
}

func readSearchCriteria(r *nex.Reader) SearchCriteria {
	var c SearchCriteria
	r.Struct(func(r *nex.Reader, _ uint8) {
		c.Attribs = nex.ReadList(r, (*nex.Reader).String)
		c.GameMode = r.String()
		c.MinParticipants = r.String()
		c.MaxParticipants = r.String()
		c.MatchmakeSystemType = r.String()
		c.VacantOnly = r.Bool()
		c.ExcludeLocked = r.Bool()
		c.ExcludeNonHostPID = r.Bool()
		c.SelectionMethod = r.U32()
		c.VacantParticipants = r.U16()
		_ = readParams(r)
		c.ExcludeUserPassword = r.Bool()
		c.ExcludeSystemPassword = r.Bool()
		c.ReferGID = r.U32()
		c.Codeword = r.String()
		r.Struct(func(r *nex.Reader, _ uint8) {
			c.ResultOffset = r.U32()
			c.ResultSz = r.U32()
		})
	})
	return c
}

func readBlockList(r *nex.Reader) {
	r.Struct(func(r *nex.Reader, _ uint8) { _ = r.U32() })
}

// joinRequest is what every create/join/auto-matchmake method reduces to.
type joinRequest struct {
	session    *MatchmakeSession // the caller's template (create, auto)
	additional []uint64          // players joining with the caller
	message    string
	criteria   []SearchCriteria
	targetGIDs []uint32

	gid          uint32 // join only
	userPassword string // join only
	systemPw     string // join only
	behavior     uint8  // join only: 1 = "I am already joined"
}

func readCreateParam(r *nex.Reader) joinRequest {
	var j joinRequest
	r.Struct(func(r *nex.Reader, _ uint8) {
		j.session = ReadMatchmakeSession(r)
		j.additional = nex.ReadList(r, (*nex.Reader).PID)
		_ = r.U32() // gidForParticipationCheck
		_ = r.U32() // option
		j.message = r.String()
		_ = r.U16() // participationCount
	})
	return j
}

func readJoinParam(r *nex.Reader) joinRequest {
	var j joinRequest
	r.Struct(func(r *nex.Reader, _ uint8) {
		j.gid = r.U32()
		j.additional = nex.ReadList(r, (*nex.Reader).PID)
		_ = r.U32() // gidForParticipationCheck
		_ = r.U32() // option
		j.behavior = r.U8()
		j.userPassword = r.String()
		j.systemPw = r.String()
		j.message = r.String()
		_ = r.U16() // participationCount
		_ = r.U16() // extraParticipants
		readBlockList(r)
	})
	return j
}

func readAutoParam(r *nex.Reader) joinRequest {
	var j joinRequest
	r.Struct(func(r *nex.Reader, _ uint8) {
		j.session = ReadMatchmakeSession(r)
		j.additional = nex.ReadList(r, (*nex.Reader).PID)
		_ = r.U32() // gidForParticipationCheck
		_ = r.U32() // option
		j.message = r.String()
		_ = r.U16() // participationCount
		j.criteria = nex.ReadList(r, readSearchCriteria)
		j.targetGIDs = nex.ReadList(r, (*nex.Reader).U32)
		readBlockList(r)
	})
	return j
}

// UpdateMatchmakeSessionParam modification flags (Pretendo's names).
const (
	modAttributes          = 0x1
	modOpenParticipation   = 0x2
	modApplicationBuffer   = 0x4
	modProgressScore       = 0x8
	modMatchmakeParam      = 0x20
	modStartedTime         = 0x80
	modUserPassword        = 0x100
	modGameMode            = 0x200
	modDescription         = 0x400
	modMinParticipants     = 0x800
	modMaxParticipants     = 0x1000
	modMatchmakeSystemType = 0x2000
	modCodeword            = 0x4000
)

type updateParam struct {
	gid, flags        uint32
	attribs           []uint32
	open              bool
	appBuffer         []byte
	progress          uint8
	params            []Param
	started           nex.DateTime
	userPassword      string
	gameMode          uint32
	description       string
	minP, maxP        uint16
	systemType        uint32
	policy, policyArg uint32
	codeword          string
}

func readUpdateParam(r *nex.Reader) updateParam {
	var u updateParam
	r.Struct(func(r *nex.Reader, _ uint8) {
		u.gid = r.U32()
		u.flags = r.U32()
		u.attribs = nex.ReadList(r, (*nex.Reader).U32)
		u.open = r.Bool()
		u.appBuffer = r.Buffer()
		u.progress = r.U8()
		u.params = readParams(r)
		u.started = r.DateTime()
		u.userPassword = r.String()
		u.gameMode = r.U32()
		u.description = r.String()
		u.minP = r.U16()
		u.maxP = r.U16()
		u.systemType = r.U32()
		u.policy = r.U32()
		u.policyArg = r.U32()
		u.codeword = r.String()
	})
	return u
}
