package gamesync

import (
	"reflect"
	"sort"
	"testing"
	"time"

	"google.golang.org/protobuf/types/known/fieldmaskpb"

	commonpb "github.com/n-popescu/grid0plus/proto/common"
	syncpb "github.com/n-popescu/grid0plus/proto/gamesync/v1"
)

func TestPrivateRoomDocumentsKeepCapturedFixedAndMutableShapes(t *testing.T) {
	s := &Server{docs: newStoreSet(time.Now)}
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
	// A private room: the room's real capacity and config name.
	if got := fixed.GetFields().GetFields()["maxu"].GetIntegerValue(); got != 10 {
		t.Errorf("fixed maxu = %d, want captured 10", got)
	}
	if got := fixed.GetFields().GetFields()["mcn"].GetStringValue(); got != "private_match_config" {
		t.Errorf("fixed mcn = %q, want captured private_match_config", got)
	}
	if got := fixed.GetFields().GetFields()["tid"].GetStringValue(); got != caller.details.Tenant {
		t.Errorf("fixed tid = %q, want tenant", got)
	}
	mutable, ok := s.roomDocument(caller, "docs/__stg/All")
	if !ok {
		t.Fatal("missing mutable room document")
	}
	rs := mutable.GetFields().GetFields()["rs"].GetMapValue().GetFields()
	if got := rs["bfmax"].GetIntegerValue(); got != 10 {
		t.Errorf("mutable bfmax = %d, want captured 10", got)
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
	if got := settings.GetFields().GetFields()["bfmax"].GetIntegerValue(); got != 7 {
		t.Errorf("__gs/m bfmax = %d, want captured 7", got)
	}
	inbox, ok := s.syntheticSessionDocument(caller, "docs/__pgn/All/__stu/us-alice")
	if !ok {
		t.Fatal("missing private-battle Pia inbox")
	}
	in := inbox.GetFields().GetFields()
	wantKeys("__stu", in, []string{"pl", "suid", "suscid", "susid", "sussid"})
	if in["pl"].GetBytesValue() == nil || len(in["pl"].GetBytesValue()) != 0 ||
		in["suscid"].GetIntegerValue() != 10001 || in["sussid"].GetIntegerValue() != 1 ||
		in["susid"].GetStringValue() != "us-alice" {
		t.Errorf("private-battle inbox = %v, want captured empty bytes and 10001/1", in)
	}
	status, _ := s.roomDocument(caller, "docs/__gs/s")
	wantKeys("__gs/s", status.GetFields().GetFields(), []string{"att", "ltc", "pgn", "st", "tn", "ucsid", "uid", "upcsid", "ussid"})
	if got := status.GetFields().GetFields()["uid"].GetStringValue(); got != "alice" {
		t.Errorf("__gs/s uid = %q", got)
	}
	_, _, err := s.docsFor(caller).apply([]*syncpb.WriteOperation{{OperationType: &syncpb.WriteOperation_MergeDocument{
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
	_, _, err = h.srv.docsFor(host).apply([]*syncpb.WriteOperation{
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
		_, _, err := h.srv.docsFor(host).apply([]*syncpb.WriteOperation{{OperationType: &syncpb.WriteOperation_MergeDocument{
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

func TestAMaskedRoomUpdateKeepsTheFieldsItDidNotName(t *testing.T) {
	s := &Server{docs: newStoreSet(time.Now)}
	caller := &session{details: &SessionDetails{
		UserID: "alice", SessionID: "room-id", Tenant: "t-dce9377b-lp1",
		Host: "example.invalid", Port: 443, ParticipantCount: 2, CreatedAt: time.Unix(1000, 0),
	}, userSession: "gameSessions/room-id/userSessions/us-alice"}
	boolean := func(b bool) *commonpb.Value {
		return &commonpb.Value{ValueType: &commonpb.Value_BooleanValue{BooleanValue: b}}
	}
	// What the console sends when the room locks.
	ops := []*syncpb.WriteOperation{{OperationType: &syncpb.WriteOperation_UpdateDocument{
		UpdateDocument: &syncpb.UpdateDocumentRequest{
			Document: &syncpb.Document{Name: "docs/__gs/m", Fields: &commonpb.MapValue{
				Fields: map[string]*commonpb.Value{"cp": boolean(false), "ebf": boolean(false), "ip": boolean(false)}}},
			UpdateMask: &fieldmaskpb.FieldMask{Paths: []string{"`ip`", "`ebf`", "`cp`"}},
		}}}}
	s.seedRoomDocuments(caller, ops)
	if _, _, err := s.docsFor(caller).apply(ops); err != nil {
		t.Fatalf("apply: %v", err)
	}
	stored, ok := s.docsFor(caller).get("docs/__gs/m")
	if !ok {
		t.Fatal("__gs/m was not stored")
	}
	fields := stored.GetFields().GetFields()
	for _, k := range []string{"bfmax", "bfmin", "prp", "pw"} {
		if fields[k] == nil {
			t.Errorf("%s was dropped by an update that did not name it", k)
		}
	}
	if fields["cp"].GetBooleanValue() || fields["ip"].GetBooleanValue() {
		t.Error("the masked fields were not applied")
	}
	if !stored.GetCreateTime().AsTime().Equal(time.Unix(1000, 0)) {
		t.Errorf("createTime = %v, want the room's", stored.GetCreateTime().AsTime())
	}
}

func TestAConsolesOwnUpcsidIncrementAnswersWithItsRank(t *testing.T) {
	h := newHarness(t)
	second, err := h.srv.newSession("bob", "userSessions/us-bob")
	if err != nil {
		t.Fatal(err)
	}
	second.details = &SessionDetails{UserID: "bob", SessionID: "m", Players: []string{"alice", "bob"}}
	iv := func(n int64) *commonpb.Value {
		return &commonpb.Value{ValueType: &commonpb.Value_IntegerValue{IntegerValue: n}}
	}
	// The console's own write when it joins.
	name := "docs/__pgn/All/__pus/us-bob"
	ops := []*syncpb.WriteOperation{{OperationType: &syncpb.WriteOperation_TransformDocument{
		TransformDocument: &syncpb.TransformDocumentRequest{Name: name, FieldTransforms: []*syncpb.FieldTransform{
			{FieldPath: "`upcsid`", TransformType: &syncpb.FieldTransform_LoadGlobalValue{LoadGlobalValue: "__upcsidn"}},
			{FieldPath: "`upcsid`", TransformType: &syncpb.FieldTransform_Maximum{Maximum: iv(20000)}},
			{FieldPath: "`upcsid`", TransformType: &syncpb.FieldTransform_Increment{Increment: iv(1)}},
			{FieldPath: "`upcsid`", TransformType: &syncpb.FieldTransform_StoreGlobalValue{StoreGlobalValue: "__upcsidn"}},
		}}}}}
	results, _, err := h.srv.docsFor(second).apply(ops)
	if err != nil {
		t.Fatal(err)
	}
	rankSessionCounter(second, ops, results)
	// Pia keeps this as its own upcsid and looks for itself among the __pus
	// documents, which carry the rank: anything else and it hosts alone.
	if got := results[0].GetTransformResult().GetResults()[2].GetIntegerValue(); got != 2 {
		t.Errorf("increment result = %d, want bob's rank 2", got)
	}
}

func TestAPrivateSalmonRunRoomGetsTheRoomShapeNotTheMatchmadeOne(t *testing.T) {
	s := &Server{docs: newStoreSet(time.Now)}
	caller := &session{details: &SessionDetails{
		UserID: "alice", SessionID: "room-id", Tenant: "t-dce9377b-lp1",
		Mode: "tenants/current/matchmakingConfigs/coop_private_config", Room: true,
		Host: "example.invalid", Port: 443, Capacity: 4, ParticipantCount: 1, CreatedAt: time.Unix(1000, 0),
	}, userSession: "gameSessions/room-id/userSessions/us-alice"}
	fixed, ok := s.roomDocument(caller, "docs/__gs/f")
	if !ok {
		t.Fatal("missing fixed room document")
	}
	f := fixed.GetFields().GetFields()
	if got := f["maxu"].GetIntegerValue(); got != 4 {
		t.Errorf("maxu = %d, want the room's own 4, not the matchmade 8", got)
	}
	if got := f["mcn"].GetStringValue(); got != "coop_private_config" {
		t.Errorf("mcn = %q, want the room's config name", got)
	}
}

func TestTheFirstPlayerSnapshotListsAPlayerWhoHasNotConnectedYet(t *testing.T) {
	h := newHarness(t)
	fast, err := h.srv.newSession("bob", "userSessions/us-bob")
	if err != nil {
		t.Fatal(err)
	}
	fast.details = &SessionDetails{
		UserID: "bob", SessionID: "m", Mode: "regular_match_config", Players: []string{"alice", "bob"},
		PlayerSessions: map[string]string{"alice": "us-alice", "bob": "us-bob"},
	}
	// alice is matched but slower: no gamesync session yet.
	docs := h.srv.collectionSnapshot(fast, "docs/__pgn/All/__pus")
	upcsid := map[string]int64{}
	for _, d := range docs {
		upcsid[d.GetName()] = d.GetFields().GetFields()["upcsid"].GetIntegerValue()
	}
	// Pia hosts when its own upcsid is the lowest it can see. Seeing only
	// itself, bob would host too.
	if got, ok := upcsid["docs/__pgn/All/__pus/us-alice"]; !ok || got != 1 {
		t.Errorf("alice's __pus = %d (present %v), want listed with rank 1 before she connects", got, ok)
	}
	if got := upcsid["docs/__pgn/All/__pus/us-bob"]; got != 2 {
		t.Errorf("bob's own upcsid = %d, want 2", got)
	}
}

// A Tricolor host starts once its __us list looks complete, so a player who
// has not connected stays out of it there; __pus still names them, so the
// host election sees everyone and only one console hosts.
func TestAPublicLobbyListsOnlyConnectedPlayersAsReady(t *testing.T) {
	for _, mode := range []string{"fest_match_tricolor_oneshot_config", "coop_bigrun_config", "regular_match_config"} {
		t.Run(mode, func(t *testing.T) {
			h := newHarness(t)
			fast, err := h.srv.newSession("bob", "userSessions/us-bob")
			if err != nil {
				t.Fatal(err)
			}
			fast.details = &SessionDetails{
				UserID: "bob", SessionID: "m", Mode: mode, Players: []string{"alice", "bob"},
				PlayerSessions: map[string]string{"alice": "us-alice", "bob": "us-bob"},
			}
			names := func(collection string) map[string]bool {
				out := map[string]bool{}
				for _, d := range h.srv.collectionSnapshot(fast, collection) {
					out[d.GetName()] = true
				}
				return out
			}
			if names("docs/__us")["docs/__us/us-alice"] {
				t.Error("alice is in the Tricolor __us list before she connects")
			}
			if !names("docs/__pgn/All/__pus")["docs/__pgn/All/__pus/us-alice"] {
				t.Error("alice is missing from __pus, so a second console could host")
			}
			late, err := h.srv.newSession("alice", "userSessions/us-alice")
			if err != nil {
				t.Fatal(err)
			}
			late.details = fast.details
			if !names("docs/__us")["docs/__us/us-alice"] {
				t.Fatal("connected player never entered readiness roster")
			}
		})
	}
}
