package pagination

import (
	"encoding/json"
	"net/http/httptest"
	"testing"

	"github.com/gofiber/fiber/v3"

	"github.com/example/go-fiber-api/errors"
)

func TestParse(t *testing.T) {
	app := fiber.New(fiber.Config{ErrorHandler: errors.FiberErrorHandler})
	app.Get("/", func(c fiber.Ctx) error {
		page, err := Parse(c)
		if err != nil {
			return err
		}
		return c.JSON(fiber.Map{"limit": page.Limit, "starting_after": page.StartingAfter})
	})

	cases := []struct {
		query  string
		status int
		limit  int
		after  string
	}{
		{"", 200, DefaultLimit, ""},
		{"?limit=10", 200, 10, ""},
		{"?limit=500", 200, MaxLimit, ""},
		{"?starting_after=01jd8y2n5r7t9v1x3z5b7d9f1h", 200, DefaultLimit, "01jd8y2n5r7t9v1x3z5b7d9f1h"},
		{"?limit=0", 422, 0, ""},
		{"?limit=-1", 422, 0, ""},
		{"?limit=ten", 422, 0, ""},
		{"?starting_after=nope", 422, 0, ""},
		{"?starting_after=01JD8Y2N5R7T9V1X3Z5B7D9F1H", 422, 0, ""},
	}
	for _, tc := range cases {
		res, err := app.Test(httptest.NewRequest("GET", "/"+tc.query, nil))
		if err != nil {
			t.Fatal(err)
		}
		if res.StatusCode != tc.status {
			t.Fatalf("%s: status %d, want %d", tc.query, res.StatusCode, tc.status)
		}
		if tc.status != 200 {
			var body struct {
				Error struct {
					Code    string `json:"code"`
					Message string `json:"message"`
				} `json:"error"`
			}
			if err := json.NewDecoder(res.Body).Decode(&body); err != nil {
				t.Fatal(err)
			}
			if body.Error.Code != "VALIDATION_ERROR" || body.Error.Message != errors.FallbackValidation {
				t.Fatalf("%s: error = %+v", tc.query, body.Error)
			}
			continue
		}
		var body struct {
			Limit         int    `json:"limit"`
			StartingAfter string `json:"starting_after"`
		}
		if err := json.NewDecoder(res.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		if body.Limit != tc.limit || body.StartingAfter != tc.after {
			t.Fatalf("%s: got %+v", tc.query, body)
		}
	}
}

func TestTrim(t *testing.T) {
	page := Page{Limit: 2}
	if rows, more := Trim([]int{1, 2, 3}, page); len(rows) != 2 || !more {
		t.Fatalf("got %v %v", rows, more)
	}
	if rows, more := Trim([]int{1, 2}, page); len(rows) != 2 || more {
		t.Fatalf("got %v %v", rows, more)
	}
}
