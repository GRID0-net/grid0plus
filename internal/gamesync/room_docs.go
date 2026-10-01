package gamesync

import (
	"crypto/sha256"
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
	const wireCapacity = 8
	settings := func() map[string]*commonpb.Value {
		return map[string]*commonpb.Value{
			"bfmax": integer(wireCapacity - 3), "bfmin": integer(1),
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
			"maxu": integer(wireCapacity), "mcn": str(""),
			"p": integer(int64(d.Port)), "tid": str(d.Tenant),
			"rs": &commonpb.Value{ValueType: &commonpb.Value_BytesValue{BytesValue: secret[:]}},
		}
	case "docs/__gs/m":
		fields = settings()
	case "docs/__gs/s":
		// The working server mirrors the caller's nine-field __us document
		// here, including its live session counters. Returning room settings
		// at this path gives the game an entirely different value type.
		fields = map[string]*commonpb.Value{
			"uid": str(d.UserID), "ussid": integer(1), "ucsid": integer(1),
			"upcsid": integer(1), "pgn": str("All"), "st": integer(2),
			"tn": str(""), "att": mapValue(nil), "ltc": mapValue(nil),
		}
		if s.docs != nil {
			if userSession, ok := s.docs.snapshot("docs/__us/" + sessionID(caller.userSession)); ok {
				for _, key := range []string{"att", "ltc", "pgn", "st", "tn", "ucsid", "upcsid", "ussid"} {
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
		mutable["bfmax"] = integer(wireCapacity)
		mutable["prp"] = mapValue(nil)
		fields = map[string]*commonpb.Value{
			"gsid": str(d.SessionID), "addr": str(d.Host), "p": integer(int64(d.Port)),
			"mcn": str(""), "maxu": integer(wireCapacity), "rs": mapValue(mutable),
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
