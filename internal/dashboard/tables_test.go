package dashboard

import (
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/n-popescu/grid0plus/internal/db"
)

// The live table browser (tables.go) is Postgres-only by design -- see its
// own package comment for why building a second implementation against
// internal/db.Memory is not worth it. Every test in this file needs a real
// database and is skipped without one, the same convention
// internal/db/store_test.go's own PostgreSQL contract test uses.
func newPostgresTestServer(t *testing.T) (*Server, *db.Postgres) {
	t.Helper()
	url := os.Getenv("SWITCHNET_TEST_DATABASE_URL")
	if url == "" {
		t.Skip("SWITCHNET_TEST_DATABASE_URL is not set; skipping the live table browser's Postgres-only tests")
	}
	store, err := db.OpenPostgres(t.Context(), url, 4, true)
	if err != nil {
		t.Fatalf("open postgres: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	srv, err := New(Options{
		Store:       store,
		Log:         slog.New(slog.NewTextHandler(io.Discard, nil)),
		Tenant:      "dce9377b",
		PresenceTTL: 5 * time.Minute,
		Now:         time.Now,
	})
	if err != nil {
		t.Fatalf("new: %v", err)
	}
	return srv, store
}

func TestDebugTablesReportsAvailableAgainstPostgres(t *testing.T) {
	srv, _ := newPostgresTestServer(t)

	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, httptest.NewRequest("GET", "/api/debug/tables", nil))
	if rec.Code != 200 {
		t.Fatalf("status %d: %s", rec.Code, rec.Body)
	}

	var body struct {
		Available bool            `json:"available"`
		Tables    []debugTableDef `json:"tables"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !body.Available {
		t.Fatal("available = false against a real Postgres store")
	}
	if len(body.Tables) != len(debugTables) {
		t.Errorf("got %d tables, want %d", len(body.Tables), len(debugTables))
	}
}

func TestDebugTablesUnavailableAgainstMemoryStore(t *testing.T) {
	srv, _ := newTestServer(t, "", time.Now())

	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, httptest.NewRequest("GET", "/api/debug/tables", nil))
	if rec.Code != 200 {
		t.Fatalf("status %d: %s", rec.Code, rec.Body)
	}
	var body struct {
		Available bool `json:"available"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body.Available {
		t.Error("available = true against the in-memory store, want false -- this file's own package comment says why it must not pretend to work there")
	}

	rowsRec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rowsRec, httptest.NewRequest("GET", "/api/debug/tables/users", nil))
	if rowsRec.Code != 503 {
		t.Errorf("GET rows against the in-memory store = %d, want 503", rowsRec.Code)
	}
}

// TestDebugTablesRegistryNeverIncludesSensitiveColumns needs no database:
// login_password_hash and password_sha256 must never be selected or
// editable, on any table -- see tables.go's package comment on why they are
// excluded from the registry entirely, not just masked. A registry
// omission, not a runtime redaction, is what this checks.
func TestDebugTablesRegistryNeverIncludesSensitiveColumns(t *testing.T) {
	for _, def := range debugTables {
		for _, c := range def.Columns {
			if c.Name == "login_password_hash" || c.Name == "password_sha256" {
				t.Errorf("%s.%s: a sensitive column is present in the registry", def.Table, c.Name)
			}
		}
	}
}

// Tests below use npln_matches, not users/presence: internal/db's own
// TestPostgresStore truncates users (CASCADE to device_accounts,
// friendships, presence, ...) many times over its run, and go test runs
// different packages' test binaries concurrently by default -- against the
// same real database, that can truncate a row out from under this package's
// own test mid-flight. npln_matches has no foreign key to users (see its
// migration's own comment) and is not in internal/db's truncateAll list, so
// it is not exposed to that race. The generic handler code being tested
// (handleDebugTableRows/handleDebugUpdateRow) does not care which table it
// is pointed at, so this loses nothing but the specific column types.
func createTestMatch(t *testing.T, store *db.Postgres, mode string) string {
	t.Helper()
	id := fmt.Sprintf("test-match-%d-%d", time.Now().UnixNano(), os.Getpid())
	if err := store.CreateMatch(t.Context(), db.Match{
		ID: id, Tenant: "dce9377b", Mode: mode,
		HostUserID: "0000000000000000", PlayerIDs: []string{"0000000000000000"},
		CreatedAt: time.Now(),
	}); err != nil {
		t.Fatalf("create match: %v", err)
	}
	return id
}

func TestDebugTableRowsReturnsRealSeededData(t *testing.T) {
	srv, store := newPostgresTestServer(t)
	id := createTestMatch(t, store, "test-mode-zora")

	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, httptest.NewRequest("GET", "/api/debug/tables/npln_matches", nil))
	if rec.Code != 200 {
		t.Fatalf("status %d: %s", rec.Code, rec.Body)
	}
	var body struct {
		Rows [][]*string `json:"rows"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}

	def, _ := findDebugTable("npln_matches")
	idIdx, modeIdx := -1, -1
	for i, c := range def.Columns {
		switch c.Name {
		case "id":
			idIdx = i
		case "mode":
			modeIdx = i
		}
	}

	found := false
	for _, row := range body.Rows {
		if row[idIdx] != nil && *row[idIdx] == id {
			found = true
			if row[modeIdx] == nil || *row[modeIdx] != "test-mode-zora" {
				t.Errorf("mode = %v, want test-mode-zora", row[modeIdx])
			}
		}
	}
	if !found {
		t.Fatalf("seeded match %s not found in the rows returned for npln_matches", id)
	}
}

func TestDebugUpdateRowEditsAnEditableColumn(t *testing.T) {
	srv, store := newPostgresTestServer(t)
	id := createTestMatch(t, store, "before")

	body, _ := json.Marshal(map[string]any{
		"pk":     map[string]string{"id": id},
		"fields": map[string]any{"mode": "after"},
	})
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("PATCH", "/api/debug/tables/npln_matches", strings.NewReader(string(body)))
	srv.Handler().ServeHTTP(rec, req)
	if rec.Code != 200 {
		t.Fatalf("status %d: %s", rec.Code, rec.Body)
	}

	matches, err := store.ListMatches(t.Context(), 200)
	if err != nil {
		t.Fatalf("list matches: %v", err)
	}
	found := false
	for _, m := range matches {
		if m.ID == id {
			found = true
			if m.Mode != "after" {
				t.Errorf("mode = %q, want after", m.Mode)
			}
		}
	}
	if !found {
		t.Fatalf("match %s not found after update", id)
	}
}

// TestDebugUpdateRowRefusesANonEditableColumn covers the safety property the
// whole registry exists to enforce: even a syntactically well-formed request
// naming a real column cannot touch a primary key or a column tables.go's
// registry marked read-only -- host_user_id is identity-shaped the same way
// a foreign key would be, per the registry's own comment, even though this
// particular table has no formal FOREIGN KEY constraint.
func TestDebugUpdateRowRefusesANonEditableColumn(t *testing.T) {
	srv, store := newPostgresTestServer(t)
	id := createTestMatch(t, store, "original")

	body, _ := json.Marshal(map[string]any{
		"pk":     map[string]string{"id": id},
		"fields": map[string]any{"host_user_id": "1111111111111111"},
	})
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("PATCH", "/api/debug/tables/npln_matches", strings.NewReader(string(body)))
	srv.Handler().ServeHTTP(rec, req)
	if rec.Code != 400 {
		t.Fatalf("status %d: %s, want 400", rec.Code, rec.Body)
	}

	matches, err := store.ListMatches(t.Context(), 200)
	if err != nil {
		t.Fatalf("list matches: %v", err)
	}
	for _, m := range matches {
		if m.ID == id && m.HostUserID != "0000000000000000" {
			t.Errorf("host_user_id = %q, want it unchanged", m.HostUserID)
		}
	}
}

// TestDebugUpdateRowSurfacesAPostgresOwnError covers this file's central
// safety property: a value that is not valid syntax for the column's own
// Postgres type answers with Postgres's own error, because that is the
// source of truth this file relies on instead of duplicating validation --
// see tables.go's package comment. 'forced' is boolean; nothing but a real
// boolean literal parses.
func TestDebugUpdateRowSurfacesAPostgresOwnError(t *testing.T) {
	srv, store := newPostgresTestServer(t)
	id := createTestMatch(t, store, "type-error")

	body, _ := json.Marshal(map[string]any{
		"pk":     map[string]string{"id": id},
		"fields": map[string]any{"forced": "not-a-boolean"},
	})
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("PATCH", "/api/debug/tables/npln_matches", strings.NewReader(string(body)))
	srv.Handler().ServeHTTP(rec, req)
	if rec.Code != 422 {
		t.Fatalf("status %d: %s, want 422", rec.Code, rec.Body)
	}
	if !strings.Contains(rec.Body.String(), "boolean") {
		t.Errorf("body = %q, want it to name the type Postgres refused", rec.Body.String())
	}
}

func TestDebugUpdateRowRejectsAnUnknownPrimaryKey(t *testing.T) {
	srv, _ := newPostgresTestServer(t)

	body, _ := json.Marshal(map[string]any{
		"pk":     map[string]string{"id": "no-such-match"},
		"fields": map[string]any{"mode": "nobody"},
	})
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("PATCH", "/api/debug/tables/npln_matches", strings.NewReader(string(body)))
	srv.Handler().ServeHTTP(rec, req)
	if rec.Code != 404 {
		t.Fatalf("status %d: %s, want 404", rec.Code, rec.Body)
	}
}

// TestEveryDebugTableCanBeRead: the registry names columns by hand, and a
// name that is not in the schema only fails when an operator opens that
// table -- npln_fest_entries listed fest_team/fest_region for weeks while
// the columns are team/region. Read every table against the real schema.
func TestEveryDebugTableCanBeRead(t *testing.T) {
	srv, _ := newPostgresTestServer(t)
	for _, def := range debugTables {
		rec := httptest.NewRecorder()
		srv.Handler().ServeHTTP(rec, httptest.NewRequest("GET", "/api/debug/tables/"+def.Table, nil))
		if rec.Code != 200 {
			t.Errorf("%s: status %d: %s", def.Table, rec.Code, rec.Body)
		}
	}
}
