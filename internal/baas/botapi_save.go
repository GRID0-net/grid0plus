package baas

import (
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"google.golang.org/protobuf/proto"

	"github.com/n-popescu/grid0plus/internal/db"
	"github.com/n-popescu/grid0plus/internal/splat3save"
)

// wipeSplatoon3Save deletes the caller's Splatoon 3 cloud save record, after
// archiving it.
//
// Why a person wants this: the console plays from its local save and only
// uploads what changes (after a wipe it writes a few keys, not the whole save).
// Someone who puts a different save on their console therefore gets its changes
// merged key by key into the old cloud record, a mix of two saves that a restore
// would bring back. Wiping first lets the cloud copy follow the new save alone.
// The game itself is unaffected: told the record is empty, it keeps its local
// save (the hall, news and gear all work after a wipe).
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

	backup, err := b.archiveSave(user.ID, "wipe", doc.Fields)
	if err != nil {
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

// archiveSave writes a record about to be replaced or deleted to the backup
// directory and returns its path.
func (b *botAPI) archiveSave(uid, why string, fields []byte) (string, error) {
	backup := filepath.Join(b.saveBackupDir,
		uid+"-"+time.Now().UTC().Format("20060102T150405Z")+"-"+why+".pb")
	if err := os.MkdirAll(b.saveBackupDir, 0o700); err != nil {
		return "", err
	}
	return backup, os.WriteFile(backup, fields, 0o600)
}

// keepForReview stores an uploaded save whose fields were not all imported,
// with the list of what was left out, under <backup dir>/../splat3-save-review/
// <uid>/<time>/. Adding the names and importing the kept save.dat again
// brings those fields back.
func (b *botAPI) keepForReview(uid string, save []byte, info splat3save.Info) (string, error) {
	dir := filepath.Join(filepath.Dir(b.saveBackupDir), "splat3-save-review", uid,
		time.Now().UTC().Format("20060102T150405Z"))
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	if err := os.WriteFile(filepath.Join(dir, "save.dat"), save, 0o600); err != nil {
		return "", err
	}
	list := "game version " + info.GameVersion + "\nfields left out (path: name hash):\n" +
		strings.Join(info.Unknown, "\n") + "\n"
	return dir, os.WriteFile(filepath.Join(dir, "unknown.txt"), []byte(list), 0o600)
}

// maxSaveUpload bounds an uploaded save.dat, base64 in JSON. The game reads
// its save into a 1 MiB buffer; real saves are about 80 KB.
const maxSaveUpload = 2 << 20

// importSplatoon3Save replaces the caller's Splatoon 3 cloud save record with one
// decoded from an uploaded save.dat (internal/splat3save).
//
// It replaces the record whole rather than merging: a console takes the server's
// record over its own save on every boot, so the uploaded save only reaches the game
// through the record, and a merge would keep fields from the save being replaced. The
// old record is archived first and a failed archive refuses the import, as for the wipe.
//
// The console must not be running Splatoon 3 meanwhile: its next write would carry the
// old save's version token and its own idea of the fields. The bot says so before it
// calls this; nothing here can tell.
func (b *botAPI) importSplatoon3Save(w http.ResponseWriter, r *http.Request, discordID string) {
	if b.saveRecordPrefix == "" || b.saveBackupDir == "" || b.saveTable == nil {
		writeBotError(w, http.StatusServiceUnavailable, "save_import_disabled", "cloud save import is not configured")
		return
	}
	var req struct {
		Username string `json:"username"`
		Save     []byte `json:"save"` // save.dat, base64
	}
	if err := decodeJSONBodyLimit(r, &req, maxSaveUpload); err != nil {
		writeBotError(w, http.StatusBadRequest, "bad_request", "unreadable body")
		return
	}
	user, ok := b.ownAccount(w, r, discordID, req.Username)
	if !ok {
		return
	}

	rec, info, err := splat3save.Decode(req.Save, b.saveTable)
	if err != nil {
		code := "bad_save"
		if errors.Is(err, splat3save.ErrCheck) {
			code = "not_a_save"
		}
		b.s.log.Info("botapi: Splatoon 3 save import refused", "user", user.ID, "bytes", len(req.Save), "error", err)
		writeBotError(w, http.StatusUnprocessableEntity, code, err.Error())
		return
	}
	// Fields with unknown names are left out of the record; the save itself
	// is kept so they can be added later, and nothing a player brought is
	// lost.
	review := ""
	if len(info.Unknown) > 0 {
		if review, err = b.keepForReview(user.ID, req.Save, info); err != nil {
			b.internal(w, "keep save for review", err)
			return
		}
		b.s.log.Warn("botapi: Splatoon 3 save has fields with unknown names; imported without them",
			"user", user.ID, "unknown", len(info.Unknown), "review", review)
	}
	fields, err := proto.Marshal(rec)
	if err != nil {
		b.internal(w, "encode save record", err)
		return
	}

	name := b.saveRecordPrefix + user.ID
	now := time.Now().UTC().Truncate(time.Microsecond)
	created, updated := now, now
	backup := ""
	old, err := b.s.store.GetUGCDocument(r.Context(), name)
	switch {
	case errors.Is(err, db.ErrNotFound):
	case err != nil:
		b.internal(w, "load save record", err)
		return
	default:
		if backup, err = b.archiveSave(user.ID, "import", old.Fields); err != nil {
			b.internal(w, "archive save record", err)
			return
		}
		created = old.CreateTime
		// The record's version must move forward, as toyohr's
		// nextSaveVersion keeps it, or a console could treat the imported
		// record as one it already has.
		if !updated.After(old.UpdateTime) {
			updated = old.UpdateTime.Add(time.Microsecond)
		}
	}
	if err := b.s.store.PutUGCDocument(r.Context(), db.UGCDocument{
		Name: name, Owner: user.ID, Fields: fields, CreateTime: created, UpdateTime: updated,
	}); err != nil {
		b.internal(w, "store save record", err)
		return
	}
	b.s.log.Info("botapi: Splatoon 3 save imported", "user", user.ID, "game_version", info.GameVersion,
		"fields", info.Fields, "bytes", len(fields), "backup", backup)
	resp := map[string]any{"login": user.LoginUsername, "gameVersion": info.GameVersion, "bytes": len(fields)}
	if backup != "" {
		resp["backup"] = filepath.Base(backup)
	}
	if review != "" {
		resp["setAside"] = len(info.Unknown)
	}
	writeJSON(w, http.StatusOK, resp)
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
