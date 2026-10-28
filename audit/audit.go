// Package audit says what a write changed, for Plat5's audit log.
//
// The gateway records every audited request before it reaches this service. A write adds
// what it changed on X-Plat5-Audit-Details, and a create names the id it made on
// X-Plat5-Audit-Created. The gateway stores both with the event and strips them before
// the response reaches the client. Contract: plat5 docs/concepts/audit.md#what-a-service-adds.
//
// Changes are {"from", "to"} for the fields that changed. A create names what it made.
// A delete sends nothing: the route and its path say what happened. Never a secret.
package audit

import (
	"encoding/json"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/gofiber/fiber/v3"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"
)

const (
	DetailsHeader = "X-Plat5-Audit-Details"
	CreatedHeader = "X-Plat5-Audit-Created"
	// MaxBytes is the gateway's cap. Over it, the gateway would drop the details.
	MaxBytes = 4096
)

// Details is one write's change.
type Details map[string]any

// Change is a field's old and new value.
type Change struct {
	From any `json:"from"`
	To   any `json:"to"`
}

// Changed records field when from and to differ.
func (d Details) Changed(field string, from, to string) {
	if from != to {
		d[field] = Change{From: from, To: to}
	}
}

// ChangedNullable records field when from and to differ, nil being null.
func (d Details) ChangedNullable(field string, from, to *string) {
	if (from == nil) != (to == nil) || (from != nil && *from != *to) {
		d[field] = Change{From: from, To: to}
	}
}

// Set puts d on the response. Empty sets nothing, so a write that changed nothing sends
// no header. Over the cap sets nothing and notes it on the trace: the event still records
// the request, just not the change.
func Set(c fiber.Ctx, d Details) {
	if len(d) == 0 {
		return
	}
	value, err := Encode(d)
	if err != nil {
		trace.SpanFromContext(c.Context()).AddEvent("audit details not sent",
			trace.WithAttributes(attribute.String("reason", err.Error())))
		return
	}
	c.Set(DetailsHeader, value)
}

// SetCreated names the id a create produced. The audit log can be searched by it.
func SetCreated(c fiber.Ctx, id string) {
	c.Set(CreatedHeader, id)
}

// Encode is d as one JSON object of visible ASCII within MaxBytes. Non-ASCII is escaped
// as \u, which is still the same JSON.
func Encode(d Details) (string, error) {
	raw, err := json.Marshal(d)
	if err != nil {
		return "", err
	}
	var b strings.Builder
	b.Grow(len(raw))
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
	if b.Len() > MaxBytes {
		return "", fmt.Errorf("audit details are %d bytes, over %d", b.Len(), MaxBytes)
	}
	return b.String(), nil
}
