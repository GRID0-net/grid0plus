package gamesync

import (
	"strings"

	syncpb "github.com/n-popescu/grid0plus/proto/gamesync/v1"
	"google.golang.org/protobuf/proto"
)

// Versus results arrive player by player. A final status is usable only after
// the recipient's personal result is present; otherwise the game can advance
// to its result screen with no result for itself. Keep the shared verdict
// unchanged, and project the pending status separately for each recipient.
func recipientVerdict(doc *syncpb.Document, userID string) *syncpb.Document {
	if !strings.HasPrefix(doc.GetName(), refereeResultPrefix) {
		return doc
	}
	f := doc.GetFields().GetFields()
	if f["status"].GetIntegerValue() != 1 || f["shift_id"] != nil || f["scenario"] != nil {
		return doc
	}
	for _, entry := range f["personal_result"].GetMapValue().GetFields() {
		if userID != "" && entry.GetMapValue().GetFields()["npln_user_id"].GetStringValue() == userID {
			return doc
		}
	}
	pending := proto.Clone(doc).(*syncpb.Document)
	pending.Fields.Fields["status"] = integer(0)
	return pending
}

// The client enters referee state 1 when it submits @@RefereeRequest, then
// requires status -1 to reach state 2. Its own @@Profile write enters state 3,
// where status 0 is accepted (v11.3.0: 0x14fe4b0 and 0x14ff540). Sending 0
// immediately skips the early handshake and leaves the 900-update fallback.
// Project readiness per user session so an earlier peer cannot skip this phase
// for a late watcher. The shared document remains at -1 until final reporting.
func (s *Server) startupVerdict(doc *syncpb.Document, scope, userSession string) *syncpb.Document {
	if !strings.HasPrefix(doc.GetName(), refereeResultPrefix) || doc.GetFields().GetFields()["status"] == nil ||
		doc.GetFields().GetFields()["status"].GetIntegerValue() != -1 || scope == "" || userSession == "" {
		return doc
	}
	battle := strings.TrimPrefix(doc.GetName(), refereeResultPrefix)
	if !strings.HasSuffix(battle, "_"+scope) {
		return doc
	}
	profile := refereeProfilePrefix + battle + "/User/" + sessionID(userSession)
	if _, ok := s.docs.in(scope).snapshot(profile); !ok {
		return doc
	}
	ready := proto.Clone(doc).(*syncpb.Document)
	ready.Fields.Fields["status"] = integer(0)
	return ready
}
