package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

const (
	lifecycleTypes          = "node.purged,user.deleted"
	lifecycleRetryAfter     = 5 * time.Second
	lifecycleRequestTimeout = 40 * time.Second
	lifecyclePageSize       = 100
)

var errCursorAhead = errors.New("lifecycle cursor ahead of newest event")

type lifecycleEvent struct {
	ID     string `json:"id"`
	Type   string `json:"type"`
	Kind   string `json:"kind"`
	NodeID string `json:"node_id"`
	UserID string `json:"user_id"`
}

// lifecycleConsumer polls identity's internal lifecycle log and cleans up purged projects and deleted users.
type lifecycleConsumer struct {
	url    string
	db     *sql.DB
	client *http.Client
	retry  time.Duration
	log    *slog.Logger
}

func newLifecycleConsumer(identityURL string, db *sql.DB) *lifecycleConsumer {
	return &lifecycleConsumer{
		url:    strings.TrimRight(identityURL, "/") + "/internal/events",
		db:     db,
		client: &http.Client{Timeout: lifecycleRequestTimeout},
		retry:  lifecycleRetryAfter,
		log:    slog.With("component", "lifecycle"),
	}
}

func (c *lifecycleConsumer) run(ctx context.Context) {
	if cur, err := c.cursor(ctx); err == nil {
		if cur == "" {
			c.log.Info("following lifecycle log from first event")
		} else {
			c.log.Info("following lifecycle log", "after", cur)
		}
	}
	for ctx.Err() == nil {
		err := c.step(ctx)
		if err == nil || ctx.Err() != nil {
			continue
		}
		c.log.Error("lifecycle read failed", "error_message", err.Error(), "retry_in_ms", c.retry.Milliseconds())
		select {
		case <-ctx.Done():
		case <-time.After(c.retry):
		}
	}
}

func (c *lifecycleConsumer) step(ctx context.Context) error {
	cursor, err := c.cursor(ctx)
	if err != nil {
		return err
	}
	page, err := c.fetch(ctx, cursor)
	if errors.Is(err, errCursorAhead) {
		c.log.Warn("lifecycle log reset: clearing cursor to replay from start", "after", cursor)
		return c.deleteCursor(ctx)
	}
	if err != nil {
		return err
	}

	var events []lifecycleEvent
	for _, e := range page {
		if e.ID != "" {
			events = append(events, e)
		}
	}
	if len(events) == 0 {
		return nil
	}

	last := events[len(events)-1].ID
	if err := c.apply(ctx, events, last); err != nil {
		return err
	}
	c.log.Info("lifecycle page applied", "events", len(events), "after", cursor, "cursor", last)
	return nil
}

func (c *lifecycleConsumer) apply(ctx context.Context, events []lifecycleEvent, last string) error {
	tx, err := c.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	for _, e := range events {
		if err := c.handleEvent(ctx, tx, e); err != nil {
			return fmt.Errorf("event %s: %w", e.ID, err)
		}
	}
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO feed_cursors (id, event_id) VALUES (1, ?)
		 ON CONFLICT (id) DO UPDATE SET event_id = excluded.event_id`, last); err != nil {
		return err
	}
	return tx.Commit()
}

func (c *lifecycleConsumer) handleEvent(ctx context.Context, tx *sql.Tx, e lifecycleEvent) error {
	switch {
	case e.Type == "node.purged" && e.Kind == "project" && e.NodeID != "":
		if err := deleteProjectTasks(ctx, tx, e.NodeID); err != nil {
			return err
		}
		c.log.Info("purged project tasks deleted", "event_id", e.ID, "node_id", e.NodeID)
	case e.Type == "user.deleted" && e.UserID != "":
		if err := deleteProfile(ctx, tx, e.UserID); err != nil {
			return err
		}
		c.log.Info("deleted user profile removed", "event_id", e.ID, "user_id", e.UserID)
	}
	return nil
}

func (c *lifecycleConsumer) fetch(ctx context.Context, cursor string) ([]lifecycleEvent, error) {
	q := url.Values{
		"types": {lifecycleTypes},
		"limit": {strconv.Itoa(lifecyclePageSize)},
	}
	if cursor != "" {
		q.Set("after", cursor)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.url+"?"+q.Encode(), nil)
	if err != nil {
		return nil, err
	}
	res, err := c.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()

	switch res.StatusCode {
	case http.StatusNoContent:
		return nil, nil
	case http.StatusConflict:
		return nil, errCursorAhead
	case http.StatusOK:
	default:
		return nil, fmt.Errorf("lifecycle log returned %d", res.StatusCode)
	}

	var page struct {
		Events *[]lifecycleEvent `json:"events"`
	}
	if err := json.NewDecoder(res.Body).Decode(&page); err != nil {
		return nil, fmt.Errorf("decode lifecycle log: %w", err)
	}
	if page.Events == nil {
		return nil, errors.New("lifecycle log response missing events")
	}
	return *page.Events, nil
}

func (c *lifecycleConsumer) cursor(ctx context.Context) (string, error) {
	var id string
	err := c.db.QueryRowContext(ctx, `SELECT event_id FROM feed_cursors WHERE id = 1`).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return id, err
}

func (c *lifecycleConsumer) deleteCursor(ctx context.Context) error {
	_, err := c.db.ExecContext(ctx, `DELETE FROM feed_cursors`)
	return err
}
