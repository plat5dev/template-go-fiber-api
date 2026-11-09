package db

import (
	"context"
	"path/filepath"
	"testing"
)

// feed_cursors holds one cursor, and opening the database again keeps it.
func TestOpenKeepsOneCursor(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "data", "app.db")
	sqlDB, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := sqlDB.Exec(`INSERT INTO feed_cursors (id, event_id) VALUES (1, '813')`); err != nil {
		t.Fatal(err)
	}
	if _, err := sqlDB.Exec(`INSERT INTO feed_cursors (id, event_id) VALUES (2, '814')`); err == nil {
		t.Fatal("a second cursor was taken")
	}
	sqlDB.Close()

	sqlDB, err = Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer sqlDB.Close()
	var cur string
	if err := sqlDB.QueryRow(`SELECT event_id FROM feed_cursors WHERE id = 1`).Scan(&cur); err != nil || cur != "813" {
		t.Fatalf("cursor %q, %v", cur, err)
	}
}
