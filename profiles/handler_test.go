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
