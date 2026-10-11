package ugcstore

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/n-popescu/grid0plus/internal/db"
	ugcpb "github.com/n-popescu/grid0plus/proto/ugcstore/v1"
)

// Attachments: the bytes behind a document -- a drawing's image, a locker's
// photo, a replay. Splatoon 3 never sends them through gRPC. It asks
// IssueUploadUri for a URL, uploads to it over HTTPS, then names the upload
// in CommitDocuments; another console asks IssueAttachmentUri for a URL to
// download it from. NextendoNetwork's capture of a drawing being posted
// (facts only, none of its code used) shows that order and a signed
// storage.googleapis.com URL; Nintendo's storage is Google Cloud Storage.
//
// The game's uploader speaks GCS's resumable protocol (found in its main,
// 2026-10-11): it PUTs chunks with "Content-Range: bytes a-b/total", checks
// a session with "Content-Range: bytes */total", reads "Range: " from a 308
// to resume, treats 200/201 as done and 404/410/499 as a session to restart
// (status handling at 0x710065e0c4). The URL IssueUploadUri returns is the
// session itself -- the binary has no x-goog-resumable initiation.
//
// Here the URLs point at this server's own HTTP front end, on a host the
// console already trusts, and the bytes live in the database.
//
// TODO(capture-needed): which write names the upload in the commit
// (ImportAttachment with the URL, or the bytes inline in SetAttachment) and
// what the final 200's body holds. Both writes are accepted and the body is
// a minimal JSON object; the console's first post, logged as "ugcstore:
// upload opened" / "attachment set", settles it. Change: serveUpload and
// CommitDocuments' attachment cases.

const (
	uploadPath   = "/grid0/ugc/upload/"
	downloadPath = "/grid0/ugc/blob/"

	// DefaultMaxAttachmentBytes bounds one attachment. A drawing or a locker
	// photo is far smaller; a replay is the largest thing the game uploads.
	DefaultMaxAttachmentBytes = 8 << 20

	uploadTTL         = time.Hour
	defaultDownloadTT = time.Hour
	// unattachedTTL is how long an upload nobody committed is kept.
	unattachedTTL = 24 * time.Hour
)

// URLSigner signs and checks the attachment URLs. kind is "upload" or
// "download"; the token names who may use it and which blob. It is handed
// in by npln, which holds the key, the same way CallerID is.
type URLSigner struct {
	Sign   func(kind, owner, blobID string, expires time.Time) (string, error)
	Verify func(kind, token string) (owner, blobID string, err error)
}

func (s *Service) attachmentsEnabled() bool {
	return s.signer.Sign != nil && s.signer.Verify != nil && s.baseURL != ""
}

// IssueUploadUri opens an upload session for the caller.
func (s *Service) IssueUploadUri(ctx context.Context, req *ugcpb.IssueUploadUriRequest) (*ugcpb.IssueUploadUriResponse, error) {
	uid, err := s.caller(ctx)
	if err != nil {
		return nil, err
	}
	if !s.attachmentsEnabled() {
		return nil, status.Error(codes.Unimplemented, "attachments are not configured")
	}
	now := s.now().UTC()
	if err := s.store.PurgeUnattachedUGCBlobs(ctx, now.Add(-unattachedTTL)); err != nil {
		s.log.Warn("ugcstore: purge unattached uploads", "error", err)
	}
	id := uuid.NewString()
	if err := s.store.PutUGCBlob(ctx, db.UGCBlob{ID: id, Owner: uid, Total: -1, CreateTime: now}); err != nil {
		s.log.Error("ugcstore: open upload", "error", err)
		return nil, status.Error(codes.Internal, "internal error")
	}
	token, err := s.signer.Sign("upload", uid, id, now.Add(uploadTTL))
	if err != nil {
		return nil, status.Error(codes.Internal, "internal error")
	}
	s.log.Info("ugcstore: upload opened", "user", uid, "blob", id, "resumable", req.GetResumable())
	return &ugcpb.IssueUploadUriResponse{Uri: s.baseURL + uploadPath + token}, nil
}

// IssueAttachmentUri answers a download URL for a document's attachment.
func (s *Service) IssueAttachmentUri(ctx context.Context, req *ugcpb.IssueAttachmentUriRequest) (*ugcpb.IssueAttachmentUriResponse, error) {
	uid, err := s.caller(ctx)
	if err != nil {
		return nil, err
	}
	uri, err := s.attachmentURI(ctx, uid, req)
	if err != nil {
		return nil, err
	}
	return &ugcpb.IssueAttachmentUriResponse{Uri: uri}, nil
}

// BulkIssueAttachmentUri answers one URL per request, in order. A document
// with no attachment answers an empty URL rather than ending the stream:
// one missing picture should not cost the console every other one.
func (s *Service) BulkIssueAttachmentUri(stream grpc.BidiStreamingServer[ugcpb.IssueAttachmentUriRequest, ugcpb.IssueAttachmentUriResponse]) error {
	uid, err := s.caller(stream.Context())
	if err != nil {
		return err
	}
	for {
		req, err := stream.Recv()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return err
		}
		uri, err := s.attachmentURI(stream.Context(), uid, req)
		if err != nil && status.Code(err) != codes.NotFound {
			return err
		}
		if err := stream.Send(&ugcpb.IssueAttachmentUriResponse{Uri: uri}); err != nil {
			return err
		}
	}
}

func (s *Service) attachmentURI(ctx context.Context, uid string, req *ugcpb.IssueAttachmentUriRequest) (string, error) {
	if !s.attachmentsEnabled() {
		return "", status.Error(codes.Unimplemented, "attachments are not configured")
	}
	key, ok := s.canonicalName(req.GetParent())
	if !ok {
		return "", status.Error(codes.InvalidArgument, "invalid document name")
	}
	b, err := s.store.GetUGCDocumentBlob(ctx, key)
	if errors.Is(err, db.ErrNotFound) || (err == nil && !b.Complete) {
		return "", status.Error(codes.NotFound, "no attachment")
	}
	if err != nil {
		s.log.Error("ugcstore: load attachment", "document", key, "error", err)
		return "", status.Error(codes.Internal, "internal error")
	}
	ttl := defaultDownloadTT
	if d := req.GetTtl(); d != nil && d.AsDuration() > 0 && d.AsDuration() < 24*time.Hour {
		ttl = d.AsDuration()
	}
	expires := s.now().Add(ttl)
	if t := req.GetExpireTime(); t != nil && t.AsTime().After(s.now()) && t.AsTime().Before(s.now().Add(24*time.Hour)) {
		expires = t.AsTime()
	}
	token, err := s.signer.Sign("download", uid, b.ID, expires)
	if err != nil {
		return "", status.Error(codes.Internal, "internal error")
	}
	return s.baseURL + downloadPath + token, nil
}

// blobFromURI resolves an upload URL this server issued back to the blob,
// for ImportAttachment. Only the uploader may attach it, and only once it
// is complete.
func (s *Service) blobFromURI(ctx context.Context, uid, uri string) (db.UGCBlob, error) {
	_, token, ok := strings.Cut(uri, uploadPath)
	if !ok || !s.attachmentsEnabled() {
		return db.UGCBlob{}, status.Error(codes.InvalidArgument, "not an upload issued here")
	}
	owner, id, err := s.signer.Verify("upload", token)
	if err != nil || owner != uid {
		return db.UGCBlob{}, status.Error(codes.PermissionDenied, "permission denied")
	}
	b, err := s.store.GetUGCBlob(ctx, id)
	if errors.Is(err, db.ErrNotFound) {
		return db.UGCBlob{}, status.Error(codes.NotFound, "upload not found")
	}
	if err != nil {
		return db.UGCBlob{}, status.Error(codes.Internal, "internal error")
	}
	if !b.Complete || b.Owner != uid {
		return db.UGCBlob{}, status.Error(codes.FailedPrecondition, "upload is not complete")
	}
	return b, nil
}

// AttachmentHandler serves the upload and download URLs. Mounted on the
// JSON front end, under /grid0/ugc/.
func (s *Service) AttachmentHandler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc(uploadPath, s.serveUpload)
	mux.HandleFunc(downloadPath, s.serveDownload)
	return mux
}

func (s *Service) serveDownload(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	_, id, err := s.signer.Verify("download", strings.TrimPrefix(r.URL.Path, downloadPath))
	if err != nil {
		w.WriteHeader(http.StatusForbidden)
		return
	}
	b, err := s.store.GetUGCBlob(r.Context(), id)
	if err != nil || !b.Complete {
		w.WriteHeader(http.StatusNotFound)
		return
	}
	mime := b.MimeType
	if mime == "" {
		mime = "application/octet-stream"
	}
	w.Header().Set("Content-Type", mime)
	w.Header().Set("Content-Length", strconv.Itoa(len(b.Data)))
	if r.Method == http.MethodGet {
		w.Write(b.Data)
	}
}

// contentRange parses "bytes a-b/total", "bytes a-b/*" and "bytes */total".
// first is -1 for a status check; total is -1 when unknown.
func contentRange(h string) (first, last, total int64, ok bool) {
	rest, found := strings.CutPrefix(strings.TrimSpace(h), "bytes ")
	if !found {
		return 0, 0, 0, false
	}
	span, tot, found := strings.Cut(rest, "/")
	if !found {
		return 0, 0, 0, false
	}
	total = -1
	if tot != "*" {
		n, err := strconv.ParseInt(tot, 10, 64)
		if err != nil || n < 0 {
			return 0, 0, 0, false
		}
		total = n
	}
	if span == "*" {
		return -1, -1, total, true
	}
	a, b, found := strings.Cut(span, "-")
	if !found {
		return 0, 0, 0, false
	}
	first, err1 := strconv.ParseInt(a, 10, 64)
	last, err2 := strconv.ParseInt(b, 10, 64)
	if err1 != nil || err2 != nil || first < 0 || last < first {
		return 0, 0, 0, false
	}
	return first, last, total, true
}

func (s *Service) serveUpload(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPut && r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	owner, id, err := s.signer.Verify("upload", strings.TrimPrefix(r.URL.Path, uploadPath))
	if err != nil {
		// GCS answers an unusable session 404, which the game's uploader
		// reads as "start again" rather than as a hard failure.
		w.WriteHeader(http.StatusNotFound)
		return
	}
	ctx := r.Context()
	b, err := s.store.GetUGCBlob(ctx, id)
	if err != nil || b.Owner != owner {
		w.WriteHeader(http.StatusNotFound)
		return
	}
	if b.Complete {
		w.WriteHeader(http.StatusOK)
		return
	}

	first, last, total := int64(0), int64(-1), int64(-1)
	if h := r.Header.Get("Content-Range"); h != "" {
		var ok bool
		if first, last, total, ok = contentRange(h); !ok {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
	}
	if total > s.maxAttachment {
		w.WriteHeader(http.StatusRequestEntityTooLarge)
		return
	}
	if total >= 0 {
		b.Total = total
	}
	if ct := r.Header.Get("Content-Type"); ct != "" && b.MimeType == "" {
		b.MimeType = ct
	}

	if first >= 0 {
		body, err := io.ReadAll(io.LimitReader(r.Body, s.maxAttachment+1))
		if err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		if r.Header.Get("Content-Range") == "" {
			// A plain PUT is the whole object.
			first, b.Total = 0, int64(len(body))
			last = int64(len(body)) - 1
		}
		if first > int64(len(b.Data)) {
			// A gap: answer what is stored so the uploader resends from there.
			s.writeIncomplete(w, b)
			return
		}
		data := append(b.Data[:first:first], body...)
		if int64(len(data)) > s.maxAttachment {
			w.WriteHeader(http.StatusRequestEntityTooLarge)
			return
		}
		_ = last
		b.Data = data
	}
	b.Complete = b.Total >= 0 && int64(len(b.Data)) == b.Total
	if err := s.store.PutUGCBlob(ctx, b); err != nil {
		s.log.Error("ugcstore: store upload", "blob", id, "error", err)
		w.WriteHeader(http.StatusServiceUnavailable)
		return
	}
	if !b.Complete {
		s.writeIncomplete(w, b)
		return
	}
	s.log.Info("ugcstore: upload complete", "user", owner, "blob", id, "bytes", len(b.Data), "type", b.MimeType)
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	fmt.Fprintf(w, `{"size":"%d"}`, len(b.Data))
}

// writeIncomplete answers GCS's "resume incomplete": 308, with the range
// stored so far when there is one.
func (s *Service) writeIncomplete(w http.ResponseWriter, b db.UGCBlob) {
	if len(b.Data) > 0 {
		w.Header().Set("Range", fmt.Sprintf("bytes=0-%d", len(b.Data)-1))
	}
	w.WriteHeader(http.StatusPermanentRedirect)
}
