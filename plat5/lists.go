package plat5

import (
	"strconv"

	"github.com/gofiber/fiber/v3"
)

const (
	DefaultLimit = 50
	MaxLimit     = 100
)

// Page is one list request's limit and cursor. An empty StartingAfter is the first page.
type Page struct {
	Limit         int
	StartingAfter string
}

// ReadPage reads limit and starting_after, as every Plat5 list takes them: limit a whole
// number of at least 1 (over 100 is 100; absent is 50), starting_after an id. Each wrong
// one is a 422 at its name. A well-formed cursor that isn't in the list is not an error:
// the walk continues after it in id order.
func ReadPage(c fiber.Ctx) (Page, error) {
	page := Page{Limit: DefaultLimit}
	var bad FieldErrors
	query := c.RequestCtx().QueryArgs()
	if query.Has("limit") {
		n, err := strconv.Atoi(string(query.Peek("limit")))
		if err != nil || n < 1 {
			bad.Add("limit", "")
		} else {
			page.Limit = min(n, MaxLimit)
		}
	}
	if query.Has("starting_after") {
		page.StartingAfter = string(query.Peek("starting_after"))
		if !IsID(page.StartingAfter) {
			bad.Add("starting_after", "")
		}
	}
	return page, bad.Err()
}

// Trim cuts rows, fetched with Limit+1, down to the page, and says whether another page
// follows.
func Trim[T any](rows []T, page Page) ([]T, bool) {
	if len(rows) > page.Limit {
		return rows[:page.Limit], true
	}
	return rows, false
}
