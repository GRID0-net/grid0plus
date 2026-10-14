package gamesync

import (
	"strings"
	"time"

	syncpb "github.com/n-popescu/grid0plus/proto/gamesync/v1"
)

// Only the stream writer accesses these watermarks. A newer initial/refresh
// snapshot can overtake already-queued live changes; never roll its state back.
// Pia inboxes have multiple senders, so each sender needs its own watermark.
type watchDelivery struct {
	write    func(*syncpb.KeepUserSessionResponse) error
	versions map[string]time.Time
}

func newWatchDelivery(write func(*syncpb.KeepUserSessionResponse) error) *watchDelivery {
	return &watchDelivery{write: write, versions: map[string]time.Time{}}
}

func deliveryKey(change *syncpb.DocumentChange) string {
	doc := change.GetDocument()
	sender := ""
	if strings.HasPrefix(doc.GetName(), peerInboxPrefix) {
		sender = doc.GetFields().GetFields()["susid"].GetStringValue()
	}
	return change.GetTargetId() + "\x00" + doc.GetName() + "\x00" + sender
}

func (d *watchDelivery) stale(change *syncpb.DocumentChange) bool {
	// Deletes carry the deleted document's old timestamp. They are a new
	// event, not an old version, and must clear even an unchanged snapshot.
	if change.GetDocumentChangeType() == syncpb.DocumentChange_DELETED || change.GetDocument().GetUpdateTime() == nil {
		return false
	}
	return change.GetDocument().GetUpdateTime().AsTime().Before(d.versions[deliveryKey(change)])
}

func (d *watchDelivery) send(msg *syncpb.KeepUserSessionResponse) error {
	if err := d.write(msg); err != nil {
		return err
	}
	change := msg.GetDocumentChange()
	if change == nil {
		return nil
	}
	if change.GetDocumentChangeType() == syncpb.DocumentChange_DELETED {
		prefix := change.GetTargetId() + "\x00" + change.GetDocument().GetName() + "\x00"
		for key := range d.versions {
			if strings.HasPrefix(key, prefix) {
				delete(d.versions, key)
			}
		}
	} else if stamp := change.GetDocument().GetUpdateTime(); stamp != nil {
		d.versions[deliveryKey(change)] = stamp.AsTime()
	}
	return nil
}

func (d *watchDelivery) reset(targetID string) {
	for key := range d.versions {
		if strings.HasPrefix(key, targetID+"\x00") {
			delete(d.versions, key)
		}
	}
}
