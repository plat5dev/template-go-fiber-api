// Package lifecycle follows identity's lifecycle feed, so the API's data goes when its
// environment does (plat5 docs/concepts/lifecycle.md#lifecycle-events).
//
// The only event it acts on is node.purged for an environment: that environment's tasks
// are deleted. A purged project's environments are purged with it, each with its own
// event. Tasks are authored, so member.removed keeps them, and there is no background
// work to stop on node.deleted. The cursor is the last event id acted on, kept in SQLite
// and advanced after acting; acting again on the same event changes nothing.
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
	"github.com/example/go-fiber-api/tasks"
)

const (
	// Interval is the time between polls while the feed is caught up.
	Interval = 5 * time.Second
	pageSize = 100
)

// Event is the part of a lifecycle event the API reads.
type Event struct {
	ID     string `json:"id"`
	Type   string `json:"type"`
	Kind   string `json:"kind"`
	NodeID string `json:"node_id"`
}

type page struct {
	Events  []Event `json:"lifecycle_events"`
	HasMore bool    `json:"has_more"`
}

type Consumer struct {
	feedURL string
	db      *sql.DB
	tasks   *tasks.Store
	client  *http.Client
	logger  zerolog.Logger
}

// New reads the feed at identityURL, identity's internal base URL.
func New(identityURL string, db *sql.DB, tasks *tasks.Store, logger zerolog.Logger) *Consumer {
	return &Consumer{
		feedURL: identityURL + "/internal/lifecycle-events",
		db:      db,
		tasks:   tasks,
		client:  &http.Client{Timeout: 10 * time.Second},
		logger:  logger.With().Str("component", "lifecycle").Logger(),
	}
}

// Run polls every Interval until ctx ends. A failed poll is logged and tried again on the
// next tick.
func (c *Consumer) Run(ctx context.Context) {
	ticker := time.NewTicker(Interval)
	defer ticker.Stop()
	for {
		if err := c.Poll(ctx); err != nil && ctx.Err() == nil {
			c.logger.Error().Err(err).Msg("lifecycle poll failed")
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

// Poll reads from the cursor until the feed has no more, acting on each page and then
// advancing the cursor past it.
func (c *Consumer) Poll(ctx context.Context) error {
	cursor, err := c.cursor(ctx)
	if err != nil {
		return err
	}
	for {
		p, err := c.fetch(ctx, cursor)
		if err != nil {
			return err
		}
		for _, e := range p.Events {
			if err := c.act(ctx, e); err != nil {
				return fmt.Errorf("event %s: %w", e.ID, err)
			}
		}
		if len(p.Events) > 0 {
			cursor = p.Events[len(p.Events)-1].ID
			if err := c.saveCursor(ctx, cursor); err != nil {
				return err
			}
		}
		if !p.HasMore {
			return nil
		}
	}
}

func (c *Consumer) act(ctx context.Context, e Event) error {
	if e.Type != "node.purged" || e.Kind != "environment" {
		return nil
	}
	if err := c.tasks.DeleteByEnvironment(ctx, e.NodeID); err != nil {
		return err
	}
	c.logger.Info().Str("event_id", e.ID).Str("node_id", e.NodeID).Msg("purged environment's tasks deleted")
	return nil
}

func (c *Consumer) fetch(ctx context.Context, cursor string) (page, error) {
	q := url.Values{"limit": {fmt.Sprint(pageSize)}}
	if cursor != "" {
		q.Set("starting_after", cursor)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.feedURL+"?"+q.Encode(), nil)
	if err != nil {
		return page{}, err
	}
	res, err := c.client.Do(req)
	if err != nil {
		return page{}, err
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return page{}, fmt.Errorf("lifecycle feed answered %d", res.StatusCode)
	}
	var p page
	if err := json.NewDecoder(res.Body).Decode(&p); err != nil {
		return page{}, fmt.Errorf("lifecycle feed: %w", err)
	}
	return p, nil
}

// cursor is the last event id acted on, or "" before the first.
func (c *Consumer) cursor(ctx context.Context) (string, error) {
	start := time.Now()
	var id string
	err := c.db.QueryRowContext(ctx, `SELECT event_id FROM lifecycle_cursor WHERE id = 1`).Scan(&id)
	if err == sql.ErrNoRows {
		err = nil
	}
	metrics.RecordDBOperation("lifecycle_cursor.find", time.Since(start), err)
	return id, err
}

func (c *Consumer) saveCursor(ctx context.Context, eventID string) error {
	start := time.Now()
	_, err := c.db.ExecContext(ctx, `
		INSERT INTO lifecycle_cursor (id, event_id) VALUES (1, ?)
		ON CONFLICT (id) DO UPDATE SET event_id = excluded.event_id`, eventID)
	metrics.RecordDBOperation("lifecycle_cursor.save", time.Since(start), err)
	return err
}
