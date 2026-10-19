package toyohr

import (
	"context"
	"strings"
	"testing"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/n-popescu/grid0plus/internal/ugcstore"
	commonpb "github.com/n-popescu/grid0plus/proto/common"
	toyohrpb "github.com/n-popescu/grid0plus/proto/toyohr/v1"
	ugcpb "github.com/n-popescu/grid0plus/proto/ugcstore/v1"
)

// TestCanolaAndLockerSelectDocumentsAreEmpty covers the reasoning in
// backlog.go's package comment: GRID0+ has no UGC document store, so an
// honest query finds nothing, for either service.
func TestCanolaAndLockerSelectDocumentsAreEmpty(t *testing.T) {
	svc, _ := newService(t, time.Now())

	canola, err := canolaService{Service: svc}.SelectDocuments(context.Background(), &toyohrpb.SelectDocumentsRequest{})
	if err != nil {
		t.Fatalf("Canola.SelectDocuments: %v", err)
	}
	if len(canola.GetDocuments()) != 0 {
		t.Errorf("Canola.SelectDocuments = %v, want none", canola.GetDocuments())
	}

	locker, err := lockerService{Service: svc}.SelectDocuments(context.Background(), &toyohrpb.SelectDocumentsRequest{})
	if err != nil {
		t.Fatalf("Locker.SelectDocuments: %v", err)
	}
	if len(locker.GetDocuments()) != 0 {
		t.Errorf("Locker.SelectDocuments = %v, want none", locker.GetDocuments())
	}
}

func TestCanolaAndLockerIkasuDocumentIsNotFound(t *testing.T) {
	svc, _ := newService(t, time.Now())

	if _, err := (canolaService{Service: svc}).IkasuDocument(context.Background(), &toyohrpb.IkasuDocumentRequest{}); status.Code(err) != codes.NotFound {
		t.Errorf("Canola.IkasuDocument = %v, want NotFound", err)
	}
	if _, err := (lockerService{Service: svc}).IkasuDocument(context.Background(), &toyohrpb.IkasuDocumentRequest{}); status.Code(err) != codes.NotFound {
		t.Errorf("Locker.IkasuDocument = %v, want NotFound", err)
	}
}

func TestAnUnissuedCodeIsNotFound(t *testing.T) {
	svc, _ := newService(t, time.Now())

	if _, err := (coopScenarioService{Service: svc}).ResolveCoopScenarioCode(
		context.Background(), &toyohrpb.ResolveCoopScenarioCodeRequest{Name: "ABCDEF"},
	); status.Code(err) != codes.NotFound {
		t.Errorf("ResolveCoopScenarioCode = %v, want NotFound", err)
	}
	if _, err := (replayService{Service: svc}).ResolveReplayCode(
		context.Background(), &toyohrpb.ResolveReplayCodeRequest{Name: "ABCDEF"},
	); status.Code(err) != codes.NotFound {
		t.Errorf("ResolveReplayCode = %v, want NotFound", err)
	}
}

// TestASharedScenarioCodeResolvesToTheJob: the code another player types
// in, with dashes and in lower case, hands back the job document that was
// registered. Unimplemented, the console showed an error at the end of
// sharing (2026-10-08).
func TestASharedScenarioCodeResolvesToTheJob(t *testing.T) {
	svc, _ := newService(t, time.Now())
	svc.docs = ugcstore.NewService(ugcstore.Options{
		Store: svc.store, Log: discardLogger(), Tenant: testTenant,
		CallerID: func(context.Context) (string, bool) { return testCaller, true },
		Now:      time.Now,
	})
	ctx := context.Background()
	job := "tenants/current/documents/services/GameRecord/users/" + testCaller + "/CoopResults/job1"
	if _, err := svc.docs.Put(ctx, testCaller, job, &commonpb.MapValue{Fields: map[string]*commonpb.Value{
		"wave": {ValueType: &commonpb.Value_IntegerValue{IntegerValue: 3}},
	}}); err != nil {
		t.Fatal(err)
	}
	reg, err := (coopScenarioService{Service: svc}).RegisterDocument(ctx,
		&toyohrpb.CoopScenarioRegisterDocumentRequest{Name: job})
	if err != nil {
		t.Fatalf("RegisterDocument: %v", err)
	}
	code := reg.GetCode().GetName()[strings.LastIndex(reg.GetCode().GetName(), "/")+1:]
	if len(code) != 16 || code[0] != 'S' {
		t.Fatalf("code = %q", code)
	}
	typed := strings.ToLower(code[:4] + "-" + code[4:8] + "-" + code[8:12] + "-" + code[12:])
	got, err := (coopScenarioService{Service: svc}).ResolveCoopScenarioCode(ctx,
		&toyohrpb.ResolveCoopScenarioCodeRequest{Name: typed})
	if err != nil {
		t.Fatalf("resolve %q: %v", typed, err)
	}
	if got.GetDocument().GetFields().GetFields()["wave"].GetIntegerValue() != 3 {
		t.Errorf("resolved %v, want the registered job", got.GetDocument())
	}
}

// TestFulfillReservationsIsEmptyNotAnError draws the same distinction
// ListDeviceAccounts (empty list) and GetDeviceAccount (NotFound) already
// draw in internal/db: a query over many things that happen to be zero is
// not the same case as one specific thing that does not exist.
func TestFulfillReservationsIsEmptyNotAnError(t *testing.T) {
	now := time.Date(2026, 9, 5, 12, 0, 0, 0, time.UTC)
	svc, _ := newService(t, now)

	resp, err := (replayService{Service: svc}).FulfillReservations(
		context.Background(), &toyohrpb.FulfillReservationsRequest{})
	if err != nil {
		t.Fatalf("FulfillReservations: %v", err)
	}
	if len(resp.GetDocument()) != 0 {
		t.Errorf("document = %v, want none", resp.GetDocument())
	}
	if !resp.GetTimestamp().AsTime().Equal(now) {
		t.Errorf("timestamp = %s, want %s", resp.GetTimestamp().AsTime(), now)
	}
}

// TestGameRecordWithoutADocumentStoreIsUnimplemented: every GameRecord call
// that GRID0+ answers stores a document, so a service built without a
// document store says Unimplemented rather than pretending.
func TestGameRecordWithoutADocumentStoreIsUnimplemented(t *testing.T) {
	svc, _ := newService(t, time.Now())
	g := gameRecordService{Service: svc}

	if _, err := g.InitializeAttributes(context.Background(), &toyohrpb.InitializeAttributesRequest{}); status.Code(err) != codes.Unimplemented {
		t.Errorf("InitializeAttributes = %v, want Unimplemented", err)
	}
	if _, err := g.CreateBankaraChallenge(context.Background(), &toyohrpb.CreateBankaraChallengeRequest{}); status.Code(err) != codes.Unimplemented {
		t.Errorf("CreateBankaraChallenge = %v, want Unimplemented", err)
	}
	if _, err := g.CreateFestPowerMeasurement(context.Background(), &toyohrpb.CreateFestPowerMeasurementRequest{}); status.Code(err) != codes.Unimplemented {
		t.Errorf("CreateFestPowerMeasurement = %v, want Unimplemented", err)
	}
}

// TestThePlazaShowsEachPlayersNewestPublishedLocker: a locker post a player
// published must reach other consoles' plaza, once per player however often
// they republished, and a drawing must not show up among the lockers.
func TestThePlazaShowsEachPlayersNewestPublishedLocker(t *testing.T) {
	svc, _ := newService(t, time.Now())
	clock := time.Date(2026, 10, 11, 12, 0, 0, 0, time.UTC)
	svc.docs = ugcstore.NewService(ugcstore.Options{
		Store: svc.store, Log: discardLogger(), Tenant: testTenant,
		CallerID: func(context.Context) (string, bool) { return testCaller, true },
		Now:      func() time.Time { clock = clock.Add(time.Second); return clock },
	})
	ctx := context.Background()
	post := func(owner, path string, v int64) {
		t.Helper()
		if _, err := svc.docs.Put(ctx, owner, "tenants/current/documents/services/toyohr/interfaces/"+path,
			&commonpb.MapValue{Fields: map[string]*commonpb.Value{
				"v": {ValueType: &commonpb.Value_IntegerValue{IntegerValue: v}},
			}}); err != nil {
			t.Fatal(err)
		}
	}
	post("alice", "locker/lockerPosts/a1", 1)
	post("bob", "locker/lockerPosts/b1", 2)
	post("alice", "locker/lockerPosts/a2", 3)
	post("alice", "canola/picturePosts/p1", 4)

	resp, err := (lockerService{Service: svc}).SelectDocuments(ctx, &toyohrpb.SelectDocumentsRequest{})
	if err != nil {
		t.Fatalf("SelectDocuments: %v", err)
	}
	var got []int64
	for _, d := range resp.GetDocuments() {
		got = append(got, d.GetFields().GetFields()["v"].GetIntegerValue())
	}
	if len(got) != 2 || got[0] != 3 || got[1] != 2 {
		t.Errorf("plaza lockers = %v, want [3 2]: alice's newest, then bob's", got)
	}

	// A "fresh!" on a post the plaza showed is acknowledged, in either tenant
	// spelling; one on anything that is not a locker post is not.
	locker := lockerService{Service: svc}
	for _, name := range []string{
		resp.GetDocuments()[0].GetName(),
		"tenants/current/documents/services/toyohr/interfaces/locker/lockerPosts/b1",
	} {
		if _, err := locker.IkasuDocument(ctx, &toyohrpb.IkasuDocumentRequest{Name: name}); err != nil {
			t.Errorf("IkasuDocument(%s): %v", name, err)
		}
	}
	for _, name := range []string{
		"tenants/current/documents/services/toyohr/interfaces/locker/lockerPosts/nope",
		"tenants/current/documents/services/toyohr/interfaces/canola/picturePosts/p1",
	} {
		if _, err := locker.IkasuDocument(ctx, &toyohrpb.IkasuDocumentRequest{Name: name}); status.Code(err) != codes.NotFound {
			t.Errorf("IkasuDocument(%s) = %v, want NotFound", name, err)
		}
	}
}

// TestARequestNamingPlayersGetsTheirPosts: when the game asks for specific
// players (type Normal3, UIDsInOrder) to draw their name plates, it must get
// exactly those players' newest posts in the order asked, not the plaza.
func TestARequestNamingPlayersGetsTheirPosts(t *testing.T) {
	svc, _ := newService(t, time.Now())
	clock := time.Date(2026, 10, 11, 12, 0, 0, 0, time.UTC)
	svc.docs = ugcstore.NewService(ugcstore.Options{
		Store: svc.store, Log: discardLogger(), Tenant: testTenant,
		CallerID: func(context.Context) (string, bool) { return testCaller, true },
		Now:      func() time.Time { clock = clock.Add(time.Second); return clock },
	})
	ctx := context.Background()
	str := func(v string) *commonpb.Value {
		return &commonpb.Value{ValueType: &commonpb.Value_StringValue{StringValue: v}}
	}
	post := func(owner, id, uid string, v int64) {
		t.Helper()
		if _, err := svc.docs.Put(ctx, owner, "tenants/current/documents/services/toyohr/interfaces/canola/deckPosts/"+id,
			&commonpb.MapValue{Fields: map[string]*commonpb.Value{
				"UID": str(uid),
				"v":   {ValueType: &commonpb.Value_IntegerValue{IntegerValue: v}},
			}}); err != nil {
			t.Fatal(err)
		}
	}
	post("alice", "a1", "u-alice", 1)
	post("bob", "b1", "u-bob", 2)
	post("alice", "a2", "u-alice", 3)
	post("carol", "c1", "u-carol", 4)

	// As a console sent it on grid0: UIDsInOrder present but empty, the
	// players in UIDs as this server's own user ids (the posts' owners).
	list := &commonpb.Value{ValueType: &commonpb.Value_ArrayValue{ArrayValue: &commonpb.ArrayValue{
		Values: []*commonpb.Value{str("bob"), str("nobody"), str("u-alice")}}}}
	empty := &commonpb.Value{ValueType: &commonpb.Value_ArrayValue{ArrayValue: &commonpb.ArrayValue{}}}
	resp, err := (canolaService{Service: svc}).SelectDocuments(ctx, &toyohrpb.SelectDocumentsRequest{
		Type:   "Normal3",
		Params: &commonpb.MapValue{Fields: map[string]*commonpb.Value{"UIDs": list, "UIDsInOrder": empty, "FestRegion": str("JP")}},
	})
	if err != nil {
		t.Fatalf("SelectDocuments: %v", err)
	}
	var got []int64
	for _, d := range resp.GetDocuments() {
		got = append(got, d.GetFields().GetFields()["v"].GetIntegerValue())
	}
	if len(got) != 2 || got[0] != 2 || got[1] != 3 {
		t.Errorf("posts = %v, want [2 3]: bob's, then alice's newest; carol was not asked for", got)
	}
}

// TestTheFestPlazaShowsThisFestsDrawings: the fest plaza's Canola request
// (type Fest, FestID) must get drawings posted during that fest -- not card
// decks, not another fest's drawings, not ones their player hid.
func TestTheFestPlazaShowsThisFestsDrawings(t *testing.T) {
	svc, _ := newService(t, time.Now())
	clock := time.Date(2026, 10, 11, 12, 0, 0, 0, time.UTC)
	svc.docs = ugcstore.NewService(ugcstore.Options{
		Store: svc.store, Log: discardLogger(), Tenant: testTenant,
		CallerID: func(context.Context) (string, bool) { return testCaller, true },
		Now:      func() time.Time { clock = clock.Add(time.Second); return clock },
	})
	ctx := context.Background()
	str := func(v string) *commonpb.Value {
		return &commonpb.Value{ValueType: &commonpb.Value_StringValue{StringValue: v}}
	}
	drawing := func(owner, id, postType, fest string, hidden bool) {
		t.Helper()
		pic := &commonpb.MapValue{Fields: map[string]*commonpb.Value{
			"PostType": str(postType), "FestID": str(fest),
			"IsHidden": {ValueType: &commonpb.Value_BooleanValue{BooleanValue: hidden}},
		}}
		if _, err := svc.docs.Put(ctx, owner, "tenants/current/documents/services/toyohr/interfaces/canola/picturePosts/"+id,
			&commonpb.MapValue{Fields: map[string]*commonpb.Value{
				"Picture": {ValueType: &commonpb.Value_MapValue{MapValue: pic}},
			}}); err != nil {
			t.Fatal(err)
		}
	}
	drawing("alice", "this-fest", "Fest", "JUEA-90003", false)
	drawing("bob", "other-fest", "Fest", "JUEA-90002", false)
	drawing("carol", "normal", "Normal", "", false)
	drawing("dave", "hidden", "Fest", "JUEA-90003", true)
	if _, err := svc.docs.Put(ctx, "erin", "tenants/current/documents/services/toyohr/interfaces/canola/deckPosts/d1",
		&commonpb.MapValue{Fields: map[string]*commonpb.Value{"UID": str("erin")}}); err != nil {
		t.Fatal(err)
	}

	resp, err := (canolaService{Service: svc}).SelectDocuments(ctx, &toyohrpb.SelectDocumentsRequest{
		Type:   "Fest",
		Params: &commonpb.MapValue{Fields: map[string]*commonpb.Value{"FestID": str("JUEA-90003"), "FestRegion": str("JP")}},
	})
	if err != nil {
		t.Fatalf("SelectDocuments: %v", err)
	}
	var got []string
	for _, d := range resp.GetDocuments() {
		got = append(got, d.GetName()[strings.LastIndex(d.GetName(), "/")+1:])
	}
	if len(got) != 1 || got[0] != "this-fest" {
		t.Errorf("fest plaza = %v, want only [this-fest]", got)
	}
}

// TestASharedReplayCodeResolvesToTheReplay: a replay code a player shares
// must bring up that replay for whoever types it in, and a scenario code
// must not resolve as a replay.
func TestASharedReplayCodeResolvesToTheReplay(t *testing.T) {
	svc, _ := newService(t, time.Now())
	svc.docs = ugcstore.NewService(ugcstore.Options{
		Store: svc.store, Log: discardLogger(), Tenant: testTenant,
		CallerID: func(context.Context) (string, bool) { return testCaller, true },
		Now:      time.Now,
	})
	ctx := context.Background()
	replay := "tenants/current/documents/services/toyohr/interfaces/replay/replayPosts/r1"
	if _, err := svc.docs.Put(ctx, testCaller, replay, &commonpb.MapValue{Fields: map[string]*commonpb.Value{
		"stage": {ValueType: &commonpb.Value_IntegerValue{IntegerValue: 7}},
	}}); err != nil {
		t.Fatal(err)
	}
	reg, err := (replayService{Service: svc}).RegisterDocument(ctx, &toyohrpb.ReplayRegisterDocumentRequest{Name: replay})
	if err != nil {
		t.Fatalf("RegisterDocument: %v", err)
	}
	code := reg.GetCode().GetName()[strings.LastIndex(reg.GetCode().GetName(), "/")+1:]
	if len(code) != 16 || code[0] != 'R' {
		t.Fatalf("code = %q", code)
	}
	got, err := (replayService{Service: svc}).ResolveReplayCode(ctx, &toyohrpb.ResolveReplayCodeRequest{Name: code})
	if err != nil || got.GetDocument().GetFields().GetFields()["stage"].GetIntegerValue() != 7 {
		t.Fatalf("resolve: %v, %v", got.GetDocument(), err)
	}
	if _, err := (coopScenarioService{Service: svc}).ResolveCoopScenarioCode(ctx,
		&toyohrpb.ResolveCoopScenarioCodeRequest{Name: code}); status.Code(err) != codes.NotFound {
		t.Errorf("a replay code resolved as a scenario: %v", err)
	}
}

// TestALockerRequestAnswersEveryPlayerEvenWithNoLockers: the lobby's locker
// request must get the caller's own entry first and one entry per named
// player, empty where nobody published. An empty answer leaves the game's
// locker data unready, which keeps the locker room shut and the lobby's big
// screen dark for everyone (2026-10-19).
func TestALockerRequestAnswersEveryPlayerEvenWithNoLockers(t *testing.T) {
	svc, _ := newService(t, time.Now())
	svc.docs = ugcstore.NewService(ugcstore.Options{
		Store: svc.store, Log: discardLogger(), Tenant: testTenant,
		CallerID: func(context.Context) (string, bool) { return testCaller, true },
	})
	ctx := context.Background()
	if _, err := svc.docs.Put(ctx, "bob", "tenants/current/documents/services/toyohr/interfaces/locker/lockerPosts/b1",
		&commonpb.MapValue{Fields: map[string]*commonpb.Value{
			"v": {ValueType: &commonpb.Value_IntegerValue{IntegerValue: 7}},
		}}); err != nil {
		t.Fatal(err)
	}
	ids := func(v ...string) *commonpb.Value {
		var out []*commonpb.Value
		for _, id := range v {
			out = append(out, &commonpb.Value{ValueType: &commonpb.Value_StringValue{StringValue: id}})
		}
		return &commonpb.Value{ValueType: &commonpb.Value_ArrayValue{ArrayValue: &commonpb.ArrayValue{Values: out}}}
	}
	ask := func(uids *commonpb.Value) []*ugcpb.Document {
		t.Helper()
		resp, err := (lockerService{Service: svc}).SelectDocuments(ctx, &toyohrpb.SelectDocumentsRequest{
			Type:   "Normal3",
			Params: &commonpb.MapValue{Fields: map[string]*commonpb.Value{"UIDs": uids, "UIDsInOrder": ids()}},
		})
		if err != nil {
			t.Fatalf("SelectDocuments: %v", err)
		}
		return resp.GetDocuments()
	}

	got := ask(ids("alice", "bob", testCaller))
	if len(got) != 3 {
		t.Fatalf("got %d documents, want 3: the caller, alice, bob", len(got))
	}
	if got[0].GetFields() != nil || got[1].GetFields() != nil {
		t.Errorf("the caller and alice have no locker, want empty documents, got %v, %v", got[0], got[1])
	}
	if got[2].GetFields().GetFields()["v"].GetIntegerValue() != 7 {
		t.Errorf("third entry = %v, want bob's locker", got[2])
	}

	if got := ask(ids()); len(got) != 1 || got[0].GetFields() != nil {
		t.Errorf("no named players = %v, want only the caller's empty entry", got)
	}
}
