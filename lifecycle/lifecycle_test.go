package lifecycle

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"slices"
	"sync"
	"testing"

	"github.com/rs/zerolog"

	"github.com/example/go-fiber-api/db"
	"github.com/example/go-fiber-api/pagination"
	"github.com/example/go-fiber-api/profiles"
	"github.com/example/go-fiber-api/tasks"
)

const (
	purged = "01jd8x3k7q0000000000000001"
	kept   = "01jd8x3k7q0000000000000002"
	gone   = "01jd8x3k7q00000000000000u1"
	stays  = "01jd8x3k7q00000000000000u2"
	fresh  = "01jd8x3k7q00000000000000u3"
)

// served is one feed: its events, two to a page under its key, and each cursor it's
// asked for.
type served struct {
	key    string
	events []Event
	asked  []string
	fail   bool
}

// identity serves the tree and user feeds at their paths.
type identity struct {
	mu   sync.Mutex
	tree served
	user served
}

func newIdentity(tree, user []Event) *identity {
	return &identity{
		tree: served{key: "tree_events", events: tree},
		user: served{key: "user_events", events: user},
	}
}

func (id *identity) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	id.mu.Lock()
	defer id.mu.Unlock()
	var f *served
	switch r.URL.Path {
	case "/internal/tree-events":
		f = &id.tree
	case "/internal/user-events":
		f = &id.user
	default:
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
		f.key:      f.events[i:end],
		"has_more": end < len(f.events),
	})
}

// asked is the cursors each feed was asked for.
func (id *identity) asked() (tree, user []string) {
	id.mu.Lock()
	defer id.mu.Unlock()
	return slices.Clone(id.tree.asked), slices.Clone(id.user.asked)
}

func setup(t *testing.T, id *identity) *Consumer {
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
	srv := httptest.NewServer(id)
	t.Cleanup(srv.Close)
	return New(srv.URL, sqlDB, tasks.NewStore(sqlDB), profiles.NewStore(sqlDB), zerolog.Nop())
}

func addTask(t *testing.T, c *Consumer, id, environmentID string) {
	t.Helper()
	err := c.tasks.Insert(context.Background(), &tasks.Task{
		ID: id, EnvironmentID: environmentID, Title: "t", Status: "todo",
		CreatedBy: "01jd8x3k7q00000000000000mm", CreatedAt: "now", UpdatedAt: "now",
	})
	if err != nil {
		t.Fatal(err)
	}
}

func count(t *testing.T, c *Consumer, environmentID string) int {
	t.Helper()
	list, _, err := c.tasks.ListByEnvironment(context.Background(), environmentID, pagination.Page{Limit: 100})
	if err != nil {
		t.Fatal(err)
	}
	return len(list)
}

func addProfile(t *testing.T, c *Consumer, userID string) {
	t.Helper()
	err := c.profiles.Insert(context.Background(), &profiles.Profile{UserID: userID, Bio: "hi", CreatedAt: "now", UpdatedAt: "now"})
	if err != nil {
		t.Fatal(err)
	}
}

func hasProfile(t *testing.T, c *Consumer, userID string) bool {
	t.Helper()
	p, err := c.profiles.FindByUserID(context.Background(), userID)
	if err != nil {
		t.Fatal(err)
	}
	return p != nil
}

func cursor(t *testing.T, c *Consumer, feed string) string {
	t.Helper()
	cur, err := c.cursor(context.Background(), feed)
	if err != nil {
		t.Fatal(err)
	}
	return cur
}

func TestTreeFeed(t *testing.T) {
	id := newIdentity([]Event{
		{ID: "7", Type: "node.created", Kind: "environment", NodeID: purged},
		{ID: "8", Type: "node.deleted", Kind: "environment", NodeID: kept},
		{ID: "9", Type: "member.removed"},
		{ID: "10", Type: "node.purged", Kind: "organization", NodeID: kept},
		{ID: "11", Type: "node.purged", Kind: "environment", NodeID: purged},
	}, nil)
	c := setup(t, id)
	addTask(t, c, "01jd8x3k7q0000000000000t01", purged)
	addTask(t, c, "01jd8x3k7q0000000000000t02", kept)

	c.Poll(context.Background())
	if n := count(t, c, purged); n != 0 {
		t.Fatalf("purged environment has %d tasks", n)
	}
	if n := count(t, c, kept); n != 1 {
		t.Fatalf("other environment has %d tasks, want 1", n)
	}
	// Three pages in one poll: has_more reads again at once, from each page's last id.
	if tree, _ := id.asked(); !slices.Equal(tree, []string{"", "8", "10"}) {
		t.Fatalf("tree asked = %q", tree)
	}
	if cur := cursor(t, c, "tree"); cur != "11" {
		t.Fatalf("tree cursor = %q", cur)
	}

	// The next poll starts from the saved cursor.
	c.Poll(context.Background())
	if tree, _ := id.asked(); tree[len(tree)-1] != "11" {
		t.Fatalf("next poll asked the tree feed from %q, want 11", tree[len(tree)-1])
	}
}

func TestUserFeed(t *testing.T) {
	id := newIdentity(
		[]Event{{ID: "900", Type: "node.created", Kind: "environment", NodeID: kept}},
		[]Event{
			{ID: "1", Type: "user.created", UserID: fresh},
			{ID: "2", Type: "user.deleted", UserID: gone},
			{ID: "3", Type: "user.created", UserID: stays},
		})
	c := setup(t, id)
	addProfile(t, c, gone)
	addProfile(t, c, stays)

	c.Poll(context.Background())
	if hasProfile(t, c, gone) {
		t.Fatal("deleted person's profile is kept")
	}
	if !hasProfile(t, c, stays) {
		t.Fatal("another person's profile is gone")
	}
	if hasProfile(t, c, fresh) {
		t.Fatal("user.created made a profile")
	}
	if _, user := id.asked(); !slices.Equal(user, []string{"", "2"}) {
		t.Fatalf("user asked = %q", user)
	}
	if tree, user := cursor(t, c, "tree"), cursor(t, c, "user"); tree != "900" || user != "3" {
		t.Fatalf("cursors: tree %q, user %q", tree, user)
	}

	// Each feed is asked from its own cursor.
	c.Poll(context.Background())
	tree, user := id.asked()
	if tree[len(tree)-1] != "900" || user[len(user)-1] != "3" {
		t.Fatalf("next poll asked tree from %q, user from %q", tree[len(tree)-1], user[len(user)-1])
	}
}

func TestFeedsAreIndependent(t *testing.T) {
	for _, failing := range []string{"tree", "user"} {
		id := newIdentity(
			[]Event{{ID: "5", Type: "node.purged", Kind: "environment", NodeID: purged}},
			[]Event{{ID: "6", Type: "user.deleted", UserID: gone}})
		if failing == "tree" {
			id.tree.fail = true
		} else {
			id.user.fail = true
		}
		c := setup(t, id)
		addTask(t, c, "01jd8x3k7q0000000000000t01", purged)
		addProfile(t, c, gone)

		c.Poll(context.Background())
		treeRead, userRead := failing != "tree", failing != "user"
		if got := count(t, c, purged) == 0; got != treeRead {
			t.Fatalf("%s failing: purged environment's tasks deleted = %v", failing, got)
		}
		if got := !hasProfile(t, c, gone); got != userRead {
			t.Fatalf("%s failing: deleted person's profile deleted = %v", failing, got)
		}
		if got := cursor(t, c, failing); got != "" {
			t.Fatalf("%s failing: its cursor = %q", failing, got)
		}
	}
}

func TestReadingTwiceIsHarmless(t *testing.T) {
	id := newIdentity(
		[]Event{{ID: "3", Type: "node.purged", Kind: "environment", NodeID: purged}},
		[]Event{{ID: "4", Type: "user.deleted", UserID: gone}})
	c := setup(t, id)
	addTask(t, c, "01jd8x3k7q0000000000000t01", purged)
	addProfile(t, c, gone)

	for range 2 {
		for _, f := range []feed{c.tree, c.user} {
			if err := c.read(context.Background(), f); err != nil {
				t.Fatalf("%s: %v", f.name, err)
			}
		}
		// Forget the cursors, so the next read gets the same events.
		if _, err := c.db.Exec(`DELETE FROM feed_cursors`); err != nil {
			t.Fatal(err)
		}
	}
	if n := count(t, c, purged); n != 0 {
		t.Fatalf("purged environment has %d tasks", n)
	}
	if hasProfile(t, c, gone) {
		t.Fatal("deleted person's profile is kept")
	}
}

func TestFailedReadKeepsCursor(t *testing.T) {
	id := newIdentity([]Event{{ID: "3", Type: "node.created", Kind: "environment", NodeID: kept}}, nil)
	id.tree.fail = true
	c := setup(t, id)

	if err := c.read(context.Background(), c.tree); err == nil {
		t.Fatal("want an error from a failing feed")
	}
	if cur := cursor(t, c, "tree"); cur != "" {
		t.Fatalf("cursor = %q", cur)
	}
}
