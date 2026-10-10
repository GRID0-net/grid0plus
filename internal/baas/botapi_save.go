package baas

import (
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/n-popescu/grid0plus/internal/db"
)

// wipeSplatoon3Save deletes the caller's Splatoon 3 cloud save record, after
// archiving it.
//
// Why a person wants this: the console plays from its local save and only
// uploads what changes (2026-10-10, hardware: after a wipe it wrote 3 keys,
// 95 bytes, not the whole save). Someone who puts a different save on their
// console therefore gets its changes merged key by key into the old cloud
// record, a mix of two saves that a restore would bring back. Wiping first
// lets the cloud copy follow the new save alone. The game itself is
// unaffected: told the record is empty, it keeps its local save (the hall,
// news and gear change all worked after a wipe on 2026-10-10).
//
// The archive is written before the delete, and a failed archive refuses the
// wipe, so a save is never lost to this command.
func (b *botAPI) wipeSplatoon3Save(w http.ResponseWriter, r *http.Request, discordID string) {
	if b.saveRecordPrefix == "" || b.saveBackupDir == "" {
		writeBotError(w, http.StatusServiceUnavailable, "save_wipe_disabled", "cloud save wipe is not configured")
		return
	}
	var req struct {
		Username string `json:"username"`
	}
	if r.ContentLength != 0 {
		if err := decodeJSONBody(r, &req); err != nil {
			writeBotError(w, http.StatusBadRequest, "bad_request", "unreadable body")
			return
		}
	}
	user, ok := b.ownAccount(w, r, discordID, req.Username)
	if !ok {
		return
	}

	name := b.saveRecordPrefix + user.ID
	doc, err := b.s.store.GetUGCDocument(r.Context(), name)
	switch {
	case errors.Is(err, db.ErrNotFound):
		writeBotError(w, http.StatusNotFound, "no_cloud_save", "this account has no Splatoon 3 cloud save")
		return
	case err != nil:
		b.internal(w, "load save record", err)
		return
	}

	backup := filepath.Join(b.saveBackupDir,
		user.ID+"-"+time.Now().UTC().Format("20060102T150405Z")+"-wipe.pb")
	if err := os.MkdirAll(b.saveBackupDir, 0o700); err != nil {
		b.internal(w, "create save backup dir", err)
		return
	}
	if err := os.WriteFile(backup, doc.Fields, 0o600); err != nil {
		b.internal(w, "archive save record", err)
		return
	}
	if err := b.s.store.DeleteUGCDocument(r.Context(), name); err != nil && !errors.Is(err, db.ErrNotFound) {
		b.internal(w, "delete save record", err)
		return
	}
	b.s.log.Info("botapi: Splatoon 3 cloud save wiped", "user", user.ID, "bytes", len(doc.Fields), "backup", backup)
	writeJSON(w, http.StatusOK, map[string]any{
		"login": user.LoginUsername, "bytes": len(doc.Fields), "backup": filepath.Base(backup),
	})
}

// ownAccount resolves the account a command acts on: the named login if one
// was given, else the caller's main account. A named login is looked up only
// among the caller's own accounts, so a login that does not exist and one
// that belongs to somebody else are the same refusal.
func (b *botAPI) ownAccount(w http.ResponseWriter, r *http.Request, discordID, login string) (db.User, bool) {
	var user db.User
	var err error
	if login == "" {
		user, err = b.s.store.GetUserByDiscordID(r.Context(), discordID)
	} else {
		user, err = b.s.store.GetUserByDiscordLogin(r.Context(), discordID, login)
	}
	switch {
	case errors.Is(err, db.ErrNotFound) || (err == nil && user.Deleted):
		if login == "" {
			writeBotError(w, http.StatusNotFound, "not_registered", "this Discord account has no GRID0+ account")
		} else {
			writeBotError(w, http.StatusNotFound, "not_your_account", "that login is not one of your accounts")
		}
		return db.User{}, false
	case err != nil:
		b.internal(w, "load account", err)
		return db.User{}, false
	}
	return user, true
}
