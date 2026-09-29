package gamesync

import (
	"testing"
	"time"
)

func TestPrivateRoomDocumentsKeepCapturedFixedAndMutableShapes(t *testing.T) {
	s := &Server{}
	caller := &session{details: &SessionDetails{
		SessionID: "room-id", Tenant: "t-dce9377b-lp1", Mode: "private_match_config",
		Host: "example.invalid", Port: 443, Capacity: 10, ParticipantCount: 1,
		CreatedAt: time.Unix(1000, 0),
	}}
	fixed, ok := s.roomDocument(caller, "docs/__gs/f")
	if !ok {
		t.Fatal("missing fixed room document")
	}
	if _, present := fixed.GetFields().GetFields()["tid"]; present {
		t.Error("fixed room document carried an extra tenant field")
	}
	if got := fixed.GetFields().GetFields()["maxu"].GetIntegerValue(); got != 10 {
		t.Errorf("fixed maxu = %d, want 10", got)
	}
	mutable, ok := s.roomDocument(caller, "docs/__stg/All")
	if !ok {
		t.Fatal("missing mutable room document")
	}
	rs := mutable.GetFields().GetFields()["rs"].GetMapValue().GetFields()
	if got := rs["bfmax"].GetIntegerValue(); got != 10 {
		t.Errorf("mutable bfmax = %d, want full room capacity 10", got)
	}
	if len(rs["prp"].GetMapValue().GetFields()) != 0 {
		t.Errorf("mutable properties = %v, want empty map", rs["prp"])
	}
	settings, _ := s.roomDocument(caller, "docs/__gs/m")
	if got := settings.GetFields().GetFields()["bfmax"].GetIntegerValue(); got != 7 {
		t.Errorf("__gs/m bfmax = %d, want 7", got)
	}
}
