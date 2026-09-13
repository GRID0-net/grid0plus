// Package messaging is an in-memory, per-user mailbox for NPLN's Messaging
// service: opaque message and acknowledgement delivery between two
// authenticated users.
//
// In memory, the same tradeoff internal/matchmaking makes for the same
// reason: a message undelivered when this process restarts is one the
// sender's own retry (or the game's own invite-flow timeout) resends, and
// there is no cross-restart invite a private network needs to survive a
// deploy for.
//
// This package knows nothing about gRPC or protobuf, the same split
// internal/matchmaking has: internal/npln/messaging.go is where a Message or
// Ack becomes a RecvMessageResponse.
//
// Reference: https://github.com/kinnay/NintendoClients/wiki/NPLN-Servers
package messaging

import (
	"strconv"
	"sync"
	"time"
)

// maxMailboxDepth bounds how many undelivered messages or acks one user's
// mailbox holds. Beyond it the oldest is dropped: a console that never
// connects to drain its mailbox must not grow this process without bound.
const maxMailboxDepth = 200

// Body is the payload SwitchNet relays without reading. MessageType and
// Fields are the game's own vocabulary -- passed through untouched, the same
// way internal/baas's presence AppField is never parsed, because guessing at
// a game-private format is exactly what CLAUDE.md warns against for an
// undocumented shape.
type Body struct {
	RequestID   string
	NeedAck     bool
	MessageType string
	// Fields holds whatever the caller passed as the message's fields --
	// opaque to this package, which never inspects it. The npln package type
	// -asserts it back to *common.MapValue when rendering the wire message.
	Fields any
}

// Message is one delivered message, queued for its receiver.
type Message struct {
	// Seq is this mailbox's resume token for this item: monotonically
	// increasing per user, never reused.
	Seq      uint64
	SenderID string
	Body     Body
	SentAt   time.Time
}

// Ack is one delivered acknowledgement, queued for the message's original
// sender.
type Ack struct {
	Seq            uint64
	RequestID      string
	OriginalSender string
	// AckedByID is who sent the acknowledgement -- informational for the
	// original sender, who may have sent the same message to several
	// receivers and needs to know which one this is.
	AckedByID string
	SentAt    time.Time
}

// Event is either a Message or an Ack, delivered live to a Subscribe channel.
type Event any

// Queue is every user's mailbox in this process.
type Queue struct {
	mu   sync.Mutex
	seq  uint64
	mail map[string]*mailbox
	now  func() time.Time
}

type mailbox struct {
	messages []Message
	acks     []Ack
	subs     []chan Event
}

// New returns an empty Queue. now is overridable in tests; nil defaults to
// time.Now.
func New(now func() time.Time) *Queue {
	if now == nil {
		now = time.Now
	}
	return &Queue{mail: map[string]*mailbox{}, now: now}
}

func (q *Queue) box(userID string) *mailbox {
	b, ok := q.mail[userID]
	if !ok {
		b = &mailbox{}
		q.mail[userID] = b
	}
	return b
}

// Send delivers body to each receiver's mailbox.
func (q *Queue) Send(senderID string, receiverIDs []string, body Body) {
	q.mu.Lock()
	defer q.mu.Unlock()
	for _, r := range receiverIDs {
		q.seq++
		m := Message{Seq: q.seq, SenderID: senderID, Body: body, SentAt: q.now()}
		b := q.box(r)
		b.messages = append(b.messages, m)
		if len(b.messages) > maxMailboxDepth {
			b.messages = b.messages[len(b.messages)-maxMailboxDepth:]
		}
		notify(b, m)
	}
}

// Ack delivers an acknowledgement to the original message sender's mailbox.
func (q *Queue) Ack(ackedByID, requestID, originalSenderID string) {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.seq++
	a := Ack{
		Seq: q.seq, RequestID: requestID,
		OriginalSender: originalSenderID, AckedByID: ackedByID, SentAt: q.now(),
	}
	b := q.box(originalSenderID)
	b.acks = append(b.acks, a)
	if len(b.acks) > maxMailboxDepth {
		b.acks = b.acks[len(b.acks)-maxMailboxDepth:]
	}
	notify(b, a)
}

func notify(b *mailbox, item Event) {
	for _, ch := range b.subs {
		select {
		case ch <- item:
		default:
			// Best-effort: a slow subscriber does not block the sender. The
			// resume token is what actually guarantees delivery -- Drain is
			// called again on every reconnect.
		}
	}
}

// Drain returns everything queued for userID after the given resume tokens,
// and the tokens to resume from next time. An empty token means "everything
// currently retained" -- the mailbox has never been read, the same as a
// console opening its invite mailbox for the first time this boot.
func (q *Queue) Drain(userID, afterMessageToken, afterAckToken string) (messages []Message, acks []Ack, messageToken, ackToken string) {
	q.mu.Lock()
	defer q.mu.Unlock()
	b := q.box(userID)

	afterM := parseToken(afterMessageToken)
	for _, m := range b.messages {
		if m.Seq > afterM {
			messages = append(messages, m)
		}
	}
	afterA := parseToken(afterAckToken)
	for _, a := range b.acks {
		if a.Seq > afterA {
			acks = append(acks, a)
		}
	}

	messageToken = afterMessageToken
	if len(messages) > 0 {
		messageToken = FormatToken(messages[len(messages)-1].Seq)
	}
	ackToken = afterAckToken
	if len(acks) > 0 {
		ackToken = FormatToken(acks[len(acks)-1].Seq)
	}
	return messages, acks, messageToken, ackToken
}

// Subscribe returns a channel of newly arrived Messages and Acks for userID,
// and a function to stop receiving them. Call Drain first: Subscribe only
// ever delivers what arrives after it is called.
func (q *Queue) Subscribe(userID string) (<-chan Event, func()) {
	q.mu.Lock()
	defer q.mu.Unlock()
	ch := make(chan Event, 8)
	b := q.box(userID)
	b.subs = append(b.subs, ch)

	stop := func() {
		q.mu.Lock()
		defer q.mu.Unlock()
		for i, s := range b.subs {
			if s == ch {
				b.subs = append(b.subs[:i], b.subs[i+1:]...)
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
		// An unparseable token from a console that made one up, or from a
		// different server's format entirely. Treated as "from the
		// beginning" rather than refusing the call: resending what the
		// console already has is a duplicate, which NeedAck-aware callers
		// already have to tolerate, whereas silently dropping messages does
		// not.
		return 0
	}
	return n
}

// FormatToken renders a sequence number as the resume token wire format.
func FormatToken(seq uint64) string { return strconv.FormatUint(seq, 10) }
