package gamesync

import (
	"reflect"
	"sort"
	"testing"
	"time"

	commonpb "github.com/n-popescu/grid0plus/proto/common"
	syncpb "github.com/n-popescu/grid0plus/proto/gamesync/v1"
)

func TestPrivateRoomDocumentsKeepCapturedFixedAndMutableShapes(t *testing.T) {
	s := &Server{docs: newStore(time.Now)}
	caller := &session{details: &SessionDetails{
		UserID: "alice", SessionID: "room-id", Tenant: "t-dce9377b-lp1", Mode: "private_match_config",
		Host: "example.invalid", Port: 443, Capacity: 10, ParticipantCount: 1,
		CreatedAt: time.Unix(1000, 0),
	}, userSession: "gameSessions/room-id/userSessions/us-alice"}
	keys := func(fields map[string]*commonpb.Value) []string {
		out := make([]string, 0, len(fields))
		for k := range fields {
			out = append(out, k)
		}
		sort.Strings(out)
		return out
	}
	wantKeys := func(name string, fields map[string]*commonpb.Value, want []string) {
		t.Helper()
		sort.Strings(want)
		if got := keys(fields); !reflect.DeepEqual(got, want) {
			t.Errorf("%s fields = %v, want captured %v", name, got, want)
		}
	}
	fixed, ok := s.roomDocument(caller, "docs/__gs/f")
	if !ok {
		t.Fatal("missing fixed room document")
	}
	wantKeys("__gs/f", fixed.GetFields().GetFields(), []string{"addr", "gsid", "maxu", "mcn", "p", "rs", "tid"})
	if got := len(fixed.GetFields().GetFields()["rs"].GetBytesValue()); got != 32 {
		t.Errorf("fixed rs length = %d, want captured 32", got)
	}
	if got := fixed.GetFields().GetFields()["maxu"].GetIntegerValue(); got != 8 {
		t.Errorf("fixed maxu = %d, want captured 8", got)
	}
	if got := fixed.GetFields().GetFields()["tid"].GetStringValue(); got != caller.details.Tenant {
		t.Errorf("fixed tid = %q, want tenant", got)
	}
	mutable, ok := s.roomDocument(caller, "docs/__stg/All")
	if !ok {
		t.Fatal("missing mutable room document")
	}
	rs := mutable.GetFields().GetFields()["rs"].GetMapValue().GetFields()
	if got := rs["bfmax"].GetIntegerValue(); got != 8 {
		t.Errorf("mutable bfmax = %d, want captured 8", got)
	}
	if len(rs["prp"].GetMapValue().GetFields()) != 0 {
		t.Errorf("mutable properties = %v, want empty map", rs["prp"])
	}
	settings, _ := s.roomDocument(caller, "docs/__gs/m")
	if settings.GetFields().GetFields()["prp"].GetMapValue() == nil {
		t.Error("room settings omit the captured prp map")
	}
	wantKeys("__gs/m", settings.GetFields().GetFields(), []string{"bfmax", "bfmin", "cp", "ebf", "ip", "prp", "pw"})
	master := s.collectionSnapshot(caller, "docs/MasterCollection")
	if len(master) != 1 || master[0].GetName() != "docs/MasterCollection/Session" ||
		master[0].GetFields().GetFields()["id"].GetStringValue() != "" {
		t.Fatalf("room's MasterCollection snapshot = %v", master)
	}
	if got := settings.GetFields().GetFields()["bfmax"].GetIntegerValue(); got != 5 {
		t.Errorf("__gs/m bfmax = %d, want captured 5", got)
	}
	status, _ := s.roomDocument(caller, "docs/__gs/s")
	wantKeys("__gs/s", status.GetFields().GetFields(), []string{"att", "ltc", "pgn", "st", "tn", "ucsid", "uid", "upcsid", "ussid"})
	if got := status.GetFields().GetFields()["uid"].GetStringValue(); got != "alice" {
		t.Errorf("__gs/s uid = %q", got)
	}
	_, _, err := s.docs.apply([]*syncpb.WriteOperation{{OperationType: &syncpb.WriteOperation_MergeDocument{
		MergeDocument: &syncpb.MergeDocumentRequest{Document: &syncpb.Document{
			Name: "docs/__us/us-alice", Fields: &commonpb.MapValue{Fields: map[string]*commonpb.Value{
				"uid":   {ValueType: &commonpb.Value_StringValue{StringValue: "mallory"}},
				"ucsid": {ValueType: &commonpb.Value_IntegerValue{IntegerValue: 7}},
			}}},
		}},
	}})
	if err != nil {
		t.Fatal(err)
	}
	status, _ = s.roomDocument(caller, "docs/__gs/s")
	if got := status.GetFields().GetFields()["ucsid"].GetIntegerValue(); got != 1 {
		t.Errorf("__gs/s used a client counter instead of the authenticated room rank: %d", got)
	}
	if got := status.GetFields().GetFields()["uid"].GetStringValue(); got != "alice" {
		t.Errorf("__gs/s trusted a client-supplied identity: %q", got)
	}
}

func TestRoomJoinerSeesCompletePeerSessionDocuments(t *testing.T) {
	h := newHarness(t)
	host, err := h.srv.newSession("alice", "userSessions/us-alice")
	if err != nil {
		t.Fatal(err)
	}
	joiner, err := h.srv.newSession("bob", "userSessions/us-bob")
	if err != nil {
		t.Fatal(err)
	}
	outsider, err := h.srv.newSession("mallory", "userSessions/us-mallory")
	if err != nil {
		t.Fatal(err)
	}
	host.details = &SessionDetails{SessionID: "room-1", Players: []string{"alice", "bob"}}
	joiner.details = &SessionDetails{SessionID: "room-1", Players: []string{"alice", "bob"}}
	outsider.details = &SessionDetails{SessionID: "room-2"}
	for _, name := range []string{"docs/__us/us-bob", "docs/__pgn/All/__pus/us-bob"} {
		member, ok := h.srv.watchDocument(joiner, name)
		if !ok {
			t.Fatalf("missing guest session document %s", name)
		}
		for _, key := range []string{"ussid", "ucsid", "upcsid"} {
			if got := member.GetFields().GetFields()[key].GetIntegerValue(); got != 2 {
				t.Errorf("%s %s = %d, want guest rank 2", name, key, got)
			}
		}
	}
	joiner.details.UserID = "bob"
	guestStatus, ok := h.srv.roomDocument(joiner, "docs/__gs/s")
	if !ok || guestStatus.GetFields().GetFields()["ussid"].GetIntegerValue() != 2 {
		t.Errorf("guest room status does not carry rank 2: %v", guestStatus)
	}
	partial := func(name string, fields map[string]*commonpb.Value) *syncpb.WriteOperation {
		return &syncpb.WriteOperation{OperationType: &syncpb.WriteOperation_MergeDocument{
			MergeDocument: &syncpb.MergeDocumentRequest{Document: &syncpb.Document{
				Name: name, Fields: &commonpb.MapValue{Fields: fields},
			}},
		}}
	}
	_, _, err = h.srv.docs.apply([]*syncpb.WriteOperation{
		partial("docs/__us/us-alice", map[string]*commonpb.Value{"att": {ValueType: &commonpb.Value_MapValue{MapValue: &commonpb.MapValue{}}}}),
		partial("docs/__pgn/All/__pus/us-alice", map[string]*commonpb.Value{"pgn": stringValue("All"), "uid": stringValue("forged")}),
		partial("docs/__us/us-mallory", map[string]*commonpb.Value{"uid": stringValue("mallory")}),
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, collection := range []string{"docs/__us", "docs/__pgn/All/__pus"} {
		docs := h.srv.collectionSnapshot(joiner, collection)
		for _, doc := range docs {
			if doc.GetName() == "docs/__us/us-mallory" || doc.GetName() == "docs/__pgn/All/__pus/us-mallory" {
				t.Fatalf("%s leaked an unrelated room player", collection)
			}
			if doc.GetName() != collection+"/us-alice" {
				continue
			}
			fields := doc.GetFields().GetFields()
			if len(fields) != 9 || fields["uid"].GetStringValue() != "alice" || fields["att"].GetMapValue() == nil || fields["ltc"].GetMapValue() == nil {
				t.Fatalf("joiner's %s host snapshot is incomplete or trusts a forged uid: %v", collection, fields)
			}
		}
	}
}

func TestAPiaInboxWatchNeverCarriesThePeersInbox(t *testing.T) {
	h := newHarness(t)
	host, err := h.srv.newSession("alice", "userSessions/us-alice")
	if err != nil {
		t.Fatal(err)
	}
	joiner, err := h.srv.newSession("bob", "userSessions/us-bob")
	if err != nil {
		t.Fatal(err)
	}
	host.details = &SessionDetails{SessionID: "room-1", Mode: "private_match_config", Players: []string{"alice", "bob"}}
	joiner.details = &SessionDetails{SessionID: "room-1", Mode: "private_match_config", Players: []string{"alice", "bob"}}
	write := func(name string, fields map[string]*commonpb.Value) {
		t.Helper()
		_, _, err := h.srv.docs.apply([]*syncpb.WriteOperation{{OperationType: &syncpb.WriteOperation_MergeDocument{
			MergeDocument: &syncpb.MergeDocumentRequest{Document: &syncpb.Document{
				Name: name, Fields: &commonpb.MapValue{Fields: fields},
			}},
		}}})
		if err != nil {
			t.Fatal(err)
		}
	}
	const prefix = "docs/__pgn/All/__stu/"

	docs := h.srv.watchedDocuments(joiner, []string{prefix + "us-bob"})
	if len(docs) != 1 || docs[0].GetName() != prefix+"us-bob" {
		t.Fatalf("joiner's inbox watch = %v, want exactly its own inbox", docs)
	}
	initial := docs[0].GetFields().GetFields()
	if initial["pl"].GetMapValue() == nil || initial["mp"].GetMapValue() == nil ||
		initial["susid"].GetStringValue() != "us-bob" ||
		initial["sussid"].GetStringValue() != "us-bob" ||
		initial["suscid"].GetStringValue() != "1" {
		t.Fatalf("initial inbox differs from the private-room capture: %v", initial)
	}

	// The joiner's packet to the host sits in the host's inbox. It must never
	// come back to the joiner: that is the joiner's own packet, and stamping
	// it "from the host" made the joiner address its packets to itself.
	own := []byte{4, 5}
	write(prefix+"us-alice", map[string]*commonpb.Value{
		"pl":    {ValueType: &commonpb.Value_BytesValue{BytesValue: own}},
		"suid":  stringValue("bob"),
		"susid": stringValue("us-bob"),
	})
	for _, doc := range h.srv.watchedDocuments(joiner, []string{prefix + "us-bob"}) {
		if reflect.DeepEqual(doc.GetFields().GetFields()["pl"].GetBytesValue(), own) {
			t.Fatalf("joiner received its own Pia packet back: %v", doc)
		}
	}

	// The host's packet to the joiner arrives exactly as the host wrote it.
	write(prefix+"us-bob", map[string]*commonpb.Value{
		"pl":     {ValueType: &commonpb.Value_BytesValue{BytesValue: []byte{1, 2, 3}}},
		"suid":   stringValue("alice"),
		"susid":  stringValue("us-alice"),
		"suscid": intValue(1),
		"sussid": intValue(1),
	})
	docs = h.srv.watchedDocuments(joiner, []string{prefix + "us-bob"})
	if len(docs) != 1 {
		t.Fatalf("joiner's inbox watch = %v, want one document", docs)
	}
	got := docs[0].GetFields().GetFields()
	if len(got) != 5 || got["mp"] != nil || got["suid"].GetStringValue() != "alice" ||
		got["susid"].GetStringValue() != "us-alice" || got["suscid"].GetIntegerValue() != 1 ||
		!reflect.DeepEqual(got["pl"].GetBytesValue(), []byte{1, 2, 3}) {
		t.Fatalf("joiner's inbox does not carry the host's packet as written: %v", got)
	}
}
