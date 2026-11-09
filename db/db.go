// Package db opens the API's SQLite database and keeps its tables.
package db

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"

	_ "modernc.org/sqlite"
)

// Open opens the database at path, making its directory if need be, and creates the
// tables it lacks.
func Open(ctx context.Context, path string) (*sql.DB, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, fmt.Errorf("create data dir: %w", err)
	}
	db, err := sql.Open("sqlite", "file:"+path+"?_pragma=foreign_keys(1)&_pragma=busy_timeout(5000)")
	if err != nil {
		return nil, err
	}
	// One connection: SQLite has one writer, and a transaction keeps its connection.
	db.SetMaxOpenConns(1)
	if err := migrate(ctx, db); err != nil {
		db.Close()
		return nil, err
	}
	return db, nil
}

func migrate(ctx context.Context, db *sql.DB) error {
	for _, stmt := range []string{
		`CREATE TABLE IF NOT EXISTS profiles (
			user_id TEXT PRIMARY KEY NOT NULL,
			bio TEXT NOT NULL DEFAULT '',
			created_at TEXT NOT NULL,
			updated_at TEXT NOT NULL
		)`,
		// A project is a Plat5 node: its tasks are keyed by its id, and go when it's purged.
		// assignee_id is a member id in the project's tree, or NULL.
		`CREATE TABLE IF NOT EXISTS tasks (
			id TEXT PRIMARY KEY NOT NULL,
			project_id TEXT NOT NULL,
			title TEXT NOT NULL,
			status TEXT NOT NULL DEFAULT 'todo',
			assignee_id TEXT,
			created_by TEXT NOT NULL,
			created_at TEXT NOT NULL,
			updated_at TEXT NOT NULL
		)`,
		`CREATE INDEX IF NOT EXISTS tasks_project_id_idx ON tasks (project_id)`,
		// The last event acted on in identity's lifecycle log: one row, an opaque decimal
		// string. Kept with the data it cleans up, so a restore rewinds both together.
		`CREATE TABLE IF NOT EXISTS feed_cursors (
			id INTEGER PRIMARY KEY NOT NULL CHECK (id = 1),
			event_id TEXT NOT NULL
		)`,
	} {
		if _, err := db.ExecContext(ctx, stmt); err != nil {
			return fmt.Errorf("migrate: %w", err)
		}
	}
	return nil
}
