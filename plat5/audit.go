package plat5

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"unicode/utf8"

	"github.com/gofiber/fiber/v3"
)

// What a write tells Plat5's audit log, on its response: the gateway records each audited
// request before it reaches the API, stores these with its event, and strips them before
// the client sees the response.
const (
	AuditDetailsHeader = "X-Plat5-Audit-Details"
	AuditCreatedHeader = "X-Plat5-Audit-Created"
	// AuditMaxBytes is the gateway's cap on details: over it, it would drop them.
	AuditMaxBytes = 4096
)

// Details is what one write changed: a create names what it made; an update, each field
// that changed as a Change. Never a secret.
type Details map[string]any

// Change is a field's old and new value.
type Change struct {
	From any `json:"from"`
	To   any `json:"to"`
}

// Changed records field when from and to differ.
func (d Details) Changed(field string, from, to string) {
	if from != to {
		d[field] = Change{from, to}
	}
}

// ChangedNullable records field when from and to differ, nil being null.
func (d Details) ChangedNullable(field string, from, to *string) {
	if (from == nil) != (to == nil) || (from != nil && *from != *to) {
		d[field] = Change{from, to}
	}
}

// SetAuditDetails puts d on the response. Empty sets nothing, so a write that changed
// nothing sends no header; so does one over the cap, with a warning: the event still
// records the request, just not the change.
func SetAuditDetails(c fiber.Ctx, d Details) {
	if len(d) == 0 {
		return
	}
	value, err := EncodeAuditDetails(d)
	if err != nil {
		slog.WarnContext(c.Context(), "audit details not sent", "request_id", c.Get("X-Request-ID"), "error_message", err.Error())
		return
	}
	c.Set(AuditDetailsHeader, value)
}

// SetAuditCreated names the id a create made.
func SetAuditCreated(c fiber.Ctx, id string) { c.Set(AuditCreatedHeader, id) }

// EncodeAuditDetails is d as one JSON object of visible ASCII, its keys sorted, within
// AuditMaxBytes. What isn't ASCII is escaped as \u, which is still the same JSON.
func EncodeAuditDetails(d Details) (string, error) {
	raw, err := json.Marshal(d)
	if err != nil {
		return "", err
	}
	var b strings.Builder
	for _, r := range string(raw) {
		switch {
		case r < utf8.RuneSelf && r != 0x7f:
			b.WriteRune(r)
		case r > 0xFFFF:
			r -= 0x10000
			fmt.Fprintf(&b, `\u%04x\u%04x`, 0xD800+(r>>10), 0xDC00+(r&0x3FF))
		default:
			fmt.Fprintf(&b, `\u%04x`, r)
		}
	}
	if b.Len() > AuditMaxBytes {
		return "", fmt.Errorf("audit details are %d bytes, over %d", b.Len(), AuditMaxBytes)
	}
	return b.String(), nil
}
