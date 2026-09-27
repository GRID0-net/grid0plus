package gamesync

import (
	"crypto/sha256"
	"time"

	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"

	commonpb "github.com/n-popescu/switchnet/proto/common"
	syncpb "github.com/n-popescu/switchnet/proto/gamesync/v1"
)

// SessionDetails comes from the verified matchmaking token and live room, not
// from a Gamesync request's self-reported document names.
type SessionDetails struct {
	UserID, UserSessionID, SessionID, Tenant, Mode, Host, Password string
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
	props := map[string]*commonpb.Value{}
	for k, v := range d.Properties.GetFields() {
		props[k] = proto.Clone(v).(*commonpb.Value)
	}
	bfmax := d.Capacity - 3
	if bfmax < 1 {
		bfmax = 1
	}
	settings := func() map[string]*commonpb.Value {
		return map[string]*commonpb.Value{
			"bfmax": integer(int64(bfmax)), "bfmin": integer(1),
			"cp": boolean(true), "ebf": boolean(false), "ip": boolean(true),
			"pw": str(d.Password), "prp": mapValue(props),
		}
	}
	var fields map[string]*commonpb.Value
	switch name {
	case "docs/__gs/f":
		secret := sha256.Sum256([]byte("switchnet-room:" + d.SessionID))
		fields = map[string]*commonpb.Value{
			"addr": str(d.Host), "gsid": str(d.SessionID),
			"maxu": integer(int64(d.Capacity)), "mcn": str(d.Mode),
			"p":   integer(int64(d.Port)),
			"rs":  &commonpb.Value{ValueType: &commonpb.Value_BytesValue{BytesValue: secret[:]}},
			"tid": str(d.Tenant),
		}
	case "docs/__gs/m", "docs/__gs/s":
		fields = settings()
	case "docs/__gs/r":
		fields = map[string]*commonpb.Value{"ipc": integer(int64(d.ParticipantCount)), "dpc": integer(0)}
	case "docs/__gs/n":
		fields = map[string]*commonpb.Value{
			"etn": boolean(false),
			"ett": &commonpb.Value{ValueType: &commonpb.Value_TimestampValue{TimestampValue: timestamppb.New(d.CreatedAt.Add(48 * time.Hour))}},
		}
	case "docs/__gs/ck":
		fields = map[string]*commonpb.Value{"k": str(d.Password)}
	case "docs/__stg/All":
		mutable := settings()
		mutable["cp"] = boolean(false)
		fields = map[string]*commonpb.Value{
			"gsid": str(d.SessionID), "addr": str(d.Host), "p": integer(int64(d.Port)),
			"mcn": str(d.Mode), "maxu": integer(int64(d.Capacity)), "rs": mapValue(mutable),
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
