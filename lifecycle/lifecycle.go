// Package lifecycle follows identity's two feeds, so the API's data goes when its
// environment or its person does (plat5 docs/concepts/lifecycle.md#lifecycle-events).
//
// On the tree feed, the only event it acts on is node.purged for an environment: that
// environment's tasks are deleted. A purged project's environments are purged with it,
// each with its own event. Tasks are authored, so member.removed keeps them, and there is
// no background work to stop on node.deleted. On the user feed, the only one is
// user.deleted: that person's profile is deleted. Each feed has its own cursor, the last
// event id acted on, kept in SQLite and advanced after acting on each page; acting again
// on the same event changes nothing.
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
	// Interval is the time between polls while the feeds are caught up.
	Interval = 5 * time.Second
	pageSize = 100
)

// Event is the part of an event the API reads, from either feed.
type Event struct {
	ID     string `json:"id"`
	Type   string `json:"type"`
	Kind   string `json:"kind"`
	NodeID string `json:"node_id"`
	UserID string `json:"user_id"`
}

// feed is one of identity's feeds: its name (its row in feed_cursors, and its logs'
// feed), its url, the key its pages list events under, and what the API does with each.
type feed struct {
	name string
	url  string
	key  string
	act  func(context.Context, Event) error
}

type Consumer struct {
	tree     feed
	user     feed
	db       *sql.DB
	tasks    *tasks.Store
	profiles *profiles.Store
	client   *http.Client
	logger   zerolog.Logger
}

// New reads the feeds at identityURL, identity's internal base URL.
func New(identityURL string, db *sql.DB, tasks *tasks.Store, profiles *profiles.Store, logger zerolog.Logger) *Consumer {
	c := &Consumer{
		db:       db,
		tasks:    tasks,
		profiles: profiles,
		client:   &http.Client{Timeout: 10 * time.Second},
		logger:   logger.With().Str("component", "lifecycle").Logger(),
	}
	c.tree = feed{name: "tree", url: identityURL + "/internal/tree-events", key: "tree_events", act: c.actOnTree}
	c.user = feed{name: "user", url: identityURL + "/internal/user-events", key: "user_events", act: c.actOnUser}
	return c
}

// Run polls every Interval until ctx ends.
func (c *Consumer) Run(ctx context.Context) {
	ticker := time.NewTicker(Interval)
	defer ticker.Stop()
	for {
		c.Poll(ctx)
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

// Poll reads the tree feed to its end, then the user feed. A feed that fails is logged and
// doesn't stop the other; it's read again on the next poll.
func (c *Consumer) Poll(ctx context.Context) {
	for _, f := range []feed{c.tree, c.user} {
		if err := c.read(ctx, f); err != nil && ctx.Err() == nil {
			c.logger.Error().Err(err).Str("feed", f.name).Msg("lifecycle poll failed")
		}
	}
}

// read reads f from its cursor until it has no more, acting on each page and then
// advancing the cursor past it.
func (c *Consumer) read(ctx context.Context, f feed) error {
	cursor, err := c.cursor(ctx, f.name)
	if err != nil {
		return err
	}
	for {
		events, more, err := c.fetch(ctx, f, cursor)
		if err != nil {
			return err
		}
		for _, e := range events {
			if err := f.act(ctx, e); err != nil {
				return fmt.Errorf("event %s: %w", e.ID, err)
			}
		}
		if len(events) > 0 {
			cursor = events[len(events)-1].ID
			if err := c.saveCursor(ctx, f.name, cursor); err != nil {
				return err
			}
		}
		if !more {
			return nil
		}
	}
}

func (c *Consumer) actOnTree(ctx context.Context, e Event) error {
	if e.Type != "node.purged" || e.Kind != "environment" {
		return nil
	}
	if err := c.tasks.DeleteByEnvironment(ctx, e.NodeID); err != nil {
		return err
	}
	c.logger.Info().Str("event_id", e.ID).Str("node_id", e.NodeID).Msg("purged environment's tasks deleted")
	return nil
}

func (c *Consumer) actOnUser(ctx context.Context, e Event) error {
	if e.Type != "user.deleted" {
		return nil
	}
	if err := c.profiles.Delete(ctx, e.UserID); err != nil {
		return err
	}
	c.logger.Info().Str("event_id", e.ID).Str("user_id", e.UserID).Msg("profile deleted with its account")
	return nil
}

// fetch reads f's page after cursor: its events, and whether there are more.
func (c *Consumer) fetch(ctx context.Context, f feed, cursor string) ([]Event, bool, error) {
	q := url.Values{"limit": {fmt.Sprint(pageSize)}}
	if cursor != "" {
		q.Set("starting_after", cursor)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, f.url+"?"+q.Encode(), nil)
	if err != nil {
		return nil, false, err
	}
	res, err := c.client.Do(req)
	if err != nil {
		return nil, false, err
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return nil, false, fmt.Errorf("%s feed answered %d", f.name, res.StatusCode)
	}
	var page map[string]json.RawMessage
	if err := json.NewDecoder(res.Body).Decode(&page); err != nil {
		return nil, false, fmt.Errorf("%s feed: %w", f.name, err)
	}
	var events []Event
	if err := json.Unmarshal(page[f.key], &events); err != nil {
		return nil, false, fmt.Errorf("%s feed: %s: %w", f.name, f.key, err)
	}
	var more bool
	if err := json.Unmarshal(page["has_more"], &more); err != nil {
		return nil, false, fmt.Errorf("%s feed: has_more: %w", f.name, err)
	}
	return events, more, nil
}

// cursor is the last event id acted on in the feed, or "" before the first.
func (c *Consumer) cursor(ctx context.Context, feed string) (string, error) {
	start := time.Now()
	var id string
	err := c.db.QueryRowContext(ctx, `SELECT event_id FROM feed_cursors WHERE feed = ?`, feed).Scan(&id)
	if err == sql.ErrNoRows {
		err = nil
	}
	metrics.RecordDBOperation("feed_cursors.find", time.Since(start), err)
	return id, err
}

func (c *Consumer) saveCursor(ctx context.Context, feed, eventID string) error {
	start := time.Now()
	_, err := c.db.ExecContext(ctx, `
		INSERT INTO feed_cursors (feed, event_id) VALUES (?, ?)
		ON CONFLICT (feed) DO UPDATE SET event_id = excluded.event_id`, feed, eventID)
	metrics.RecordDBOperation("feed_cursors.save", time.Since(start), err)
	return err
}
