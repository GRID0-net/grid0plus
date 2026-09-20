package scsi

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/n-popescu/switchnet/internal/db"
	sjwt "github.com/n-popescu/switchnet/internal/jwt"
)

const testApplicationID = "0100abcd00010000"

type harness struct {
	t      *testing.T
	server *httptest.Server
	store  *db.Memory

	userID          string
	deviceAccountID string
	issuer          *sjwt.Issuer
}

func newHarness(t *testing.T) *harness {
	t.Helper()

	ring := sjwt.NewKeyring(t.TempDir(), true)
	accessKey, err := ring.Key(sjwt.IssuerBAASAccess)
	if err != nil {
		t.Fatal(err)
	}

	store := db.NewMemory()
	now := time.Now().UTC()
	user := db.User{
		ID: "0000000000000001", Nickname: "Tester", Birthday: "0000-00-00",
		Permissions: db.DefaultPermissions(now), Extras: db.EmptyExtras(),
		BlocksUpdatedAt: now, FriendsUpdatedAt: now, CreatedAt: now, UpdatedAt: now,
	}
	deviceAccount := db.DeviceAccount{
		ID: "1000000000000001", UserID: user.ID,
		PasswordSHA256: "0000000000000000000000000000000000000000000000000000000000000000",
		CreatedAt:      now, UpdatedAt: now,
	}
	if err := store.CreateUser(context.Background(), user, deviceAccount); err != nil {
		t.Fatalf("create user: %v", err)
	}

	blobs, err := NewLocalDisk(t.TempDir(), 0)
	if err != nil {
		t.Fatal(err)
	}

	srv := New(Config{PublicURL: "https://storage.lp1.scsi.srv.nintendo.net"},
		store, blobs, sjwt.NewVerifier(accessKey), slog.New(slog.DiscardHandler))

	mux := http.NewServeMux()
	srv.Routes(mux)

	h := &harness{
		t: t, store: store, userID: user.ID, deviceAccountID: deviceAccount.ID,
		issuer: sjwt.NewIssuer(accessKey, "https://e0d67c509fb203858ebcb2fe3f88c2aa.baas.nintendo.com/1.0.0/internal_certificates", true),
	}
	h.server = httptest.NewServer(mux)
	t.Cleanup(h.server.Close)
	return h
}

// accessToken mints the user access token a console would present, the same
// shape internal/baas/accounts.go's own login/federation handlers sign.
func (h *harness) accessToken() string {
	h.t.Helper()
	now := time.Now()
	token, err := h.issuer.Sign(sjwt.BAASTokenClaims{
		Subject: h.userID, Audience: "test-audience", IssuedAt: now.Unix(),
		Expires: now.Add(time.Hour).Unix(), Issuer: "e0d67c509fb203858ebcb2fe3f88c2aa.baas.nintendo.com",
		TokenID: sjwt.NewTokenID(), Type: "token", GrantType: 2,
		DeviceAccountID: h.deviceAccountID, Status: sjwt.UserTokenStatus,
	})
	if err != nil {
		h.t.Fatal(err)
	}
	return token
}

func (h *harness) do(method, path string, body any) *http.Response {
	h.t.Helper()
	var reader *bytes.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			h.t.Fatal(err)
		}
		reader = bytes.NewReader(b)
	} else {
		reader = bytes.NewReader(nil)
	}
	req, err := http.NewRequest(method, h.server.URL+path, reader)
	if err != nil {
		h.t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+h.accessToken())
	req.Header.Set("X-SwitchNet-Application-Id", testApplicationID)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		h.t.Fatal(err)
	}
	h.t.Cleanup(func() { resp.Body.Close() })
	return resp
}

func decode[T any](t *testing.T, resp *http.Response) T {
	t.Helper()
	var v T
	if err := json.NewDecoder(resp.Body).Decode(&v); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	return v
}

// ---- tests ------------------------------------------------------------------

func TestUnauthenticatedRequestIsRefused(t *testing.T) {
	h := newHarness(t)
	req, _ := http.NewRequest("GET", h.server.URL+"/api/nx/v2/save_data_archives", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401", resp.StatusCode)
	}
}

func TestFullBackupAndRestoreCycle(t *testing.T) {
	h := newHarness(t)

	// start_upload creates the archive.
	resp := h.do("POST", "/api/nx/v2/save_data_archives/start_upload", startUploadRequest{
		Datatype: 1, DataSize: 4, SavedAtUnixtime: time.Now().Unix(), Desc: "test save",
	})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("start_upload status = %d", resp.StatusCode)
	}
	created := decode[map[string]archiveView](t, resp)["save_data_archive"]
	if created.Status != "uploading" {
		t.Errorf("status = %q, want uploading", created.Status)
	}
	if created.Desc != "test save" {
		t.Errorf("desc = %q", created.Desc)
	}
	archiveID := created.ID

	// create a component file
	resp = h.do("POST", "/api/nx/v2/save_data_archives/"+archiveID+"/component_files/create",
		createComponentFileRequest{Datatype: "save", Index: 0})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("component create status = %d", resp.StatusCode)
	}
	comp := decode[map[string]componentView](t, resp)["component_file"]

	// signed_uri hands back a same-server URL
	resp = h.do("GET", "/api/nx/v2/component_files/"+comp.ID+"/signed_uri", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("signed_uri status = %d", resp.StatusCode)
	}
	uri := decode[map[string]string](t, resp)["url"]
	if uri == "" {
		t.Fatal("empty signed uri")
	}

	// upload the raw bytes (a plain PUT, not JSON, so built by hand rather
	// than through h.do)
	payload := []byte("save")
	req, _ := http.NewRequest("PUT", h.server.URL+"/api/nx/v2/component_files/"+comp.ID+"/data", bytes.NewReader(payload))
	req.Header.Set("Authorization", "Bearer "+h.accessToken())
	uploadResp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer uploadResp.Body.Close()
	if uploadResp.StatusCode != http.StatusNoContent {
		t.Fatalf("upload status = %d", uploadResp.StatusCode)
	}

	// finish the component upload
	resp = h.do("POST", "/api/nx/v2/component_files/"+comp.ID+"/finish_upload",
		map[string]string{"encoded_digest": "ZGlnZXN0"})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("component finish_upload status = %d", resp.StatusCode)
	}
	finishedComp := decode[map[string]componentView](t, resp)["component_file"]
	if finishedComp.Status != "fixed" || finishedComp.ArchiveSize != int64(len(payload)) {
		t.Errorf("finished component = %+v", finishedComp)
	}

	// finish the archive upload
	resp = h.do("POST", "/api/nx/v2/save_data_archives/"+archiveID+"/finish_upload",
		map[string]string{"encoded_mac": "bWFj"})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("archive finish_upload status = %d", resp.StatusCode)
	}
	finished := decode[map[string]archiveView](t, resp)["save_data_archive"]
	if finished.Status != "fixed" {
		t.Errorf("archive status = %q, want fixed", finished.Status)
	}
	if len(finished.ComponentFiles) != 1 {
		t.Fatalf("component_files = %+v", finished.ComponentFiles)
	}

	// list finds it, filtered by application id
	resp = h.do("GET", "/api/nx/v2/save_data_archives?application_id="+testApplicationID, nil)
	list := decode[map[string][]archiveView](t, resp)["save_data_archives"]
	if len(list) != 1 || list[0].ID != archiveID {
		t.Errorf("list = %+v", list)
	}

	// restore: start_download, then download the component's raw bytes back
	resp = h.do("GET", "/api/nx/v2/save_data_archives/"+archiveID+"/start_download", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("start_download status = %d", resp.StatusCode)
	}
	restoreReq, _ := http.NewRequest("GET", h.server.URL+"/api/nx/v2/component_files/"+comp.ID+"/data", nil)
	restoreReq.Header.Set("Authorization", "Bearer "+h.accessToken())
	restoreResp, err := http.DefaultClient.Do(restoreReq)
	if err != nil {
		t.Fatal(err)
	}
	defer restoreResp.Body.Close()
	got := make([]byte, len(payload))
	if _, err := restoreResp.Body.Read(got); err != nil && err.Error() != "EOF" {
		t.Fatal(err)
	}
	if string(got) != string(payload) {
		t.Errorf("restored bytes = %q, want %q", got, payload)
	}
}

func TestArchiveOwnershipIsEnforced(t *testing.T) {
	h := newHarness(t)

	resp := h.do("POST", "/api/nx/v2/save_data_archives/start_upload", startUploadRequest{DataSize: 1})
	created := decode[map[string]archiveView](t, resp)["save_data_archive"]

	// A second user must not be able to read the first user's archive.
	other := db.User{
		ID: "0000000000000002", Nickname: "Other", Birthday: "0000-00-00",
		Permissions: db.DefaultPermissions(time.Now()), Extras: db.EmptyExtras(),
		CreatedAt: time.Now(), UpdatedAt: time.Now(),
	}
	otherDevice := db.DeviceAccount{ID: "1000000000000002", UserID: other.ID,
		PasswordSHA256: "0000000000000000000000000000000000000000000000000000000000000000",
		CreatedAt:      time.Now(), UpdatedAt: time.Now()}
	if err := h.store.CreateUser(context.Background(), other, otherDevice); err != nil {
		t.Fatal(err)
	}
	h2 := *h
	h2.userID, h2.deviceAccountID = other.ID, otherDevice.ID

	resp = h2.do("GET", "/api/nx/v2/save_data_archives/"+created.ID, nil)
	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("status = %d, want 403", resp.StatusCode)
	}
}

func TestQuotaExceededRefusesUpload(t *testing.T) {
	h := newHarness(t)
	blobs, err := NewLocalDisk(t.TempDir(), 2) // 2-byte quota
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	New(Config{PublicURL: "https://x"}, h.store, blobs,
		sjwt.NewVerifier(h.issuer.Key()), slog.New(slog.DiscardHandler)).Routes(mux)
	h.server.Close()
	h.server = httptest.NewServer(mux)

	resp := h.do("POST", "/api/nx/v2/save_data_archives/start_upload", startUploadRequest{DataSize: 5})
	archiveID := decode[map[string]archiveView](t, resp)["save_data_archive"].ID

	resp = h.do("POST", "/api/nx/v2/save_data_archives/"+archiveID+"/component_files/create",
		createComponentFileRequest{Datatype: "save"})
	compID := decode[map[string]componentView](t, resp)["component_file"].ID

	req, _ := http.NewRequest("PUT", h.server.URL+"/api/nx/v2/component_files/"+compID+"/data",
		bytes.NewReader([]byte("too many bytes")))
	req.Header.Set("Authorization", "Bearer "+h.accessToken())
	uploadResp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer uploadResp.Body.Close()
	if uploadResp.StatusCode != http.StatusInsufficientStorage {
		t.Errorf("status = %d, want 507", uploadResp.StatusCode)
	}
}
