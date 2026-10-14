package gamesync

import (
	"crypto/sha256"
	"fmt"
	"strings"

	commonpb "github.com/n-popescu/grid0plus/proto/common"
	syncpb "github.com/n-popescu/grid0plus/proto/gamesync/v1"
)

// Opt-in lobby diagnostics record packet direction and shape, never payloads,
// ICE addresses, credentials or player profile contents.
func (s *Server) logPeerDocument(caller *session, doc *syncpb.Document, event, target string) {
	if !s.lobbyLog.Load() {
		return
	}
	f := doc.GetFields().GetFields()
	if strings.HasPrefix(doc.GetName(), refereeResultPrefix) {
		s.log.Info("gamesync: referee delivery", "event", event, "session", scopeOf(caller),
			"user", caller.userID, "target", target, "battle", strings.TrimPrefix(doc.GetName(), refereeResultPrefix),
			"status", f["status"].GetIntegerValue())
		return
	}
	if !strings.HasPrefix(doc.GetName(), peerInboxPrefix) {
		return
	}
	digest := sha256.Sum256(f["pl"].GetBytesValue())
	s.log.Info("gamesync: peer transport", "event", event, "session", scopeOf(caller),
		"user", caller.userID, "source_usid", f["susid"].GetStringValue(),
		"recipient_usid", sessionID(doc.GetName()), "target", target,
		"payload_kind", peerValueKind(f["pl"]), "bytes", len(f["pl"].GetBytesValue()), "payload_hash", fmt.Sprintf("%x", digest[:8]),
		"connection_id_kind", peerValueKind(f["suscid"]), "session_id_kind", peerValueKind(f["sussid"]))
}

func peerValueKind(value *commonpb.Value) string {
	switch value.GetValueType().(type) {
	case *commonpb.Value_BytesValue:
		return "bytes"
	case *commonpb.Value_MapValue:
		return "map"
	case *commonpb.Value_IntegerValue:
		return "integer"
	case *commonpb.Value_StringValue:
		return "string"
	default:
		return "other"
	}
}
