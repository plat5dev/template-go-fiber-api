package lifecycle

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync"
	"testing"

	"github.com/rs/zerolog"

	"github.com/example/go-fiber-api/db"
	"github.com/example/go-fiber-api/pagination"
	"github.com/example/go-fiber-api/tasks"
)

const (
	purged = "01jd8x3k7q0000000000000001"
	kept   = "01jd8x3k7q0000000000000002"
)

// feed serves events two to a page, from starting_after, and records each cursor it's
// asked for.
type feed struct {
	mu     sync.Mutex
	events []Event
	asked  []string
	fail   bool
}

func (f *feed) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if r.URL.Path != "/internal/lifecycle-events" {
		http.NotFound(w, r)
		return
	}
	if f.fail {
		w.WriteHeader(http.StatusServiceUnavailable)
		return
	}
	after := r.URL.Query().Get("starting_after")
	f.asked = append(f.asked, after)
	i := 0
	if after != "" {
		for i < len(f.events) && f.events[i].ID != after {
			i++
		}
		i++
	}
	end := min(i+2, len(f.events))
	if i > end {
		i = end
	}
	_ = json.NewEncoder(w).Encode(map[string]any{
		"lifecycle_events": f.events[i:end],
		"has_more":         end < len(f.events),
	})
}

func setup(t *testing.T, f *feed) (*Consumer, *tasks.Store) {
	t.Helper()
	t.Setenv("DATABASE_PATH", filepath.Join(t.TempDir(), "app.db"))
	sqlDB, err := db.Connect(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { sqlDB.Close() })
	if err := db.Migrate(context.Background(), sqlDB); err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(f)
	t.Cleanup(srv.Close)
	store := tasks.NewStore(sqlDB)
	return New(srv.URL, sqlDB, store, zerolog.Nop()), store
}

func addTask(t *testing.T, store *tasks.Store, id, projectID string) {
	t.Helper()
	err := store.Insert(context.Background(), &tasks.Task{
		ID: id, ProjectID: projectID, Title: "t", Status: "todo",
		CreatedBy: "01jd8x3k7q00000000000000mm", CreatedAt: "now", UpdatedAt: "now",
	})
	if err != nil {
		t.Fatal(err)
	}
}

func count(t *testing.T, store *tasks.Store, projectID string) int {
	t.Helper()
	list, _, err := store.ListByProject(context.Background(), projectID, pagination.Page{Limit: 100})
	if err != nil {
		t.Fatal(err)
	}
	return len(list)
}

func TestPollDeletesPurgedProjectsTasks(t *testing.T) {
	f := &feed{events: []Event{
		{ID: "7", Type: "node.created", Kind: "project", NodeID: purged},
		{ID: "8", Type: "node.deleted", Kind: "project", NodeID: kept},
		{ID: "9", Type: "member.removed"},
		{ID: "10", Type: "node.purged", Kind: "organization", NodeID: kept},
		{ID: "11", Type: "node.purged", Kind: "project", NodeID: purged},
	}}
	c, store := setup(t, f)
	addTask(t, store, "01jd8x3k7q0000000000000t01", purged)
	addTask(t, store, "01jd8x3k7q0000000000000t02", kept)

	if err := c.Poll(context.Background()); err != nil {
		t.Fatal(err)
	}
	if n := count(t, store, purged); n != 0 {
		t.Fatalf("purged project has %d tasks", n)
	}
	if n := count(t, store, kept); n != 1 {
		t.Fatalf("other project has %d tasks, want 1", n)
	}
	// Three pages in one poll: has_more reads again at once, from each page's last id.
	if got := f.asked; len(got) != 3 || got[0] != "" || got[1] != "8" || got[2] != "10" {
		t.Fatalf("asked = %q", got)
	}
	if cur, err := c.cursor(context.Background()); err != nil || cur != "11" {
		t.Fatalf("cursor = %q, %v", cur, err)
	}

	// The next poll starts from the saved cursor.
	if err := c.Poll(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := f.asked[len(f.asked)-1]; got != "11" {
		t.Fatalf("next poll asked from %q, want 11", got)
	}
}

func TestActIsIdempotent(t *testing.T) {
	f := &feed{events: []Event{{ID: "3", Type: "node.purged", Kind: "project", NodeID: purged}}}
	c, store := setup(t, f)
	addTask(t, store, "01jd8x3k7q0000000000000t01", purged)

	for range 2 {
		if err := c.act(context.Background(), f.events[0]); err != nil {
			t.Fatal(err)
		}
	}
	if n := count(t, store, purged); n != 0 {
		t.Fatalf("purged project has %d tasks", n)
	}
}

func TestPollFailureKeepsCursor(t *testing.T) {
	f := &feed{events: []Event{{ID: "3", Type: "node.created", Kind: "project", NodeID: kept}}, fail: true}
	c, _ := setup(t, f)

	if err := c.Poll(context.Background()); err == nil {
		t.Fatal("want an error from a failing feed")
	}
	if cur, err := c.cursor(context.Background()); err != nil || cur != "" {
		t.Fatalf("cursor = %q, %v", cur, err)
	}
}
