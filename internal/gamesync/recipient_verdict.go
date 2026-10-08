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
