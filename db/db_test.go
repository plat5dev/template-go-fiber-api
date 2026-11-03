package db

import (
	"context"
	"path/filepath"
	"testing"
)

// feed_cursors holds one cursor, and migrating again keeps it.
func TestMigrateKeepsOneCursor(t *testing.T) {
	ctx := context.Background()
	t.Setenv("DATABASE_PATH", filepath.Join(t.TempDir(), "app.db"))
	sqlDB, err := Connect(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer sqlDB.Close()
	if err := Migrate(ctx, sqlDB); err != nil {
		t.Fatal(err)
	}
	if _, err := sqlDB.Exec(`INSERT INTO feed_cursors (id, event_id) VALUES (1, '813')`); err != nil {
		t.Fatal(err)
	}
	if _, err := sqlDB.Exec(`INSERT INTO feed_cursors (id, event_id) VALUES (2, '814')`); err == nil {
		t.Fatal("a second cursor was taken")
	}
	// Migrating again keeps the cursor.
	if err := Migrate(ctx, sqlDB); err != nil {
		t.Fatal(err)
	}
	var cur string
	if err := sqlDB.QueryRow(`SELECT event_id FROM feed_cursors WHERE id = 1`).Scan(&cur); err != nil || cur != "813" {
		t.Fatalf("cursor %q, %v", cur, err)
	}
}
