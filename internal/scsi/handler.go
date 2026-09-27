// Package scsi reimplements the two Nintendo servers behind OLSC (Online
// Save Cloud), the OS-level per-game save-data backup tied to a Nintendo
// Switch Online subscription. Phase 4 of GRID0+ — see docs/ROADMAP.md.
//
// Endpoints:
//
//	https://storage.hac.lp1.scsi.srv.nintendo.net       (save data archives, v2)
//	https://permission.lp1.sata.srv.nintendo.net    (save data sets / key package)
//
// Reference: https://github.com/kinnay/NintendoClients/wiki/OLSC-Servers
//
// This is NOT the same thing as any one game's own cloud-save API —
// Splatoon 3's CloudSave.GetSaveRecord (see ROADMAP.md phase 2.4) is a
// separate, game-specific RPC on a separate protocol. OLSC is system-wide,
// used by any title with save data, independent of NPLN or any other game
// protocol.
//
// See internal/scsi/README.md for exactly which endpoints and fields below
// are confirmed from the wiki page above versus this project's own
// defensible reading of an undocumented one — most are marked inline with
// TODO(capture-needed) where they are the latter.
package scsi

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"github.com/n-popescu/grid0plus/internal/db"
	sjwt "github.com/n-popescu/grid0plus/internal/jwt"
)

// Store is the subset of db.Store this server needs.
type Store interface {
	GetUser(ctx context.Context, id string) (db.User, error)
	GetDeviceAccount(ctx context.Context, id string) (db.DeviceAccount, error)

	CreateSaveDataArchive(ctx context.Context, a db.SaveDataArchive) (db.SaveDataArchive, error)
	GetSaveDataArchive(ctx context.Context, id string) (db.SaveDataArchive, error)
	ListSaveDataArchives(ctx context.Context, q db.SaveDataArchiveQuery) ([]db.SaveDataArchive, error)
	UpdateSaveDataArchive(ctx context.Context, id string, p db.SaveDataArchivePatch) (db.SaveDataArchive, error)
	DeleteSaveDataArchive(ctx context.Context, id string) error

	CreateComponentFile(ctx context.Context, f db.ComponentFile) (db.ComponentFile, error)
	GetComponentFile(ctx context.Context, id string) (db.ComponentFile, error)
	ListComponentFiles(ctx context.Context, archiveID string) ([]db.ComponentFile, error)
	UpdateComponentFile(ctx context.Context, id string, p db.ComponentFilePatch) (db.ComponentFile, error)
}

// Config is what the server needs to run.
type Config struct {
	// PublicURL is this service's own externally-reachable base URL (e.g.
	// "https://storage.hac.lp1.scsi.srv.nintendo.net"), used to build the
	// signed_uri this project's own upload/download endpoint is reached
	// through. See signedURI's TODO(capture-needed).
	PublicURL string
}

// Server serves the SCSI/SATA OLSC API.
type Server struct {
	cfg        Config
	store      Store
	blobs      BlobStore
	idVerifier *sjwt.Verifier
	log        *slog.Logger
}

// New returns an OLSC server. idVerifier must verify tokens signed with the
// same key as internal/baas's own baas_id issuer -- see auth.go.
func New(cfg Config, store Store, blobs BlobStore, idVerifier *sjwt.Verifier, log *slog.Logger) *Server {
	return &Server{cfg: cfg, store: store, blobs: blobs, idVerifier: idVerifier, log: log}
}

// Routes registers every route on mux.
func (s *Server) Routes(mux *http.ServeMux) {
	// ---- storage.hac.lp1.scsi.srv.nintendo.net -- save data archives, v2 ------
	mux.HandleFunc("GET /api/nx/v2/save_data_archives", s.authenticated(s.listArchives))
	mux.HandleFunc("GET /api/nx/v2/save_data_archives/{id}", s.authenticated(s.getArchive))
	// Both forms from the wiki's own URL table route to the same handler:
	// with no {id} segment it creates a new archive; with one, it (re)starts
	// an upload against that already-known id. See startUpload's own comment.
	mux.HandleFunc("POST /api/nx/v2/save_data_archives/start_upload", s.authenticated(s.startUpload))
	mux.HandleFunc("POST /api/nx/v2/save_data_archives/{id}/start_upload", s.authenticated(s.startUpload))
	mux.HandleFunc("POST /api/nx/v2/save_data_archives/{id}/finish_upload", s.authenticated(s.finishUpload))
	mux.HandleFunc("POST /api/nx/v2/save_data_archives/{id}/extend_upload_timeout", s.authenticated(s.extendUploadTimeout))
	mux.HandleFunc("POST /api/nx/v2/save_data_archives/{id}/generate_key_seed_package", s.authenticated(s.generateKeySeedPackage))
	mux.HandleFunc("POST /api/nx/v2/save_data_archives/{id}/delete", s.authenticated(s.deleteArchive))
	mux.HandleFunc("GET /api/nx/v2/save_data_archives/{id}/start_download", s.authenticated(s.startDownload))
	mux.HandleFunc("POST /api/nx/v2/save_data_archives/{id}/finish_download", s.authenticated(s.finishDownload))
	mux.HandleFunc("POST /api/nx/v2/save_data_archives/{id}/component_files/create", s.authenticated(s.createComponentFile))

	mux.HandleFunc("GET /api/nx/v2/component_files/{id}/signed_uri", s.authenticated(s.signedURI))
	mux.HandleFunc("POST /api/nx/v2/component_files/{id}/finish_upload", s.authenticated(s.finishComponentUpload))
	mux.HandleFunc("POST /api/nx/v2/component_files/{id}/update", s.authenticated(s.unimplementedComponentUpdate))
	// Not on the wiki at all — this project's own slot for the raw bytes a
	// signed_uri points at. See signedURI's TODO(capture-needed).
	mux.HandleFunc("PUT /api/nx/v2/component_files/{id}/data", s.authenticated(s.uploadComponentData))
	mux.HandleFunc("GET /api/nx/v2/component_files/{id}/data", s.authenticated(s.downloadComponentData))

	mux.HandleFunc("POST /api/nx/v2/network_service_accounts/{id}/notification_tokens", s.authenticated(s.registerNotificationToken))
	mux.HandleFunc("GET /api/nx/v2/permissions", s.authenticated(s.permissions))

	// A 2026-09-23 hardware capture shows a real Splatoon 3 console calling
	// these same two resources under /api/console/v3/, not /api/nx/v2/ --
	// this project's whole OLSC surface was built against the wiki's v2
	// paths and had no route for v3 at all, so every one of these requests
	// 404ed. The console did not treat that 404 as harmless: it retried
	// roughly every 700ms, continuously, for the whole time the hall was
	// open -- logUnimplemented's own doc comment assumed a clean 404
	// wouldn't cause a retry loop, and empirically, here, it did.
	//
	// Routed to the same handlers as their v2 counterparts, not a new
	// implementation: both handlers already ignore anything path-shaped that
	// would differ between the two prefixes (registerNotificationToken never
	// reads the path's {id} at all; listArchives filters only on query
	// parameters). Reusing them is applying already-confirmed-defensible
	// logic to a newly-confirmed real path, not inventing a new one.
	//
	// A 2026-09-24 hardware capture (both preserved working Nextendo hall
	// captures agree) confirms a third /api/console/v3/ path: start_upload,
	// same two forms as the v2 route above. Two consecutive GRID0+
	// hardware attempts on 2026-09-24 both reached this exact request and
	// got logUnimplemented's 404 -- the same retry-loop-causing shape this
	// comment already found once for the two routes above, on the RPC
	// immediately after the one this project's own hall investigation had
	// never seen a console reach before today.
	//
	// TODO(capture-needed): whether /api/console/v3/ is Splatoon 3's whole
	// OLSC surface (in which case every remaining /api/nx/v2/ route above
	// may need a v3 twin too) or just these three, and what "datatype" in
	// ?datatype=0 on save_data_archives selects -- listArchives does not
	// look at it today. Add a v3 route here, to the same handler, the
	// moment another one is confirmed.
	mux.HandleFunc("POST /api/console/v3/network_service_accounts/{id}/notification_tokens", s.authenticated(s.registerNotificationToken))
	mux.HandleFunc("GET /api/console/v3/save_data_archives", s.authenticated(s.listArchives))
	mux.HandleFunc("POST /api/console/v3/save_data_archives/start_upload", s.authenticated(s.startUpload))
	mux.HandleFunc("POST /api/console/v3/save_data_archives/{id}/start_upload", s.authenticated(s.startUpload))

	// ---- permission.lp1.sata.srv.nintendo.net ------------------------------
	mux.HandleFunc("GET /api/nx/v1/save_data_sets", s.authenticated(s.listSaveDataSets))
	mux.HandleFunc("POST /api/nx/v1/save_data_sets/generate_key_package", s.authenticated(s.generateKeyPackage))
	mux.HandleFunc("POST /api/nx/v1/customer_operations", s.authenticated(s.unimplemented))
	mux.HandleFunc("POST /api/nx/v1/customer_operations/update_action_completed", s.authenticated(s.unimplemented))

	mux.HandleFunc("/", s.notFound)
}

func (s *Server) notFound(w http.ResponseWriter, r *http.Request) {
	s.logUnimplemented(r)
	writeError(w, http.StatusNotFound, "resource_is_not_found", "Specified resource is not found")
}

func (s *Server) unimplemented(w http.ResponseWriter, r *http.Request) {
	s.logUnimplemented(r)
	w.WriteHeader(http.StatusNotImplemented)
}

func (s *Server) unimplementedComponentUpdate(w http.ResponseWriter, r *http.Request) {
	// No basis to guess this one's purpose at all -- unlike every other
	// endpoint in this file, nothing about the name or its neighbours
	// suggests what it changes. Logged loudly rather than given a plausible
	// fake shape, per CLAUDE.md's ground rule against inventing undocumented
	// Nintendo internals.
	s.logUnimplemented(r)
	w.WriteHeader(http.StatusNotImplemented)
}

// ---- save data archives ----------------------------------------------------

func (s *Server) listArchives(w http.ResponseWriter, r *http.Request) {
	c := callerFrom(r.Context())
	q := db.SaveDataArchiveQuery{
		NSAID:         c.NSAID,
		ApplicationID: r.URL.Query().Get("application_id"),
		DeviceID:      resolveMe(r.URL.Query().Get("device_id"), c.DeviceAccountID),
	}
	archives, err := s.store.ListSaveDataArchives(r.Context(), q)
	if err != nil {
		s.writeInternalError(w, "list archives", err)
		return
	}
	views := make([]archiveView, len(archives))
	for i, a := range archives {
		views[i] = toArchiveView(a, nil)
	}
	writeJSON(w, http.StatusOK, map[string]any{"save_data_archives": views})
}

// resolveMe turns the documented "me" device_id filter value into the
// caller's own device account id.
func resolveMe(deviceID, callerDeviceID string) string {
	if deviceID == "me" {
		return callerDeviceID
	}
	return deviceID
}

func (s *Server) getArchive(w http.ResponseWriter, r *http.Request) {
	c := callerFrom(r.Context())
	a, err := s.loadOwnedArchive(w, r, c)
	if err != nil {
		return
	}
	components, err := s.store.ListComponentFiles(r.Context(), a.ID)
	if err != nil {
		s.writeInternalError(w, "list component files", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"save_data_archive": toArchiveView(a, components)})
}

// loadOwnedArchive loads the {id} path archive and refuses it (writing the
// response itself) unless it belongs to the caller. Every handler below that
// takes an existing archive id uses this rather than a bare GetSaveDataArchive,
// for the same fail-closed reason internal/baas/router.go's requireSelf
// exists: an archive id is not a secret, so ownership must be checked on
// every access, not assumed from having been handed one.
func (s *Server) loadOwnedArchive(w http.ResponseWriter, r *http.Request, c Caller) (db.SaveDataArchive, error) {
	a, err := s.store.GetSaveDataArchive(r.Context(), r.PathValue("id"))
	switch {
	case errors.Is(err, db.ErrNotFound):
		writeError(w, http.StatusNotFound, "resource_is_not_found", "Specified resource is not found")
		return db.SaveDataArchive{}, err
	case err != nil:
		s.writeInternalError(w, "load archive", err)
		return db.SaveDataArchive{}, err
	}
	if a.NSAID != c.NSAID {
		writeError(w, http.StatusForbidden, "forbidden", "Forbidden")
		return db.SaveDataArchive{}, errors.New("not owner")
	}
	return a, nil
}

// startUploadRequest is the wiki's documented body of
// POST /api/nx/v2/save_data_archives/<id>/start_upload, plus ApplicationID:
// both preserved working Nextendo hall captures' real start_upload request
// bodies (decoded 2026-09-24, facts only, not Nextendo's code) carry
// "application_id" as a plain JSON field, settling what this project used
// to read from an invented X-SwitchNet-Application-Id header instead
// (removed the same day this was found).
type startUploadRequest struct {
	ApplicationID         string `json:"application_id"`
	Datatype              int    `json:"datatype"`
	DataSize              int64  `json:"data_size"`
	SavedAtUnixtime       int64  `json:"saved_at_as_unixtime"`
	LaunchRequiredVersion int    `json:"launch_required_version"`
	// json.Number, not int64: a second real value observed on real
	// hardware 2026-09-24 (12237957226414712540) exceeds int64's own
	// maximum -- see db.SaveDataArchive.SeriesID's own comment.
	SeriesID      json.Number `json:"series_id"`
	EncodedDigest string      `json:"encoded_digest"`
	HasThumbnail  bool        `json:"has_thumbnail"`
	Desc          string      `json:"desc"`
	AutoBackup    bool        `json:"auto_backup"`
}

func (s *Server) startUpload(w http.ResponseWriter, r *http.Request) {
	c := callerFrom(r.Context())
	var req startUploadRequest
	body, err := io.ReadAll(io.LimitReader(r.Body, 8192))
	r.Body.Close()
	if err != nil {
		s.log.Error("scsi: start_upload: read request body", "error", err)
		writeError(w, http.StatusBadRequest, "invalid_params", "Invalid Params")
		return
	}
	// Logged raw on any decode failure -- the exact shape a real console
	// sends here has already surprised this project once (application_id,
	// splat3-start-upload-console-v3-2026-09-24.md), so a second mismatch
	// is worth seeing verbatim rather than guessing at from a bare error.
	if err := json.Unmarshal(body, &req); err != nil {
		s.log.Error("scsi: start_upload: decode request body", "error", err, "body", string(body))
		writeError(w, http.StatusBadRequest, "invalid_params", "Invalid Params")
		return
	}

	id := r.PathValue("id")
	if id == "" {
		var err error
		id, err = newHexID(8)
		if err != nil {
			s.writeInternalError(w, "generate archive id", err)
			return
		}
	}

	savedAt := time.Unix(req.SavedAtUnixtime, 0).UTC()
	if req.SavedAtUnixtime == 0 {
		savedAt = time.Now().UTC()
	}
	// "0", not "" -- an empty string is not a valid JSON number literal, and
	// archiveView.SeriesID (json.Number) would marshal it as bare, invalid
	// JSON the next time this archive is read back.
	seriesID := req.SeriesID.String()
	if seriesID == "" {
		seriesID = "0"
	}
	a, err := s.store.CreateSaveDataArchive(r.Context(), db.SaveDataArchive{
		ID: id, NSAID: c.NSAID, ApplicationID: req.ApplicationID, DeviceID: c.DeviceAccountID,
		DataSize: req.DataSize, Description: req.Desc, SeriesID: seriesID, Datatype: req.Datatype,
		Status: "uploading", AutoBackup: req.AutoBackup, LaunchRequiredVersion: req.LaunchRequiredVersion,
		EncodedDigest: req.EncodedDigest, SavedAt: savedAt,
	})
	if err != nil {
		if errors.Is(err, db.ErrConflict) {
			writeError(w, http.StatusConflict, "resource_already_exists", "Resource already exists")
			return
		}
		s.writeInternalError(w, "create archive", err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"save_data_archive": toArchiveView(a, nil)})
}

func (s *Server) finishUpload(w http.ResponseWriter, r *http.Request) {
	c := callerFrom(r.Context())
	a, err := s.loadOwnedArchive(w, r, c)
	if err != nil {
		return
	}
	var req struct {
		EncodedMAC string `json:"encoded_mac"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_params", "Invalid Params")
		return
	}
	finishedAt := time.Now().UTC()
	status := "fixed"
	updated, err := s.store.UpdateSaveDataArchive(r.Context(), a.ID, db.SaveDataArchivePatch{
		Status: &status, FinishedAt: &finishedAt,
	})
	if err != nil {
		s.writeInternalError(w, "finish upload", err)
		return
	}
	components, err := s.store.ListComponentFiles(r.Context(), a.ID)
	if err != nil {
		s.writeInternalError(w, "list component files", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"save_data_archive": toArchiveView(updated, components)})
}

func (s *Server) extendUploadTimeout(w http.ResponseWriter, r *http.Request) {
	c := callerFrom(r.Context())
	if _, err := s.loadOwnedArchive(w, r, c); err != nil {
		return
	}
	// GRID0+ enforces no upload timeout to extend -- this only needs to
	// answer the shape the wiki confirms.
	writeJSON(w, http.StatusOK, map[string]any{"code": 2000})
}

func (s *Server) generateKeySeedPackage(w http.ResponseWriter, r *http.Request) {
	c := callerFrom(r.Context())
	if _, err := s.loadOwnedArchive(w, r, c); err != nil {
		return
	}
	var req struct {
		EncodedChallenge string `json:"encoded_challenge"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_params", "Invalid Params")
		return
	}
	pkg, err := opaqueKeyMaterial(384) // 384 raw bytes -> 512 base64url characters, the confirmed size
	if err != nil {
		s.writeInternalError(w, "generate key seed package", err)
		return
	}
	// TODO(capture-needed): the wiki confirms the response's field name and
	// exact encoded length (512 base64url characters) but not the byte
	// content's meaning. Real Nintendo hardware likely wraps this around the
	// console's own SSL device key via an HSM GRID0+ has no equivalent
	// of. This is this project's most defensible placeholder -- opaque,
	// correctly sized, freshly random per request -- not a guess at the
	// real wrapping format. If a console cryptographically validates this
	// value before trusting a restored save, no shape guessed here will
	// pass that check; see internal/scsi/README.md and
	// docs/CAPTURES/README.md for why this is flagged as the single most
	// likely hard blocker in this whole feature, the same way nxAccountBlob's
	// header prefix was for phase 1's account linking.
	writeJSON(w, http.StatusOK, map[string]any{"encoded_key_seed_package": pkg})
}

func (s *Server) deleteArchive(w http.ResponseWriter, r *http.Request) {
	c := callerFrom(r.Context())
	a, err := s.loadOwnedArchive(w, r, c)
	if err != nil {
		return
	}
	components, err := s.store.ListComponentFiles(r.Context(), a.ID)
	if err != nil {
		s.writeInternalError(w, "list component files", err)
		return
	}
	for _, comp := range components {
		if err := s.blobs.Delete(comp.BlobKey); err != nil {
			s.log.Warn("scsi: delete blob", "component_file", comp.ID, "error", err)
		}
	}
	if err := s.store.DeleteSaveDataArchive(r.Context(), a.ID); err != nil {
		s.writeInternalError(w, "delete archive", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{})
}

// startDownload and finishDownload back a restore.
//
// TODO(capture-needed): neither is documented beyond appearing, method
// unconfirmed ("?"), in the wiki's own URL table. The most defensible
// reading: start_download hands back exactly what GET .../{id} already
// does -- the archive plus its component_files, each with an id the console
// then resolves via GET .../component_files/{id}/signed_uri the same way an
// upload does -- since inventing a second, different way to name the same
// component files would only create a second shape to keep in sync with
// the first. finish_download is treated as a pure acknowledgement, the same
// posture as extend_upload_timeout's minimal {"code": ...} response.
func (s *Server) startDownload(w http.ResponseWriter, r *http.Request) {
	s.getArchive(w, r)
}

func (s *Server) finishDownload(w http.ResponseWriter, r *http.Request) {
	c := callerFrom(r.Context())
	if _, err := s.loadOwnedArchive(w, r, c); err != nil {
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{})
}

// ---- component files --------------------------------------------------------

// createComponentFileRequest is this project's own defensible reading of
// POST /api/nx/v2/save_data_archives/<id>/component_files/create's body --
// the wiki lists the URL with method "?" and no field shape at all. The two
// fields here are the minimum the documented component-file object needs
// that the server cannot decide on its own (datatype, and index for
// resumable multi-part uploads).
//
// TODO(capture-needed): confirm this shape, or replace it, against a real
// capture.
type createComponentFileRequest struct {
	Datatype string `json:"datatype"`
	Index    int    `json:"index"`
}

func (s *Server) createComponentFile(w http.ResponseWriter, r *http.Request) {
	c := callerFrom(r.Context())
	a, err := s.loadOwnedArchive(w, r, c)
	if err != nil {
		return
	}
	if a.Status != "uploading" {
		writeError(w, http.StatusConflict, "resource_already_exists", "Resource already exists")
		return
	}
	var req createComponentFileRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_params", "Invalid Params")
		return
	}
	id, err := newHexID(8)
	if err != nil {
		s.writeInternalError(w, "generate component file id", err)
		return
	}
	f, err := s.store.CreateComponentFile(r.Context(), db.ComponentFile{
		ID: id, ArchiveID: a.ID, Index: req.Index, Datatype: req.Datatype,
		Status: "uploading", BlobKey: id,
	})
	if err != nil {
		s.writeInternalError(w, "create component file", err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"component_file": toComponentView(f)})
}

// signedURI answers GET /api/nx/v2/component_files/<id>/signed_uri.
//
// TODO(capture-needed): the wiki lists this URL's method as "?" with no
// response shape. Real Nintendo infrastructure almost certainly hands back a
// time-limited, cryptographically-signed URL to a separate storage backend
// (S3-style — see the CloudFront/X-Amz-Cf-* headers in BAAS's own example
// HTTP responses on the wiki, evidence the same underlying infrastructure is
// in play here). GRID0+ controls both ends of this exchange, so it
// answers with a same-server URL under its own /data path instead of
// implementing real request signing — there is nothing to sign against, and
// no third party the URL needs to be safe to hand to. If a capture shows the
// console rejects a URL that is not on the same host as the archive
// endpoints, or expects a query parameter this does not have, update this
// function and PublicURL's config docs together.
func (s *Server) signedURI(w http.ResponseWriter, r *http.Request) {
	c := callerFrom(r.Context())
	f, err := s.loadOwnedComponentFile(w, r, c)
	if err != nil {
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"url": s.cfg.PublicURL + "/api/nx/v2/component_files/" + f.ID + "/data",
	})
}

func (s *Server) loadOwnedComponentFile(w http.ResponseWriter, r *http.Request, c Caller) (db.ComponentFile, error) {
	f, err := s.store.GetComponentFile(r.Context(), r.PathValue("id"))
	switch {
	case errors.Is(err, db.ErrNotFound):
		writeError(w, http.StatusNotFound, "resource_is_not_found", "Specified resource is not found")
		return db.ComponentFile{}, err
	case err != nil:
		s.writeInternalError(w, "load component file", err)
		return db.ComponentFile{}, err
	}
	a, err := s.store.GetSaveDataArchive(r.Context(), f.ArchiveID)
	if err != nil {
		s.writeInternalError(w, "load parent archive", err)
		return db.ComponentFile{}, err
	}
	if a.NSAID != c.NSAID {
		writeError(w, http.StatusForbidden, "forbidden", "Forbidden")
		return db.ComponentFile{}, errors.New("not owner")
	}
	return f, nil
}

// uploadComponentData and downloadComponentData are this project's own
// endpoint, not a Nintendo one -- see signedURI's TODO(capture-needed) for
// why. They exist because something has to be at the URL signedURI hands
// back.
func (s *Server) uploadComponentData(w http.ResponseWriter, r *http.Request) {
	c := callerFrom(r.Context())
	f, err := s.loadOwnedComponentFile(w, r, c)
	if err != nil {
		return
	}

	used, err := s.usedBytes(r.Context(), c.NSAID)
	if err != nil {
		s.writeInternalError(w, "compute usage", err)
		return
	}
	// This upload replaces f's own existing bytes rather than adding to
	// them (Put overwrites the blob at f.BlobKey), so its current recorded
	// size must not count against the quota this write is checked against --
	// otherwise re-uploading the same component (a retry, or a save that grew
	// slightly) could be wrongly refused even though the account's real usage
	// after the write would still fit.
	used -= f.ArchiveSize
	if used < 0 {
		used = 0
	}
	n, err := s.blobs.Put(c.NSAID, f.BlobKey, r.Body, used)
	if err != nil {
		if errors.Is(err, ErrQuotaExceeded) {
			writeError(w, http.StatusInsufficientStorage, "quota_exceeded", "Storage quota exceeded")
			return
		}
		s.writeInternalError(w, "store component data", err)
		return
	}
	if _, err := s.store.UpdateComponentFile(r.Context(), f.ID, db.ComponentFilePatch{ArchiveSize: &n}); err != nil {
		s.writeInternalError(w, "record component size", err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) downloadComponentData(w http.ResponseWriter, r *http.Request) {
	c := callerFrom(r.Context())
	f, err := s.loadOwnedComponentFile(w, r, c)
	if err != nil {
		return
	}
	body, size, err := s.blobs.Get(f.BlobKey)
	if err != nil {
		s.writeInternalError(w, "open component data", err)
		return
	}
	defer body.Close()
	w.Header().Set("Content-Length", strconv.FormatInt(size, 10))
	w.Header().Set("Content-Type", "application/octet-stream")
	if _, err := io.Copy(w, body); err != nil {
		s.log.Warn("scsi: stream component data", "component_file", f.ID, "error", err)
	}
}

func (s *Server) finishComponentUpload(w http.ResponseWriter, r *http.Request) {
	c := callerFrom(r.Context())
	f, err := s.loadOwnedComponentFile(w, r, c)
	if err != nil {
		return
	}
	var req struct {
		EncodedDigest string `json:"encoded_digest"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_params", "Invalid Params")
		return
	}
	status := "fixed"
	updated, err := s.store.UpdateComponentFile(r.Context(), f.ID, db.ComponentFilePatch{
		Status: &status, EncodedArchiveDigest: &req.EncodedDigest,
	})
	if err != nil {
		s.writeInternalError(w, "finish component upload", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"component_file": toComponentView(updated)})
}

// usedBytes sums an owner's stored bytes across every archive, for quota
// enforcement. O(archives) per upload -- fine at the scale a private
// deployment operates at; see internal/scsi/README.md if that ever changes.
func (s *Server) usedBytes(ctx context.Context, nsaID string) (int64, error) {
	archives, err := s.store.ListSaveDataArchives(ctx, db.SaveDataArchiveQuery{NSAID: nsaID})
	if err != nil {
		return 0, err
	}
	var total int64
	for _, a := range archives {
		components, err := s.store.ListComponentFiles(ctx, a.ID)
		if err != nil {
			return 0, err
		}
		for _, comp := range components {
			total += comp.ArchiveSize
		}
	}
	return total, nil
}

// ---- misc documented endpoints ----------------------------------------------

func (s *Server) registerNotificationToken(w http.ResponseWriter, r *http.Request) {
	var req struct {
		NotificationToken string `json:"notification_token"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_params", "Invalid Params")
		return
	}
	// Real delivery ("your backup finished") needs npns:s, which GRID0+
	// does not implement (see docs/ROADMAP.md and switchnet-re's own
	// FIRMWARE_IMPACT_AND_NPNS findings) -- acknowledged and otherwise
	// dropped rather than stored, so this never silently promises a push
	// notification that will never arrive.
	writeJSON(w, http.StatusOK, map[string]any{"notification_token": req.NotificationToken})
}

// permissions answers GET /api/nx/v2/permissions.
//
// TODO(capture-needed): wiki lists method "?" with no shape. Answered
// permissively (an empty object, no denial fields set) rather than refused,
// matching this project's existing convention for a policy check with no
// confirmed shape -- see internal/stubs's op2 membership stub, which
// likewise always answers "allowed" for the same reason: a private
// deployment has no legitimate case where its own operator's own consoles
// should be denied their own save backups.
func (s *Server) permissions(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{})
}

// ---- permission.lp1.sata.srv.nintendo.net ----------------------------------

// listSaveDataSets answers GET /api/nx/v1/save_data_sets.
//
// TODO(capture-needed): wiki lists this with no method or shape at all.
// Answered as an empty list -- the same "nothing to report" default
// GetSaveRecord uses for a fresh account (see ROADMAP.md phase 2.4's
// CloudSave table) -- rather than guessing at a nonempty shape.
func (s *Server) listSaveDataSets(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"save_data_sets": []any{}})
}

// generateKeyPackage answers POST /api/nx/v1/save_data_sets/generate_key_package.
//
// TODO(capture-needed): confirmed to exist (both the wiki's URL table and a
// literal "%s/api/nx/v1/save_data_sets/generate_key_package" string found
// directly in a decompile of title 010000000000003E) but with zero
// documented request/response fields -- not even a field name, unlike
// generate_key_seed_package's confirmed 512-character response. This is
// flagged as being possibly on equal footing with that one as a hard
// blocker: if a console cryptographically validates whatever this returns
// before it will even attempt an upload, no answer given here without a
// capture will pass. See internal/scsi/README.md.
func (s *Server) generateKeyPackage(w http.ResponseWriter, r *http.Request) {
	pkg, err := opaqueKeyMaterial(384)
	if err != nil {
		s.writeInternalError(w, "generate key package", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"encoded_key_package": pkg})
}

// ---- helpers ----------------------------------------------------------------

func decodeJSON(r *http.Request, v any) error {
	defer r.Body.Close()
	dec := json.NewDecoder(r.Body)
	if err := dec.Decode(v); err != nil && err != io.EOF {
		return fmt.Errorf("decode request body: %w", err)
	}
	return nil
}

// opaqueKeyMaterial returns n cryptographically random bytes, base64url
// encoded without padding -- the encoding both generate_key_seed_package and
// generate_key_package's confirmed/plausible field descriptions specify.
func opaqueKeyMaterial(n int) (string, error) {
	buf := make([]byte, n)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("generate key material: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(buf), nil
}
