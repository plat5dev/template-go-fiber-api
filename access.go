package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/propagation"
)

// accessClient evaluates permissions against Plat5 identity's internal API (fail-closed).
type accessClient struct {
	baseURL string
	http    *http.Client
}

func newAccessClient(identityURL string) *accessClient {
	return &accessClient{
		baseURL: strings.TrimRight(identityURL, "/"),
		http:    &http.Client{Timeout: 10 * time.Second},
	}
}

type evalEntity struct {
	Type string `json:"type"`
	ID   string `json:"id"`
}

type evalRequest struct {
	Subject  evalEntity `json:"subject"`
	Resource evalEntity `json:"resource"`
	Action   struct {
		Name string `json:"name"`
	} `json:"action"`
}

func (c *accessClient) evaluate(ctx context.Context, requestID, memberID, kind, nodeID, permission string) (bool, error) {
	reqBody := evalRequest{
		Subject:  evalEntity{Type: "member", ID: memberID},
		Resource: evalEntity{Type: kind, ID: nodeID},
	}
	reqBody.Action.Name = permission

	decision, err := c.doEvaluate(ctx, requestID, reqBody)
	if err != nil {
		slog.ErrorContext(ctx, "access evaluation failed", "component", "access", "request_id", requestID, "error_message", err.Error())
	}
	return decision, err
}

func (c *accessClient) doEvaluate(ctx context.Context, requestID string, payload evalRequest) (bool, error) {
	if c.baseURL == "" {
		return false, errors.New("IDENTITY_INTERNAL_URL is unset")
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return false, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/access/v1/evaluation", bytes.NewReader(body))
	if err != nil {
		return false, err
	}
	req.Header.Set("Content-Type", "application/json")
	if requestID != "" {
		req.Header.Set("X-Request-ID", requestID)
	}
	otel.GetTextMapPropagator().Inject(ctx, propagation.HeaderCarrier(req.Header))

	res, err := c.http.Do(req)
	if err != nil {
		return false, err
	}
	defer res.Body.Close()

	if res.StatusCode != http.StatusOK {
		return false, fmt.Errorf("access evaluation returned %d", res.StatusCode)
	}
	var answer struct {
		Decision *bool `json:"decision"`
	}
	if err := json.NewDecoder(res.Body).Decode(&answer); err != nil {
		return false, fmt.Errorf("decode access evaluation: %w", err)
	}
	if answer.Decision == nil {
		return false, errors.New("access evaluation response missing decision")
	}
	return *answer.Decision, nil
}
