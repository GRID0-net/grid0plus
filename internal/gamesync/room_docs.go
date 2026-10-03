package gamesync

import (
	"crypto/sha256"
	"strings"
	"time"

	"google.golang.org/protobuf/types/known/timestamppb"

	commonpb "github.com/n-popescu/grid0plus/proto/common"
	syncpb "github.com/n-popescu/grid0plus/proto/gamesync/v1"
)

// SessionDetails comes from the verified matchmaking token and live room, not
// from a Gamesync request's self-reported document names.
type SessionDetails struct {
	UserID, UserSessionID, SessionID, Tenant, Mode, Host, Password string
	Players                                                        []string
	Port                                                           int32
	Capacity, ParticipantCount                                     int
	CreatedAt                                                      time.Time
	Properties                                                     *commonpb.MapValue
	// Room is true for a room a host opened directly (a private battle, a
	// private Salmon Run, a room friends join), false for a matchmade game.
	Room bool
	// PlayerSessions maps each player of the game session to the user-session
	// id they will join it with. Known as soon as the match forms, before
	// the players connect: see matchedPlayerDocuments.
	PlayerSessions map[string]string
}

const privateBattleConfig = "private_match_config"

// isPrivateBattle reports a room a host opened directly rather than a
// matchmade game.
//
// Only the private battle has a capture of a working server (see
// roomDocument): its room documents carry the room's own capacity and config
// name, and its __stu the private shape. Private Salmon Run
// (coop_private_config) and the rooms friends join
// (join_room_4_no_referee_config) are opened through the same room API and
// are given the same shape, a reading rather than a capture: before the
// private-battle fix every room got the matchmade shape, and that is what
// broke private battles.
func isPrivateBattle(d *SessionDetails) bool {
	if d == nil || d.Capacity <= 0 {
		return false
	}
	if d.Room {
		return true
	}
	return roomConfig(d) == privateBattleConfig
}

func roomConfig(d *SessionDetails) string {
	mode := d.Mode
	if i := strings.LastIndex(mode, "/"); i >= 0 {
		mode = mode[i+1:]
	}
	return mode
}

func (s *Server) roomDocument(caller *session, name string) (*syncpb.Document, bool) {
	d := caller.details
	if d == nil {
		return nil, false
	}
	str := func(v string) *commonpb.Value {
		return &commonpb.Value{ValueType: &commonpb.Value_StringValue{StringValue: v}}
	}
	integer := func(v int64) *commonpb.Value {
		return &commonpb.Value{ValueType: &commonpb.Value_IntegerValue{IntegerValue: v}}
	}
	boolean := func(v bool) *commonpb.Value {
		return &commonpb.Value{ValueType: &commonpb.Value_BooleanValue{BooleanValue: v}}
	}
	mapValue := func(fields map[string]*commonpb.Value) *commonpb.Value {
		return &commonpb.Value{ValueType: &commonpb.Value_MapValue{MapValue: &commonpb.MapValue{Fields: fields}}}
	}
	// All three room-creation modes in the working capture (private battle,
	// join-room, and Coop private) publish the same Gamesync format capacity of
	// eight, even though their NPLN tickets advertise 10/4/4 respectively.
	// The game reads these documents after the ticket succeeds.
	//
	// Nextendo's server has since changed for private battles: a Ryujinx
	// private room on 2026-10-02 got maxu 10, mcn "private_match_config", and
	// bfmax 7 (capacity minus three) / 10 in __stg, i.e. the room's real
	// capacity and config name. Other modes keep the earlier shape until a
	// capture shows them too.
	wireCapacity, mcn := 8, ""
	if isPrivateBattle(d) {
		wireCapacity, mcn = d.Capacity, roomConfig(d)
	}
	settings := func() map[string]*commonpb.Value {
		return map[string]*commonpb.Value{
			"bfmax": integer(int64(wireCapacity - 3)), "bfmin": integer(1),
			"cp": boolean(true), "ebf": boolean(false), "ip": boolean(true),
			"prp": mapValue(nil), "pw": str(d.Password),
		}
	}
	var fields map[string]*commonpb.Value
	switch name {
	case "docs/__gs/f":
		secret := sha256.Sum256([]byte("switchnet-room:" + d.SessionID))
		fields = map[string]*commonpb.Value{
			"addr": str(d.Host), "gsid": str(d.SessionID),
			"maxu": integer(int64(wireCapacity)), "mcn": str(mcn),
			"p": integer(int64(d.Port)), "tid": str(d.Tenant),
			"rs": &commonpb.Value{ValueType: &commonpb.Value_BytesValue{BytesValue: secret[:]}},
		}
	case "docs/__gs/m":
		fields = settings()
	case "docs/__gs/s":
		// The working server mirrors the caller's nine-field __us document
		// here, including its live session counters. Returning room settings
		// at this path gives the game an entirely different value type.
		rank := participantRank(caller)
		fields = map[string]*commonpb.Value{
			"uid": str(d.UserID), "ussid": integer(rank), "ucsid": integer(rank),
			"upcsid": integer(rank), "pgn": str("All"), "st": integer(2),
			"tn": str(""), "att": mapValue(nil), "ltc": mapValue(nil),
		}
		if s.docs != nil {
			if userSession, ok := s.docsFor(caller).snapshot("docs/__us/" + sessionID(caller.userSession)); ok {
				for _, key := range []string{"att", "ltc", "pgn", "st", "tn"} {
					if value := userSession.GetFields().GetFields()[key]; value != nil {
						fields[key] = value
					}
				}
			}
		}
	case "docs/__gs/r":
		count := d.ParticipantCount
		if s.liveParticipants != nil {
			var member bool
			count, member = s.liveParticipants(d.SessionID, caller.userID)
			if !member {
				return nil, false
			}
		}
		fields = map[string]*commonpb.Value{"ipc": integer(int64(count)), "dpc": integer(0)}
	case "docs/__gs/n":
		fields = map[string]*commonpb.Value{
			"etn": boolean(false),
			"ett": &commonpb.Value{ValueType: &commonpb.Value_TimestampValue{TimestampValue: timestamppb.New(d.CreatedAt.Add(48 * time.Hour))}},
		}
	case "docs/__gs/ck":
		fields = map[string]*commonpb.Value{"k": str(d.Password)}
	case "docs/MasterCollection/Session":
		// The working room watch lists this anchor before the client writes its
		// friend-room and tracking state into the same document.
		fields = map[string]*commonpb.Value{"id": str("")}
	case "docs/__stg/All":
		mutable := settings()
		mutable["cp"] = boolean(false)
		mutable["bfmax"] = integer(int64(wireCapacity))
		mutable["prp"] = mapValue(nil)
		fields = map[string]*commonpb.Value{
			"gsid": str(d.SessionID), "addr": str(d.Host), "p": integer(int64(d.Port)),
			"mcn": str(mcn), "maxu": integer(int64(wireCapacity)), "rs": mapValue(mutable),
		}
	default:
		return nil, false
	}
	return &syncpb.Document{
		Name: name, Fields: &commonpb.MapValue{Fields: fields},
		CreateTime: timestamppb.New(d.CreatedAt), UpdateTime: timestamppb.New(d.CreatedAt),
	}, true
}

func (s *Server) roomCollectionDocuments(caller *session, collection string) []*syncpb.Document {
	if collection == "docs/MasterCollection" && caller.details != nil {
		doc, _ := s.roomDocument(caller, "docs/MasterCollection/Session")
		return []*syncpb.Document{doc}
	}
	if collection != "docs/__gs" || caller.details == nil {
		return nil
	}
	var docs []*syncpb.Document
	for _, suffix := range []string{"ck", "f", "m", "n", "r", "s"} {
		if doc, ok := s.roomDocument(caller, "docs/__gs/"+suffix); ok {
			docs = append(docs, doc)
		}
	}
	return docs
}

// seedRoomDocuments stores the generated version of each room document a
// write is about to change, before the write applies.
//
// Room documents such as docs/__gs/m are generated on read and not stored.
// The console changes them with masked updates -- __gs/m gets only cp, ebf
// and ip when the room locks -- and without a stored document to merge into,
// that update created a document holding just those three fields, which
// then replaced the generated one for every reader: bfmax, bfmin, prp and pw
// were gone and createTime moved. A masked update has to land on the
// document the console was shown.
//
// docs/__gs/r is left out: its counts are live and a stored copy would
// freeze them.
func (s *Server) seedRoomDocuments(caller *session, ops []*syncpb.WriteOperation) {
	for _, op := range ops {
		var name string
		switch o := op.GetOperationType().(type) {
		case *syncpb.WriteOperation_UpdateDocument:
			if len(o.UpdateDocument.GetUpdateMask().GetPaths()) == 0 {
				continue // a full replacement does not need the old fields
			}
			name = o.UpdateDocument.GetDocument().GetName()
		case *syncpb.WriteOperation_MergeDocument:
			name = o.MergeDocument.GetDocument().GetName()
		case *syncpb.WriteOperation_TransformDocument:
			name = o.TransformDocument.GetName()
		default:
			continue
		}
		if name == "docs/__gs/r" {
			continue
		}
		if doc, ok := s.roomDocument(caller, name); ok {
			s.docsFor(caller).seed(doc)
		}
	}
}
