package scsi

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/n-popescu/grid0plus/internal/db"
	sjwt "github.com/n-popescu/grid0plus/internal/jwt"
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
	idKey, err := ring.Key(sjwt.IssuerBAASID)
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

	srv := New(Config{PublicURL: "https://storage.hac.lp1.scsi.srv.nintendo.net"},
		store, blobs, sjwt.NewVerifier(idKey), slog.New(slog.DiscardHandler))

	mux := http.NewServeMux()
	srv.Routes(mux)

	h := &harness{
		t: t, store: store, userID: user.ID, deviceAccountID: deviceAccount.ID,
		issuer: sjwt.NewIssuer(idKey, "https://e0d67c509fb203858ebcb2fe3f88c2aa.baas.nintendo.com/1.0.0/certificates", true),
	}
	h.server = httptest.NewServer(mux)
	t.Cleanup(h.server.Close)
	return h
}

// accessToken mints the id token a real console presents to this service,
// the same shape internal/npln's own exchangeIDToken verifies at its
// equivalent seam -- see auth.go's authenticate for why this is an id token,
// not a BAAS access token.
func (h *harness) accessToken() string {
	h.t.Helper()
	now := time.Now()
	token, err := h.issuer.Sign(sjwt.BAASTokenClaims{
		Subject: h.userID, Audience: "test-audience", IssuedAt: now.Unix(),
		Expires: now.Add(time.Hour).Unix(), Issuer: "e0d67c509fb203858ebcb2fe3f88c2aa.baas.nintendo.com",
		TokenID: sjwt.NewTokenID(), Type: "id_token",
		DeviceAccountID: h.deviceAccountID,
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

// TestConsoleV3RoutesAnswerTheSameAsTheirV2Counterparts is the regression
// test for the hall-stall cause: a real console calls
// /api/console/v3/save_data_archives and
// /api/console/v3/network_service_accounts/{id}/notification_tokens, not the
// /api/nx/v2/ paths, and a 404 on both is retried continuously roughly every
// 700ms rather than treated as harmless.
func TestConsoleV3RoutesAnswerTheSameAsTheirV2Counterparts(t *testing.T) {
	h := newHarness(t)

	list := h.do("GET", "/api/console/v3/save_data_archives?datatype=0", nil)
	if list.StatusCode != http.StatusOK {
		t.Errorf("GET .../v3/save_data_archives: status = %d, want 200", list.StatusCode)
	}
	body := decode[map[string]any](t, list)
	if _, ok := body["save_data_archives"]; !ok {
		t.Errorf("response has no save_data_archives field: %v", body)
	}

	reg := h.do("POST", "/api/console/v3/network_service_accounts/"+h.userID+"/notification_tokens",
		map[string]string{"notification_token": "test-token"})
	if reg.StatusCode != http.StatusOK {
		t.Errorf("POST .../v3/.../notification_tokens: status = %d, want 200", reg.StatusCode)
	}
}

// TestStartUploadReachableUnderConsoleV3Path is the regression test for the
// same retry loop: POST /api/console/v3/save_data_archives/start_upload answered
// with a 404 from logUnimplemented makes the console retry continuously rather
// than move on. Routed to the same handler as its v2 counterpart, the same way
// the two routes above it already are.
func TestStartUploadReachableUnderConsoleV3Path(t *testing.T) {
	h := newHarness(t)

	resp := h.do("POST", "/api/console/v3/save_data_archives/start_upload", startUploadRequest{
		ApplicationID: testApplicationID, DataSize: 4,
	})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("start_upload under /api/console/v3/ status = %d, want 201", resp.StatusCode)
	}
	created := decode[map[string]archiveView](t, resp)["save_data_archive"]
	if created.ApplicationID != testApplicationID {
		t.Errorf("ApplicationID = %q, want %q -- from the request body, not a header", created.ApplicationID, testApplicationID)
	}
}

// TestStartUploadSeriesIDSurvivesBeyondInt64Range is the regression test for
// a real finding: a real console's series_id (12237957226414712540) exceeds
// int64's own maximum (9223372036854775807), so it must round-trip as an exact
// digit string, never any fixed-width integer type.
func TestStartUploadSeriesIDSurvivesBeyondInt64Range(t *testing.T) {
	h := newHarness(t)
	const wantSeriesID = "12237957226414712540"

	resp := h.do("POST", "/api/console/v3/save_data_archives/start_upload", startUploadRequest{
		ApplicationID: testApplicationID, DataSize: 4, SeriesID: json.Number(wantSeriesID),
	})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("start_upload status = %d, want 201", resp.StatusCode)
	}
	created := decode[map[string]archiveView](t, resp)["save_data_archive"]
	if created.SeriesID.String() != wantSeriesID {
		t.Errorf("SeriesID = %q, want %q", created.SeriesID.String(), wantSeriesID)
	}

	// And a real read-back (GetSaveDataArchive) must marshal it as a bare
	// JSON number, matching a real console's own response shape -- not a
	// quoted string, which archiveView.SeriesID being anything other than
	// json.Number would produce.
	get := h.do("GET", "/api/nx/v2/save_data_archives/"+created.ID, nil)
	body, err := io.ReadAll(get.Body)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(body, []byte(`"series_id":`+wantSeriesID)) {
		t.Errorf("GetSaveDataArchive body does not contain an unquoted series_id:%s -- got %s", wantSeriesID, body)
	}
}

func TestFullBackupAndRestoreCycle(t *testing.T) {
	h := newHarness(t)

	// start_upload creates the archive.
	resp := h.do("POST", "/api/nx/v2/save_data_archives/start_upload", startUploadRequest{
		ApplicationID: testApplicationID,
		Datatype:      1, DataSize: 4, SavedAtUnixtime: time.Now().Unix(), Desc: "test save",
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

	resp := h.do("POST", "/api/nx/v2/save_data_archives/start_upload", startUploadRequest{ApplicationID: testApplicationID, DataSize: 1})
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

	resp := h.do("POST", "/api/nx/v2/save_data_archives/start_upload", startUploadRequest{ApplicationID: testApplicationID, DataSize: 5})
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

// TestReuploadingAComponentDoesNotDoubleCountItsOwnOldBytesAgainstQuota
// guards against a bug where a component file's own previously-stored size
// was counted as "already used" and then the new upload was checked against
// the quota on top of that -- rejecting a same-size or smaller replacement
// upload that would fit fine once the old bytes are actually replaced.
func TestReuploadingAComponentDoesNotDoubleCountItsOwnOldBytesAgainstQuota(t *testing.T) {
	h := newHarness(t)
	blobs, err := NewLocalDisk(t.TempDir(), 5) // 5-byte quota
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	New(Config{PublicURL: "https://x"}, h.store, blobs,
		sjwt.NewVerifier(h.issuer.Key()), slog.New(slog.DiscardHandler)).Routes(mux)
	h.server.Close()
	h.server = httptest.NewServer(mux)

	resp := h.do("POST", "/api/nx/v2/save_data_archives/start_upload", startUploadRequest{ApplicationID: testApplicationID, DataSize: 5})
	archiveID := decode[map[string]archiveView](t, resp)["save_data_archive"].ID
	resp = h.do("POST", "/api/nx/v2/save_data_archives/"+archiveID+"/component_files/create",
		createComponentFileRequest{Datatype: "save"})
	compID := decode[map[string]componentView](t, resp)["component_file"].ID

	upload := func(data []byte) int {
		req, _ := http.NewRequest("PUT", h.server.URL+"/api/nx/v2/component_files/"+compID+"/data", bytes.NewReader(data))
		req.Header.Set("Authorization", "Bearer "+h.accessToken())
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		return resp.StatusCode
	}

	// Fill the entire 5-byte quota once.
	if status := upload([]byte("12345")); status != http.StatusNoContent {
		t.Fatalf("first upload status = %d, want 204", status)
	}
	// Re-uploading the SAME 5 bytes to the SAME component must still fit --
	// it replaces the old content, it does not add to it. The buggy version
	// counted the component's existing 5 bytes as "used" and then rejected
	// this because 5 (used) + 5 (new) > 5 (quota).
	if status := upload([]byte("54321")); status != http.StatusNoContent {
		t.Errorf("re-upload of the same size status = %d, want 204 (quota must not double-count the file being replaced)", status)
	}
	// A genuinely larger replacement that would exceed the quota must still
	// be refused.
	if status := upload([]byte("123456")); status != http.StatusInsufficientStorage {
		t.Errorf("oversized re-upload status = %d, want 507", status)
	}
}

func TestCustomerOperationsRequiresAuthentication(t *testing.T) {
	h := newHarness(t)
	req, _ := http.NewRequest("POST", h.server.URL+"/api/nx/v1/customer_operations", bytes.NewReader(nil))
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401 (unimplemented endpoints must still require a token, like every other endpoint here)", resp.StatusCode)
	}
}
