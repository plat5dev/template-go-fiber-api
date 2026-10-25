// Package pagination reads list parameters the way every Plat5 list takes them.
//
// limit defaults to 50; over 100 is clamped. starting_after is the id of the last item on
// the previous page. Lists run in id order (ids are ULIDs, so oldest first) and answer
// with the collection and has_more. Contract: plat5 docs/reference/lists.md.
package pagination

import (
	"strconv"
	"strings"

	"github.com/gofiber/fiber/v3"
	"github.com/oklog/ulid/v2"

	"github.com/example/go-fiber-api/errors"
)

const (
	DefaultLimit = 50
	MaxLimit     = 100
)

// Page is one request's limit and cursor. An empty StartingAfter is the first page.
type Page struct {
	Limit         int
	StartingAfter string
}

// Parse reads limit and starting_after. A limit under 1 or not a number, or a cursor
// that isn't a lowercase ULID, is a 422. A well-formed cursor that isn't in the list is not an
// error: the walk continues after it in id order.
func Parse(c fiber.Ctx) (Page, error) {
	page := Page{Limit: DefaultLimit}
	if v := strings.TrimSpace(c.Query("limit")); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 {
			return Page{}, errors.FieldError("limit", "")
		}
		page.Limit = min(n, MaxLimit)
	}
	if v := strings.TrimSpace(c.Query("starting_after")); v != "" {
		if _, err := ulid.ParseStrict(v); err != nil || v != strings.ToLower(v) {
			return Page{}, errors.FieldError("starting_after", "")
		}
		page.StartingAfter = strings.Clone(v)
	}
	return page, nil
}

// Trim cuts a result fetched with Limit+1 rows down to the page, and says whether
// another page exists.
func Trim[T any](rows []T, page Page) ([]T, bool) {
	if len(rows) > page.Limit {
		return rows[:page.Limit], true
	}
	return rows, false
}
