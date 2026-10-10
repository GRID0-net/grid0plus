package nplnrest

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
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
