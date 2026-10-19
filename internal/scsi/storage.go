package scsi

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"
)

// ErrQuotaExceeded means writing this blob would exceed the configured quota.
var ErrQuotaExceeded = errors.New("scsi: storage quota exceeded")

// BlobStore holds the raw bytes of uploaded component files.
//
// Metadata (who owns which archive, its status, its digest) lives in the SQL
// store; this only ever sees an opaque key and a byte stream. It never
// inspects the content — GRID0+ has no way to decrypt a console's save
// data and no legitimate reason to look at it.
type BlobStore interface {
	// Put writes r under key, replacing any existing content, and returns the
	// number of bytes written. usedByOwner is the owner's current total usage
	// before this write, used to enforce PerOwnerQuota without a second pass
	// over the owner's blobs.
	Put(owner, key string, r io.Reader, usedByOwner int64) (int64, error)
	// Get opens the blob at key for reading. The caller must Close it.
	Get(key string) (io.ReadCloser, int64, error)
	// Delete removes the blob at key. Deleting a missing key is not an error —
	// the caller has already lost track of it either way.
	Delete(key string) error
}

// LocalDisk is a BlobStore backed by a directory on the same box GRID0+
// runs on: no new recurring cost, bounded by whatever disk the box has.
//
// Keys are opaque component-file ids (see db.ComponentFile.BlobKey), written
// as flat files directly under Dir. No directory sharding: a private
// deployment's total file count is nowhere near where that would matter, and
// it would only make the quota accounting in Put harder to reason about.
type LocalDisk struct {
	// Dir is the root directory. Must already exist.
	Dir string
	// PerOwnerQuota caps one owner's (one BAAS user's) total stored bytes
	// across every archive. Zero means unlimited -- the config validator
	// warns rather than refuses on zero, the same posture as other
	// unbounded-by-default settings in config/config.go, but an operator
	// should set this: a single console must not be able to fill the disk and
	// take the rest of GRID0+ down with it.
	PerOwnerQuota int64

	mu sync.Mutex
}

// NewLocalDisk returns a LocalDisk rooted at dir, creating it if missing.
func NewLocalDisk(dir string, perOwnerQuota int64) (*LocalDisk, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("scsi: create storage dir: %w", err)
	}
	return &LocalDisk{Dir: dir, PerOwnerQuota: perOwnerQuota}, nil
}

func (d *LocalDisk) path(key string) string {
	// filepath.Base strips any path separators a caller-controlled key might
	// contain, so a component file id can never be used to write outside Dir.
	return filepath.Join(d.Dir, filepath.Base(key))
}

func (d *LocalDisk) Put(_ /* owner, only used for the quota message */ string, key string, r io.Reader, usedByOwner int64) (int64, error) {
	// One writer at a time: two uploads racing past the quota check could
	// both pass it and together exceed the quota. A private deployment's
	// upload volume never makes this a real bottleneck.
	d.mu.Lock()
	defer d.mu.Unlock()

	tmp, err := os.CreateTemp(d.Dir, ".upload-*")
	if err != nil {
		return 0, fmt.Errorf("scsi: create temp file: %w", err)
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath) // no-op once the rename below succeeds

	var n int64
	if d.PerOwnerQuota > 0 {
		limit := d.PerOwnerQuota - usedByOwner
		if limit < 0 {
			limit = 0
		}
		// Read one byte past the limit so a blob that exactly fits does not
		// look like it overflowed.
		n, err = io.Copy(tmp, io.LimitReader(r, limit+1))
		if err == nil && n > limit {
			tmp.Close()
			return 0, ErrQuotaExceeded
		}
	} else {
		n, err = io.Copy(tmp, r)
	}
	if err != nil {
		tmp.Close()
		return 0, fmt.Errorf("scsi: write blob: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return 0, fmt.Errorf("scsi: close temp file: %w", err)
	}

	// Rename rather than write in place: a reader opening the final path
	// mid-upload (there is none today, but Get makes no such promise) must
	// never see a partial file.
	if err := os.Rename(tmpPath, d.path(key)); err != nil {
		return 0, fmt.Errorf("scsi: finalize blob: %w", err)
	}
	return n, nil
}

func (d *LocalDisk) Get(key string) (io.ReadCloser, int64, error) {
	f, err := os.Open(d.path(key))
	if err != nil {
		return nil, 0, fmt.Errorf("scsi: open blob: %w", err)
	}
	info, err := f.Stat()
	if err != nil {
		f.Close()
		return nil, 0, fmt.Errorf("scsi: stat blob: %w", err)
	}
	return f, info.Size(), nil
}

func (d *LocalDisk) Delete(key string) error {
	if err := os.Remove(d.path(key)); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("scsi: delete blob: %w", err)
	}
	return nil
}
