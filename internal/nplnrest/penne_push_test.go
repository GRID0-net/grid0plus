package nplnrest

import (
	"bufio"
	"context"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/n-popescu/grid0plus/internal/db"
	"github.com/n-popescu/grid0plus/internal/presencebus"
)

// fbString reads the string a uoffset slot points at.
func fbString(p []byte, slot int) string {
	pos, ok := fbTable(p, slot)
	if !ok {
		return ""
	}
	n := int(binary.LittleEndian.Uint32(p[pos:]))
	return string(p[pos+4 : pos+4+n])
}

// A friend's presence change reaches every console the friend is signed in
// on, as a PutRecord npns hands to the friends sysmodule; and npns holds one
// record at a time, so a second waits for its Ack.
func TestAPresenceChangeIsPushedToAFriendsConsoleOneRecordAtATime(t *testing.T) {
	ctx := t.Context()
	store := db.NewMemory()
	now := time.Date(2026, 8, 28, 12, 0, 0, 0, time.UTC)
	for _, id := range []string{"aaaaaaaaaaaaaaaa", "bbbbbbbbbbbbbbbb"} {
		if err := store.CreateUser(ctx, db.User{ID: id, Nickname: id[:1], CreatedAt: now}, db.DeviceAccount{ID: "da-" + id}); err != nil {
			t.Fatal(err)
		}
	}
	if err := store.AddFriendship(ctx, "aaaaaaaaaaaaaaaa", "bbbbbbbbbbbbbbbb", now); err != nil {
		t.Fatal(err)
	}
	const uid = "0123456789abcdef0123456789abcdef"
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	must(store.SetPushChannel(ctx, db.PushChannel{UserID: "bbbbbbbbbbbbbbbb", DeviceAccountID: "da-b", DeviceToken: "tok-b"}))
	must(store.SetPenneToken(ctx, db.PenneToken{Token: "tok-b", PenneID: "penne-b", Account: uid}))
	must(store.SetPenneTicket(ctx, "ticket-b", "penne-b", now.Add(time.Hour)))

	s, err := New(Options{
		Log: slog.New(slog.NewTextHandler(io.Discard, nil)), FrontlineHost: frontlineHost,
		Now: func() time.Time { return now }, Store: store,
	})
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(s.Handler())
	defer server.Close()
	conn, err := net.DialTimeout("tcp", server.Listener.Addr().String(), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	conn.SetDeadline(time.Now().Add(3 * time.Second))
	fmt.Fprintf(conn, "POST / HTTP/1.1\r\nHost: %s\r\nAuthorization: Bearer ticket-b\r\nTransfer-Encoding: chunked\r\n\r\n", frontlineHost)
	resp, err := http.ReadResponse(bufio.NewReader(conn), &http.Request{Method: http.MethodPost})
	if err != nil {
		t.Fatal(err)
	}
	read := func() []byte {
		t.Helper()
		f, err := readFrontlineFrame(resp.Body)
		if err != nil {
			t.Fatal(err)
		}
		return f
	}
	sendChunk := func(frame []byte) {
		t.Helper()
		fmt.Fprintf(conn, "%x\r\n%s\r\n", len(frame), frame)
	}
	read() // HandoverResult

	// Before the twin sync ends, nothing is pushed.
	playing := presencebus.Event{UserID: "aaaaaaaaaaaaaaaa", State: db.StatePlaying, AppID: "0100c2500fc20000", At: now}
	s.notifyPresence(ctx, playing)
	sendChunk(frontlineFrame(frontlineRootHash, now, func(w *fbWriter) int { pos, _ := w.table(nil); return pos }))
	read() // the RootHash echo

	// First npns's own control record, keeping presence on BAAS.
	ctl := read()
	if frontlineFrameType(ctl) != 4 || !strings.Contains(string(ctl), "presence:nxbaas") || !strings.Contains(string(ctl), "penne") {
		t.Fatalf("first record is not the presence-mode control: % x", ctl)
	}
	sendChunk(frontlineFrame(frontlineAck, now, func(w *fbWriter) int { pos, _ := w.table(nil); return pos }))

	// The queued presence record goes out once that one is acknowledged.
	put := read()
	if frontlineFrameType(put) != 4 {
		t.Fatalf("pushed type = %d, want PutRecord", frontlineFrameType(put))
	}
	body, _ := frontlineBody(put)
	vecSlot, _ := fbFieldPos(put, body, 0)
	vec, _ := fbTable(put, vecSlot)
	if n := binary.LittleEndian.Uint32(put[vec:]); n != 1 {
		t.Fatalf("%d records, npns takes exactly one", n)
	}
	rec, _ := fbTable(put, vec+4)
	if kind, _ := fbFieldPos(put, rec, 0); put[kind] != 1 {
		t.Fatalf("record kind = %d, want 1 (notification)", put[kind])
	}
	nSlot, _ := fbFieldPos(put, rec, 1)
	note, _ := fbTable(put, nSlot)
	subSlot, _ := fbFieldPos(put, note, 2)
	sub, _ := fbTable(put, subSlot)
	topicSlot, _ := fbFieldPos(put, sub, 1)
	uidSlot, _ := fbFieldPos(put, sub, 2)
	if got := fbString(put, topicSlot); got != friendsTopic {
		t.Errorf("topic = %q", got)
	}
	if got := fbString(put, uidSlot); got != uid {
		t.Errorf("account = %q", got)
	}
	payloadSlot, _ := fbFieldPos(put, note, 4)
	var payload struct {
		Type  string `json:"type"`
		Value struct {
			UserID string                    `json:"userId"`
			State  string                    `json:"state"`
			Extras map[string]map[string]any `json:"extras"`
		} `json:"value"`
	}
	if err := json.Unmarshal([]byte(fbString(put, payloadSlot)), &payload); err != nil {
		t.Fatal(err)
	}
	if payload.Type != "presence_updated" || payload.Value.UserID != "aaaaaaaaaaaaaaaa" || payload.Value.State != db.StatePlaying ||
		payload.Value.Extras["friends"]["appInfo:appId"] != "0100c2500fc20000" {
		t.Fatalf("payload = %+v", payload)
	}

	// A second change waits for the console's Ack.
	s.notifyPresence(ctx, presencebus.Event{UserID: "aaaaaaaaaaaaaaaa", State: db.StateOnline, At: now})
	conns := s.registry.of("penne-b")
	if len(conns) != 1 {
		t.Fatalf("%d registered frontlines", len(conns))
	}
	conns[0].mu.Lock()
	queued, inflight := len(conns[0].queue), conns[0].inflight
	conns[0].mu.Unlock()
	if queued != 1 || !inflight {
		t.Fatalf("queued %d, inflight %v: a second record went out before the first was acknowledged", queued, inflight)
	}
	sendChunk(frontlineFrame(frontlineAck, now, func(w *fbWriter) int { pos, _ := w.table(nil); return pos }))
	if got := frontlineFrameType(read()); got != 4 {
		t.Fatalf("after the Ack got type %d, want the next PutRecord", got)
	}
}

// npns's token request: the token must be stable across re-registrations (BAAS
// keeps it as the push channel) and remembered as that console's and
// account's, which is what a push looks up. Sample body from a real console.
func TestANotificationTokenIsStableAndRemembersItsConsole(t *testing.T) {
	store := db.NewMemory()
	s, err := New(Options{
		Log: slog.New(slog.NewTextHandler(io.Discard, nil)), FrontlineHost: frontlineHost, Store: store,
	})
	if err != nil {
		t.Fatal(err)
	}
	const host = "god.hac.lp1.penne.srv.nintendo.net"
	post := func(account string) string {
		t.Helper()
		req := httptest.NewRequest("POST", "http://"+host+"/v1/accounts/"+account+"/notification_tokens",
			strings.NewReader(`{"penne_id":"pzlfh504x771mrn0rpq0mutguiutqebj2","application_id":"0x0000000000000000"}`))
		rec := httptest.NewRecorder()
		s.Handler().ServeHTTP(rec, req)
		var got struct {
			Token string `json:"notification_token"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil || len(got.Token) != 36 ||
			strings.Trim(got.Token, "0123456789abcdef") != "" {
			t.Fatalf("answer %q: want 36 lowercase hex digits", rec.Body.String())
		}
		return got.Token
	}
	a := post("10001ed3f08dc3b4679c13b4a847679c")
	if again := post("10001ed3f08dc3b4679c13b4a847679c"); again != a {
		t.Errorf("a re-registration got %q, then %q", a, again)
	}
	if other := post("10001ecfbee52144679c13b4a847679c"); other == a {
		t.Error("two accounts got the same token")
	}
	owners, err := store.PenneTokens(t.Context(), []string{a})
	if err != nil || len(owners) != 1 || owners[0].PenneID != "pzlfh504x771mrn0rpq0mutguiutqebj2" ||
		owners[0].Account != "10001ed3f08dc3b4679c13b4a847679c" {
		t.Fatalf("token owner = %+v, %v", owners, err)
	}
}

// BAAS's friend notifications reach the recipient's consoles unchanged.
func TestAFriendRequestNotificationReachesTheRecipientsConsole(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	store := db.NewMemory()
	now := time.Date(2026, 8, 28, 12, 0, 0, 0, time.UTC)
	if err := store.CreateUser(ctx, db.User{ID: "bbbbbbbbbbbbbbbb", CreatedAt: now}, db.DeviceAccount{ID: "da-b"}); err != nil {
		t.Fatal(err)
	}
	_ = store.SetPushChannel(ctx, db.PushChannel{UserID: "bbbbbbbbbbbbbbbb", DeviceAccountID: "da-b", DeviceToken: "tok-b"})
	_ = store.SetPenneToken(ctx, db.PenneToken{Token: "tok-b", PenneID: "penne-b", Account: "0123456789abcdef0123456789abcdef"})
	bus := presencebus.NewLocal()
	s, err := New(Options{
		Log: slog.New(slog.NewTextHandler(io.Discard, nil)), FrontlineHost: frontlineHost,
		Now: func() time.Time { return now }, Store: store, Notify: bus,
	})
	if err != nil {
		t.Fatal(err)
	}
	got := make(chan []byte, 1)
	conn := &frontlineConn{penneID: "penne-b", send: func(f []byte) error { got <- f; return nil }}
	conn.synced()
	s.registry.add(conn)
	go s.Run(ctx)

	payload := []byte(`{"type":"friend_request_received","value":{"id":"7cfa2df61adf4e7a"}}`)
	deadline := time.After(2 * time.Second)
	for {
		_ = bus.Publish(ctx, presencebus.Event{UserID: "bbbbbbbbbbbbbbbb", Type: "friend_request_received", Payload: payload})
		select {
		case f := <-got:
			if frontlineFrameType(f[4:]) != 4 || !strings.Contains(string(f), string(payload)) {
				t.Fatalf("pushed % x", f)
			}
			return
		case <-time.After(20 * time.Millisecond):
		case <-deadline:
			t.Fatal("nothing pushed")
		}
	}
}
