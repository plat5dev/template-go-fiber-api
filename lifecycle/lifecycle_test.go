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

	"github.com/example/go-fiber-api/db"
	"github.com/example/go-fiber-api/plat5"
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
// filters, two events to a page, an after past the newest event is a 409 CURSOR_AHEAD,
// and otherwise it waits for a matching event up to wait, then answers 204. The first
// requests answer fail's statuses, one each.
type identity struct {
	mu      sync.Mutex
	events  []Event
	wait    time.Duration
	fail    []int    // statuses for the first requests
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
	n := atoi(after)
	var match []Event
	for _, e := range id.events {
		if atoi(e.ID) > n && slices.Contains(types, e.Type) {
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
	if len(id.fail) > 0 {
		status := id.fail[0]
		id.fail = id.fail[1:]
		id.mu.Unlock()
		w.WriteHeader(status)
		return
	}
	newest := 0
	if len(id.events) > 0 {
		newest = atoi(id.events[len(id.events)-1].ID)
	}
	if atoi(after) > newest {
		id.mu.Unlock()
		w.WriteHeader(http.StatusConflict)
		_, _ = w.Write([]byte(`{"error":{"type":"invalid_request_error","code":"CURSOR_AHEAD","message":"The log has no event at this cursor. Read from its first event.","request_id":null,"details":null}}`))
		return
	}
	events, more := id.page(after, types)
	timeout := time.After(id.wait)
	for len(events) == 0 {
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

func atoi(s string) int {
	n, _ := strconv.Atoi(s)
	return n
}

func (id *identity) askedAfter() []string {
	id.mu.Lock()
	defer id.mu.Unlock()
	return slices.Clone(id.asked)
}

func setup(t *testing.T, id *identity) *Consumer {
	t.Helper()
	sqlDB, err := db.Open(context.Background(), filepath.Join(t.TempDir(), "app.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { sqlDB.Close() })
	srv := httptest.NewServer(id)
	t.Cleanup(srv.Close)
	c := New(srv.URL, sqlDB)
	c.retry = 10 * time.Millisecond
	return c
}

func addTask(t *testing.T, c *Consumer, id, projectID string) {
	t.Helper()
	err := tasks.NewStore(c.db).Insert(context.Background(), &tasks.Task{
		ID: id, ProjectID: projectID, Title: "t", Status: "todo",
		CreatedBy: "01jd8x3k7q00000000000000mm", CreatedAt: "now", UpdatedAt: "now",
	})
	if err != nil {
		t.Fatal(err)
	}
}

func count(t *testing.T, c *Consumer, projectID string) int {
	t.Helper()
	list, _, err := tasks.NewStore(c.db).List(context.Background(), projectID, plat5.Page{Limit: 100})
	if err != nil {
		t.Fatal(err)
	}
	return len(list)
}

func addProfile(t *testing.T, c *Consumer, userID string) {
	t.Helper()
	if _, _, err := profiles.NewStore(c.db).Put(context.Background(), userID, "hi", "now"); err != nil {
		t.Fatal(err)
	}
}

func hasProfile(t *testing.T, c *Consumer, userID string) bool {
	t.Helper()
	var n int
	if err := c.db.QueryRow(`SELECT count(*) FROM profiles WHERE user_id = ?`, userID).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n > 0
}

func cursor(t *testing.T, c *Consumer) string {
	t.Helper()
	cur, err := c.cursor(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return cur
}

func step(t *testing.T, c *Consumer) {
	t.Helper()
	if err := c.Step(context.Background()); err != nil {
		t.Fatal(err)
	}
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
		Event{ID: "7", Type: "node.created", Kind: "project", NodeID: purged},
		Event{ID: "8", Type: "user.created", UserID: fresh},
		Event{ID: "9", Type: "node.purged", Kind: "organization", NodeID: kept},
		Event{ID: "10", Type: "member.removed"},
		Event{ID: "12", Type: "node.purged", Kind: "project", NodeID: purged},
		Event{ID: "13", Type: "user.deleted", UserID: gone},
		Event{ID: "20", Type: "node.deleted", Kind: "project", NodeID: kept},
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
		t.Fatalf("purged project has %d tasks", n)
	}
	if n := count(t, c, kept); n != 1 {
		t.Fatalf("other project has %d tasks, want 1", n)
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
	step(t, c)
	step(t, c)
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

func TestA204ChangesNothing(t *testing.T) {
	id := theLog()
	c := setup(t, id)
	step(t, c)
	step(t, c)

	// At the head: identity waits, answers 204, and nothing changes.
	step(t, c)
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
	// Identity's Postgres unavailable is a 503; any other failure, a 500.
	id.fail = []int{http.StatusServiceUnavailable, http.StatusInternalServerError}
	c := setup(t, id)
	seed(t, c)

	if err := c.Step(context.Background()); err == nil {
		t.Fatal("want an error from a 503")
	}
	if cur := cursor(t, c); cur != "" {
		t.Fatalf("cursor after a failed read = %q", cur)
	}

	// Run logs the 500, waits c.retry, and reads on.
	run(t, c, func() bool { return cursor(t, c) == "13" })
	checkActed(t, c)
	if got := id.askedAfter(); !slices.Equal(got[:4], []string{"", "", "", "12"}) {
		t.Fatalf("asked after %q", got)
	}
}

func TestAResetLogIsReadAgainFromItsFirstEvent(t *testing.T) {
	id := theLog()
	c := setup(t, id)
	seed(t, c)
	// A cursor saved past the log's newest event: identity's log was reset.
	if _, err := c.db.Exec(`INSERT INTO feed_cursors (id, event_id) VALUES (1, '99')`); err != nil {
		t.Fatal(err)
	}
	addProfile(t, c, gone) // acted on before the reset; deleted again, harmlessly

	run(t, c, func() bool { return cursor(t, c) == "13" })
	checkActed(t, c)
	if got := id.askedAfter(); !slices.Equal(got[:3], []string{"99", "", "12"}) {
		t.Fatalf("asked after %q", got)
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
		if err := c.Step(context.Background()); err == nil {
			t.Fatal("want an error when the cursor can't be saved")
		}
	}
	if n := count(t, c, purged); n != 1 {
		t.Fatalf("purged project has %d tasks, want its delete rolled back", n)
	}
	if cur := cursor(t, c); cur != "" {
		t.Fatalf("cursor = %q", cur)
	}
}

// An event's keys match exactly, and one without an id is skipped.
func TestEventsAreReadExactly(t *testing.T) {
	page := `{"events":[
		{"id":"1","type":"node.purged","Kind":"project","node_id":"` + purged + `"},
		{"type":"user.deleted","user_id":"` + gone + `"},
		{"id":"3","type":"user.deleted","USER_ID":"` + stays + `"},
		{"id":"4","type":"node.purged","kind":"project","node_id":"` + kept + `","root_id":"r","occurred_at":"2026-10-27T09:14:03Z"}
	],"has_more":false}`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("after") != "" {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		_, _ = w.Write([]byte(page))
	}))
	t.Cleanup(srv.Close)
	c := setup(t, newIdentity())
	c.url = srv.URL
	seed(t, c)
	step(t, c)
	if count(t, c, purged) != 1 || !hasProfile(t, c, gone) || !hasProfile(t, c, stays) {
		t.Fatal("acted on an event it shouldn't have")
	}
	if count(t, c, kept) != 0 {
		t.Fatal("didn't act on a well-formed event")
	}
	if cur := cursor(t, c); cur != "4" {
		t.Fatalf("cursor %q, want 4", cur)
	}
}
