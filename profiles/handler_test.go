package profiles

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gofiber/fiber/v3"

	"github.com/example/go-fiber-api/audit"
	"github.com/example/go-fiber-api/db"
	"github.com/example/go-fiber-api/errors"
)

const user = "01jd8x3k7q00000000000000uu"

func TestProfile(t *testing.T) {
	t.Setenv("DATABASE_PATH", filepath.Join(t.TempDir(), "app.db"))
	sqlDB, err := db.Connect(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { sqlDB.Close() })
	if err := db.Migrate(context.Background(), sqlDB); err != nil {
		t.Fatal(err)
	}
	h := NewHandler(NewStore(sqlDB), nil)
	app := fiber.New(fiber.Config{ErrorHandler: errors.FiberErrorHandler})
	app.Get("/users/:user_id/profile", h.Get)
	app.Put("/users/:user_id/profile", h.Upsert)

	res, err := app.Test(httptest.NewRequest("GET", "/users/"+user+"/profile", nil))
	if err != nil {
		t.Fatal(err)
	}
	var p Profile
	if err := json.NewDecoder(res.Body).Decode(&p); err != nil {
		t.Fatal(err)
	}
	if res.StatusCode != 200 || p.UserID != user || p.Bio != "" {
		t.Fatalf("get: status %d, profile %+v", res.StatusCode, p)
	}

	req := httptest.NewRequest("PUT", "/users/"+user+"/profile", strings.NewReader(`{"bio":"Hi"}`))
	req.Header.Set("Content-Type", "application/json")
	res, err = app.Test(req)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.NewDecoder(res.Body).Decode(&p); err != nil {
		t.Fatal(err)
	}
	if res.StatusCode != 200 || p.Bio != "Hi" {
		t.Fatalf("put: status %d, profile %+v", res.StatusCode, p)
	}
	if got, want := res.Header.Get(audit.DetailsHeader), `{"bio":{"from":"","to":"Hi"}}`; got != want {
		t.Fatalf("put details = %s, want %s", got, want)
	}
}

func newApp(t *testing.T) *fiber.App {
	t.Helper()
	t.Setenv("DATABASE_PATH", filepath.Join(t.TempDir(), "app.db"))
	sqlDB, err := db.Connect(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { sqlDB.Close() })
	if err := db.Migrate(context.Background(), sqlDB); err != nil {
		t.Fatal(err)
	}
	h := NewHandler(NewStore(sqlDB), nil)
	app := fiber.New(fiber.Config{ErrorHandler: errors.FiberErrorHandler})
	app.Put("/users/:user_id/profile", h.Upsert)
	return app
}

func put(t *testing.T, app *fiber.App, contentType, body string) (int, string, string) {
	t.Helper()
	req := httptest.NewRequest("PUT", "/users/"+user+"/profile", strings.NewReader(body))
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	res, err := app.Test(req)
	if err != nil {
		t.Fatal(err)
	}
	var out struct {
		Error struct {
			Message string `json:"message"`
			Details struct {
				Fields []struct {
					Path string `json:"path"`
				} `json:"fields"`
			} `json:"details"`
		} `json:"error"`
	}
	_ = json.NewDecoder(res.Body).Decode(&out)
	path := ""
	if len(out.Error.Details.Fields) == 1 {
		path = out.Error.Details.Fields[0].Path
	}
	return res.StatusCode, path, out.Error.Message
}

// A bio not sent as JSON is a 422 at body.
func TestProfileBodyMustBeJSON(t *testing.T) {
	app := newApp(t)
	for _, contentType := range []string{"", "text/plain"} {
		if status, path, msg := put(t, app, contentType, `{"bio":"Hi"}`); status != 422 || path != "body" || msg != errors.FallbackValidation {
			t.Fatalf("%q: %d at %q, %q", contentType, status, path, msg)
		}
	}
}

// A bio's limit counts characters, not bytes.
func TestBioLimitIsCharacters(t *testing.T) {
	app := newApp(t)
	if status, _, _ := put(t, app, "application/json", `{"bio":"`+strings.Repeat("é", 2000)+`"}`); status != 200 {
		t.Fatalf("2000 characters: %d", status)
	}
	if status, path, msg := put(t, app, "application/json", `{"bio":"`+strings.Repeat("é", 2001)+`"}`); status != 422 || path != "bio" || msg != "Bio is too long." {
		t.Fatalf("2001 characters: %d at %q, %q", status, path, msg)
	}
}
