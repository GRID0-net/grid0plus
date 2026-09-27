package baas

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/jpeg"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/n-popescu/grid0plus/internal/db"
)

// ---- POST /1.0.0/image_upload -------------------------------------------

// imageUploadRequest is the JSON body.
//
// Reference: https://github.com/kinnay/NintendoClients/wiki/BAAS-Server#post-100image_upload
type imageUploadRequest struct {
	// RawContent is a base64-encoded JPEG.
	RawContent string `json:"rawContent"`
	OwnerID    string `json:"ownerId"`
	// AllowTransform lets the server resize the image. GRID0+ stores what it
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

	img, thumb1, err := s.storeProfileImage(r.Context(), c.UserID, raw)
	if errors.Is(err, errNotAJPEG) {
		writeError(w, r, errInvalidRawContent)
		return
	}
	if err != nil {
		writeInternalError(s.log, w, r, "store image", err)
		return
	}
	imageID, now := img.ID, img.CreatedAt
	cfg := struct{ Width, Height int }{img.Width, img.Height}

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
			URL:          thumb1,
			URLExpiresAt: 2147483647,
		},
		CreatedAt: now.Unix(),
		UpdatedAt: now.Unix(),
	})
}

var errNotAJPEG = errors.New("image is not a JPEG")

// storeProfileImage writes a JPEG to the image directory, records it, and
// makes it the user's profile picture. Shared by the console's upload and
// the Discord bot's avatar sync.
func (s *Server) storeProfileImage(ctx context.Context, userID string, raw []byte) (db.Image, string, error) {
	// Decode the image before writing anything. This is what makes it safe: a
	// file that does not decode as a JPEG never reaches the disk, so the
	// image directory cannot be used to host arbitrary content served from a
	// hostname the consoles trust.
	cfg, format, err := image.DecodeConfig(bytes.NewReader(raw))
	if err != nil || format != "jpeg" {
		s.log.Warn("baas: image upload is not a JPEG",
			"user", userID, "format", format, "error", err)
		return db.Image{}, "", errNotAJPEG
	}

	imageID, err := newHexID(16)
	if err != nil {
		return db.Image{}, "", err
	}
	// The filename is built from the generated id, never from the request, so
	// there is no path to traverse.
	relPath := imageID + ".jpg"
	if err := os.MkdirAll(s.cfg.ImageDir, 0o750); err != nil {
		return db.Image{}, "", err
	}
	if err := os.WriteFile(filepath.Join(s.cfg.ImageDir, relPath), raw, 0o640); err != nil {
		return db.Image{}, "", err
	}

	now := s.now()
	img := db.Image{
		ID: imageID, OwnerID: userID, Format: "jpg",
		Width: cfg.Width, Height: cfg.Height, SizeBytes: len(raw),
		RelPath: relPath, CreatedAt: now, UpdatedAt: now,
	}
	if err := s.store.CreateImage(ctx, img); err != nil {
		return db.Image{}, "", err
	}

	thumb1, thumb2 := s.imageURLs(relPath)
	// Point the user's thumbnail at the new image. Without this the upload
	// succeeds and the profile picture never changes, which looks like the
	// upload failing.
	if _, err := s.store.UpdateUser(ctx, userID, db.UserPatch{
		ThumbnailURL:        &thumb1,
		Thumbnail2URL:       &thumb2,
		ThumbnailUploadedAt: &now,
	}); err != nil {
		return db.Image{}, "", err
	}
	return img, thumb1, nil
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
// there, so a request cannot reach outside the image directory. Registered at
// both /1/{name} and /2/{name} (see router.go) -- GRID0+ stores one file
// per image, not two sizes of it, so both paths serve the same bytes.
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

// imageURLs builds the two URLs a stored image is reachable at.
//
// A 2026-09-19 capture of a real, working Nintendo Account link -- the
// console's very next request after a successful federation, immediately
// fetching its own profile picture -- showed the console asking for
// '/1/<etag>' and '/2/<etag>' at the root of the cdn-image host, not
// '/images/<name>'. GRID0+'s own request log had never shown that
// request at all, on any of the 17+ prior attempts at this same bug: the
// shape mismatch meant the client's own URL validation rejected the field
// before ever making the request, which is consistent with -- and the first
// concrete explanation for -- the "succeeds through federation, then zero
// further network activity" signature every one of those attempts shared.
// See docs/CAPTURES/README.md and the 2124-0292 investigation notes.
func (s *Server) imageURLs(relPath string) (thumb1, thumb2 string) {
	base := strings.TrimSuffix(s.cfg.ImageBaseURL, "/")
	return base + "/1/" + relPath, base + "/2/" + relPath
}

// defaultAvatarURLOrEmpty is ensureDefaultAvatar with the one error-handling
// policy every caller wants: log and fall back to empty thumbnail URLs rather
// than fail the request a picture was never going to block.
func (s *Server) defaultAvatarURLOrEmpty() (thumb1, thumb2 string) {
	relPath, err := s.ensureDefaultAvatar()
	if err != nil {
		s.log.Warn("baas: could not create default avatar", "error", err)
		return "", ""
	}
	return s.imageURLs(relPath)
}

// defaultAvatarRelPath is the filename ensureDefaultAvatar writes and
// naUsersMe (nalink.go) points a person with no uploaded thumbnail at.
//
// Deliberately not "default-avatar.jpg" (no extension, matching the real
// server's shape). A 2026-09-21 hardware capture, the first to show the
// console actually requesting this URL at all, confirmed it fetches
// exactly the relPath this constant sets -- meaning the extension mismatch
// was invisible in every capture before that one purely because nothing had
// exercised the "brand new account, no uploaded picture" path over a
// hardware capture until then. The fetch itself still succeeded (a real
// image came back either way), so this is a fidelity fix, not a diagnosis
// of that session's actual failure.
const defaultAvatarRelPath = "default"

// ensureDefaultAvatar returns the filename of a default avatar, relative to
// ImageDir, generating the file the first time it is needed.
//
// A freshly registered person (website.go) has no uploaded thumbnail, and
// real Nextendo's "add this user to this console" confirmation screen — the
// model the whole Nintendo Account link flow is built to match, per the
// operator 2026-08-26 — always has something to show there, never a blank
// space. Checked and regenerated lazily rather than at startup: ImageDir may
// not exist yet on a fresh deployment, and this endpoint is low-traffic
// enough that an os.Stat per request costs nothing worth avoiding.
func (s *Server) ensureDefaultAvatar() (string, error) {
	path := filepath.Join(s.cfg.ImageDir, defaultAvatarRelPath)
	if _, err := os.Stat(path); err == nil {
		return defaultAvatarRelPath, nil
	}

	if err := os.MkdirAll(s.cfg.ImageDir, 0o750); err != nil {
		return "", err
	}

	// A plain solid square. Its only job is to not be blank; there is
	// nothing to personalise it with yet.
	const size = 256
	img := image.NewRGBA(image.Rect(0, 0, size, size))
	fill := color.RGBA{R: 0x4a, G: 0x5a, B: 0x8a, A: 0xff}
	for y := range size {
		for x := range size {
			img.Set(x, y, fill)
		}
	}

	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, img, &jpeg.Options{Quality: 85}); err != nil {
		return "", err
	}
	if err := os.WriteFile(path, buf.Bytes(), 0o640); err != nil {
		return "", err
	}
	return defaultAvatarRelPath, nil
}
