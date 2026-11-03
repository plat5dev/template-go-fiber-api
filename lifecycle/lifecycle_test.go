package lifecycle

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

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

// identity serves the one lifecycle log at /internal/events as identity does: types
// filters, two events to a page, no after is answered at once, and with after it waits for
// a matching event up to wait, then answers 204. The first fail requests answer 500.
type identity struct {
	mu      sync.Mutex
	events  []Event
	wait    time.Duration
	fail    int
	asked   []string // each request's after
	types   []string // each request's types
	waiting chan struct{}
	added   chan struct{}
}

func newIdentity(events ...Event) *identity {
	return &identity{events: events, wait: 50 * time.Millisecond, waiting: make(chan struct{}, 100), added: make(chan struct{})}
}

// add appends an event and wakes any waiting read.
func (id *identity) add(e Event) {
	id.mu.Lock()
	id.events = append(id.events, e)
	close(id.added)
	id.added = make(chan struct{})
	id.mu.Unlock()
}

// page is the events after after that match types, at most two, and whether more follow.
func (id *identity) page(after string, types []string) ([]Event, bool) {
	n, _ := strconv.Atoi(after)
	var match []Event
	for _, e := range id.events {
		if m, _ := strconv.Atoi(e.ID); m > n && slices.Contains(types, e.Type) {
			match = append(match, e)
		}
	}
	end := min(2, len(match))
	return match[:end], end < len(match)
}

func (id *identity) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/internal/events" {
		http.NotFound(w, r)
		return
	}
	q := r.URL.Query()
	after, types := q.Get("after"), strings.Split(q.Get("types"), ",")
	id.mu.Lock()
	id.asked = append(id.asked, after)
	id.types = append(id.types, q.Get("types"))
	if id.fail > 0 {
		id.fail--
		id.mu.Unlock()
		w.WriteHeader(http.StatusInternalServerError)
		return
	}
	events, more := id.page(after, types)
	timeout := time.After(id.wait)
	for after != "" && len(events) == 0 {
		added := id.added
		id.mu.Unlock()
		select { // tell a test the read is waiting, if it's listening
		case id.waiting <- struct{}{}:
		default:
		}
		select {
		case <-added:
		case <-timeout:
			w.WriteHeader(http.StatusNoContent)
			return
		case <-r.Context().Done():
			return
		}
		id.mu.Lock()
		events, more = id.page(after, types)
	}
	id.mu.Unlock()
	if events == nil {
		events = []Event{}
	}
	_ = json.NewEncoder(w).Encode(map[string]any{"events": events, "has_more": more})
}

func (id *identity) askedAfter() []string {
	id.mu.Lock()
	defer id.mu.Unlock()
	return slices.Clone(id.asked)
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
	c := New(srv.URL, sqlDB, tasks.NewStore(sqlDB), profiles.NewStore(sqlDB), zerolog.Nop())
	c.retry = 10 * time.Millisecond
	return c
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

func cursor(t *testing.T, c *Consumer) string {
	t.Helper()
	cur, err := c.cursor(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return cur
}

func step(t *testing.T, c *Consumer) bool {
	t.Helper()
	again, err := c.Step(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return again
}

// run runs c until cond holds, then stops it and waits for Run to return.
func run(t *testing.T, c *Consumer, cond func() bool) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); c.Run(ctx) }()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			cancel()
			t.Fatal("condition not met in 5s")
		}
		time.Sleep(5 * time.Millisecond)
	}
	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Run didn't return when its context ended")
	}
}

func theLog() *identity {
	return newIdentity(
		Event{ID: "7", Type: "node.created", Kind: "environment", NodeID: purged},
		Event{ID: "8", Type: "user.created", UserID: fresh},
		Event{ID: "9", Type: "node.purged", Kind: "organization", NodeID: kept},
		Event{ID: "10", Type: "member.removed"},
		Event{ID: "12", Type: "node.purged", Kind: "environment", NodeID: purged},
		Event{ID: "13", Type: "user.deleted", UserID: gone},
		Event{ID: "20", Type: "node.deleted", Kind: "environment", NodeID: kept},
	)
}

func seed(t *testing.T, c *Consumer) {
	t.Helper()
	addTask(t, c, "01jd8x3k7q0000000000000t01", purged)
	addTask(t, c, "01jd8x3k7q0000000000000t02", kept)
	addProfile(t, c, gone)
	addProfile(t, c, stays)
}

func checkActed(t *testing.T, c *Consumer) {
	t.Helper()
	if n := count(t, c, purged); n != 0 {
		t.Fatalf("purged environment has %d tasks", n)
	}
	if n := count(t, c, kept); n != 1 {
		t.Fatalf("other environment has %d tasks, want 1", n)
	}
	if hasProfile(t, c, gone) {
		t.Fatal("deleted person's profile is kept")
	}
	if !hasProfile(t, c, stays) {
		t.Fatal("another person's profile is gone")
	}
	if hasProfile(t, c, fresh) {
		t.Fatal("user.created made a profile")
	}
}

func TestActsOnEachPageAndAdvancesTheCursor(t *testing.T) {
	id := theLog()
	c := setup(t, id)
	seed(t, c)

	// No cursor yet: from the first event, then from each page's last id.
	for range 2 {
		if !step(t, c) {
			t.Fatal("a page should be followed at once by another ask")
		}
	}
	checkActed(t, c)
	if cur := cursor(t, c); cur != "13" {
		t.Fatalf("cursor = %q, want 13", cur)
	}
	if got := id.askedAfter(); !slices.Equal(got, []string{"", "12"}) {
		t.Fatalf("asked after %q", got)
	}
	for _, ty := range id.types {
		if ty != "node.purged,user.deleted" {
			t.Fatalf("asked for types %q", ty)
		}
	}
}

func TestA204IsAskedAgainAtOnce(t *testing.T) {
	id := theLog()
	c := setup(t, id)
	step(t, c)
	step(t, c)

	// At the head: identity waits, answers 204, and nothing changes.
	if !step(t, c) {
		t.Fatal("a 204 should be followed at once by another ask")
	}
	if cur := cursor(t, c); cur != "13" {
		t.Fatalf("cursor after a 204 = %q", cur)
	}
	if got := id.askedAfter(); got[len(got)-1] != "13" {
		t.Fatalf("asked after %q", got)
	}
}

func TestAWaitingReadGetsANewEvent(t *testing.T) {
	id := theLog()
	id.wait = 5 * time.Second
	c := setup(t, id)
	step(t, c)
	step(t, c)
	addProfile(t, c, stays)

	go func() {
		<-id.waiting
		id.add(Event{ID: "21", Type: "node.created"}) // not asked for: the wait goes on
		<-id.waiting
		id.add(Event{ID: "22", Type: "user.deleted", UserID: stays})
	}()
	start := time.Now()
	step(t, c)
	if time.Since(start) > 2*time.Second {
		t.Fatal("the read waited out its timeout")
	}
	if hasProfile(t, c, stays) {
		t.Fatal("the new event wasn't acted on")
	}
	if cur := cursor(t, c); cur != "22" {
		t.Fatalf("cursor = %q, want 22", cur)
	}
}

func TestAnErrorIsRetried(t *testing.T) {
	id := theLog()
	id.fail = 2
	c := setup(t, id)
	seed(t, c)

	if _, err := c.Step(context.Background()); err == nil {
		t.Fatal("want an error from a 500")
	}
	if cur := cursor(t, c); cur != "" {
		t.Fatalf("cursor after a failed read = %q", cur)
	}

	// Run logs the next 500, waits c.retry, and reads on.
	run(t, c, func() bool { return cursor(t, c) == "13" })
	checkActed(t, c)
	if got := id.askedAfter(); !slices.Equal(got[:4], []string{"", "", "", "12"}) {
		t.Fatalf("asked after %q", got)
	}
}

func TestAnEmptyLogIsNotAskedInATightLoop(t *testing.T) {
	id := newIdentity()
	c := setup(t, id)
	if step(t, c) {
		t.Fatal("an immediate empty answer should wait before asking again")
	}
	c.retry = 50 * time.Millisecond
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Millisecond)
	defer cancel()
	c.Run(ctx)
	if n := len(id.askedAfter()); n > 5 {
		t.Fatalf("asked %d times in 120ms", n)
	}
}

func TestStoppingCancelsTheReadInFlight(t *testing.T) {
	id := theLog()
	id.wait = time.Minute
	c := setup(t, id)
	step(t, c)
	step(t, c)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); c.Run(ctx) }()
	<-id.waiting
	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Run didn't return while identity was waiting")
	}
}

func TestReplayChangesNothing(t *testing.T) {
	id := theLog()
	c := setup(t, id)
	seed(t, c)
	run(t, c, func() bool { return cursor(t, c) == "13" })

	// Reset the cursor, as the README says, and read the log again from its first event.
	if _, err := c.db.Exec(`DELETE FROM feed_cursors`); err != nil {
		t.Fatal(err)
	}
	before := len(id.askedAfter())
	run(t, c, func() bool { return cursor(t, c) == "13" })
	checkActed(t, c)
	if got := id.askedAfter()[before]; got != "" {
		t.Fatalf("replay asked after %q, want the first event", got)
	}
}

func TestAPageCommitsWithItsCursor(t *testing.T) {
	id := theLog()
	c := setup(t, id)
	seed(t, c)
	// The cursor can't be saved, so the page's deletes roll back with it.
	if _, err := c.db.Exec(`CREATE TRIGGER no_cursor BEFORE INSERT ON feed_cursors BEGIN SELECT RAISE(ABORT, 'no'); END`); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if _, err := c.Step(context.Background()); err == nil {
			t.Fatal("want an error when the cursor can't be saved")
		}
	}
	if n := count(t, c, purged); n != 1 {
		t.Fatalf("purged environment has %d tasks, want its delete rolled back", n)
	}
	if cur := cursor(t, c); cur != "" {
		t.Fatalf("cursor = %q", cur)
	}
}
