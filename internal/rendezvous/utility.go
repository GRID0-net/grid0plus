package rendezvous

import (
	"crypto/rand"
	"encoding/binary"
	"errors"
	"strconv"

	"github.com/n-popescu/grid0plus/internal/nex"
)

// Utility (protocol 110): NEX unique ids and server settings.
// https://github.com/kinnay/NintendoClients/wiki/Utility-Protocol
const ProtocolUtility = 0x6E

const (
	methodAcquireNexUniqueID                         = 1
	methodAcquireNexUniqueIDWithPassword             = 2
	methodAssociateNexUniqueIDWithMyPrincipalID      = 3
	methodAssociateNexUniqueIDsWithMyPrincipalID     = 4
	methodGetAssociatedNexUniqueIDWithMyPrincipalID  = 5
	methodGetAssociatedNexUniqueIDsWithMyPrincipalID = 6
	methodGetIntegerSettings                         = 7
	methodGetStringSettings                          = 8
)

// KindUniqueID is the record kind of an allocated NEX unique id.
const KindUniqueID = "unique_id"

// UniqueID is a NEX unique id: a stable identifier a game keeps in its save
// data, separate from the principal. It is what a title's own records name a
// player by (Splatoon 2's league teams and Splatfest scores, for instance),
// so it has to be unique and it has to stay the one handed out.
type UniqueID struct {
	ID       uint64 `json:"id"`
	Password uint64 `json:"password"`
	// Associated is whether the owner has claimed it with Associate*.
	Associated bool `json:"associated"`
}

func (s *Server) registerUtility() {
	const p = ProtocolUtility
	s.Handle(p, "Utility", methodAcquireNexUniqueID, "AcquireNexUniqueId", s.acquireUniqueID(false))
	s.Handle(p, "Utility", methodAcquireNexUniqueIDWithPassword, "AcquireNexUniqueIdWithPassword", s.acquireUniqueID(true))
	s.Handle(p, "Utility", methodAssociateNexUniqueIDWithMyPrincipalID, "AssociateNexUniqueIdWithMyPrincipalId", s.associateUniqueIDs(false))
	s.Handle(p, "Utility", methodAssociateNexUniqueIDsWithMyPrincipalID, "AssociateNexUniqueIdsWithMyPrincipalId", s.associateUniqueIDs(true))
	s.Handle(p, "Utility", methodGetAssociatedNexUniqueIDWithMyPrincipalID, "GetAssociatedNexUniqueIdWithMyPrincipalId", s.getAssociatedUniqueIDs(false))
	s.Handle(p, "Utility", methodGetAssociatedNexUniqueIDsWithMyPrincipalID, "GetAssociatedNexUniqueIdsWithMyPrincipalId", s.getAssociatedUniqueIDs(true))
	s.Handle(p, "Utility", methodGetIntegerSettings, "GetIntegerSettings", s.getSettings(true))
	s.Handle(p, "Utility", methodGetStringSettings, "GetStringSettings", s.getSettings(false))
}

// writeUniqueIDInfo writes UniqueIdInfo{u64 id, u64 password}.
func writeUniqueIDInfo(w *nex.Writer, u UniqueID) {
	w.Struct(0, func(w *nex.Writer) {
		w.U64(u.ID)
		w.U64(u.Password)
	})
}

// ReadUniqueIDInfo reads a UniqueIdInfo.
func ReadUniqueIDInfo(r *nex.Reader) UniqueID {
	var u UniqueID
	r.Struct(func(s *nex.Reader, _ uint8) {
		u.ID = s.U64()
		u.Password = s.U64()
	})
	return u
}

func random63() uint64 {
	var b [8]byte
	_, _ = rand.Read(b[:])
	// Positive as a signed 64-bit value, and never zero: some games keep
	// these as signed integers, and 0 reads as "none".
	return binary.LittleEndian.Uint64(b[:])&0x7FFF_FFFF_FFFF_FFFF | 1
}

// acquireUniqueID allocates a unique id for the caller: a bare u64, or
// UniqueIdInfo with a password.
//
// A title only asks on a fresh account and then keeps the id in its save,
// which is why a missing answer looks like "works for most people":
// NextendoNetwork records Splatoon 2 calling the WithPassword form and
// showing 2306-0103 when it went unanswered (facts only).
func (s *Server) acquireUniqueID(withPassword bool) Method {
	return func(c *nex.Conn, req *nex.Request) *nex.Response {
		u := UniqueID{ID: random63()}
		if withPassword {
			u.Password = random63()
		}
		if err := s.Records().Put(KindUniqueID, strconv.FormatUint(u.ID, 10), UserIDForPID(c.PID), u); err != nil {
			s.log.Error("nex: store unique id", "pid", c.PID, "error", err)
			return req.Error(nex.ResultNotImplemented)
		}
		w := nex.NewWriter()
		if withPassword {
			writeUniqueIDInfo(w, u)
		} else {
			w.U64(u.ID)
		}
		return req.Success(w.Bytes())
	}
}

// ErrNotYours is a unique id the caller did not allocate, or presented with
// the wrong password.
var ErrNotYours = errors.New("rendezvous: unique id is not the caller's")

// ClaimUniqueID associates a unique id with the caller. Only an id this
// server handed to this same user, presented with its password, can be
// claimed: an id is how a title's records name a player, so claiming
// someone else's would be claiming their league results and fest scores.
func (s *Server) ClaimUniqueID(pid uint64, u UniqueID) error {
	var stored UniqueID
	rec, err := s.Records().Get(KindUniqueID, strconv.FormatUint(u.ID, 10), &stored)
	if err != nil {
		return ErrNotYours
	}
	if rec.Owner != UserIDForPID(pid) || stored.Password != u.Password {
		return ErrNotYours
	}
	stored.Associated = true
	return s.Records().Put(KindUniqueID, rec.Key, rec.Owner, stored)
}

func (s *Server) associateUniqueIDs(list bool) Method {
	return func(c *nex.Conn, req *nex.Request) *nex.Response {
		r := nex.NewReader(req.Body)
		var ids []UniqueID
		if list {
			ids = nex.ReadList(r, ReadUniqueIDInfo)
		} else {
			ids = []UniqueID{ReadUniqueIDInfo(r)}
		}
		if r.Err() != nil {
			return req.Error(nex.ResultInvalidArgument)
		}
		for _, u := range ids {
			if err := s.ClaimUniqueID(c.PID, u); err != nil {
				s.log.Warn("nex: refused to associate a unique id", "pid", c.PID, "unique_id", u.ID)
				return req.Error(nex.ResultPermissionDenied)
			}
		}
		return req.Success(nil)
	}
}

// UniqueIDsOf returns the unique ids a player has associated.
func (s *Server) UniqueIDsOf(pid uint64) []UniqueID {
	all, err := List[UniqueID](s.Records(), KindUniqueID, UserIDForPID(pid))
	if err != nil {
		s.log.Error("nex: list unique ids", "pid", pid, "error", err)
		return nil
	}
	var out []UniqueID
	for _, u := range all {
		if u.Associated {
			out = append(out, u)
		}
	}
	return out
}

func (s *Server) getAssociatedUniqueIDs(list bool) Method {
	return func(c *nex.Conn, req *nex.Request) *nex.Response {
		ids := s.UniqueIDsOf(c.PID)
		w := nex.NewWriter()
		if list {
			nex.WriteList(w, ids, writeUniqueIDInfo)
		} else {
			var u UniqueID // none yet: zeros, which a title reads as "acquire one"
			if len(ids) > 0 {
				u = ids[0]
			}
			writeUniqueIDInfo(w, u)
		}
		return req.Success(w.Bytes())
	}
}

// getSettings answers GetIntegerSettings (Map<u16, s32>) and
// GetStringSettings (Map<u16, String>) with an empty map.
//
// TODO(capture-needed): what Splatoon 2's settings groups contain. An empty
// map is what NextendoNetwork's server answers every title that has not
// been shown to need more (facts only), and nothing records Splatoon 2
// reading one. A capture of the real answer would settle it; the single
// function to change is this one.
func (s *Server) getSettings(integer bool) Method {
	return func(c *nex.Conn, req *nex.Request) *nex.Response {
		index := nex.NewReader(req.Body).U32()
		s.log.Debug("nex: settings requested", "pid", c.PID, "integer", integer, "index", index)
		w := nex.NewWriter()
		w.U32(0)
		return req.Success(w.Bytes())
	}
}
