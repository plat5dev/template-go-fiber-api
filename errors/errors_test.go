package errors

import (
	"encoding/json"
	"io"
	"net/http/httptest"
	"testing"

	"github.com/gofiber/fiber/v3"
)

func send(t *testing.T, app *fiber.App, method, path string) (int, string, map[string]any) {
	t.Helper()
	req := httptest.NewRequest(method, path, nil)
	req.Header.Set("X-Request-ID", "req-1")
	res, err := app.Test(req)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(res.Body)
	var body map[string]any
	if err := json.Unmarshal(b, &body); err != nil {
		t.Fatalf("%s %s: %s", method, path, b)
	}
	return res.StatusCode, res.Header.Get("Allow"), body
}

// A known path in a method it doesn't serve, OPTIONS included, is the 404 of an unknown
// route, with no Allow header.
func TestWrongMethodIsUnknownRoute(t *testing.T) {
	app := fiber.New(fiber.Config{ErrorHandler: FiberErrorHandler})
	app.Get("/things/:id", func(c fiber.Ctx) error { return c.SendStatus(200) })

	status, allow, want := send(t, app, "GET", "/nope")
	if status != 404 || allow != "" {
		t.Fatalf("unknown route: status %d, Allow %q", status, allow)
	}
	wantJSON, _ := json.Marshal(want)
	if got := string(wantJSON); got != `{"error":{"code":"NOT_FOUND","details":{"id":null,"resource":"resource"},"message":"Resource not found.","request_id":"req-1","type":"invalid_request_error"}}` {
		t.Fatalf("unknown route body = %s", got)
	}
	for _, method := range []string{"PUT", "PATCH", "DELETE", "POST", "OPTIONS"} {
		status, allow, body := send(t, app, method, "/things/1")
		gotJSON, _ := json.Marshal(body)
		if status != 404 || allow != "" || string(gotJSON) != string(wantJSON) {
			t.Fatalf("%s: status %d, Allow %q, body %s", method, status, allow, gotJSON)
		}
	}
}

// An unexpected framework error is a 500 with the fallback sentence, never its own text.
func TestFrameworkTextNeverInMessage(t *testing.T) {
	app := fiber.New(fiber.Config{ErrorHandler: FiberErrorHandler})
	app.Get("/teapot", func(c fiber.Ctx) error { return fiber.NewError(fiber.StatusTeapot, "I'm a teapot internals") })
	status, _, body := send(t, app, "GET", "/teapot")
	e := body["error"].(map[string]any)
	if status != 500 || e["code"] != "INTERNAL_ERROR" || e["message"] != "An unexpected error occurred." {
		t.Fatalf("status %d, error %v", status, e)
	}
}
