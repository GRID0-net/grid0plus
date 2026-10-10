package nplnrest

import (
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"time"
)

// Frontline frames, both directions: a 4-byte little-endian length, then a
// FlatBuffer of that many bytes. Settled by decompiling the npns sysmodule
// (FUN_71000e8d70); see docs/CAPTURES/npns-sysmodule-decompile.md.

// frontlineMaxFrame is the client's own receive buffer (0x3400); it refuses
// anything longer, so nothing longer is accepted from it either.
const frontlineMaxFrame = 0x3400

var errFrontlineFrame = errors.New("nplnrest: bad frontline frame")

// readFrontlineFrame reads one frame's payload.
func readFrontlineFrame(r io.Reader) ([]byte, error) {
	var head [4]byte
	if _, err := io.ReadFull(r, head[:]); err != nil {
		return nil, err
	}
	n := binary.LittleEndian.Uint32(head[:])
	if n == 0 || n > frontlineMaxFrame {
		return nil, fmt.Errorf("%w: length %d", errFrontlineFrame, n)
	}
	payload := make([]byte, n)
	if _, err := io.ReadFull(r, payload); err != nil {
		return nil, err
	}
	return payload, nil
}

// frontlineFrameType is the root table's union tag (field 0), or -1 when the
// buffer is too short or malformed to say.
func frontlineFrameType(p []byte) int {
	if len(p) < 8 {
		return -1
	}
	root := int(binary.LittleEndian.Uint32(p))
	if root+4 > len(p) {
		return -1
	}
	vt := root - int(int32(binary.LittleEndian.Uint32(p[root:])))
	if vt < 0 || vt+4 > len(p) {
		return -1
	}
	vtSize := int(binary.LittleEndian.Uint16(p[vt:]))
	if vtSize < 6 || vt+6 > len(p) {
		return -1
	}
	off := int(binary.LittleEndian.Uint16(p[vt+4:]))
	if off == 0 {
		return 0
	}
	if root+off >= len(p) {
		return -1
	}
	return int(p[root+off])
}

// frontlineTypeNames is the union enum, in the client's own order (its name
// table at 0x7100168968).
var frontlineTypeNames = []string{
	"NONE", "MessageRequest", "PublishRequest", "KickRequest", "PutRecord",
	"DeleteRecord", "RootHash", "LeafHash", "SyncComplete", "Ack",
	"HandoverRequest", "HandoverResponse", "HandoverResult", "Reset",
	"PowerState", "DebugDisconnect", "Ping", "Pong", "DebugUpdateRecord",
	"DebugDeleteRecord", "Idle", "PutRequest", "DeleteRequest",
	"SubscribeTopic", "Nack", "SubscriptionList", "ResendRequest",
	"DAPresence", "SubscriptionListRequest", "MyAccountMessageRequest",
	"ReloadLink", "SubscriptionUpdated", "Hi", "Onset",
	"PresenceNotifyRequest", "DenySubscribe", "TopicRead", "CrewNotifyRequest",
}

func frontlineTypeName(t int) string {
	if t >= 0 && t < len(frontlineTypeNames) {
		return frontlineTypeNames[t]
	}
	return fmt.Sprintf("type%d", t)
}

// frontlineReply answers one console frame, or returns nil. It only ever
// answers: npns aborts on a Pong or Ack it is not waiting for, so nothing
// here is sent unprompted.
//
//   - RootHash: answered with the server's own, which lists no collections.
//     npns compares the server's list with its local mirror of server-owned
//     records (the "s-" keys, FUN_71000b2d00), not with the "c-" list it
//     sends; an echo of that list read as server collections it lacked, so it
//     asked for c-appearance's leaves and stalled. With an empty list it
//     drops any stale mirror and goes FUN_71000dba50 -> FUN_71000dbe50 ->
//     FUN_71000da010, state 8. The list field must be present: npns follows
//     it without checking (FUN_71000da2f0).
//   - SubscribeTopic and DAPresence: an Ack. Both leave npns with a request
//     pending and a 10 s deadline (FUN_71000dd980, FUN_71000de140); with no
//     Ack it drops the connection, which on hardware meant a reconnect every
//     ~11 s and no presence ever settling. The Ack's field 0 must be the id
//     npns stored, compared with strcmp and aborting on a mismatch
//     (FUN_71000deab0): the constant "topic.subscription" for a subscribe,
//     "%016llx/%016llx" of the user and device account ids for a presence --
//     the frame's own fields 1 and 2 (argument order read from the
//     disassembly at 0x71000de254 and the string order in FUN_71000ebf40).
//     Other request kinds stay unanswered until their ids are confirmed.
//   - Ping: a Pong carrying the Ping's first field, which npns compares with
//     the id it stored when it sent the Ping (FUN_71000df9e0).
func (s *Server) frontlineReply(frame []byte, t int) []byte {
	now := time.Now()
	if s.now != nil {
		now = s.now()
	}
	switch t {
	case frontlineRootHash:
		return frontlineFrame(frontlineRootHash, now, func(w *fbWriter) int {
			pos, refs := w.table([]*fbField{ref()})
			w.patch(refs[0], w.emptyVector())
			return pos
		})
	case frontlineSubscribeTopic:
		return frontlineAckFrame(now, "topic.subscription")
	case frontlineDAPresence:
		d, err := parseDAPresence(frame)
		if err != nil {
			return nil
		}
		return frontlineAckFrame(now, d.UserID+"/"+d.DeviceAccountID)
	case frontlinePing:
		body, ok := frontlineBody(frame)
		if !ok {
			return nil
		}
		id, _ := fbU64(frame, body, 0)
		return frontlineFrame(frontlinePong, now, func(w *fbWriter) int {
			pos, _ := w.table([]*fbField{u64(id)})
			return pos
		})
	}
	return nil
}

const (
	frontlineRootHash       = 6
	frontlinePing           = 16
	frontlinePong           = 17
	frontlineAck            = 9
	frontlineDAPresence     = 27
	frontlineSubscribeTopic = 23
)

// firstBytesLogger logs the first bytes the console sends on the frontline,
// whatever their shape, so a body that never forms a whole frame still shows.
type firstBytesLogger struct {
	r      io.Reader
	log    *slog.Logger
	logged int
}

func (l *firstBytesLogger) Read(p []byte) (int, error) {
	n, err := l.r.Read(p)
	if n > 0 && l.logged < 256 {
		shown := p[:min(n, 256-l.logged)]
		l.logged += len(shown)
		l.log.Info("nplnrest: frontline bytes from console", "bytes", n, "hex", hex.EncodeToString(shown))
	}
	return n, err
}

// frontlineAckFrame acknowledges the request npns has pending under id.
func frontlineAckFrame(now time.Time, id string) []byte {
	return frontlineFrame(frontlineAck, now, func(w *fbWriter) int {
		pos, refs := w.table([]*fbField{ref()})
		w.patch(refs[0], w.str(id))
		return pos
	})
}
