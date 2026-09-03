// Package lobbymessaging is an in-memory broadcast topic per lobby, for
// Splatoon 3's nn.npln.toyohr.v1.LobbyMessaging.
//
// It is deliberately a topic, not a roster: SwitchNet has no separate
// concept of "who is in lobby X" anywhere else, and inventing a membership
// list this package would have to maintain independently of the console's
// own view is exactly the kind of second copy of state ROADMAP.md warns
// against for presence and friends. Instead, membership is defined the way
// a live broadcast defines it everywhere else in this codebase
// (internal/presencebus, internal/matchmaking's ticket Watch): you are "in"
// the lobby for delivery purposes exactly as long as you are subscribed to
// its stream. A console calls RecvMessage naming the lobby it is currently
// in; that call IS the membership.
//
// This package knows nothing about gRPC or protobuf, the same split
// internal/messaging and internal/matchmaking have.
package lobbymessaging

import (
	"strconv"
	"sync"
	"time"
)

// maxTopicDepth bounds how many retained messages one lobby's topic holds.
// Beyond it the oldest is dropped -- the same reasoning internal/messaging's
// maxMailboxDepth uses: a lobby nobody is draining must not grow this
// process without bound.
const maxTopicDepth = 200

// Body is the payload SwitchNet relays without reading, the same opacity
// internal/messaging.Body has and for the same reason: MessageType and
// Fields are the game's own vocabulary.
type Body struct {
	RequestID   string
	MessageType string
	Fields      any
}

// Message is one broadcast message, retained on its lobby's topic.
type Message struct {
	// Seq is this topic's resume token for this item: monotonically
	// increasing per lobby, never reused.
	Seq      uint64
	SenderID string
	Body     Body
	SentAt   time.Time
}

// Queue is every lobby's topic in this process.
type Queue struct {
	mu     sync.Mutex
	seq    uint64
	topics map[string]*topic
	now    func() time.Time
}

type topic struct {
	messages []Message
	subs     []chan Message
}

// New returns an empty Queue. now is overridable in tests; nil defaults to
// time.Now.
func New(now func() time.Time) *Queue {
	if now == nil {
		now = time.Now
	}
	return &Queue{topics: map[string]*topic{}, now: now}
}

func (q *Queue) topicFor(lobbyID string) *topic {
	t, ok := q.topics[lobbyID]
	if !ok {
		t = &topic{}
		q.topics[lobbyID] = t
	}
	return t
}

// Send broadcasts body to everyone currently subscribed to lobbyID's topic,
// and retains it for anyone who reconnects and drains from before it.
func (q *Queue) Send(lobbyID, senderID string, body Body) Message {
	q.mu.Lock()
	defer q.mu.Unlock()

	q.seq++
	m := Message{Seq: q.seq, SenderID: senderID, Body: body, SentAt: q.now()}
	t := q.topicFor(lobbyID)
	t.messages = append(t.messages, m)
	if len(t.messages) > maxTopicDepth {
		t.messages = t.messages[len(t.messages)-maxTopicDepth:]
	}
	for _, ch := range t.subs {
		select {
		case ch <- m:
		default:
			// Best-effort, the same trade internal/messaging's notify makes:
			// a slow subscriber does not block the sender, and the resume
			// token is what actually guarantees delivery on the next Drain.
		}
	}
	return m
}

// Drain returns everything retained on lobbyID's topic after the given
// resume token, and the token to resume from next time. An empty token
// means "everything currently retained" -- a console tuning into a lobby it
// just joined should see what is already there, the same as
// internal/messaging's mailbox on first read.
func (q *Queue) Drain(lobbyID, afterToken string) (messages []Message, token string) {
	q.mu.Lock()
	defer q.mu.Unlock()

	t := q.topicFor(lobbyID)
	after := parseToken(afterToken)
	for _, m := range t.messages {
		if m.Seq > after {
			messages = append(messages, m)
		}
	}
	token = afterToken
	if len(messages) > 0 {
		token = FormatToken(messages[len(messages)-1].Seq)
	}
	return messages, token
}

// Subscribe returns a channel of newly arrived messages on lobbyID's topic,
// and a function to stop receiving them. Call Drain first: Subscribe only
// ever delivers what arrives after it is called.
func (q *Queue) Subscribe(lobbyID string) (<-chan Message, func()) {
	q.mu.Lock()
	defer q.mu.Unlock()

	ch := make(chan Message, 8)
	t := q.topicFor(lobbyID)
	t.subs = append(t.subs, ch)

	stop := func() {
		q.mu.Lock()
		defer q.mu.Unlock()
		for i, s := range t.subs {
			if s == ch {
				t.subs = append(t.subs[:i], t.subs[i+1:]...)
				return
			}
		}
	}
	return ch, stop
}

func parseToken(s string) uint64 {
	if s == "" {
		return 0
	}
	n, err := strconv.ParseUint(s, 10, 64)
	if err != nil {
		// The same tolerance internal/messaging's parseToken has: an
		// unparseable token is treated as "from the beginning" rather than
		// refused, so a console with a token from a different server's
		// format still gets a working stream instead of an error it cannot
		// act on.
		return 0
	}
	return n
}

// FormatToken renders a sequence number as the resume token wire format.
func FormatToken(seq uint64) string { return strconv.FormatUint(seq, 10) }
