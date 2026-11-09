package plat5

import (
	"strings"
	"time"

	"github.com/oklog/ulid/v2"
)

// NewID is a new id: a lowercase ULID, as every Plat5 id is. Ids made in the same
// millisecond still sort in the order they were made.
func NewID() string { return strings.ToLower(ulid.Make().String()) }

// IsID says whether s is an id: a lowercase ULID.
func IsID(s string) bool {
	_, err := ulid.ParseStrict(s)
	return err == nil && s == strings.ToLower(s)
}

// Timestamp is t as the API writes times: RFC 3339 in UTC, to the millisecond, always three
// digits (2026-10-28T00:00:00.100Z).
func Timestamp(t time.Time) string { return t.UTC().Format("2006-01-02T15:04:05.000Z07:00") }
