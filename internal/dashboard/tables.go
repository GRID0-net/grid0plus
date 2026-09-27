package dashboard

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/n-popescu/grid0plus/internal/db"
)

// ---- the live table browser/editor ----------------------------------------
//
// A generic, spreadsheet-shaped view over every table BAAS and NPLN read
// from directly -- the same store the rest of this Debug tab already acts
// on -- extended so an operator can see every row of every table, not just
// the curated account lookup above, and edit the fields that are safe to
// edit outside the app's own validation.
//
// Deliberately Postgres-only. internal/db.Memory exists for the handler test
// suite, which is what makes it fast and dependency-free (CLAUDE.md: no
// database needed, tests finish in seconds) -- it is not a deployment
// target, and reflecting a generic row/column browser onto it would mean
// building a second, parallel implementation of this whole file for a
// backend nothing actually runs against. handleDebugTables reports honestly
// (its own "available" field) when the configured store is not Postgres,
// the same "say so rather than invent a fallback" rule the rest of this
// codebase follows, rather than silently returning nothing.
//
// Every SELECT casts every column to text (`col::text`) and every UPDATE
// casts the submitted value back with the column's own Postgres type
// (`$1::jsonb`, `$1::text[]`, ...): the wire shape is always a string, in
// exactly the textual form Postgres itself uses for that type, so there is
// no Go-side reconstruction of pgx's driver-level array/jsonb representation
// to get subtly wrong. An edit that is not valid syntax for the column's
// type fails with Postgres's own error message, which is also why 'state'
// and other CHECK-constrained columns need no separate validation here: the
// database is already the source of truth for what is valid.
//
// Never selected or editable, anywhere: primary key columns (editing an
// identity column is a rename, a different operation this tool does not
// attempt), columns that reference another table's primary key even where
// no formal FOREIGN KEY exists (npln_matches.host_user_id/player_ids,
// npln_fest_entries.fest_id/user_id -- identity-shaped the same way), and
// the two password hash columns, which this dashboard already treats as
// never worth exposing (see debugDeviceAccount's own comment). A handful of
// columns whose precise value another system depends on byte-for-byte stay
// read-only too: npln_fests.resource/revision/pack_keys (the console derives
// a decryption key and an AES IV from these; see internal/bcat), and
// images' metadata columns (they describe bytes stored on disk that this
// tool cannot also update, so editing the row alone would desync the two).
// schema_migrations is not in this registry at all: it is migration
// tooling's own bookkeeping, not application data, and a bad edit there
// risks the database refusing to start.

// columnKind tells the frontend which editor widget to render, and is also
// exactly the Postgres cast this column's UPDATE uses.
type columnKind string

const (
	kindText      columnKind = "text"
	kindBool      columnKind = "boolean"
	kindInt       columnKind = "integer"
	kindBigint    columnKind = "bigint"
	kindTimestamp columnKind = "timestamptz"
	kindJSON      columnKind = "jsonb"
	kindTextArray columnKind = "text[]"
	kindIntArray  columnKind = "bigint[]"
)

type debugColumn struct {
	Name     string     `json:"name"`
	Kind     columnKind `json:"kind"`
	PK       bool       `json:"pk"`
	Editable bool       `json:"editable"`
	Nullable bool       `json:"nullable"`
}

type debugTableDef struct {
	Table   string        `json:"table"`
	Columns []debugColumn `json:"columns"`
}

func col(name string, kind columnKind) debugColumn { return debugColumn{Name: name, Kind: kind} }
func pk(name string, kind columnKind) debugColumn {
	return debugColumn{Name: name, Kind: kind, PK: true}
}
func edit(name string, kind columnKind) debugColumn {
	return debugColumn{Name: name, Kind: kind, Editable: true}
}
func editNull(name string, kind columnKind) debugColumn {
	return debugColumn{Name: name, Kind: kind, Editable: true, Nullable: true}
}
func viewNull(name string, kind columnKind) debugColumn {
	return debugColumn{Name: name, Kind: kind, Nullable: true}
}

// debugTables is the registry driving both /api/debug/tables endpoints and
// this file's own SQL. Order here is the order tabs render in.
var debugTables = []debugTableDef{
	{Table: "users", Columns: []debugColumn{
		pk("id", kindText),
		edit("nickname", kindText), edit("country", kindText), edit("birthday", kindText),
		edit("thumbnail_url", kindText), edit("thumbnail2_url", kindText),
		editNull("thumbnail_uploaded_at", kindTimestamp),
		edit("permissions", kindJSON), edit("extras", kindJSON),
		edit("deleted", kindBool),
		edit("blocks_updated_at", kindTimestamp), edit("friends_updated_at", kindTimestamp),
		col("created_at", kindTimestamp), col("updated_at", kindTimestamp),
		editNull("login_username", kindText),
		// login_password_hash: never selected, see package comment above.
	}},
	{Table: "device_accounts", Columns: []debugColumn{
		pk("id", kindText),
		col("user_id", kindText),
		// password_sha256: never selected, see package comment above.
		viewNull("device_id", kindText),
		col("created_at", kindTimestamp), col("updated_at", kindTimestamp),
		editNull("deleted_at", kindTimestamp), editNull("tokens_revoked_before", kindTimestamp),
	}},
	{Table: "devices", Columns: []debugColumn{
		pk("id", kindText),
		edit("serial_number", kindText), edit("product_code", kindText), edit("device_type", kindText),
		edit("ist", kindBool), edit("system_version", kindText),
		edit("first_seen_at", kindTimestamp), edit("last_seen_at", kindTimestamp),
	}},
	{Table: "friend_codes", Columns: []debugColumn{
		pk("user_id", kindText),
		edit("code", kindText),
		col("created_at", kindTimestamp), col("updated_at", kindTimestamp),
		edit("regenerable_at", kindTimestamp),
	}},
	{Table: "friend_requests", Columns: []debugColumn{
		pk("id", kindText),
		col("from_user_id", kindText), col("to_user_id", kindText),
		edit("state", kindText),
		col("created_at", kindTimestamp), col("updated_at", kindTimestamp),
	}},
	{Table: "friendships", Columns: []debugColumn{
		pk("user_id", kindText), pk("friend_id", kindText),
		edit("favorite", kindBool), edit("became_friend_at", kindTimestamp),
	}},
	{Table: "blocks", Columns: []debugColumn{
		pk("user_id", kindText), pk("blocked_user_id", kindText),
		col("created_at", kindTimestamp),
	}},
	{Table: "presence", Columns: []debugColumn{
		pk("user_id", kindText),
		edit("state", kindText),
		editNull("app_id", kindText), editNull("presence_group_id", kindText),
		edit("app_field", kindText), edit("acd_index", kindInt),
		edit("extras", kindJSON),
		editNull("reported_at", kindTimestamp), edit("last_seen_at", kindTimestamp),
		editNull("logout_at", kindTimestamp),
	}},
	{Table: "images", Columns: []debugColumn{
		pk("id", kindText),
		col("owner_id", kindText),
		col("format", kindText), col("width", kindInt), col("height", kindInt), col("size_bytes", kindInt),
		col("rel_path", kindText),
		col("created_at", kindTimestamp), col("updated_at", kindTimestamp),
	}},
	{Table: "npln_fests", Columns: []debugColumn{
		pk("id", kindText),
		col("resource", kindText), col("revision", kindText),
		col("start_at", kindTimestamp),
		edit("regions", kindTextArray),
		col("pack_keys", kindTextArray),
		edit("enabled", kindBool),
		col("created_at", kindTimestamp), col("updated_at", kindTimestamp),
	}},
	{Table: "npln_fest_entries", Columns: []debugColumn{
		pk("fest_id", kindText), pk("user_id", kindText),
		edit("fest_team", kindText), edit("fest_region", kindText),
		col("created_at", kindTimestamp),
	}},
	{Table: "npln_matches", Columns: []debugColumn{
		pk("id", kindText),
		edit("tenant", kindText), edit("mode", kindText),
		col("host_user_id", kindText), col("player_ids", kindTextArray),
		edit("forced", kindBool),
		col("created_at", kindTimestamp),
	}},
	{Table: "npln_coop_shifts", Columns: []debugColumn{
		pk("id", kindText),
		col("kind", kindText),
		edit("stage_id", kindInt), edit("boss", kindText),
		edit("weapons", kindIntArray), edit("kuma_weapon_id", kindBigint),
		edit("reward_gear_id", kindInt), edit("rewards", kindIntArray),
		edit("start_at", kindTimestamp), edit("end_at", kindTimestamp),
		col("created_at", kindTimestamp), col("updated_at", kindTimestamp),
	}},
}

func findDebugTable(name string) (debugTableDef, bool) {
	for _, t := range debugTables {
		if t.Table == name {
			return t, true
		}
	}
	return debugTableDef{}, false
}

// postgres returns the raw connection pool when the configured store is
// Postgres, and false otherwise (the in-memory store used by tests, or any
// future Store implementation this file does not know about).
func (s *Server) postgres() (*pgxpool.Pool, bool) {
	pg, ok := s.store.(*db.Postgres)
	if !ok {
		return nil, false
	}
	return pg.Pool(), true
}

// ---- HTTP -------------------------------------------------------------

// handleDebugTables lists the registry itself, so the frontend can build its
// table picker and column headers without duplicating this file's schema
// knowledge.
func (s *Server) handleDebugTables(w http.ResponseWriter, r *http.Request) {
	_, ok := s.postgres()
	writeJSON(w, map[string]any{
		"available": ok,
		"tables":    debugTables,
	})
}

// handleDebugTableRows answers one table's full contents, every column cast
// to text -- see the package comment for why.
func (s *Server) handleDebugTableRows(w http.ResponseWriter, r *http.Request) {
	table := r.PathValue("table")
	def, ok := findDebugTable(table)
	if !ok {
		http.Error(w, "no such table", http.StatusNotFound)
		return
	}
	pool, ok := s.postgres()
	if !ok {
		http.Error(w, "the live table browser needs a Postgres store; this instance is running the in-memory one", http.StatusServiceUnavailable)
		return
	}

	names := make([]string, len(def.Columns))
	pkOrder := make([]string, 0, 2)
	for i, c := range def.Columns {
		names[i] = fmt.Sprintf("%s::text AS %s", quoteIdent(c.Name), quoteIdent(c.Name))
		if c.PK {
			pkOrder = append(pkOrder, quoteIdent(c.Name))
		}
	}
	query := fmt.Sprintf("SELECT %s FROM %s ORDER BY %s",
		strings.Join(names, ", "), quoteIdent(def.Table), strings.Join(pkOrder, ", "))

	rows, err := pool.Query(r.Context(), query)
	if err != nil {
		s.fail(w, "debug: list table "+table, err)
		return
	}
	defer rows.Close()

	out := make([][]*string, 0, 64)
	for rows.Next() {
		vals := make([]any, len(def.Columns))
		ptrs := make([]*string, len(def.Columns))
		for i := range vals {
			ptrs[i] = new(string)
			vals[i] = &sqlNullString{ptr: ptrs[i]}
		}
		if err := rows.Scan(vals...); err != nil {
			s.fail(w, "debug: scan table "+table, err)
			return
		}
		out = append(out, ptrs)
	}
	if err := rows.Err(); err != nil {
		s.fail(w, "debug: read table "+table, err)
		return
	}

	writeJSON(w, map[string]any{"table": table, "rows": out})
}

// sqlNullString scans a possibly-NULL text column into a *string that stays
// nil on NULL, rather than pgx's default of erroring when scanning NULL into
// a plain *string.
type sqlNullString struct{ ptr *string }

func (n *sqlNullString) Scan(src any) error {
	if src == nil {
		*n.ptr = ""
		return nil
	}
	switch v := src.(type) {
	case string:
		*n.ptr = v
	case []byte:
		*n.ptr = string(v)
	default:
		*n.ptr = fmt.Sprint(v)
	}
	return nil
}

// handleDebugUpdateRow patches the editable columns of one row, identified
// by the table's full primary key.
func (s *Server) handleDebugUpdateRow(w http.ResponseWriter, r *http.Request) {
	table := r.PathValue("table")
	def, ok := findDebugTable(table)
	if !ok {
		http.Error(w, "no such table", http.StatusNotFound)
		return
	}
	pool, ok := s.postgres()
	if !ok {
		http.Error(w, "the live table browser needs a Postgres store; this instance is running the in-memory one", http.StatusServiceUnavailable)
		return
	}

	var body struct {
		PK     map[string]string  `json:"pk"`
		Fields map[string]*string `json:"fields"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}

	editable := map[string]debugColumn{}
	pkCols := map[string]debugColumn{}
	for _, c := range def.Columns {
		if c.PK {
			pkCols[c.Name] = c
		}
		if c.Editable {
			editable[c.Name] = c
		}
	}
	if len(body.PK) != len(pkCols) {
		http.Error(w, "the primary key of the row to update is required", http.StatusBadRequest)
		return
	}
	for name := range body.PK {
		if _, ok := pkCols[name]; !ok {
			http.Error(w, "unknown primary key column: "+name, http.StatusBadRequest)
			return
		}
	}
	if len(body.Fields) == 0 {
		http.Error(w, "nothing to update", http.StatusBadRequest)
		return
	}

	setClauses := make([]string, 0, len(body.Fields))
	args := make([]any, 0, len(body.Fields)+len(body.PK))
	n := 1
	for name, val := range body.Fields {
		c, ok := editable[name]
		if !ok {
			http.Error(w, "not an editable column: "+name, http.StatusBadRequest)
			return
		}
		if val == nil {
			if !c.Nullable {
				http.Error(w, name+" is not nullable", http.StatusBadRequest)
				return
			}
			setClauses = append(setClauses, fmt.Sprintf("%s = NULL", quoteIdent(name)))
			continue
		}
		setClauses = append(setClauses, fmt.Sprintf("%s = $%d::%s", quoteIdent(name), n, c.Kind))
		args = append(args, *val)
		n++
	}
	// updated_at exists on most of these tables and nowhere gets a direct
	// editor of its own (see the registry above) -- bump it here, the same
	// thing every real Store.Update* method already does on a write, so a
	// raw edit through this tool does not leave a stale value a console's
	// own cache-invalidation logic reads.
	hasUpdatedAt := false
	for _, c := range def.Columns {
		if c.Name == "updated_at" {
			hasUpdatedAt = true
			break
		}
	}
	if hasUpdatedAt {
		setClauses = append(setClauses, "updated_at = now()")
	}

	whereClauses := make([]string, 0, len(body.PK))
	for name, val := range body.PK {
		whereClauses = append(whereClauses, fmt.Sprintf("%s = $%d::%s", quoteIdent(name), n, pkCols[name].Kind))
		args = append(args, val)
		n++
	}

	query := fmt.Sprintf("UPDATE %s SET %s WHERE %s",
		quoteIdent(def.Table), strings.Join(setClauses, ", "), strings.Join(whereClauses, " AND "))

	tag, err := pool.Exec(r.Context(), query, args...)
	if err != nil {
		// A CHECK constraint (presence.state, friend_requests.state, ...) or a
		// malformed value for the column's type answers here in Postgres's own
		// words -- exactly the source of truth this file's package comment
		// says it relies on instead of duplicating validation.
		http.Error(w, err.Error(), http.StatusUnprocessableEntity)
		return
	}
	if tag.RowsAffected() == 0 {
		http.Error(w, "no row matched that primary key", http.StatusNotFound)
		return
	}

	s.log.Info("dashboard: debug table row updated", "table", table, "pk", body.PK, "fields", fieldNames(body.Fields))
	writeJSON(w, map[string]any{"table": table, "updated": true})
}

func fieldNames(m map[string]*string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

// quoteIdent double-quotes a Postgres identifier. Safe here specifically
// because every identifier this file ever formats with it comes from
// debugTables -- a fixed, source-controlled registry -- never from a
// request; findDebugTable and the editable/pkCols lookups above are what
// keep a request-supplied name from ever reaching a query as anything but a
// parameter value.
func quoteIdent(name string) string {
	return `"` + strings.ReplaceAll(name, `"`, `""`) + `"`
}
