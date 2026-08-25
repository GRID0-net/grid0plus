package db

import (
	"context"
	"embed"
	"fmt"
	"io/fs"
	"sort"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5"
)

//go:embed migrations/*.sql
var migrationFS embed.FS

// Migration is one numbered schema change.
type Migration struct {
	Version int
	Name    string
	Up      string
}

// Migrations returns the embedded up-migrations in version order.
//
// The files use golang-migrate's naming (NNNN_name.up.sql), so an operator who
// prefers the migrate CLI can point it at internal/db/migrations and get the
// same result. The runner below exists so that a plain `docker compose up`
// needs no extra tool.
func Migrations() ([]Migration, error) {
	entries, err := fs.Glob(migrationFS, "migrations/*.up.sql")
	if err != nil {
		return nil, err
	}
	out := make([]Migration, 0, len(entries))
	for _, path := range entries {
		base := strings.TrimSuffix(strings.TrimPrefix(path, "migrations/"), ".up.sql")
		idx := strings.Index(base, "_")
		if idx <= 0 {
			return nil, fmt.Errorf("migration %s: expected NNNN_name.up.sql", path)
		}
		version, err := strconv.Atoi(base[:idx])
		if err != nil {
			return nil, fmt.Errorf("migration %s: bad version: %w", path, err)
		}
		body, err := migrationFS.ReadFile(path)
		if err != nil {
			return nil, err
		}
		out = append(out, Migration{Version: version, Name: base[idx+1:], Up: string(body)})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Version < out[j].Version })

	for i, m := range out {
		if m.Version != i+1 {
			return nil, fmt.Errorf("migration versions must be contiguous from 1: found %d at position %d", m.Version, i+1)
		}
	}
	return out, nil
}

// Migrate applies every pending migration.
//
// Each migration runs in its own transaction, together with the row recording
// it, so a failure part-way leaves the schema at the last complete version
// rather than half-applied. An advisory lock keeps two processes starting at
// once from racing — which is exactly what happens when docker-compose brings
// five services up together.
func Migrate(ctx context.Context, conn *pgx.Conn) (applied int, err error) {
	migrations, err := Migrations()
	if err != nil {
		return 0, err
	}

	// A fixed, arbitrary key. Any other SwitchNet process migrating at the same
	// moment waits here instead of both trying to create the same table.
	const lockKey = 0x5734c4e7
	if _, err := conn.Exec(ctx, "SELECT pg_advisory_lock($1)", lockKey); err != nil {
		return 0, fmt.Errorf("acquire migration lock: %w", err)
	}
	defer func() {
		if _, uerr := conn.Exec(ctx, "SELECT pg_advisory_unlock($1)", lockKey); uerr != nil && err == nil {
			err = fmt.Errorf("release migration lock: %w", uerr)
		}
	}()

	_, err = conn.Exec(ctx, `
		CREATE TABLE IF NOT EXISTS schema_migrations (
			version     INTEGER PRIMARY KEY,
			name        TEXT        NOT NULL,
			applied_at  TIMESTAMPTZ NOT NULL DEFAULT now()
		)`)
	if err != nil {
		return 0, fmt.Errorf("create schema_migrations: %w", err)
	}

	var current int
	if err := conn.QueryRow(ctx,
		"SELECT coalesce(max(version), 0) FROM schema_migrations").Scan(&current); err != nil {
		return 0, fmt.Errorf("read schema version: %w", err)
	}

	for _, m := range migrations {
		if m.Version <= current {
			continue
		}
		tx, err := conn.Begin(ctx)
		if err != nil {
			return applied, fmt.Errorf("begin migration %d: %w", m.Version, err)
		}
		if _, err := tx.Exec(ctx, m.Up); err != nil {
			_ = tx.Rollback(ctx)
			return applied, fmt.Errorf("apply migration %04d_%s: %w", m.Version, m.Name, err)
		}
		if _, err := tx.Exec(ctx,
			"INSERT INTO schema_migrations (version, name) VALUES ($1, $2)", m.Version, m.Name); err != nil {
			_ = tx.Rollback(ctx)
			return applied, fmt.Errorf("record migration %d: %w", m.Version, err)
		}
		if err := tx.Commit(ctx); err != nil {
			return applied, fmt.Errorf("commit migration %d: %w", m.Version, err)
		}
		applied++
	}
	return applied, nil
}
