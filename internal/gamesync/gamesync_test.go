package gamesync

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/fieldmaskpb"

	commonpb "github.com/n-popescu/grid0plus/proto/common"
	syncpb "github.com/n-popescu/grid0plus/proto/gamesync/v1"
)

// harness runs the service over an in-process listener with a clock the test
// controls.
//
// bufconn rather than a real port: these tests drive it through a real gRPC
// client over a real connection, so metadata, status codes and bidirectional
// streaming behave exactly as they would on the wire.
type harness struct {
	t      *testing.T
	srv    *Server
	client syncpb.GamesyncClient
	now    time.Time
}

// idTokens maps a fake id token onto the player it names, standing in for
// internal/npln's verifier.
var idTokens = map[string]string{
	"token-alice": "alice",
	"token-bob":   "bob",
}

func newHarness(t *testing.T) *harness {
	return newHarnessWithSigner(t, nil)
}

func newHarnessWithSigner(t *testing.T, issue func(string, string, time.Time) (string, error)) *harness {
	t.Helper()

	h := &harness{t: t, now: time.Date(2026, 8, 28, 12, 0, 0, 0, time.UTC)}

	srv, err := New(Options{
		Log: slog.New(slog.NewTextHandler(io.Discard, nil)),
		VerifyIDToken: func(token string) (string, error) {
			if user, ok := idTokens[token]; ok {
				return user, nil
			}
			return "", errors.New("unknown token")
		},
		Now:              func() time.Time { return h.now },
		IssueAccessToken: issue,
	})
	if err != nil {
		t.Fatalf("new: %v", err)
	}
	h.srv = srv

	lis := bufconn.Listen(1 << 20)
	go func() { _ = srv.Serve(lis) }()

	conn, err := grpc.NewClient("passthrough:///bufnet",
		grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) {
			return lis.DialContext(ctx)
		}),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	h.client = syncpb.NewGamesyncClient(conn)

	t.Cleanup(func() {
		_ = conn.Close()
		srv.Stop()
		_ = lis.Close()
	})
	return h
}

// session issues a gamesync session for a player and returns a call context.
func (h *harness) session(idToken string) context.Context {
	h.t.Helper()
	resp, err := h.client.IssueToken(context.Background(), &syncpb.IssueTokenRequest{
		MatchmakingIdToken: idToken,
		UserSession:        "userSessions/" + idToken,
	})
	if err != nil {
		h.t.Fatalf("issue token: %v", err)
	}
	return h.authed(resp.GetToken().GetAccessToken())
}

func (h *harness) authed(accessToken string) context.Context {
	return metadata.NewOutgoingContext(context.Background(),
		metadata.Pairs(mdAuthorization, "Bearer "+accessToken))
}

func intValue(n int64) *commonpb.Value {
	return &commonpb.Value{ValueType: &commonpb.Value_IntegerValue{IntegerValue: n}}
}

func stringValue(s string) *commonpb.Value {
	return &commonpb.Value{ValueType: &commonpb.Value_StringValue{StringValue: s}}
}

func doc(name string, fields map[string]*commonpb.Value) *syncpb.Document {
	return &syncpb.Document{Name: name, Fields: &commonpb.MapValue{Fields: fields}}
}

func merge(d *syncpb.Document) *syncpb.WriteOperation {
	return &syncpb.WriteOperation{
		OperationType: &syncpb.WriteOperation_MergeDocument{
			MergeDocument: &syncpb.MergeDocumentRequest{Document: d},
		},
	}
}

// ---- authentication -------------------------------------------------------

func TestAServiceWithNoVerifierRefusesToStart(t *testing.T) {
	// A service that cannot identify a caller and starts anyway is a service
	// that will eventually answer one player's lobby with another's.
	if _, err := New(Options{}); err == nil {
		t.Fatal("a service with no token verifier was accepted")
	}
}

func TestAnUnknownIdTokenBuysNoSession(t *testing.T) {
	h := newHarness(t)

	_, err := h.client.IssueToken(context.Background(), &syncpb.IssueTokenRequest{
		MatchmakingIdToken: "not-a-token",
	})
	if status.Code(err) != codes.Unauthenticated {
		t.Errorf("an unverifiable id token gave %v, want Unauthenticated", err)
	}
}

func TestASignedAccessTokenProbeStillAuthorizesTheNextCall(t *testing.T) {
	h := newHarnessWithSigner(t, func(userID, userSession string, expiresAt time.Time) (string, error) {
		if userID != "alice" || userSession != "gameSessions/room/userSessions/alice" || expiresAt.IsZero() {
			t.Errorf("signer received user=%q session=%q expiration=%s", userID, userSession, expiresAt)
		}
		return "header.payload.signature", nil
	})
	issued, err := h.client.IssueToken(context.Background(), &syncpb.IssueTokenRequest{
		MatchmakingIdToken: "token-alice",
		UserSession:        "gameSessions/room/userSessions/alice",
	})
	if err != nil {
		t.Fatal(err)
	}
	if issued.GetToken().GetAccessToken() != "header.payload.signature" {
		t.Fatalf("returned access token = %q", issued.GetToken().GetAccessToken())
	}
	_, err = h.client.GetDocument(h.authed(issued.GetToken().GetAccessToken()), &syncpb.GetDocumentRequest{Name: "documents/missing"})
	if status.Code(err) != codes.NotFound {
		t.Errorf("signed probe token did not authenticate the following call: %v", err)
	}
}

func TestEveryOtherRPCNeedsAnAccessToken(t *testing.T) {
	h := newHarness(t)

	// An allowlist, not a denylist: a new RPC is authenticated until somebody
	// says otherwise.
	_, err := h.client.GetDocument(context.Background(),
		&syncpb.GetDocumentRequest{Name: "lobbies/1/players/alice"})
	if status.Code(err) != codes.Unauthenticated {
		t.Errorf("reading with no token gave %v, want Unauthenticated", err)
	}

	_, err = h.client.GetDocument(h.authed("made-up"),
		&syncpb.GetDocumentRequest{Name: "lobbies/1/players/alice"})
	if status.Code(err) != codes.Unauthenticated {
		t.Errorf("reading with an invented token gave %v, want Unauthenticated", err)
	}
}

func TestAnExpiredSessionStopsWorking(t *testing.T) {
	h := newHarness(t)
	alice := h.session("token-alice")

	if _, err := h.client.ListDocuments(alice, &syncpb.ListDocumentsRequest{}); err != nil {
		t.Fatalf("list while the session is fresh: %v", err)
	}

	h.now = h.now.Add(2 * defaultSessionTTL)
	_, err := h.client.ListDocuments(alice, &syncpb.ListDocumentsRequest{})
	if status.Code(err) != codes.Unauthenticated {
		t.Errorf("an expired session gave %v, want Unauthenticated", err)
	}
}

func TestARefreshTokenIsSingleUse(t *testing.T) {
	h := newHarness(t)

	first, err := h.client.IssueToken(context.Background(), &syncpb.IssueTokenRequest{
		MatchmakingIdToken: "token-alice",
	})
	if err != nil {
		t.Fatalf("issue: %v", err)
	}
	refresh := first.GetToken().GetRefreshToken()

	second, err := h.client.RefreshToken(context.Background(),
		&syncpb.RefreshTokenRequest{RefreshToken: refresh})
	if err != nil {
		t.Fatalf("refresh: %v", err)
	}
	if second.GetToken().GetAccessToken() == first.GetToken().GetAccessToken() {
		t.Error("refreshing reused the same access token")
	}

	// Single-use, so a leaked refresh token stops working the moment the
	// legitimate console uses it.
	_, err = h.client.RefreshToken(context.Background(),
		&syncpb.RefreshTokenRequest{RefreshToken: refresh})
	if status.Code(err) != codes.Unauthenticated {
		t.Errorf("reusing a refresh token gave %v, want Unauthenticated", err)
	}

	// And the old access token went with it.
	_, err = h.client.ListDocuments(h.authed(first.GetToken().GetAccessToken()),
		&syncpb.ListDocumentsRequest{})
	if status.Code(err) != codes.Unauthenticated {
		t.Errorf("the replaced access token still works: %v", err)
	}
}

// ---- documents ------------------------------------------------------------

func TestAWrittenDocumentCanBeReadBack(t *testing.T) {
	h := newHarness(t)
	alice := h.session("token-alice")

	resp, err := h.client.WriteDocuments(alice, &syncpb.WriteDocumentsRequest{
		WriteOperations: []*syncpb.WriteOperation{
			merge(doc("lobbies/1/players/alice", map[string]*commonpb.Value{
				"ready":  intValue(1),
				"weapon": stringValue("splattershot"),
			})),
		},
	})
	if err != nil {
		t.Fatalf("write: %v", err)
	}
	if resp.GetCommitTime() == nil {
		t.Error("no commit time, so a client cannot order its writes")
	}

	got, err := h.client.GetDocument(alice,
		&syncpb.GetDocumentRequest{Name: "lobbies/1/players/alice"})
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.GetFields().GetFields()["weapon"].GetStringValue() != "splattershot" {
		t.Errorf("read back %v", got.GetFields())
	}
	if got.GetCreateTime() == nil || got.GetUpdateTime() == nil {
		t.Error("a stored document has no timestamps, so a precondition on update_time cannot work")
	}
}

func TestReadingSeveralDocumentsReportsTheMissingOnesRatherThanOmittingThem(t *testing.T) {
	h := newHarness(t)
	alice := h.session("token-alice")

	if _, err := h.client.WriteDocuments(alice, &syncpb.WriteDocumentsRequest{
		WriteOperations: []*syncpb.WriteOperation{
			merge(doc("lobbies/1/players/alice", map[string]*commonpb.Value{"ready": intValue(1)})),
		},
	}); err != nil {
		t.Fatalf("write: %v", err)
	}

	resp, err := h.client.ReadDocuments(alice, &syncpb.ReadDocumentsRequest{
		Documents: []string{"lobbies/1/players/alice", "lobbies/1/players/bob"},
	})
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if len(resp.GetResults()) != 2 {
		t.Fatalf("got %d results for 2 documents", len(resp.GetResults()))
	}
	// A short answer means the client cannot tell "absent" from "the server did
	// not look".
	if resp.GetResults()[0].GetFound() == nil {
		t.Error("the document that exists came back as missing")
	}
	if resp.GetResults()[1].GetMissing() != "lobbies/1/players/bob" {
		t.Errorf("the absent document is reported as %v, want missing by name",
			resp.GetResults()[1].GetResult())
	}
}

func TestListingACollectionReturnsItsDirectChildrenOnly(t *testing.T) {
	h := newHarness(t)
	alice := h.session("token-alice")

	if _, err := h.client.WriteDocuments(alice, &syncpb.WriteDocumentsRequest{
		WriteOperations: []*syncpb.WriteOperation{
			merge(doc("lobbies/1/players/alice", map[string]*commonpb.Value{"ready": intValue(1)})),
			merge(doc("lobbies/1/players/bob", map[string]*commonpb.Value{"ready": intValue(0)})),
			merge(doc("lobbies/1/players/bob/gear/hat", map[string]*commonpb.Value{"id": intValue(7)})),
			merge(doc("lobbies/2/players/carol", map[string]*commonpb.Value{"ready": intValue(1)})),
		},
	}); err != nil {
		t.Fatalf("write: %v", err)
	}

	resp, err := h.client.ListDocuments(alice,
		&syncpb.ListDocumentsRequest{Parent: "lobbies/1/players"})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	// A recursive list would return a lobby's whole subtree to a client that
	// asked for one level, and the client cannot tell from the response.
	if len(resp.GetDocuments()) != 2 {
		var names []string
		for _, d := range resp.GetDocuments() {
			names = append(names, d.GetName())
		}
		t.Fatalf("listed %v, want the two direct children", names)
	}
	// Deterministic order: a list that reorders between identical calls makes a
	// client's diff show changes that did not happen.
	if resp.GetDocuments()[0].GetName() != "lobbies/1/players/alice" {
		t.Errorf("first document is %q, want alice", resp.GetDocuments()[0].GetName())
	}

	ids, err := h.client.QueryCollectionIds(alice,
		&syncpb.QueryCollectionIdsRequest{Document: "lobbies/1/players/bob"})
	if err != nil {
		t.Fatalf("query collections: %v", err)
	}
	if len(ids.GetCollectionIds()) != 1 || ids.GetCollectionIds()[0] != "gear" {
		t.Errorf("collections = %v, want [gear]", ids.GetCollectionIds())
	}
}

func TestAReadMaskDropsTheFieldsItDoesNotName(t *testing.T) {
	h := newHarness(t)
	alice := h.session("token-alice")

	if _, err := h.client.WriteDocuments(alice, &syncpb.WriteDocumentsRequest{
		WriteOperations: []*syncpb.WriteOperation{
			merge(doc("lobbies/1/players/alice", map[string]*commonpb.Value{
				"ready":  intValue(1),
				"weapon": stringValue("splattershot"),
			})),
		},
	}); err != nil {
		t.Fatalf("write: %v", err)
	}

	got, err := h.client.GetDocument(alice, &syncpb.GetDocumentRequest{
		Name:     "lobbies/1/players/alice",
		ReadMask: fieldMask("ready"),
	})
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	fields := got.GetFields().GetFields()
	if _, ok := fields["weapon"]; ok {
		t.Error("a field the mask did not name came back")
	}
	if _, ok := fields["ready"]; !ok {
		t.Error("the field the mask named did not come back")
	}

	// And masking must not have mutated the stored document -- the tree is
	// shared between every console in the lobby.
	full, err := h.client.GetDocument(alice,
		&syncpb.GetDocumentRequest{Name: "lobbies/1/players/alice"})
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if _, ok := full.GetFields().GetFields()["weapon"]; !ok {
		t.Error("a masked read deleted a field from the store")
	}
}

// ---- atomicity and preconditions ------------------------------------------

func TestABatchWithAFailingPreconditionChangesNothing(t *testing.T) {
	h := newHarness(t)
	alice := h.session("token-alice")

	_, err := h.client.WriteDocuments(alice, &syncpb.WriteDocumentsRequest{
		WriteOperations: []*syncpb.WriteOperation{
			merge(doc("lobbies/1/state", map[string]*commonpb.Value{"phase": stringValue("waiting")})),
			{
				// This one cannot succeed: the document does not exist.
				OperationType: &syncpb.WriteOperation_UpdateDocument{
					UpdateDocument: &syncpb.UpdateDocumentRequest{
						Document: doc("lobbies/1/missing", map[string]*commonpb.Value{"x": intValue(1)}),
						CurrentDocument: &syncpb.Precondition{
							ConditionType: &syncpb.Precondition_Exists{Exists: true},
						},
					},
				},
			},
		},
	})
	if status.Code(err) != codes.FailedPrecondition {
		t.Fatalf("the batch gave %v, want FailedPrecondition", err)
	}

	// A half-applied batch leaves the client's next read showing a state
	// neither side intended, with no way to tell which half landed.
	_, err = h.client.GetDocument(alice, &syncpb.GetDocumentRequest{Name: "lobbies/1/state"})
	if status.Code(err) != codes.NotFound {
		t.Errorf("the first write in a failed batch was applied: %v", err)
	}
}

func TestTwoConsolesUpdatingTheSameDocumentDoNotBothWin(t *testing.T) {
	h := newHarness(t)
	alice := h.session("token-alice")
	bob := h.session("token-bob")

	if _, err := h.client.WriteDocuments(alice, &syncpb.WriteDocumentsRequest{
		WriteOperations: []*syncpb.WriteOperation{
			merge(doc("lobbies/1/state", map[string]*commonpb.Value{"phase": stringValue("waiting")})),
		},
	}); err != nil {
		t.Fatalf("write: %v", err)
	}

	// Both read the same version.
	read, err := h.client.GetDocument(alice, &syncpb.GetDocumentRequest{Name: "lobbies/1/state"})
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	version := read.GetUpdateTime()

	guarded := func(phase string) *syncpb.WriteDocumentsRequest {
		return &syncpb.WriteDocumentsRequest{
			WriteOperations: []*syncpb.WriteOperation{{
				OperationType: &syncpb.WriteOperation_UpdateDocument{
					UpdateDocument: &syncpb.UpdateDocumentRequest{
						Document: doc("lobbies/1/state",
							map[string]*commonpb.Value{"phase": stringValue(phase)}),
						CurrentDocument: &syncpb.Precondition{
							ConditionType: &syncpb.Precondition_UpdateTime{UpdateTime: version},
						},
					},
				},
			}},
		}
	}

	// Alice's write moves the update time on.
	h.now = h.now.Add(time.Second)
	if _, err := h.client.WriteDocuments(alice, guarded("starting")); err != nil {
		t.Fatalf("alice's write: %v", err)
	}
	// Bob's, against the version he read, must lose. This is the whole point of
	// the precondition: without it the later write silently wins and one
	// console's view of the lobby is quietly discarded.
	if _, err := h.client.WriteDocuments(bob, guarded("cancelled")); status.Code(err) != codes.FailedPrecondition {
		t.Errorf("bob's stale write gave %v, want FailedPrecondition", err)
	}

	got, err := h.client.GetDocument(alice, &syncpb.GetDocumentRequest{Name: "lobbies/1/state"})
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.GetFields().GetFields()["phase"].GetStringValue() != "starting" {
		t.Errorf("phase = %v, want alice's write to have stood",
			got.GetFields().GetFields()["phase"])
	}
}

func TestAMergeLeavesTheFieldsItDoesNotMention(t *testing.T) {
	h := newHarness(t)
	alice := h.session("token-alice")

	if _, err := h.client.WriteDocuments(alice, &syncpb.WriteDocumentsRequest{
		WriteOperations: []*syncpb.WriteOperation{
			merge(doc("lobbies/1/players/alice", map[string]*commonpb.Value{
				"ready":  intValue(1),
				"weapon": stringValue("splattershot"),
			})),
		},
	}); err != nil {
		t.Fatalf("write: %v", err)
	}

	if _, err := h.client.WriteDocuments(alice, &syncpb.WriteDocumentsRequest{
		WriteOperations: []*syncpb.WriteOperation{
			merge(doc("lobbies/1/players/alice", map[string]*commonpb.Value{"ready": intValue(0)})),
		},
	}); err != nil {
		t.Fatalf("merge: %v", err)
	}

	got, err := h.client.GetDocument(alice,
		&syncpb.GetDocumentRequest{Name: "lobbies/1/players/alice"})
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	fields := got.GetFields().GetFields()
	if fields["ready"].GetIntegerValue() != 0 {
		t.Errorf("ready = %v, want the merged 0", fields["ready"])
	}
	if fields["weapon"].GetStringValue() != "splattershot" {
		t.Error("a merge cleared a field it did not mention")
	}
}

func TestAMaskedUpdateClearsAFieldTheClientOmitted(t *testing.T) {
	h := newHarness(t)
	alice := h.session("token-alice")

	if _, err := h.client.WriteDocuments(alice, &syncpb.WriteDocumentsRequest{
		WriteOperations: []*syncpb.WriteOperation{
			merge(doc("lobbies/1/players/alice", map[string]*commonpb.Value{
				"ready":  intValue(1),
				"weapon": stringValue("splattershot"),
			})),
		},
	}); err != nil {
		t.Fatalf("write: %v", err)
	}

	// A masked field the client did not send is a deletion. Treating it as
	// "leave alone" would make clearing a field impossible.
	if _, err := h.client.WriteDocuments(alice, &syncpb.WriteDocumentsRequest{
		WriteOperations: []*syncpb.WriteOperation{{
			OperationType: &syncpb.WriteOperation_UpdateDocument{
				UpdateDocument: &syncpb.UpdateDocumentRequest{
					Document:   doc("lobbies/1/players/alice", nil),
					UpdateMask: fieldMask("weapon"),
				},
			},
		}},
	}); err != nil {
		t.Fatalf("update: %v", err)
	}

	got, err := h.client.GetDocument(alice,
		&syncpb.GetDocumentRequest{Name: "lobbies/1/players/alice"})
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if _, ok := got.GetFields().GetFields()["weapon"]; ok {
		t.Error("a masked update with the field absent did not clear it")
	}
	if got.GetFields().GetFields()["ready"].GetIntegerValue() != 1 {
		t.Error("a masked update touched a field outside its mask")
	}
}

func TestConsoleQuotedAndWildcardMasksKeepItsRoomDocuments(t *testing.T) {
	h := newHarness(t)
	alice := h.session("token-alice")
	name := "docs/SessionInfo/alice"
	if _, err := h.client.WriteDocuments(alice, &syncpb.WriteDocumentsRequest{
		WriteOperations: []*syncpb.WriteOperation{{OperationType: &syncpb.WriteOperation_UpdateDocument{
			UpdateDocument: &syncpb.UpdateDocumentRequest{
				Document: doc(name, map[string]*commonpb.Value{
					"done_match_step": intValue(1), "name": stringValue("alice"),
				}),
				UpdateMask: fieldMask("`done_match_step`", "`name`"),
			},
		}}},
	}); err != nil {
		t.Fatal(err)
	}
	read, err := h.client.GetDocument(alice, &syncpb.GetDocumentRequest{
		Name: name, ReadMask: fieldMask("`done_match_step`"),
	})
	if err != nil {
		t.Fatal(err)
	}
	fields := read.GetFields().GetFields()
	if len(fields) != 1 || fields["done_match_step"].GetIntegerValue() != 1 {
		t.Fatalf("quoted read mask returned %v", fields)
	}

	if _, err := h.client.WriteDocuments(alice, &syncpb.WriteDocumentsRequest{
		WriteOperations: []*syncpb.WriteOperation{{OperationType: &syncpb.WriteOperation_UpdateDocument{
			UpdateDocument: &syncpb.UpdateDocumentRequest{
				Document:   doc(name, map[string]*commonpb.Value{"name": stringValue("updated")}),
				UpdateMask: fieldMask("*"),
			},
		}}},
	}); err != nil {
		t.Fatal(err)
	}
	read, err = h.client.GetDocument(alice, &syncpb.GetDocumentRequest{Name: name, ReadMask: fieldMask("*")})
	if err != nil {
		t.Fatal(err)
	}
	fields = read.GetFields().GetFields()
	if len(fields) != 1 || fields["name"].GetStringValue() != "updated" {
		t.Errorf("wildcard mask returned %v", fields)
	}
}

func TestPartialConsoleSessionWritesRemainCompleteOnTheWatch(t *testing.T) {
	h := newHarness(t)
	alice := h.session("token-alice")
	ctx, cancel := context.WithTimeout(alice, 3*time.Second)
	defer cancel()
	stream, err := h.client.KeepUserSession(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := stream.Send(&syncpb.KeepUserSessionRequest{RequestType: &syncpb.KeepUserSessionRequest_UpdateTarget{
		UpdateTarget: &syncpb.UpdateTargetRequest{Target: &syncpb.Target{
			Name:       "userSessions/current/targets/5",
			TargetType: &syncpb.Target_Collection{Collection: &syncpb.CollectionTarget{Collection: "docs/__us"}},
		}},
	}}); err != nil {
		t.Fatal(err)
	}
	for {
		msg, err := stream.Recv()
		if err != nil {
			t.Fatal(err)
		}
		if msg.GetTargetChange().GetTargetChangeType() == syncpb.TargetChange_LISTED {
			break
		}
	}
	name := "docs/__us/token-alice"
	if _, err := h.client.WriteDocuments(alice, &syncpb.WriteDocumentsRequest{WriteOperations: []*syncpb.WriteOperation{{
		OperationType: &syncpb.WriteOperation_UpdateDocument{UpdateDocument: &syncpb.UpdateDocumentRequest{
			Document: doc(name, map[string]*commonpb.Value{"att": {
				ValueType: &commonpb.Value_MapValue{MapValue: &commonpb.MapValue{}},
			}}),
			UpdateMask: fieldMask("`att`"),
		}},
	}}}); err != nil {
		t.Fatal(err)
	}
	for {
		msg, err := stream.Recv()
		if err != nil {
			t.Fatal(err)
		}
		if change := msg.GetDocumentChange(); change != nil && change.GetDocument().GetName() == name {
			fields := change.GetDocument().GetFields().GetFields()
			if len(fields) != 9 || fields["uid"].GetStringValue() != "alice" || fields["att"].GetMapValue() == nil {
				t.Fatalf("partial write published incomplete session: %v", fields)
			}
			break
		}
	}
	read, err := h.client.GetDocument(alice, &syncpb.GetDocumentRequest{Name: name})
	if err != nil || len(read.GetFields().GetFields()) != 9 {
		t.Fatalf("partial session readback = %v, %v", read, err)
	}

	presence := "docs/__pgn/All/__pus/token-alice"
	if _, err := h.client.WriteDocuments(alice, &syncpb.WriteDocumentsRequest{WriteOperations: []*syncpb.WriteOperation{{
		OperationType: &syncpb.WriteOperation_UpdateDocument{UpdateDocument: &syncpb.UpdateDocumentRequest{
			Document:   doc(presence, map[string]*commonpb.Value{"uid": stringValue("alice"), "upcsid": intValue(1)}),
			UpdateMask: fieldMask("*"),
		}},
	}}}); err != nil {
		t.Fatal(err)
	}
	if _, err := h.client.WriteDocuments(alice, &syncpb.WriteDocumentsRequest{WriteOperations: []*syncpb.WriteOperation{{
		OperationType: &syncpb.WriteOperation_TransformDocument{TransformDocument: &syncpb.TransformDocumentRequest{
			Name: presence,
			FieldTransforms: []*syncpb.FieldTransform{{FieldPath: "`upcsid`",
				TransformType: &syncpb.FieldTransform_Increment{Increment: intValue(1)}}},
		}},
	}}}); err != nil {
		t.Fatal(err)
	}
	read, err = h.client.GetDocument(alice, &syncpb.GetDocumentRequest{Name: presence})
	if err != nil {
		t.Fatal(err)
	}
	fields := read.GetFields().GetFields()
	if len(fields) != 9 || fields["upcsid"].GetIntegerValue() != 2 {
		t.Errorf("presence after quoted transform = %v", fields)
	}
}

// ---- transforms ----------------------------------------------------------

func TestIncrementingAFieldThatDoesNotExistCreatesIt(t *testing.T) {
	h := newHarness(t)
	alice := h.session("token-alice")

	transform := func(delta int64) *syncpb.WriteOperation {
		return &syncpb.WriteOperation{
			OperationType: &syncpb.WriteOperation_TransformDocument{
				TransformDocument: &syncpb.TransformDocumentRequest{
					Name: "lobbies/1/score",
					FieldTransforms: []*syncpb.FieldTransform{{
						FieldPath: "points",
						TransformType: &syncpb.FieldTransform_Increment{
							Increment: intValue(delta),
						},
					}},
				},
			},
		}
	}

	// There is no separate "create field" step, so incrementing an absent field
	// has to create it — otherwise the client's first increment is lost.
	resp, err := h.client.WriteDocuments(alice, &syncpb.WriteDocumentsRequest{
		WriteOperations: []*syncpb.WriteOperation{transform(3), transform(4)},
	})
	if err != nil {
		t.Fatalf("transform: %v", err)
	}
	if len(resp.GetWriteResults()) != 2 {
		t.Fatalf("got %d results for 2 transforms", len(resp.GetWriteResults()))
	}
	// The result carries the value after the transform, so the client does not
	// need a read to learn what it now is.
	last := resp.GetWriteResults()[1].GetTransformResult().GetResults()
	if len(last) != 1 || last[0].GetIntegerValue() != 7 {
		t.Errorf("transform reported %v, want 7", last)
	}

	got, err := h.client.GetDocument(alice, &syncpb.GetDocumentRequest{Name: "lobbies/1/score"})
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	// Integers stay integers: a client that increments an integer and reads back
	// a double has had its type changed underneath it.
	if v := got.GetFields().GetFields()["points"]; v.GetIntegerValue() != 7 {
		t.Errorf("points = %v, want integer 7", v)
	}
}

func TestMaximumAndMinimumKeepTheBetterValue(t *testing.T) {
	h := newHarness(t)
	alice := h.session("token-alice")

	apply := func(kind string, v int64) error {
		var tt syncpb.FieldTransform
		tt.FieldPath = "best"
		if kind == "max" {
			tt.TransformType = &syncpb.FieldTransform_Maximum{Maximum: intValue(v)}
		} else {
			tt.TransformType = &syncpb.FieldTransform_Minimum{Minimum: intValue(v)}
		}
		_, err := h.client.WriteDocuments(alice, &syncpb.WriteDocumentsRequest{
			WriteOperations: []*syncpb.WriteOperation{{
				OperationType: &syncpb.WriteOperation_TransformDocument{
					TransformDocument: &syncpb.TransformDocumentRequest{
						Name:            "lobbies/1/score",
						FieldTransforms: []*syncpb.FieldTransform{&tt},
					},
				},
			}},
		})
		return err
	}

	if err := apply("max", 10); err != nil {
		t.Fatalf("max: %v", err)
	}
	if err := apply("max", 4); err != nil {
		t.Fatalf("max: %v", err)
	}
	got, err := h.client.GetDocument(alice, &syncpb.GetDocumentRequest{Name: "lobbies/1/score"})
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if v := got.GetFields().GetFields()["best"].GetIntegerValue(); v != 10 {
		t.Errorf("best = %d after max(10) then max(4), want 10", v)
	}
	if err := apply("min", 2); err != nil {
		t.Fatalf("min: %v", err)
	}
	got, _ = h.client.GetDocument(alice, &syncpb.GetDocumentRequest{Name: "lobbies/1/score"})
	if v := got.GetFields().GetFields()["best"].GetIntegerValue(); v != 2 {
		t.Errorf("best = %d after min(2), want 2", v)
	}
}

func TestATransformThisServerCannotApplyIsRefusedBeforeAnythingIsWritten(t *testing.T) {
	h := newHarness(t)
	alice := h.session("token-alice")

	// An unset transform type cannot be applied. Reject it before the first
	// write in the batch, preserving the document store's atomicity.
	_, err := h.client.WriteDocuments(alice, &syncpb.WriteDocumentsRequest{
		WriteOperations: []*syncpb.WriteOperation{
			merge(doc("lobbies/1/state", map[string]*commonpb.Value{"phase": stringValue("waiting")})),
			{
				OperationType: &syncpb.WriteOperation_TransformDocument{
					TransformDocument: &syncpb.TransformDocumentRequest{
						Name:            "lobbies/1/score",
						FieldTransforms: []*syncpb.FieldTransform{{FieldPath: "unknown"}},
					},
				},
			},
		},
	})
	if status.Code(err) != codes.Unimplemented {
		t.Fatalf("an unimplemented transform gave %v, want Unimplemented", err)
	}
	if _, err := h.client.GetDocument(alice,
		&syncpb.GetDocumentRequest{Name: "lobbies/1/state"}); status.Code(err) != codes.NotFound {
		t.Error("the batch partly applied before the unsupported transform was noticed")
	}
}

func TestGlobalValueTransformsReturnTypedConnectionSequences(t *testing.T) {
	h := newHarness(t)
	alice := h.session("token-alice")
	transform := func(name string, kind string) (*syncpb.WriteDocumentsResponse, error) {
		ft := &syncpb.FieldTransform{FieldPath: "upcsid"}
		switch kind {
		case "load":
			ft.TransformType = &syncpb.FieldTransform_LoadGlobalValue{LoadGlobalValue: "connection"}
		case "store":
			ft.TransformType = &syncpb.FieldTransform_StoreGlobalValue{StoreGlobalValue: "connection"}
		case "clear":
			ft.TransformType = &syncpb.FieldTransform_ClearGlobalValue{ClearGlobalValue: "connection"}
		}
		return h.client.WriteDocuments(alice, &syncpb.WriteDocumentsRequest{WriteOperations: []*syncpb.WriteOperation{{
			OperationType: &syncpb.WriteOperation_TransformDocument{TransformDocument: &syncpb.TransformDocumentRequest{
				Name: name, FieldTransforms: []*syncpb.FieldTransform{ft},
			}},
		}}})
	}
	first, err := transform("rooms/1/member/alice", "load")
	if err != nil {
		t.Fatal(err)
	}
	sequence := first.GetWriteResults()[0].GetTransformResult().GetResults()[0].GetIntegerValue()
	if sequence <= 0 {
		t.Fatalf("first connection sequence = %d, want positive", sequence)
	}
	second, err := transform("rooms/1/member/bob", "load")
	if err != nil || second.GetWriteResults()[0].GetTransformResult().GetResults()[0].GetIntegerValue() != sequence {
		t.Fatalf("reloaded sequence = %v, %v", second, err)
	}
	if _, err := transform("rooms/1/member/alice", "store"); err != nil {
		t.Fatal(err)
	}
	if _, err := transform("rooms/1/member/alice", "clear"); err != nil {
		t.Fatal(err)
	}
	third, err := transform("rooms/1/member/bob", "load")
	if err != nil || third.GetWriteResults()[0].GetTransformResult().GetResults()[0].GetIntegerValue() <= sequence {
		t.Fatalf("new sequence after clear = %v, %v", third, err)
	}
}

// ---- transactions --------------------------------------------------------

func TestATransactionCommitsOnceAndCannotBeReplayed(t *testing.T) {
	h := newHarness(t)
	alice := h.session("token-alice")

	begun, err := h.client.BeginTransaction(alice, &syncpb.BeginTransactionRequest{})
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	if len(begun.GetTransaction()) == 0 {
		t.Fatal("no transaction token")
	}

	write := &syncpb.CommitTransactionRequest{
		Transaction: begun.GetTransaction(),
		WriteOperations: []*syncpb.WriteOperation{{
			OperationType: &syncpb.WriteOperation_TransformDocument{
				TransformDocument: &syncpb.TransformDocumentRequest{
					Name: "lobbies/1/score",
					FieldTransforms: []*syncpb.FieldTransform{{
						FieldPath:     "points",
						TransformType: &syncpb.FieldTransform_Increment{Increment: intValue(5)},
					}},
				},
			},
		}},
	}

	if _, err := h.client.CommitTransaction(alice, write); err != nil {
		t.Fatalf("commit: %v", err)
	}
	// A client retrying a commit it believes failed must not double a field it
	// incremented.
	if _, err := h.client.CommitTransaction(alice, write); status.Code(err) != codes.FailedPrecondition {
		t.Errorf("committing twice gave %v, want FailedPrecondition", err)
	}

	got, err := h.client.GetDocument(alice, &syncpb.GetDocumentRequest{Name: "lobbies/1/score"})
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if v := got.GetFields().GetFields()["points"].GetIntegerValue(); v != 5 {
		t.Errorf("points = %d, want 5 — the transaction applied twice", v)
	}
}

func TestOneConsoleCannotCommitAnothersTransaction(t *testing.T) {
	h := newHarness(t)
	alice := h.session("token-alice")
	bob := h.session("token-bob")

	begun, err := h.client.BeginTransaction(alice, &syncpb.BeginTransactionRequest{})
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	if _, err := h.client.CommitTransaction(bob, &syncpb.CommitTransactionRequest{
		Transaction: begun.GetTransaction(),
	}); status.Code(err) != codes.FailedPrecondition {
		t.Errorf("bob committing alice's transaction gave %v, want FailedPrecondition", err)
	}
}

func TestRollingBackTwiceIsNotAnError(t *testing.T) {
	h := newHarness(t)
	alice := h.session("token-alice")

	begun, err := h.client.BeginTransaction(alice, &syncpb.BeginTransactionRequest{})
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	for i := 0; i < 2; i++ {
		// A client that lost its connection mid-transaction retries the
		// rollback, and failing it leaves the client believing state is still
		// pending.
		if _, err := h.client.RollbackTransaction(alice, &syncpb.RollbackTransactionRequest{
			Transaction: begun.GetTransaction(),
		}); err != nil {
			t.Errorf("rollback %d: %v", i, err)
		}
	}
}

// ---- the watch stream ----------------------------------------------------

func TestADefermentIsAcknowledgedAndRunsWhenTheUserSessionCloses(t *testing.T) {
	h := newHarness(t)
	alice := h.session("token-alice")
	ctx, cancel := context.WithTimeout(alice, time.Second)
	defer cancel()
	stream, err := h.client.KeepUserSession(ctx)
	if err != nil {
		t.Fatal(err)
	}
	// An echo proves the watch is established before scheduling cleanup.
	if err := stream.Send(&syncpb.KeepUserSessionRequest{
		RequestType: &syncpb.KeepUserSessionRequest_Echo{Echo: "ready"},
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := stream.Recv(); err != nil {
		t.Fatal(err)
	}
	resp, err := h.client.WriteDocuments(alice, &syncpb.WriteDocumentsRequest{
		DefermentOperations: []*syncpb.DefermentOperation{{
			OperationType: &syncpb.DefermentOperation_UpdateDeferment{
				UpdateDeferment: &syncpb.UpdateDefermentRequest{Deferment: &syncpb.Deferment{
					Name: "cleanup/alice", WriteOperations: []*syncpb.WriteOperation{
						merge(doc("rooms/1/state", map[string]*commonpb.Value{"closed": intValue(1)})),
					},
				}},
			},
		}},
	})
	if err != nil || resp.GetCommitTime() == nil {
		t.Fatalf("register cleanup = %v, %v", resp, err)
	}
	if _, err := h.client.GetDocument(alice, &syncpb.GetDocumentRequest{Name: "rooms/1/state"}); status.Code(err) != codes.NotFound {
		t.Fatalf("cleanup ran before session closed: %v", err)
	}
	if err := stream.CloseSend(); err != nil {
		t.Fatal(err)
	}
	if _, err := stream.Recv(); err != io.EOF {
		t.Fatalf("closed stream = %v, want EOF", err)
	}
	got, err := h.client.GetDocument(alice, &syncpb.GetDocumentRequest{Name: "rooms/1/state"})
	if err != nil || got.GetFields().GetFields()["closed"].GetIntegerValue() != 1 {
		t.Fatalf("deferred cleanup = %v, %v", got, err)
	}
}

func TestOwnUserSessionArrivesBeforeWatchSnapshotCompletes(t *testing.T) {
	h := newHarness(t)
	alice := h.session("token-alice")
	stream, err := h.client.KeepUserSession(alice)
	if err != nil {
		t.Fatal(err)
	}
	if err := stream.Send(&syncpb.KeepUserSessionRequest{
		RequestType: &syncpb.KeepUserSessionRequest_UpdateTarget{
			UpdateTarget: &syncpb.UpdateTargetRequest{Target: &syncpb.Target{
				Name: "userSessions/current/targets/1",
				TargetType: &syncpb.Target_Documents{Documents: &syncpb.DocumentsTarget{
					Documents: []string{"docs/__us/token-alice"},
				}},
			}},
		},
	}); err != nil {
		t.Fatal(err)
	}
	first, err := stream.Recv()
	if err != nil || first.GetTargetChange().GetTargetId() != "1" || first.GetTargetChange().GetTargetChangeType() != syncpb.TargetChange_UPDATED {
		t.Fatalf("snapshot start = %v, %v", first, err)
	}
	second, err := stream.Recv()
	if err != nil {
		t.Fatal(err)
	}
	change := second.GetDocumentChange()
	if change.GetTargetId() != "1" || change.GetDocumentChangeType() != syncpb.DocumentChange_EXIST || change.GetDocument().GetName() != "docs/__us/token-alice" {
		t.Fatalf("session snapshot = %v", second)
	}
	fields := change.GetDocument().GetFields().GetFields()
	if len(fields) != 9 || fields["uid"].GetStringValue() != "alice" || fields["st"].GetIntegerValue() != 2 || fields["pgn"].GetStringValue() != "All" || fields["att"].GetMapValue() == nil || fields["ltc"].GetMapValue() == nil {
		t.Fatalf("session fields = %v", fields)
	}
	last, err := stream.Recv()
	if err != nil || last.GetTargetChange().GetTargetId() != "1" || last.GetTargetChange().GetTargetChangeType() != syncpb.TargetChange_LISTED {
		t.Fatalf("snapshot end = %v, %v", last, err)
	}
	read, err := h.client.GetDocument(alice, &syncpb.GetDocumentRequest{Name: "docs/__us/token-alice"})
	if err != nil || !proto.Equal(read, change.GetDocument()) {
		t.Fatalf("point read disagrees with the watch snapshot: %v, %v", read, err)
	}
}

func TestOwnPresenceAndPlayerStateHaveTheCapturedFieldTypes(t *testing.T) {
	h := newHarness(t)
	alice := h.session("token-alice")
	presence, err := h.client.ListDocuments(alice, &syncpb.ListDocumentsRequest{Parent: "docs/__pgn/All/__pus"})
	if err != nil || len(presence.GetDocuments()) != 1 {
		t.Fatalf("presence collection = %v, %v", presence, err)
	}
	member := presence.GetDocuments()[0]
	if member.GetName() != "docs/__pgn/All/__pus/token-alice" || member.GetFields().GetFields()["uid"].GetStringValue() != "alice" {
		t.Fatalf("presence member = %v", member)
	}
	state, err := h.client.GetDocument(alice, &syncpb.GetDocumentRequest{Name: "docs/__pgn/All/__stu/token-alice"})
	if err != nil {
		t.Fatal(err)
	}
	fields := state.GetFields().GetFields()
	if fields["suid"].GetStringValue() != "alice" || fields["suscid"].GetStringValue() != "1" {
		t.Fatalf("player state identity = %v", fields)
	}
	if _, ok := fields["pl"].GetValueType().(*commonpb.Value_MapValue); !ok {
		t.Fatalf("player state pl has type %T, want map", fields["pl"].GetValueType())
	}
	if _, ok := fields["mp"].GetValueType().(*commonpb.Value_MapValue); !ok {
		t.Fatalf("player state mp has type %T, want map", fields["mp"].GetValueType())
	}
}

func TestAWatchedDocumentChangeReachesTheOtherConsole(t *testing.T) {
	h := newHarness(t)
	alice := h.session("token-alice")
	bob := h.session("token-bob")

	stream, err := h.client.KeepUserSession(bob)
	if err != nil {
		t.Fatalf("open stream: %v", err)
	}

	// The client is told its target is watched BEFORE any change arrives, so it
	// can tell "nothing has changed" from "my target never took effect".
	if err := stream.Send(&syncpb.KeepUserSessionRequest{
		RequestType: &syncpb.KeepUserSessionRequest_UpdateTarget{
			UpdateTarget: &syncpb.UpdateTargetRequest{
				Target: &syncpb.Target{
					Name: "target-players",
					TargetType: &syncpb.Target_Collection{
						Collection: &syncpb.CollectionTarget{Collection: "lobbies/1/players"},
					},
				},
			},
		},
	}); err != nil {
		t.Fatalf("send target: %v", err)
	}

	updated, err := stream.Recv()
	if err != nil || updated.GetTargetChange().GetTargetChangeType() != syncpb.TargetChange_UPDATED {
		t.Fatalf("target start = %v, %v, want UPDATED", updated, err)
	}
	ack, err := stream.Recv()
	if err != nil {
		t.Fatalf("recv target ack: %v", err)
	}
	if ack.GetTargetChange().GetTargetChangeType() != syncpb.TargetChange_LISTED {
		t.Fatalf("target ack is %v, want LISTED", ack.GetTargetChange())
	}

	if _, err := h.client.WriteDocuments(alice, &syncpb.WriteDocumentsRequest{
		WriteOperations: []*syncpb.WriteOperation{
			merge(doc("lobbies/1/players/alice", map[string]*commonpb.Value{"ready": intValue(1)})),
			// Outside the target: a collection target fires for direct children
			// only, or the client receives changes it has no document for.
			merge(doc("lobbies/1/players/alice/gear/hat", map[string]*commonpb.Value{"id": intValue(3)})),
			merge(doc("lobbies/2/players/carol", map[string]*commonpb.Value{"ready": intValue(1)})),
		},
	}); err != nil {
		t.Fatalf("write: %v", err)
	}

	change, err := stream.Recv()
	if err != nil {
		t.Fatalf("recv change: %v", err)
	}
	dc := change.GetDocumentChange()
	if dc.GetDocument().GetName() != "lobbies/1/players/alice" {
		t.Errorf("got a change for %q", dc.GetDocument().GetName())
	}
	// The target id, so the client can match a change to the target it asked
	// for -- the same document can be watched under different names.
	if dc.GetTargetId() != "target-players" {
		t.Errorf("target id = %q, want target-players", dc.GetTargetId())
	}
	if dc.GetDocumentChangeType() != syncpb.DocumentChange_UPDATED {
		t.Errorf("change type = %v, want UPDATED", dc.GetDocumentChangeType())
	}
	boundary, err := stream.Recv()
	if err != nil {
		t.Fatalf("recv live snapshot boundary: %v", err)
	}
	if tc := boundary.GetTargetChange(); tc == nil || tc.GetTargetId() != "target-players" || tc.GetTargetChangeType() != syncpb.TargetChange_UPDATED {
		t.Errorf("live snapshot boundary = %v, want UPDATED for target-players", boundary)
	}

	// Nothing else: the subtree write and the other lobby's must not arrive.
	deleted := make(chan *syncpb.DocumentChange, 1)
	go func() {
		for {
			msg, err := stream.Recv()
			if err != nil {
				return
			}
			if c := msg.GetDocumentChange(); c != nil {
				deleted <- c
			}
		}
	}()
	select {
	case extra := <-deleted:
		t.Errorf("an unwatched change arrived: %q", extra.GetDocument().GetName())
	case <-time.After(200 * time.Millisecond):
	}
}

func TestAnEchoComesBackVerbatim(t *testing.T) {
	h := newHarness(t)
	alice := h.session("token-alice")

	stream, err := h.client.KeepUserSession(alice)
	if err != nil {
		t.Fatalf("open stream: %v", err)
	}
	if err := stream.Send(&syncpb.KeepUserSessionRequest{
		RequestType: &syncpb.KeepUserSessionRequest_Echo{Echo: "are-you-there"},
	}); err != nil {
		t.Fatalf("send echo: %v", err)
	}

	got, err := stream.Recv()
	if err != nil {
		t.Fatalf("recv echo: %v", err)
	}
	// It is the client's own liveness check, so changing the payload defeats
	// the only thing it is for.
	if got.GetEcho() != "are-you-there" {
		t.Errorf("echo = %q, want it back unchanged", got.GetEcho())
	}
}

func TestRemovingATargetStopsTheChanges(t *testing.T) {
	h := newHarness(t)
	alice := h.session("token-alice")
	bob := h.session("token-bob")

	stream, err := h.client.KeepUserSession(bob)
	if err != nil {
		t.Fatalf("open stream: %v", err)
	}
	send := func(req *syncpb.KeepUserSessionRequest) {
		t.Helper()
		if err := stream.Send(req); err != nil {
			t.Fatalf("send: %v", err)
		}
		if _, err := stream.Recv(); err != nil {
			t.Fatalf("recv ack: %v", err)
		}
		if req.GetUpdateTarget() != nil {
			if _, err := stream.Recv(); err != nil {
				t.Fatalf("recv snapshot end: %v", err)
			}
		}
	}

	send(&syncpb.KeepUserSessionRequest{
		RequestType: &syncpb.KeepUserSessionRequest_UpdateTarget{
			UpdateTarget: &syncpb.UpdateTargetRequest{
				Target: &syncpb.Target{
					Name: "t1",
					TargetType: &syncpb.Target_Documents{
						Documents: &syncpb.DocumentsTarget{
							Documents: []string{"lobbies/1/state"},
						},
					},
				},
			},
		},
	})
	send(&syncpb.KeepUserSessionRequest{
		RequestType: &syncpb.KeepUserSessionRequest_DeleteTarget{
			DeleteTarget: &syncpb.DeleteTargetRequest{Name: "t1"},
		},
	})

	if _, err := h.client.WriteDocuments(alice, &syncpb.WriteDocumentsRequest{
		WriteOperations: []*syncpb.WriteOperation{
			merge(doc("lobbies/1/state", map[string]*commonpb.Value{"phase": stringValue("go")})),
		},
	}); err != nil {
		t.Fatalf("write: %v", err)
	}

	changes := make(chan struct{}, 1)
	go func() {
		for {
			msg, err := stream.Recv()
			if err != nil {
				return
			}
			if msg.GetDocumentChange() != nil {
				changes <- struct{}{}
			}
		}
	}()
	select {
	case <-changes:
		t.Error("a change arrived for a target that was removed")
	case <-time.After(200 * time.Millisecond):
	}
}

func TestDeletingADocumentIsAChangeAndAbsenceAfterwards(t *testing.T) {
	h := newHarness(t)
	alice := h.session("token-alice")

	if _, err := h.client.WriteDocuments(alice, &syncpb.WriteDocumentsRequest{
		WriteOperations: []*syncpb.WriteOperation{
			merge(doc("lobbies/1/state", map[string]*commonpb.Value{"phase": stringValue("go")})),
		},
	}); err != nil {
		t.Fatalf("write: %v", err)
	}

	if _, err := h.client.WriteDocuments(alice, &syncpb.WriteDocumentsRequest{
		WriteOperations: []*syncpb.WriteOperation{{
			OperationType: &syncpb.WriteOperation_DeleteDocument{
				DeleteDocument: &syncpb.DeleteDocumentRequest{Name: "lobbies/1/state"},
			},
		}},
	}); err != nil {
		t.Fatalf("delete: %v", err)
	}

	if _, err := h.client.GetDocument(alice,
		&syncpb.GetDocumentRequest{Name: "lobbies/1/state"}); status.Code(err) != codes.NotFound {
		t.Errorf("a deleted document reads as %v, want NotFound", err)
	}
	// Deleting something that is already gone is not an error: a client
	// retrying a delete must not be told the lobby is broken.
	if _, err := h.client.WriteDocuments(alice, &syncpb.WriteDocumentsRequest{
		WriteOperations: []*syncpb.WriteOperation{{
			OperationType: &syncpb.WriteOperation_DeleteDocument{
				DeleteDocument: &syncpb.DeleteDocumentRequest{Name: "lobbies/1/state"},
			},
		}},
	}); err != nil {
		t.Errorf("deleting an absent document errored: %v", err)
	}
}

func TestCreateRoundEchoesTheClientsRoundWithoutInventingFields(t *testing.T) {
	h := newHarness(t)
	alice := h.session("token-alice")

	// Round has five fields and four of them are Unk1..Unk4 in the decompiled
	// definitions. The answer may carry only what the client sent.
	sent := &syncpb.Round{Unk1: "a", Unk4: []string{"x", "y"}}
	got, err := h.client.CreateRound(alice, &syncpb.CreateRoundRequest{Round: sent})
	if err != nil {
		t.Fatalf("CreateRound: %v", err)
	}
	if !proto.Equal(got, sent) {
		t.Errorf("CreateRound answered %v, want exactly the round sent %v", got, sent)
	}

	empty, err := h.client.CreateRound(alice, &syncpb.CreateRoundRequest{})
	if err != nil || !proto.Equal(empty, &syncpb.Round{}) {
		t.Errorf("CreateRound with no round: %v, %v; want an empty round", empty, err)
	}
}

// fieldMask builds a read or update mask.
func fieldMask(paths ...string) *fieldmaskpb.FieldMask {
	return &fieldmaskpb.FieldMask{Paths: paths}
}
