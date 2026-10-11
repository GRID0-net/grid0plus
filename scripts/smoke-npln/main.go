// Command smoke-npln drives phase 2 the way a console would, and reports what
// came back.
//
// CLAUDE.md rule 4: run it, do not just test it. Three of the bugs fixed in this
// repository were invisible to a green test suite and obvious within thirty
// seconds of starting the processes — and two more were found by this program.
// scripts/seed-dev-data.sh is the same idea for phase 1; this is the phase 2
// half, because the gRPC services cannot be driven with curl.
//
//	make up
//	scripts/seed-phase2.sh          # registers two accounts, then runs this
//
// It goes through the REAL chain end to end: a device token, an application
// token, a BAAS login, the id token that login returns, an NPLN session, a
// match between two players, a matchmaking id token, a gamesync document, and
// the announced Splatfest. If this completes, everything except the console side
// works.
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"strings"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"

	authpb "github.com/n-popescu/grid0plus/proto/auth/v1"
	commonpb "github.com/n-popescu/grid0plus/proto/common"
	syncpb "github.com/n-popescu/grid0plus/proto/gamesync/v1"
	mmpb "github.com/n-popescu/grid0plus/proto/matchmaking/v1"
	toyohrpb "github.com/n-popescu/grid0plus/proto/toyohr/v1"
)

func main() {
	tenant := flag.String("tenant", "dce9377b", "NPLN tenant id")
	gateway := flag.String("gateway", "localhost:8085", "the NPLN gRPC gateway")
	gamesync := flag.String("gamesync", "localhost:8088", "gamesync's internal address")
	tokens := flag.String("tokens", "", "comma-separated files, each holding one BAAS id token")
	users := flag.String("users", "", "comma-separated user ids, in the same order")
	region := flag.String("region", "EU", "region to ask for a Splatfest schedule in")
	flag.Parse()

	idTokens := readAll(strings.Split(*tokens, ","))
	userIDs := strings.Split(*users, ",")
	if len(idTokens) < 2 || len(userIDs) != len(idTokens) {
		log.Fatal("need at least two id tokens and the matching user ids; " +
			"run scripts/seed-phase2.sh instead of this directly")
	}

	conn := dial(*gateway)
	defer conn.Close()
	syncConn := dial(*gamesync)
	defer syncConn.Close()

	call := func(bearer, uid string) context.Context {
		pairs := []string{"npln-tenant-id", *tenant}
		if bearer != "" {
			pairs = append(pairs, "authorization", "Bearer "+bearer)
		}
		if uid != "" {
			pairs = append(pairs, "uid", uid)
		}
		return metadata.NewOutgoingContext(context.Background(), metadata.Pairs(pairs...))
	}

	// 1. The phase 1 → phase 2 seam: a BAAS id token buys an NPLN session.
	auth := authpb.NewAuthClient(conn)
	sessions := make([]context.Context, len(idTokens))
	for i, idToken := range idTokens {
		resp, err := auth.IssueToken(call("", ""), &authpb.IssueTokenRequest{
			ExternalIdToken: &authpb.ExternalIdToken{
				Token: &authpb.ExternalIdToken_NsaIdToken{NsaIdToken: idToken},
			},
		})
		if err != nil {
			log.Fatalf("player %d: IssueToken: %v", i+1, err)
		}
		fmt.Printf("player %d: NPLN session for %s, ttl %s\n", i+1,
			resp.GetToken().GetUser(), resp.GetToken().GetTtl().AsDuration())
		sessions[i] = call(resp.GetToken().GetAccessToken(), userIDs[i])
	}

	// 2. Matchmaking, in the order a console does it: player one queues and
	//    starts tracking BEFORE player two arrives, because the stream is how it
	//    learns about the match.
	mm := mmpb.NewMatchmakerClient(conn)
	config := "tenants/" + *tenant + "/matchmakingConfigs/turf_war"

	first, err := mm.CreateMatchmakingTicket(sessions[0], &mmpb.CreateMatchmakingTicketRequest{
		MatchmakingTicket: &mmpb.MatchmakingTicket{MatchmakingConfig: config},
	})
	if err != nil {
		log.Fatalf("player 1: queue: %v", err)
	}
	fmt.Printf("player 1: ticket %s\n", first.GetState())

	stream, err := mm.TrackMatchmakingTicket(sessions[0],
		&mmpb.TrackMatchmakingTicketRequest{Name: first.GetName()})
	if err != nil {
		log.Fatalf("player 1: track: %v", err)
	}
	if opened, err := stream.Recv(); err != nil {
		log.Fatalf("player 1: the stream sent nothing on attaching: %v", err)
	} else {
		fmt.Printf("player 1: stream opened on %s\n", opened.GetState())
	}

	second, err := mm.CreateMatchmakingTicket(sessions[1], &mmpb.CreateMatchmakingTicketRequest{
		MatchmakingTicket: &mmpb.MatchmakingTicket{MatchmakingConfig: config},
	})
	if err != nil {
		log.Fatalf("player 2: queue: %v", err)
	}
	fmt.Printf("player 2: ticket %s\n", second.GetState())

	stream2, err := mm.TrackMatchmakingTicket(sessions[1],
		&mmpb.TrackMatchmakingTicketRequest{Name: second.GetName()})
	if err != nil {
		log.Fatalf("player 2: track: %v", err)
	}

	// Two players below npln.players_per_match are an even count, so the match
	// forms only after npln.versus_grace_period with nobody else arriving. The
	// stream says PLACING while that runs; read past it to the match.
	matched := waitMatched("player 1", stream)
	fmt.Printf("player 1: told %s, host %s, %d players\n", matched.GetState(),
		matched.GetGameSession().GetHost(), len(matched.GetMatchedUserSessions()))
	other := waitMatched("player 2", stream2)

	// The assertion that matters: two players told about DIFFERENT sessions is a
	// lobby that never fills, and it looks fine from either side alone.
	if matched.GetGameSession().GetName() != other.GetGameSession().GetName() {
		log.Fatalf("the two players were told about different sessions:\n  %s\n  %s",
			matched.GetGameSession().GetName(), other.GetGameSession().GetName())
	}
	session := matched.GetGameSession().GetName()
	fmt.Printf("both players: session %s\n", session)

	// 3. Join, and get the id token gamesync needs.
	gs := mmpb.NewGameSessionServiceClient(conn)
	joined, err := gs.JoinGameSession(sessions[0], &mmpb.JoinGameSessionRequest{Name: session})
	if err != nil {
		log.Fatalf("join: %v", err)
	}
	fmt.Printf("player 1: joined, %d/%d participants\n",
		joined.GetGameSession().GetCurrentParticipantCount(),
		joined.GetGameSession().GetMaxParticipantCount())

	issued, err := gs.IssueMatchmakingIdToken(sessions[0], &mmpb.IssueMatchmakingIdTokenRequest{
		GameSession:         session,
		IncludeIdTokenUsers: []string{userIDs[0]},
	})
	if err != nil {
		log.Fatalf("issue matchmaking id token: %v", err)
	}
	// The token is bound to the user session it was issued with, and
	// gamesync checks the two match, so keep both.
	var matchmakingIDToken, userSession string
	for _, s := range issued.GetMatchedUserSessions() {
		if s.GetMatchmakingIdToken() != "" {
			matchmakingIDToken, userSession = s.GetMatchmakingIdToken(), s.GetUserSession()
		}
	}
	if matchmakingIDToken == "" {
		log.Fatal("no matchmaking id token was issued for the caller's own user")
	}
	fmt.Println("player 1: matchmaking id token issued")

	// 4. gamesync: a session bought with that token, a transform, and a read
	//    back. The transform is the interesting one -- incrementing a field that
	//    does not exist has to create it.
	sync := syncpb.NewGamesyncClient(syncConn)
	syncSession, err := sync.IssueToken(context.Background(), &syncpb.IssueTokenRequest{
		MatchmakingIdToken: matchmakingIDToken,
		UserSession:        userSession,
	})
	if err != nil {
		log.Fatalf("gamesync: IssueToken: %v", err)
	}
	fmt.Printf("gamesync: session, ttl %s\n", syncSession.GetToken().GetTtl().AsDuration())

	syncCtx := metadata.NewOutgoingContext(context.Background(),
		metadata.Pairs("authorization", "Bearer "+syncSession.GetToken().GetAccessToken()))
	doc := "lobbies/" + lastSegment(session) + "/score"
	if _, err := sync.WriteDocuments(syncCtx, &syncpb.WriteDocumentsRequest{
		WriteOperations: []*syncpb.WriteOperation{{
			OperationType: &syncpb.WriteOperation_TransformDocument{
				TransformDocument: &syncpb.TransformDocumentRequest{
					Name: doc,
					FieldTransforms: []*syncpb.FieldTransform{{
						FieldPath: "points",
						TransformType: &syncpb.FieldTransform_Increment{
							Increment: &commonpb.Value{
								ValueType: &commonpb.Value_IntegerValue{IntegerValue: 7},
							},
						},
					}},
				},
			},
		}},
	}); err != nil {
		log.Fatalf("gamesync: write: %v", err)
	}
	read, err := sync.GetDocument(syncCtx, &syncpb.GetDocumentRequest{Name: doc})
	if err != nil {
		log.Fatalf("gamesync: read: %v", err)
	}
	points := read.GetFields().GetFields()["points"]
	if points.GetIntegerValue() != 7 {
		log.Fatalf("gamesync: points = %v, want integer 7 — an increment on an "+
			"absent field must create it, and must stay an integer", points)
	}
	fmt.Printf("gamesync: %s points=%d\n", doc, points.GetIntegerValue())

	// 5. The Splatfest, if one is announced.
	fest := toyohrpb.NewFestServiceClient(conn)
	schedule, err := fest.SelectFestSchedule(sessions[0],
		&toyohrpb.SelectFestScheduleRequest{Region: *region})
	if err != nil {
		log.Fatalf("SelectFestSchedule: %v", err)
	}
	if s := schedule.GetSchedule(); s != nil {
		fmt.Printf("fest: %s resource=%s revision=%s\n",
			lastSegment(s.GetFest()), s.GetFestGameData(), s.GetFestGameDataRevision())
		keys, err := fest.GetFestDecryptionKey(sessions[0],
			&toyohrpb.GetFestDecryptionKeyRequest{Name: s.GetFest() + "/decryptionKey"})
		if err != nil {
			log.Fatalf("GetFestDecryptionKey: %v", err)
		}
		// The three announced values plus these keys are what must match the
		// seed on the console. `switchnetctl fest build` makes them match by
		// construction; this is where a mismatch would first be visible.
		fmt.Printf("fest: keys notice=%t start=%t alpha=%t\n",
			keys.GetNoticeKey() != "", keys.GetStartKey() != "", keys.GetTeamAlphaKey() != "")
	} else {
		fmt.Printf("fest: none announced for %s (build one with `switchnetctl fest build`)\n", *region)
	}

	// 6. One unimplemented RPC, to prove the to-do-list log line fires. Every
	//    toyohr service is registered precisely so that it does.
	_, err = toyohrpb.NewCloudSaveClient(conn).GetSaveRecord(sessions[0],
		&toyohrpb.GetSaveRecordRequest{})
	fmt.Printf("unimplemented RPC answered: %v\n", err)
	fmt.Println("\nDone. Check the server log for:")
	fmt.Println(`  msg="npln: the console asked for an RPC this server does not implement"`)
	fmt.Println(`  msg="npln: matchmaking mode has no configured size"`)
	fmt.Println("Those two lines are the to-do list and the mode-name discovery.")
}

func dial(addr string) *grpc.ClientConn {
	// Cleartext: TLS is terminated at the nginx edge, and this talks to the
	// backend directly, the same way nginx does.
	conn, err := grpc.NewClient(addr, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		log.Fatalf("dial %s: %v", addr, err)
	}
	return conn
}

func readAll(paths []string) []string {
	out := make([]string, 0, len(paths))
	for _, p := range paths {
		if p = strings.TrimSpace(p); p == "" {
			continue
		}
		raw, err := os.ReadFile(p)
		if err != nil {
			log.Fatalf("read %s: %v", p, err)
		}
		out = append(out, strings.TrimSpace(string(raw)))
	}
	return out
}

func lastSegment(name string) string {
	if i := strings.LastIndex(name, "/"); i >= 0 {
		return name[i+1:]
	}
	return name
}

// waitMatched reads a ticket stream until the ticket succeeds, printing each
// state it passes through, and stops the run on any other ending.
func waitMatched(who string, stream grpc.ServerStreamingClient[mmpb.MatchmakingTicket]) *mmpb.MatchmakingTicket {
	for {
		t, err := stream.Recv()
		if err != nil {
			log.Fatalf("%s was never told its ticket matched: %v", who, err)
		}
		switch t.GetState() {
		case mmpb.MatchmakingTicket_SUCCEEDED:
			return t
		case mmpb.MatchmakingTicket_SEARCHING, mmpb.MatchmakingTicket_PLACING:
			fmt.Printf("%s: %s\n", who, t.GetState())
		default:
			log.Fatalf("%s: ticket ended %s instead of matching", who, t.GetState())
		}
	}
}
