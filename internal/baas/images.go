package baas

import (
	"bytes"
	"encoding/base64"
	"errors"
	"fmt"
	"image"
	_ "image/jpeg" // registers the JPEG decoder used to validate uploads
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/n-popescu/switchnet/internal/db"
)

// ---- POST /1.0.0/image_upload -------------------------------------------

// imageUploadRequest is the JSON body.
//
// Reference: https://github.com/kinnay/NintendoClients/wiki/BAAS-Server#post-100image_upload
type imageUploadRequest struct {
	// RawContent is a base64-encoded JPEG.
	RawContent string `json:"rawContent"`
	OwnerID    string `json:"ownerId"`
	// AllowTransform lets the server resize the image. SwitchNet stores what it
	// is given, so this is accepted and not acted on.
	AllowTransform bool `json:"allowTransform"`
}

// imageUploadResponse is the reply.
type imageUploadResponse struct {
	ID        string       `json:"id"`
	OwnerID   string       `json:"ownerId"`
	Owner     imageOwner   `json:"owner"`
	State     string       `json:"state"`
	Content   imageContent `json:"content"`
	CreatedAt int64        `json:"createdAt"`
	UpdatedAt int64        `json:"updatedAt"`
}

type imageOwner struct {
	ID string `json:"id"`
}

type imageContent struct {
	ID     string `json:"id"`
	Width  int    `json:"width"`
	Height int    `json:"height"`
	Format string `json:"format"`
	URL    string `json:"url"`
	// URLExpiresAt is 2147483647 on Nintendo's server: the maximum signed
	// 32-bit value, meaning "never".
	URLExpiresAt int64 `json:"urlExpiresAt"`
}

// uploadImage stores a profile picture.
func (s *Server) uploadImage(w http.ResponseWriter, r *http.Request, c *caller) {
	var req imageUploadRequest
	if err := decodeJSONBodyLimit(r, &req, s.cfg.MaxImageBytes+imageEnvelopeSlack); err != nil {
		writeError(w, r, errInvalidParams)
		return
	}

	// The owner comes from the token, not the body. Honouring ownerId as sent
	// would let any console overwrite anybody's profile picture.
	if req.OwnerID != "" && !strings.EqualFold(req.OwnerID, c.UserID) {
		s.log.Warn("baas: refused image upload for another user",
			"caller", c.UserID, "claimed_owner", req.OwnerID)
		writeError(w, r, errInsufficientScope)
		return
	}

	raw, err := decodeImageContent(req.RawContent)
	if err != nil {
		s.log.Warn("baas: rejected image upload", "user", c.UserID, "error", err)
		writeError(w, r, errInvalidRawContent)
		return
	}
	if int64(len(raw)) > s.cfg.MaxImageBytes {
		writeError(w, r, errInvalidRawContent)
		return
	}

	// Decode the image before writing anything. This is what makes the endpoint
	// safe: a file that does not decode as a JPEG never reaches the disk, so the
	// image directory cannot be used to host arbitrary content served from a
	// hostname the consoles trust.
	cfg, format, err := image.DecodeConfig(bytes.NewReader(raw))
	if err != nil || format != "jpeg" {
		s.log.Warn("baas: image upload is not a JPEG",
			"user", c.UserID, "format", format, "error", err)
		writeError(w, r, errInvalidRawContent)
		return
	}

	imageID, err := newHexID(16)
	if err != nil {
		writeInternalError(s.log, w, r, "generate image id", err)
		return
	}
	// The filename is built from the generated id, never from the request, so
	// there is no path to traverse.
	relPath := imageID + ".jpg"

	if err := os.MkdirAll(s.cfg.ImageDir, 0o750); err != nil {
		writeInternalError(s.log, w, r, "create image directory", err)
		return
	}
	if err := os.WriteFile(filepath.Join(s.cfg.ImageDir, relPath), raw, 0o640); err != nil {
		writeInternalError(s.log, w, r, "write image", err)
		return
	}

	now := s.now()
	img := db.Image{
		ID: imageID, OwnerID: c.UserID, Format: "jpg",
		Width: cfg.Width, Height: cfg.Height, SizeBytes: len(raw),
		RelPath: relPath, CreatedAt: now, UpdatedAt: now,
	}
	if err := s.store.CreateImage(r.Context(), img); err != nil {
		writeInternalError(s.log, w, r, "record image", err)
		return
	}

	url := strings.TrimSuffix(s.cfg.ImageBaseURL, "/") + "/" + relPath

	// Point the user's thumbnail at the new image. Without this the upload
	// succeeds and the profile picture never changes, which looks like the
	// upload failing.
	if _, err := s.store.UpdateUser(r.Context(), c.UserID, db.UserPatch{
		ThumbnailURL:        &url,
		ThumbnailUploadedAt: &now,
	}); err != nil {
		writeInternalError(s.log, w, r, "update thumbnail", err)
		return
	}

	s.log.Info("baas: image uploaded",
		"user", c.UserID, "image", imageID, "bytes", len(raw),
		"width", cfg.Width, "height", cfg.Height)

	writeJSON(w, http.StatusOK, imageUploadResponse{
		ID:      imageID,
		OwnerID: c.UserID,
		Owner:   imageOwner{ID: c.UserID},
		State:   "STORED",
		Content: imageContent{
			ID:           imageID,
			Width:        cfg.Width,
			Height:       cfg.Height,
			Format:       "jpg",
			URL:          url,
			URLExpiresAt: 2147483647,
		},
		CreatedAt: now.Unix(),
		UpdatedAt: now.Unix(),
	})
}

// imageEnvelopeSlack is how much bigger the JSON body may be than the image
// itself: base64 adds a third, plus the field names.
const imageEnvelopeSlack = 4 << 10

// decodeImageContent decodes the base64 payload.
//
// The console has been observed to use standard base64 here, but the dauth and
// aauth servers use base64url without padding, so both are tried rather than
// rejecting an upload over an encoding detail.
func decodeImageContent(s string) ([]byte, error) {
	if s == "" {
		return nil, errors.New("empty rawContent")
	}
	for _, enc := range []*base64.Encoding{
		base64.StdEncoding, base64.RawStdEncoding,
		base64.URLEncoding, base64.RawURLEncoding,
	} {
		if raw, err := enc.DecodeString(s); err == nil {
			return raw, nil
		}
	}
	return nil, fmt.Errorf("rawContent is not base64")
}

// imageFileServer serves uploaded images.
//
// http.Dir refuses to escape its root, and only files this server wrote are in
// there, so a request cannot reach outside the image directory.
func (s *Server) imageFileServer() http.Handler {
	fs := http.FileServer(http.Dir(s.cfg.ImageDir))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Profile pictures change rarely and the console fetches them for every
		// friend on every friend-list draw, so a long cache saves real traffic.
		w.Header().Set("Cache-Control", "public, max-age=86400")
		fs.ServeHTTP(w, r)
	})
}

// GetImagePath returns the on-disk path of a stored image. Used by tests.
func (s *Server) GetImagePath(relPath string) string {
	return filepath.Join(s.cfg.ImageDir, relPath)
}
