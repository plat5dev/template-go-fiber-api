// Package access asks identity whether a member holds a permission on a node, for a member
// who isn't the caller (plat5 docs/guides/write-a-service.md#checking-access-outside-a-request).
//
// Each question is one evaluation on identity's internal port (plat5
// docs/reference/api/internal.md#access-evaluation). Only a 200 with a boolean decision is
// an answer. Anything else is an error, logged with the request id, and no answer is no.
package access

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/rs/zerolog"
	"go.opentelemetry.io/otel/propagation"
)

type Client struct {
	url        string
	client     *http.Client
	propagator propagation.TextMapPropagator
	logger     zerolog.Logger
}

// New asks identity at identityURL, its internal base URL. With identityURL empty, every
// question fails: there's no one to ask.
func New(identityURL string, propagator propagation.TextMapPropagator, logger zerolog.Logger) *Client {
	return &Client{
		url:        identityURL,
		client:     &http.Client{Timeout: 10 * time.Second},
		propagator: propagator,
		logger:     logger.With().Str("component", "access").Logger(),
	}
}

type entity struct {
	Type string `json:"type"`
	ID   string `json:"id"`
}

type action struct {
	Name string `json:"name"`
}

type evaluation struct {
	Subject  entity `json:"subject"`
	Resource entity `json:"resource"`
	Action   action `json:"action"`
}

// Evaluate asks whether memberID holds permission on the node nodeID, whose kind is kind.
// requestID is the request's X-Request-ID: sent to identity, and logged with a failure.
func (c *Client) Evaluate(ctx context.Context, requestID, memberID, kind, nodeID, permission string) (bool, error) {
	decision, err := c.evaluate(ctx, requestID, evaluation{
		Subject:  entity{Type: "member", ID: memberID},
		Resource: entity{Type: kind, ID: nodeID},
		Action:   action{Name: permission},
	})
	if err != nil {
		c.logger.Error().Err(err).Str("request_id", requestID).Msg("access evaluation failed")
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
	c.propagator.Inject(ctx, propagation.HeaderCarrier(req.Header))
	res, err := c.client.Do(req)
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
