package nplnrest

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/n-popescu/grid0plus/internal/db"
	"github.com/n-popescu/grid0plus/internal/presence"
	"github.com/n-popescu/grid0plus/internal/presencebus"
)

// Push notifications, as npns receives them:
//
//   - npns asks god.hac.*.penne for a notification token per local account
//     (POST /v1/accounts/<Uid>/notification_tokens, {"penne_id",
//     "application_id"}); the account sysmodule registers that token with BAAS
//     as the user's push channel.
//   - the frontline connection belongs to a penne id, through the login ticket it
//     presents.
//   - a notification is a PutRecord whose record (kind 1) names a topic
//     ("0x<program id>" reaches whoever called npns ListenTo with it), the local
//     account Uid, and a payload npns hands over unchanged.
//
// So a push to a user walks user -> push channels -> tokens -> (penne id, Uid) ->
// that penne id's open frontlines.

// friendsTopic reaches the friends sysmodule, which listens on its own program
// id (0x010000000000000e).
const friendsTopic = "0x010000000000000e"

// godPenne answers god.hac.*.penne.srv.nintendo.net.
func (s *Server) godPenne(w http.ResponseWriter, r *http.Request) {
	path := r.URL.Path
	switch {
	case r.Method == http.MethodPost && path == "/v1/penne_ids":
		s.penneIDs(w, r)
	case r.Method == http.MethodPost && strings.HasPrefix(path, "/v1/accounts/") &&
		strings.HasSuffix(path, "/notification_tokens"):
		s.penneNotificationToken(w, r, strings.TrimSuffix(strings.TrimPrefix(path, "/v1/accounts/"), "/notification_tokens"))
	default:
		// /links, /immigrate and the rest: the console proceeds on an empty
		// object, as it did when internal/stubs answered this host.
		s.answer(w, r, http.StatusOK, map[string]any{})
	}
}

// penneIDs answers POST /v1/penne_ids with a fresh identity: an id (npns keeps
// at most 33 characters) and a password (32). Nothing checks the password
// later: the ticket is what authenticates a frontline.
func (s *Server) penneIDs(w http.ResponseWriter, r *http.Request) {
	id, pw := make([]byte, 16), make([]byte, 16)
	if _, err := rand.Read(id); err != nil {
		http.Error(w, "", http.StatusInternalServerError)
		return
	}
	if _, err := rand.Read(pw); err != nil {
		http.Error(w, "", http.StatusInternalServerError)
		return
	}
	s.answer(w, r, http.StatusOK, map[string]any{"id": hex.EncodeToString(id), "password": hex.EncodeToString(pw)})
}

// penneNotificationToken answers POST /v1/accounts/<Uid>/notification_tokens
// and remembers which console and account the token belongs to.
//
// The token is derived exactly as internal/stubs derived it before this host
// moved here, so a channel BAAS already holds still names the same token.
func (s *Server) penneNotificationToken(w http.ResponseWriter, r *http.Request, account string) {
	var body struct {
		PenneID       string `json:"penne_id"`
		ApplicationID string `json:"application_id"`
	}
	if raw, err := io.ReadAll(io.LimitReader(r.Body, 4096)); err == nil {
		_ = json.Unmarshal(raw, &body)
	}
	sum := sha256.Sum256([]byte("penne-notification-token\x00" + account + "\x00" + body.ApplicationID))
	token := hex.EncodeToString(sum[:18])
	if s.store != nil && body.PenneID != "" && account != "" && !strings.Contains(account, "/") {
		if err := s.store.SetPenneToken(r.Context(), db.PenneToken{
			Token: token, PenneID: body.PenneID, Account: strings.ToLower(account), ApplicationID: body.ApplicationID,
		}); err != nil {
			s.log.Error("nplnrest: store notification token", "error", err)
		}
	}
	s.answer(w, r, http.StatusOK, map[string]any{"notification_token": token})
}

// ---- connections ----------------------------------------------------------

// frontlineConn is one console's open frontline. npns holds at most one
// PutRecord at a time and aborts on a second before acknowledging the first, so
// records queue here until the console's Ack.
type frontlineConn struct {
	penneID string
	send    func([]byte) error
	// log, when set, records each pushed record: replies are logged where
	// they are made, pushes were not, and console Acks were the only trace.
	log *slog.Logger

	mu       sync.Mutex
	users    map[string]string // user id -> console account Uid, from DAPresence
	ready    bool              // the twin sync is over: npns is in state 8
	inflight bool
	closed   bool // the handler is returning; its ResponseWriter is gone
	queue    [][]byte
}

func (c *frontlineConn) close() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.closed = true
}

const frontlineQueueMax = 32

func (c *frontlineConn) push(frame []byte) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.queue) >= frontlineQueueMax {
		c.queue = c.queue[1:]
	}
	c.queue = append(c.queue, frame)
	c.flushLocked()
}

// pushFirst queues frame ahead of everything waiting.
func (c *frontlineConn) pushFirst(frame []byte) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.queue = append([][]byte{frame}, c.queue...)
	c.flushLocked()
}

// acked is the console's Ack for the record in flight.
func (c *frontlineConn) acked() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.inflight = false
	c.flushLocked()
}

func (c *frontlineConn) synced() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.ready = true
	c.flushLocked()
}

func (c *frontlineConn) flushLocked() {
	if c.closed || !c.ready || c.inflight || len(c.queue) == 0 {
		return
	}
	frame := c.queue[0]
	c.queue = c.queue[1:]
	if err := c.send(frame); err == nil {
		c.inflight = true
		if c.log != nil {
			c.log.Info("nplnrest: frontline record pushed", "penne_id", c.penneID, "bytes", len(frame)-4, "queued", len(c.queue))
		}
	}
}

type frontlineRegistry struct {
	mu    sync.Mutex
	conns map[string]map[*frontlineConn]bool
}

func (r *frontlineRegistry) add(c *frontlineConn) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.conns == nil {
		r.conns = map[string]map[*frontlineConn]bool{}
	}
	if r.conns[c.penneID] == nil {
		r.conns[c.penneID] = map[*frontlineConn]bool{}
	}
	r.conns[c.penneID][c] = true
}

func (r *frontlineRegistry) remove(c *frontlineConn) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.conns[c.penneID], c)
	if len(r.conns[c.penneID]) == 0 {
		delete(r.conns, c.penneID)
	}
}

// bind records that userID is signed in on this console as account (Uid).
func (c *frontlineConn) bind(userID, account string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.users == nil {
		c.users = map[string]string{}
	}
	c.users[userID] = account
}

func (c *frontlineConn) accountOf(userID string) (string, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	a, ok := c.users[userID]
	return a, ok
}

// boundTo returns the connections a verified DAPresence tied to userID.
func (r *frontlineRegistry) boundTo(userID string) []*frontlineConn {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []*frontlineConn
	for _, set := range r.conns {
		for c := range set {
			if _, ok := c.accountOf(userID); ok {
				out = append(out, c)
			}
		}
	}
	return out
}

func (r *frontlineRegistry) of(penneID string) []*frontlineConn {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]*frontlineConn, 0, len(r.conns[penneID]))
	for c := range r.conns[penneID] {
		out = append(out, c)
	}
	return out
}

// ---- pushing --------------------------------------------------------------

// Notify sends payload to every console where userID is signed in, on topic.
func (s *Server) Notify(ctx context.Context, userID, topic string, payload []byte) {
	if s.store == nil {
		return
	}
	now := s.clock()
	sent := map[string]bool{}
	// Consoles whose DAPresence named this user. This reaches consoles that
	// linked their Nintendo Account before push channels were remembered,
	// which never send PUT /push_channels again until they re-link.
	for _, c := range s.registry.boundTo(userID) {
		account, _ := c.accountOf(userID)
		key := c.penneID + "/" + account
		if sent[key] {
			continue
		}
		sent[key] = true
		c.push(notificationFrame(now, topic, account, payload))
	}
	chans, err := s.store.PushChannels(ctx, []string{userID})
	if err != nil || len(chans) == 0 {
		return
	}
	tokens := make([]string, 0, len(chans))
	for _, c := range chans {
		tokens = append(tokens, c.DeviceToken)
	}
	owners, err := s.store.PenneTokens(ctx, tokens)
	if err != nil {
		s.log.Warn("nplnrest: look up notification tokens", "error", err)
		return
	}
	for _, o := range owners {
		key := o.PenneID + "/" + o.Account
		if sent[key] {
			continue
		}
		sent[key] = true
		for _, c := range s.registry.of(o.PenneID) {
			c.push(notificationFrame(now, topic, o.Account, payload))
		}
	}
}

func (s *Server) clock() time.Time {
	if s.now != nil {
		return s.now()
	}
	return time.Now()
}

// notificationFrame is a PutRecord carrying one notification record.
//
// Record: { kind ubyte = 1, body union = Notification }. Notification fields:
// 0 id string (<= 47), 1 u16 kind (not 2: 2 is a per-NSA notification whose
// target comes from the id), 2 a table of {1 topic list, 2 Uid as 32 hex digits,
// 4 target string}, 4 payload (<= 4095), 7 expiry as Unix seconds.
func notificationFrame(now time.Time, topic, account string, payload []byte) []byte {
	id := make([]byte, 16)
	_, _ = rand.Read(id)
	return frontlineFrame(4, now, func(w *fbWriter) int {
		put, putRefs := w.table([]*fbField{ref()})
		// The vector of records, one element.
		w.align(4)
		vec := len(w.b)
		w.b = append(w.b, 1, 0, 0, 0, 0, 0, 0, 0)
		w.patch(putRefs[0], vec)
		rec, recRefs := w.table([]*fbField{u8(1), ref()})
		w.patch(vec+4, rec)
		n, nRefs := w.table([]*fbField{ref(), u16(1), ref(), nil, ref(), nil, nil, i64(now.Add(24 * time.Hour).Unix())})
		w.patch(recRefs[1], n)
		uidField := ref()
		if account == "" {
			uidField = nil
		}
		sub, subRefs := w.table([]*fbField{nil, ref(), uidField, nil, ref()})
		w.patch(nRefs[2], sub)
		w.patch(nRefs[0], w.str(hex.EncodeToString(id)))
		w.patch(nRefs[4], w.str(string(payload)))
		w.patch(subRefs[1], w.str(topic))
		if account != "" {
			w.patch(subRefs[2], w.str(account))
		}
		w.patch(subRefs[4], w.str(""))
		return put
	})
}

// watchPresence turns presence changes into presence_updated notifications to
// the changed user's friends, which is what makes a console's friend list
// show "playing" without waiting for its next refresh.
func (s *Server) watchPresence(ctx context.Context) {
	events, err := s.bus.Subscribe(ctx)
	if err != nil {
		s.log.Warn("nplnrest: presence bus unavailable; no presence notifications", "error", err)
		return
	}
	sent := map[string]string{}
	tick := time.NewTicker(presenceSweepEvery)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case e, ok := <-events:
			if !ok {
				return
			}
			sent[e.UserID] = e.State
			s.notifyPresence(ctx, e)
		case <-tick.C:
			s.sweepLapsedPresence(ctx, sent)
		}
	}
}

// presenceSweepEvery is how often a pushed state is checked against the TTL
// rules. A friend's console only changes what it shows when it is told, so a
// state that lapsed without a report -- a console put to sleep or switched
// off -- stayed "online" on every friend's list until they reopened it.
const presenceSweepEvery = 30 * time.Second

// sweepLapsedPresence pushes the state a user's last pushed state has
// lapsed to, once, and forgets users who have lapsed to OFFLINE. It returns
// what it pushed.
func (s *Server) sweepLapsedPresence(ctx context.Context, sent map[string]string) []presencebus.Event {
	if s.resolver.ReportTTL <= 0 || s.resolver.ConsoleTTL <= 0 || len(sent) == 0 {
		return nil
	}
	ids := make([]string, 0, len(sent))
	for id := range sent {
		ids = append(ids, id)
	}
	now := s.now()
	effective, err := s.resolver.EffectiveMany(ctx, ids, now)
	if err != nil {
		s.log.Warn("nplnrest: presence sweep", "error", err)
		return nil
	}
	var pushed []presencebus.Event
	for _, id := range ids {
		p, ok := effective[id]
		if !ok || p.State == sent[id] {
			if ok && p.State == db.StateOffline {
				delete(sent, id)
			}
			continue
		}
		s.log.Info("nplnrest: presence lapsed", "user", id, "from", sent[id], "to", p.State)
		e := presencebus.Event{
			UserID: id, State: p.State, AppID: p.AppID, PresenceGroupID: p.PresenceGroupID,
			AppField: p.AppField, At: now, Source: "sweep",
		}
		s.notifyPresence(ctx, e)
		pushed = append(pushed, e)
		if p.State == db.StateOffline {
			delete(sent, id)
		} else {
			sent[id] = p.State
		}
	}
	return pushed
}

func (s *Server) notifyPresence(ctx context.Context, e presencebus.Event) {
	owner, err := s.store.GetUser(ctx, e.UserID)
	if err != nil {
		return
	}
	friends, err := s.store.ListFriends(ctx, e.UserID, 300)
	if err != nil {
		return
	}
	payload, err := json.Marshal(presenceUpdated(e))
	if err != nil {
		return
	}
	for _, f := range friends {
		if presence.Visible(ctx, s.store, owner, f.FriendID) {
			s.Notify(ctx, f.FriendID, friendsTopic, payload)
		}
	}
}

// presenceUpdated is the friends sysmodule's "presence_updated" notification
// (dispatched on $.type): the same presence object the friend list carries.
func presenceUpdated(e presencebus.Event) map[string]any {
	friends := map[string]any{}
	if e.State == db.StatePlaying {
		friends[presence.KeyAppID] = e.AppID
		friends[presence.KeyPresenceGroupID] = e.PresenceGroupID
		friends[presence.KeyAppField] = e.AppField
		friends[presence.KeyACDIndex] = 0
	}
	value := map[string]any{
		"userId":    e.UserID,
		"state":     e.State,
		"pf":        1,
		"updatedAt": e.At.Unix(),
		"logoutAt":  0,
		"extras":    map[string]any{"friends": friends},
	}
	if e.State == db.StateOffline {
		value["logoutAt"] = e.At.Unix()
	}
	return map[string]any{"type": "presence_updated", "createdAt": e.At.Unix(), "value": value}
}

// presenceModeFrame is npns's own control record: payload "presence:nxbaas" on
// topic "penne" sets its presence mode to BAAS. In the other mode, "penne", the
// friends sysmodule hands its presence to npns to send up the frontline instead
// of PATCHing /1.0.0/users/<id>, and the BAAS path is the one GRID0+ stores,
// resolves and shows on the dashboard.
func presenceModeFrame(now time.Time) []byte {
	return notificationFrame(now, "penne", "", []byte("presence:nxbaas"))
}

// watchNotifications forwards BAAS's notifications to the recipient's
// consoles, on the friends topic: every type BAAS sends is one the friends
// sysmodule dispatches.
func (s *Server) watchNotifications(ctx context.Context) {
	events, err := s.notify.Subscribe(ctx)
	if err != nil {
		s.log.Warn("nplnrest: notification bus unavailable", "error", err)
		return
	}
	for {
		select {
		case <-ctx.Done():
			return
		case e, ok := <-events:
			if !ok {
				return
			}
			if e.Type != "" && len(e.Payload) > 0 {
				s.Notify(ctx, e.UserID, friendsTopic, e.Payload)
			}
		}
	}
}
