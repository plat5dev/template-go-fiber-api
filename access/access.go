// Package access asks identity whether a member holds a permission on a node, for a member
// who isn't the caller (plat5 docs/guides/write-a-service.md#checking-access-outside-a-request).
//
// Each question is one evaluation on identity's internal port (plat5
// docs/reference/api/internal.md#access-evaluation). Only a 200 with a boolean decision is
// an answer. Anything else is an error, logged with the request id: no answer is no.
package access

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/propagation"
)

// Client asks identity at its internal base URL. With no URL, every question fails: there's
// no one to ask.
type Client struct {
	url  string
	http *http.Client
}

// New asks identity at identityURL.
func New(identityURL string) *Client {
	return &Client{url: identityURL, http: &http.Client{Timeout: 10 * time.Second}}
}

type entity struct {
	Type string `json:"type"`
	ID   string `json:"id"`
}

type evaluation struct {
	Subject  entity `json:"subject"`
	Resource entity `json:"resource"`
	Action   struct {
		Name string `json:"name"`
	} `json:"action"`
}

// Evaluate asks whether memberID holds permission on the node nodeID, whose kind is kind.
// It sends requestID as X-Request-ID, and ctx's trace as traceparent.
func (c *Client) Evaluate(ctx context.Context, requestID, memberID, kind, nodeID, permission string) (bool, error) {
	e := evaluation{Subject: entity{"member", memberID}, Resource: entity{kind, nodeID}}
	e.Action.Name = permission
	decision, err := c.evaluate(ctx, requestID, e)
	if err != nil {
		slog.ErrorContext(ctx, "access evaluation failed", "component", "access", "request_id", requestID, "error_message", err.Error())
	}
	return decision, err
}

func (c *Client) evaluate(ctx context.Context, requestID string, e evaluation) (bool, error) {
	if c.url == "" {
		return false, errors.New("IDENTITY_INTERNAL_URL is unset")
	}
	body, err := json.Marshal(e)
	if err != nil {
		return false, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.url+"/access/v1/evaluation", bytes.NewReader(body))
	if err != nil {
		return false, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Request-ID", requestID)
	otel.GetTextMapPropagator().Inject(ctx, propagation.HeaderCarrier(req.Header))
	res, err := c.http.Do(req)
	if err != nil {
		return false, err
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return false, fmt.Errorf("access evaluation answered %d", res.StatusCode)
	}
	var answer struct {
		Decision *bool `json:"decision"`
	}
	if err := json.NewDecoder(res.Body).Decode(&answer); err != nil {
		return false, fmt.Errorf("access evaluation: %w", err)
	}
	if answer.Decision == nil {
		return false, errors.New("access evaluation answered without a decision")
	}
	return *answer.Decision, nil
}
