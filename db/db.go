package db

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"time"

	_ "modernc.org/sqlite"
)

func Connect(ctx context.Context) (*sql.DB, error) {
	path := os.Getenv("DATABASE_PATH")
	if path == "" {
		path = "./data/app.db"
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, fmt.Errorf("create data dir: %w", err)
	}

	// modernc.org/sqlite driver name is "sqlite"
	dsn := fmt.Sprintf("file:%s?_pragma=foreign_keys(1)&_pragma=busy_timeout(5000)", path)
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	db.SetConnMaxLifetime(0)

	pingCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if err := db.PingContext(pingCtx); err != nil {
		_ = db.Close()
		return nil, err
	}
	return db, nil
}

func Migrate(ctx context.Context, db *sql.DB) error {
	stmts := []string{
		`CREATE TABLE IF NOT EXISTS profiles (
			user_id TEXT PRIMARY KEY NOT NULL,
			bio TEXT NOT NULL DEFAULT '',
			created_at TEXT NOT NULL,
			updated_at TEXT NOT NULL
		)`,
		// A project is a Plat5 node: its tasks are keyed by its id, and go when it's purged.
		`CREATE TABLE IF NOT EXISTS tasks (
			id TEXT PRIMARY KEY NOT NULL,
			project_id TEXT NOT NULL,
			title TEXT NOT NULL,
			status TEXT NOT NULL DEFAULT 'todo',
			created_by TEXT NOT NULL,
			created_at TEXT NOT NULL,
			updated_at TEXT NOT NULL
		)`,
		`CREATE INDEX IF NOT EXISTS tasks_project_id_idx ON tasks (project_id)`,
		// The last lifecycle event acted on: one row, an opaque decimal string.
		`CREATE TABLE IF NOT EXISTS lifecycle_cursor (
			id INTEGER PRIMARY KEY CHECK (id = 1),
			event_id TEXT NOT NULL
		)`,
	}
	for _, s := range stmts {
		if _, err := db.ExecContext(ctx, s); err != nil {
			return fmt.Errorf("migrate: %w", err)
		}
	}
	return nil
}
