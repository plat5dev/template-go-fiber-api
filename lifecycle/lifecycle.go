// Package lifecycle follows identity's lifecycle log, so the API's data goes when its
// project or its person does (plat5 docs/concepts/lifecycle.md#following-the-log).
//
// It asks only for the two events it acts on. node.purged for a project deletes that
// project's tasks; a purged organization's projects are purged with it, each with its own
// event. user.deleted deletes that person's profile. Tasks are authored, so member.removed
// keeps them. One cursor, the last event id acted on, is kept in SQLite with the data it
// cleans up, and saved in the same transaction as a page's deletes. Acting again on the
// same event changes nothing, so the log can be replayed by resetting it.
package lifecycle

import (
	"context"
	"database/sql"
	"encoding/json/v2"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"github.com/example/go-fiber-api/profiles"
	"github.com/example/go-fiber-api/tasks"
	"github.com/example/go-fiber-api/telemetry"
)

const (
	// Types are the events the API acts on, and the only ones it asks for.
	Types = "node.purged,user.deleted"
	// RetryAfter is the wait after a failed read before asking again.
	RetryAfter = 5 * time.Second
	// Identity waits up to 25 s before a 204, so a read may take that long.
	requestTimeout = 40 * time.Second
	pageSize       = 100
)

// Event is the part of an event the API reads (plat5
// docs/reference/api/internal.md#lifecycle-events). Keys match exactly.
type Event struct {
	ID     string `json:"id"`
	Type   string `json:"type"`
	Kind   string `json:"kind"`
	NodeID string `json:"node_id"`
	UserID string `json:"user_id"`
}

// Consumer reads the log and acts on it.
type Consumer struct {
	url    string
	db     *sql.DB
	client *http.Client
	retry  time.Duration
	log    *slog.Logger
}

// New reads the log at identityURL, identity's internal base URL, and acts on db.
func New(identityURL string, db *sql.DB) *Consumer {
	return &Consumer{
		url:    identityURL + "/internal/events",
		db:     db,
		client: &http.Client{Timeout: requestTimeout},
		retry:  RetryAfter,
		log:    slog.With("component", "lifecycle"),
	}
}

// Run follows the log until ctx ends, which also cancels a read in flight. After a page or
// a 204 it asks again at once; after an error it logs it and asks again after RetryAfter.
// An immediate answer with no events (an empty log, or a cursor past identity's newest
// event) is also asked again after RetryAfter, so it isn't asked in a tight loop.
func (c *Consumer) Run(ctx context.Context) {
	if cur, err := c.cursor(ctx); err == nil {
		if cur == "" {
			c.log.Info("following the lifecycle log from its first event")
		} else {
			c.log.Info("following the lifecycle log", "after", cur)
		}
	}
	for ctx.Err() == nil {
		again, err := c.Step(ctx)
		if again || ctx.Err() != nil {
			continue
		}
		if err != nil {
			c.log.Error("lifecycle read failed", "error_message", err.Error(), "retry_in_ms", c.retry.Milliseconds())
		}
		select {
		case <-ctx.Done():
		case <-time.After(c.retry):
		}
	}
}

// Step asks once for the events after the saved cursor, acts on them, and saves the cursor
// past them in the same transaction as their deletes. again is whether to ask again at
// once: after a page of events, or a 204 (identity waited and nothing came). An event
// without an id is skipped: there's no cursor to save for it.
func (c *Consumer) Step(ctx context.Context) (again bool, err error) {
	cursor, err := c.cursor(ctx)
	if err != nil {
		return false, err
	}
	page, waited, err := c.fetch(ctx, cursor)
	if err != nil {
		return false, err
	}
	var events []Event
	for _, e := range page {
		if e.ID != "" {
			events = append(events, e)
		}
	}
	if len(events) == 0 {
		return waited, nil
	}
	last := events[len(events)-1].ID
	if err := c.apply(ctx, events, last); err != nil {
		return false, err
	}
	c.log.Info("lifecycle page read", "events", len(events), "after", cursor, "cursor", last)
	return true, nil
}

// apply acts on events and saves last as the cursor in one transaction: the deletes and
// the cursor commit together or not at all.
func (c *Consumer) apply(ctx context.Context, events []Event, last string) (err error) {
	tx, err := c.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, e := range events {
		if err := c.act(ctx, tx, e); err != nil {
			return fmt.Errorf("event %s: %w", e.ID, err)
		}
	}
	if err := saveCursor(ctx, tx, last); err != nil {
		return err
	}
	return tx.Commit()
}

func (c *Consumer) act(ctx context.Context, tx *sql.Tx, e Event) error {
	switch {
	case e.Type == "node.purged" && e.Kind == "project" && e.NodeID != "":
		if err := tasks.DeleteProject(ctx, tx, e.NodeID); err != nil {
			return err
		}
		c.log.Info("purged project's tasks deleted", "event_id", e.ID, "node_id", e.NodeID)
	case e.Type == "user.deleted" && e.UserID != "":
		if err := profiles.Delete(ctx, tx, e.UserID); err != nil {
			return err
		}
		c.log.Info("profile deleted with its account", "event_id", e.ID, "user_id", e.UserID)
	}
	return nil
}

// fetch asks for the events after cursor, or from the first event when there's none yet.
// With a cursor identity waits for one; its 204 is no events, and waited.
func (c *Consumer) fetch(ctx context.Context, cursor string) (events []Event, waited bool, err error) {
	q := url.Values{"types": {Types}, "limit": {strconv.Itoa(pageSize)}}
	if cursor != "" {
		q.Set("after", cursor)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.url+"?"+q.Encode(), nil)
	if err != nil {
		return nil, false, err
	}
	res, err := c.client.Do(req)
	if err != nil {
		return nil, false, err
	}
	defer res.Body.Close()
	switch res.StatusCode {
	case http.StatusNoContent:
		return nil, true, nil
	case http.StatusOK:
	default:
		return nil, false, fmt.Errorf("lifecycle log answered %d", res.StatusCode)
	}
	var page struct {
		Events *[]Event `json:"events"`
	}
	if err := json.UnmarshalRead(res.Body, &page); err != nil {
		return nil, false, fmt.Errorf("lifecycle log: %w", err)
	}
	if page.Events == nil {
		return nil, false, errors.New("lifecycle log: no events in its answer")
	}
	return *page.Events, false, nil
}

// cursor is the last event id acted on, or "" before the first.
func (c *Consumer) cursor(ctx context.Context) (id string, err error) {
	defer telemetry.DB("feed_cursors.find", time.Now(), &err)
	err = c.db.QueryRowContext(ctx, `SELECT event_id FROM feed_cursors WHERE id = 1`).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return id, err
}

func saveCursor(ctx context.Context, tx *sql.Tx, id string) (err error) {
	defer telemetry.DB("feed_cursors.save", time.Now(), &err)
	_, err = tx.ExecContext(ctx, `INSERT INTO feed_cursors (id, event_id) VALUES (1, ?)
		ON CONFLICT (id) DO UPDATE SET event_id = excluded.event_id`, id)
	return err
}
