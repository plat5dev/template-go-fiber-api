// Package lifecycle follows identity's lifecycle log, so the API's data goes when its
// project or its person does (plat5 docs/concepts/lifecycle.md#following-the-log).
//
// It asks only for the two events it acts on. node.purged for a project deletes that
// project's tasks; a purged organization's projects are purged with it, each with its
// own event. user.deleted deletes that person's profile. Tasks are authored, so
// member.removed keeps them. One cursor, the last event id acted on, is kept in SQLite
// with the data it cleans up, and saved in the same transaction as a page's deletes. Acting
// again on the same event changes nothing, so the log can be replayed by resetting it.
package lifecycle

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"time"

	"github.com/rs/zerolog"

	"github.com/example/go-fiber-api/metrics"
	"github.com/example/go-fiber-api/profiles"
	"github.com/example/go-fiber-api/tasks"
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

// Event is the part of an event the API reads.
type Event struct {
	ID     string `json:"id"`
	Type   string `json:"type"`
	Kind   string `json:"kind"`
	NodeID string `json:"node_id"`
	UserID string `json:"user_id"`
}

type Consumer struct {
	url      string
	db       *sql.DB
	tasks    *tasks.Store
	profiles *profiles.Store
	client   *http.Client
	retry    time.Duration
	logger   zerolog.Logger
}

// New reads the log at identityURL, identity's internal base URL.
func New(identityURL string, db *sql.DB, tasks *tasks.Store, profiles *profiles.Store, logger zerolog.Logger) *Consumer {
	return &Consumer{
		url:      identityURL + "/internal/events",
		db:       db,
		tasks:    tasks,
		profiles: profiles,
		client:   &http.Client{Timeout: requestTimeout},
		retry:    RetryAfter,
		logger:   logger.With().Str("component", "lifecycle").Logger(),
	}
}

// Run follows the log until ctx ends, which also cancels a read in flight. After a page or
// a 204 it asks again at once; after an error it logs it and asks again after RetryAfter.
// An immediate answer with no events (an empty log, or a cursor past identity's newest
// event) is also asked again after RetryAfter, so it isn't asked in a tight loop.
func (c *Consumer) Run(ctx context.Context) {
	if cur, err := c.cursor(ctx); err == nil {
		if cur == "" {
			c.logger.Info().Msg("following the lifecycle log from its first event")
		} else {
			c.logger.Info().Str("after", cur).Msg("following the lifecycle log")
		}
	}
	for ctx.Err() == nil {
		again, err := c.Step(ctx)
		if again || ctx.Err() != nil {
			continue
		}
		if err != nil {
			c.logger.Error().Err(err).Dur("retry_in", c.retry).Msg("lifecycle read failed")
		}
		select {
		case <-ctx.Done():
		case <-time.After(c.retry):
		}
	}
}

// Step asks once for the events after the saved cursor, acts on them, and saves the cursor
// past them in the same transaction as their deletes. again is whether to ask again at
// once: after a page of events, or a 204 (identity waited and nothing came).
func (c *Consumer) Step(ctx context.Context) (again bool, err error) {
	cursor, err := c.cursor(ctx)
	if err != nil {
		return false, err
	}
	events, waited, err := c.fetch(ctx, cursor)
	if err != nil || len(events) == 0 {
		return waited, err
	}
	if err := c.apply(ctx, events); err != nil {
		return false, err
	}
	c.logger.Info().Int("events", len(events)).Str("after", cursor).
		Str("cursor", events[len(events)-1].ID).Msg("lifecycle page read")
	return true, nil
}

// apply acts on a page and saves its last id as the cursor in one transaction: the deletes
// and the cursor commit together or not at all.
func (c *Consumer) apply(ctx context.Context, events []Event) error {
	tx, err := c.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	ts, ps := c.tasks.WithTx(tx), c.profiles.WithTx(tx)
	for _, e := range events {
		if err := c.act(ctx, ts, ps, e); err != nil {
			return fmt.Errorf("event %s: %w", e.ID, err)
		}
	}
	start := time.Now()
	_, err = tx.ExecContext(ctx, `
		INSERT INTO feed_cursors (id, event_id) VALUES (1, ?)
		ON CONFLICT (id) DO UPDATE SET event_id = excluded.event_id`, events[len(events)-1].ID)
	metrics.RecordDBOperation("feed_cursors.save", time.Since(start), err)
	if err != nil {
		return err
	}
	return tx.Commit()
}

func (c *Consumer) act(ctx context.Context, ts *tasks.Store, ps *profiles.Store, e Event) error {
	switch {
	case e.Type == "node.purged" && e.Kind == "project":
		if err := ts.DeleteByProject(ctx, e.NodeID); err != nil {
			return err
		}
		c.logger.Info().Str("event_id", e.ID).Str("node_id", e.NodeID).Msg("purged project's tasks deleted")
	case e.Type == "user.deleted":
		if err := ps.Delete(ctx, e.UserID); err != nil {
			return err
		}
		c.logger.Info().Str("event_id", e.ID).Str("user_id", e.UserID).Msg("profile deleted with its account")
	}
	return nil
}

// fetch asks for the events after cursor, or from the first event when there's none yet.
// With a cursor identity waits for one; its 204 is no events, and waited.
func (c *Consumer) fetch(ctx context.Context, cursor string) (events []Event, waited bool, err error) {
	q := url.Values{"types": {Types}, "limit": {fmt.Sprint(pageSize)}}
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
	if err := json.NewDecoder(res.Body).Decode(&page); err != nil {
		return nil, false, fmt.Errorf("lifecycle log: %w", err)
	}
	if page.Events == nil {
		return nil, false, fmt.Errorf("lifecycle log: no events in its answer")
	}
	return *page.Events, false, nil
}

// cursor is the last event id acted on, or "" before the first.
func (c *Consumer) cursor(ctx context.Context) (string, error) {
	start := time.Now()
	var id string
	err := c.db.QueryRowContext(ctx, `SELECT event_id FROM feed_cursors WHERE id = 1`).Scan(&id)
	if err == sql.ErrNoRows {
		err = nil
	}
	metrics.RecordDBOperation("feed_cursors.find", time.Since(start), err)
	return id, err
}
