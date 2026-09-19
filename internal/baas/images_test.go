package baas

import (
	"bytes"
	"encoding/base64"
	"image"
	"image/color"
	"image/jpeg"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// jpegBytes returns a real JPEG of the given size. A synthetic byte slice would
// not exercise the decode that makes the upload endpoint safe.
func jpegBytes(t *testing.T, w, h int) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := range h {
		for x := range w {
			img.Set(x, y, color.RGBA{R: uint8(x), G: uint8(y), B: 0x40, A: 0xff})
		}
	}
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, img, nil); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func TestImageUploadStoresAndPointsTheThumbnailAtIt(t *testing.T) {
	h := newHarness(t)
	a := h.newAccount()

	raw := jpegBytes(t, 256, 256)
	resp := h.do(request{
		method: "POST", path: "/1.0.0/image_upload",
		token: a.AccessToken,
		json: `{"rawContent":"` + base64.StdEncoding.EncodeToString(raw) +
			`","ownerId":"` + a.UserID + `","allowTransform":true}`,
	})
	requireStatus(t, resp, 200)

	var body imageUploadResponse
	decode(t, resp, &body)

	if body.State != "STORED" {
		t.Errorf("state = %q, want STORED", body.State)
	}
	if body.OwnerID != a.UserID || body.Owner.ID != a.UserID {
		t.Errorf("owner = %q/%q, want %q", body.OwnerID, body.Owner.ID, a.UserID)
	}
	// The dimensions come from decoding the image, not from the request.
	if body.Content.Width != 256 || body.Content.Height != 256 {
		t.Errorf("dimensions = %dx%d, want 256x256", body.Content.Width, body.Content.Height)
	}
	if body.Content.Format != "jpg" {
		t.Errorf("format = %q, want jpg", body.Content.Format)
	}
	// Nintendo sends the maximum signed 32-bit value, meaning "never expires".
	if body.Content.URLExpiresAt != 2147483647 {
		t.Errorf("urlExpiresAt = %d, want 2147483647", body.Content.URLExpiresAt)
	}
	if !strings.HasPrefix(body.Content.URL, "https://cdn-image.example.invalid/1/") {
		t.Errorf("url = %q, want it under the configured image base URL", body.Content.URL)
	}

	// The bytes are on disk under a filename built from the generated id.
	name := body.ID + ".jpg"
	onDisk, err := os.ReadFile(h.srv.GetImagePath(name))
	if err != nil {
		t.Fatalf("the image was not written to disk: %v", err)
	}
	if !bytes.Equal(onDisk, raw) {
		t.Error("the stored bytes differ from what was uploaded")
	}

	// Without this the upload succeeds and the profile picture never changes,
	// which looks exactly like the upload failing.
	if got := h.self(t, a).ThumbnailURL; got != body.Content.URL {
		t.Errorf("thumbnailUrl = %q, want the uploaded image's URL %q", got, body.Content.URL)
	}
	if h.self(t, a).ThumbnailUploadedAt == 0 {
		t.Error("thumbnailUploadedAt was not set")
	}
}

// TestImageUploadRejectsAnythingThatIsNotAJPEG is what makes the endpoint safe.
//
// The image directory is served from a hostname every console on the network
// trusts. A file that does not decode as a JPEG must never reach the disk, or
// the endpoint becomes a way to host arbitrary content there.
func TestImageUploadRejectsAnythingThatIsNotAJPEG(t *testing.T) {
	h := newHarness(t)
	a := h.newAccount()

	cases := map[string]string{
		"HTML":            base64.StdEncoding.EncodeToString([]byte("<html><script>x</script>")),
		"an ELF binary":   base64.StdEncoding.EncodeToString([]byte("\x7fELF\x02\x01\x01\x00")),
		"random bytes":    base64.StdEncoding.EncodeToString([]byte{0, 1, 2, 3, 4, 5, 6, 7}),
		"not base64":      "!!!not base64!!!",
		"empty":           "",
		"a PNG, not JPEG": base64.StdEncoding.EncodeToString([]byte("\x89PNG\r\n\x1a\n")),
	}
	for name, content := range cases {
		resp := h.do(request{
			method: "POST", path: "/1.0.0/image_upload",
			token: a.AccessToken,
			json:  `{"rawContent":"` + content + `","ownerId":"` + a.UserID + `"}`,
		})
		if resp.StatusCode == 200 {
			t.Errorf("%s: accepted", name)
		}
	}

	// Nothing was written for the rejected uploads. newAccount's own
	// userResponse calls lazily create the shared default-avatar.jpg, which
	// is expected and not one of the rejected uploads under test here.
	entries, err := os.ReadDir(h.srv.GetImagePath(""))
	if err == nil {
		for _, e := range entries {
			if e.Name() != defaultAvatarRelPath {
				t.Errorf("file written for a rejected upload: %v", e.Name())
			}
		}
	}
}

// TestImageUploadOwnerComesFromTheTokenNotTheBody stops any console overwriting
// anybody's profile picture.
func TestImageUploadOwnerComesFromTheTokenNotTheBody(t *testing.T) {
	h := newHarness(t)
	attacker := h.newAccount()
	victim := h.newAccount()

	raw := base64.StdEncoding.EncodeToString(jpegBytes(t, 64, 64))
	resp := h.do(request{
		method: "POST", path: "/1.0.0/image_upload",
		token: attacker.AccessToken,
		json:  `{"rawContent":"` + raw + `","ownerId":"` + victim.UserID + `"}`,
	})
	requireError(t, resp, 403, "insufficient_scope")

	// A user with no uploaded picture gets the shared default avatar, not a
	// blank thumbnailUrl -- see ensureDefaultAvatar. "Unchanged" means still
	// that default, not the attacker's image.
	wantThumb1, _ := h.srv.defaultAvatarURLOrEmpty()
	if got, want := h.self(t, victim).ThumbnailURL, wantThumb1; got != want {
		t.Errorf("the victim's thumbnail was changed to %q, want the default %q", got, want)
	}
}

func TestImageUploadWithoutAnOwnerIdUsesTheToken(t *testing.T) {
	h := newHarness(t)
	a := h.newAccount()

	raw := base64.StdEncoding.EncodeToString(jpegBytes(t, 64, 64))
	resp := h.do(request{
		method: "POST", path: "/1.0.0/image_upload",
		token: a.AccessToken,
		json:  `{"rawContent":"` + raw + `"}`,
	})
	requireStatus(t, resp, 200)

	var body imageUploadResponse
	decode(t, resp, &body)
	if body.OwnerID != a.UserID {
		t.Errorf("owner = %q, want the caller %q", body.OwnerID, a.UserID)
	}
}

func TestImageUploadEnforcesTheSizeLimit(t *testing.T) {
	h := newHarness(t, func(c *Config) { c.MaxImageBytes = 2048 })
	a := h.newAccount()

	// A 512x512 photo-like JPEG comfortably exceeds 2 KiB.
	raw := base64.StdEncoding.EncodeToString(jpegBytes(t, 512, 512))
	resp := h.do(request{
		method: "POST", path: "/1.0.0/image_upload",
		token: a.AccessToken,
		json:  `{"rawContent":"` + raw + `","ownerId":"` + a.UserID + `"}`,
	})
	if resp.StatusCode == 200 {
		t.Error("an oversized image was accepted")
	}
}

func TestImageUploadAcceptsBase64URLEncoding(t *testing.T) {
	h := newHarness(t)
	a := h.newAccount()

	// The dauth and aauth servers use base64url without padding, so an upload in
	// that encoding is plausible and costs nothing to accept.
	raw := base64.RawURLEncoding.EncodeToString(jpegBytes(t, 64, 64))
	resp := h.do(request{
		method: "POST", path: "/1.0.0/image_upload",
		token: a.AccessToken,
		json:  `{"rawContent":"` + raw + `","ownerId":"` + a.UserID + `"}`,
	})
	requireStatus(t, resp, 200)
}

func TestUploadedImageIsServedBack(t *testing.T) {
	h := newHarness(t)
	a := h.newAccount()

	raw := jpegBytes(t, 128, 128)
	resp := h.do(request{
		method: "POST", path: "/1.0.0/image_upload",
		token: a.AccessToken,
		json: `{"rawContent":"` + base64.StdEncoding.EncodeToString(raw) +
			`","ownerId":"` + a.UserID + `"}`,
	})
	requireStatus(t, resp, 200)

	var body imageUploadResponse
	decode(t, resp, &body)

	// The console fetches this for every friend on every friend-list draw, at
	// both '/1/' and '/2/' -- see images.go's imageURLs doc comment.
	for _, prefix := range []string{"/1/", "/2/"} {
		resp = h.do(request{
			method: "GET", path: prefix + body.ID + ".jpg", token: a.AccessToken,
		})
		requireStatus(t, resp, 200)
	}
}

// TestImageFileServerCannotEscapeTheImageDirectory covers path traversal. The
// filename is built from a generated id and never from the request, and
// http.Dir refuses to leave its root, so both layers are checked.
func TestImageFileServerCannotEscapeTheImageDirectory(t *testing.T) {
	h := newHarness(t)
	a := h.newAccount()

	secret := filepath.Join(filepath.Dir(h.srv.GetImagePath("")), "secret.txt")
	if err := os.WriteFile(secret, []byte("private"), 0o600); err != nil {
		t.Fatal(err)
	}

	for _, path := range []string{
		"/1/../secret.txt",
		"/1/..%2Fsecret.txt",
		"/1/%2e%2e/secret.txt",
		"/1/....//secret.txt",
	} {
		resp := h.do(request{method: "GET", path: path, token: a.AccessToken})
		if resp.StatusCode == 200 {
			t.Errorf("%s: served a file from outside the image directory", path)
		}
	}
}
