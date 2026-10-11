package ugcstore

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/n-popescu/grid0plus/internal/db"
	ugcpb "github.com/n-popescu/grid0plus/proto/ugcstore/v1"
)

// fakeSigner signs "kind|owner|blob"; good enough to test everything but the
// cryptography, which npln's issuer already covers.
var fakeSigner = URLSigner{
	Sign: func(kind, owner, blob string, _ time.Time) (string, error) {
		return kind + "~" + owner + "~" + blob, nil
	},
	Verify: func(kind, token string) (string, string, error) {
		p := strings.Split(token, "~")
		if len(p) != 3 || p[0] != kind {
			return "", "", errors.New("bad token")
		}
		return p[1], p[2], nil
	},
}

// attachmentServices is two players' views of one store.
func attachmentServices(t *testing.T) (alice, bob *Service, srv *httptest.Server) {
	t.Helper()
	store := db.NewMemory()
	mk := func(uid string) *Service {
		return NewService(Options{
			Store: store, Log: slog.New(slog.NewTextHandler(io.Discard, nil)), Tenant: "dce9377b",
			CallerID: func(context.Context) (string, bool) { return uid, true },
			Now:      func() time.Time { return time.Date(2026, 10, 11, 12, 0, 0, 0, time.UTC) },
			Signer:   fakeSigner, AttachmentBaseURL: "https://example.test",
		})
	}
	alice, bob = mk("alice"), mk("bob")
	srv = httptest.NewServer(alice.AttachmentHandler())
	t.Cleanup(srv.Close)
	return alice, bob, srv
}

func put(t *testing.T, url, contentRange string, body []byte) *http.Response {
	t.Helper()
	req, _ := http.NewRequest(http.MethodPut, url, bytes.NewReader(body))
	if contentRange != "" {
		req.Header.Set("Content-Range", contentRange)
	}
	req.Header.Set("Content-Type", "image/jpeg")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	return resp
}

// TestADrawingUploadedInChunksReachesAnotherPlayer is the whole path the
// game takes to post a drawing: an upload session, resumable chunks and a
// status check the way its uploader sends them, the commit that names the
// upload, and another console downloading it.
func TestADrawingUploadedInChunksReachesAnotherPlayer(t *testing.T) {
	alice, bob, srv := attachmentServices(t)
	ctx := context.Background()
	image := bytes.Repeat([]byte("ink"), 1000)

	up, err := alice.IssueUploadUri(ctx, &ugcpb.IssueUploadUriRequest{Tenant: "tenants/current", Resumable: true})
	if err != nil {
		t.Fatalf("IssueUploadUri: %v", err)
	}
	session := strings.Replace(up.GetUri(), "https://example.test", srv.URL, 1)

	if r := put(t, session, "bytes 0-1999/3000", image[:2000]); r.StatusCode != http.StatusPermanentRedirect ||
		r.Header.Get("Range") != "bytes=0-1999" {
		t.Fatalf("first chunk: %d Range=%q, want 308 bytes=0-1999", r.StatusCode, r.Header.Get("Range"))
	}
	if r := put(t, session, "bytes */3000", nil); r.StatusCode != http.StatusPermanentRedirect ||
		r.Header.Get("Range") != "bytes=0-1999" {
		t.Fatalf("status check: %d Range=%q", r.StatusCode, r.Header.Get("Range"))
	}
	if r := put(t, session, "bytes 2000-2999/3000", image[2000:]); r.StatusCode != http.StatusOK {
		t.Fatalf("last chunk: %d, want 200", r.StatusCode)
	}

	doc := "tenants/current/documents/services/toyohr/interfaces/canola/picturePosts/p1"
	if _, err := alice.CommitDocuments(ctx, &ugcpb.CommitDocumentsRequest{WriteOperations: []*ugcpb.WriteOperation{
		{OperationType: &ugcpb.WriteOperation_UpdateDocument{UpdateDocument: &ugcpb.UpdateDocumentRequest{
			Document: &ugcpb.Document{Name: doc}}}},
		{OperationType: &ugcpb.WriteOperation_ImportAttachment{ImportAttachment: &ugcpb.ImportAttachmentRequest{
			Parent: doc, Uri: up.GetUri()}}},
	}}); err != nil {
		t.Fatalf("CommitDocuments: %v", err)
	}

	got, err := bob.IssueAttachmentUri(ctx, &ugcpb.IssueAttachmentUriRequest{Parent: doc})
	if err != nil {
		t.Fatalf("IssueAttachmentUri: %v", err)
	}
	resp, err := http.Get(strings.Replace(got.GetUri(), "https://example.test", srv.URL, 1))
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if !bytes.Equal(body, image) || resp.Header.Get("Content-Type") != "image/jpeg" {
		t.Errorf("downloaded %d bytes (%s), want the %d uploaded", len(body), resp.Header.Get("Content-Type"), len(image))
	}

	// Bob cannot attach Alice's upload to a document of his own.
	_, err = bob.CommitDocuments(ctx, &ugcpb.CommitDocumentsRequest{WriteOperations: []*ugcpb.WriteOperation{
		{OperationType: &ugcpb.WriteOperation_ImportAttachment{ImportAttachment: &ugcpb.ImportAttachmentRequest{
			Parent: "tenants/current/documents/users/bob/photoAlbum/x", Uri: up.GetUri()}}},
	}})
	if err == nil {
		t.Error("another player's upload was attached")
	}
}

// TestAnInlineAttachmentIsServedAndRemovedWithItsDocument covers
// SetAttachment, the other way a commit can carry bytes, and that deleting
// the document takes its attachment with it.
func TestAnInlineAttachmentIsServedAndRemovedWithItsDocument(t *testing.T) {
	alice, _, _ := attachmentServices(t)
	ctx := context.Background()
	doc := "tenants/current/documents/users/alice/photoAlbum/PA1-x"
	commit := func(ops ...*ugcpb.WriteOperation) {
		t.Helper()
		if _, err := alice.CommitDocuments(ctx, &ugcpb.CommitDocumentsRequest{WriteOperations: ops}); err != nil {
			t.Fatalf("CommitDocuments: %v", err)
		}
	}
	commit(
		&ugcpb.WriteOperation{OperationType: &ugcpb.WriteOperation_UpdateDocument{UpdateDocument: &ugcpb.UpdateDocumentRequest{
			Document: &ugcpb.Document{Name: doc}}}},
		&ugcpb.WriteOperation{OperationType: &ugcpb.WriteOperation_SetAttachment{SetAttachment: &ugcpb.SetAttachmentRequest{
			Parent: doc, Body: []byte("photo"), MimeType: "image/jpeg"}}},
	)
	if _, err := alice.IssueAttachmentUri(ctx, &ugcpb.IssueAttachmentUriRequest{Parent: doc}); err != nil {
		t.Fatalf("IssueAttachmentUri after SetAttachment: %v", err)
	}
	commit(&ugcpb.WriteOperation{OperationType: &ugcpb.WriteOperation_DeleteDocument{DeleteDocument: &ugcpb.DeleteDocumentRequest{Name: doc}}})
	if _, err := alice.IssueAttachmentUri(ctx, &ugcpb.IssueAttachmentUriRequest{Parent: doc}); err == nil {
		t.Error("a deleted document's attachment is still served")
	}
}
