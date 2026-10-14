package projects

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gofiber/fiber/v3"

	"github.com/example/go-fiber-api/audit"
	"github.com/example/go-fiber-api/db"
	"github.com/example/go-fiber-api/errors"
)

const org = "01JD8X3K7Q0000000000000000"

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
	r := app.Group("/organizations/:organization_id/projects")
	r.Get("/", h.List)
	r.Post("/", h.Create)
	r.Patch("/:project_id", h.Update)
	r.Delete("/:project_id", h.Delete)
	return app
}

func do(t *testing.T, app *fiber.App, method, path, body string) *http.Response {
	t.Helper()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	res, err := app.Test(req)
	if err != nil {
		t.Fatal(err)
	}
	return res
}

type listBody struct {
	Projects []Project `json:"projects"`
	HasMore  bool      `json:"has_more"`
}

func list(t *testing.T, app *fiber.App, query string) listBody {
	t.Helper()
	res := do(t, app, "GET", "/organizations/"+org+"/projects"+query, "")
	if res.StatusCode != 200 {
		t.Fatalf("list%s: status %d", query, res.StatusCode)
	}
	var out listBody
	if err := json.NewDecoder(res.Body).Decode(&out); err != nil {
		t.Fatal(err)
	}
	return out
}

func TestListPages(t *testing.T) {
	app := newApp(t)
	var ids []string
	for _, name := range []string{"a", "b", "c"} {
		res := do(t, app, "POST", "/organizations/"+org+"/projects", `{"name":"`+name+`"}`)
		var p Project
		if err := json.NewDecoder(res.Body).Decode(&p); err != nil {
			t.Fatal(err)
		}
		ids = append(ids, p.ID)
	}

	first := list(t, app, "?limit=2")
	if len(first.Projects) != 2 || !first.HasMore || first.Projects[0].ID != ids[0] {
		t.Fatalf("first page = %+v", first)
	}
	second := list(t, app, "?limit=2&starting_after="+first.Projects[1].ID)
	if len(second.Projects) != 1 || second.HasMore || second.Projects[0].ID != ids[2] {
		t.Fatalf("second page = %+v", second)
	}
	if all := list(t, app, ""); len(all.Projects) != 3 || all.HasMore {
		t.Fatalf("default page = %+v", all)
	}
}

func TestAuditDetails(t *testing.T) {
	app := newApp(t)

	res := do(t, app, "POST", "/organizations/"+org+"/projects", `{"name":"Launch"}`)
	var p Project
	if err := json.NewDecoder(res.Body).Decode(&p); err != nil {
		t.Fatal(err)
	}
	if got := res.Header.Get(audit.CreatedHeader); got != p.ID {
		t.Fatalf("created = %q, want %q", got, p.ID)
	}
	if got, want := res.Header.Get(audit.DetailsHeader), `{"name":"Launch","project_id":"`+p.ID+`"}`; got != want {
		t.Fatalf("create details = %s, want %s", got, want)
	}

	res = do(t, app, "PATCH", "/organizations/"+org+"/projects/"+p.ID, `{"name":"Launch v2"}`)
	if got, want := res.Header.Get(audit.DetailsHeader), `{"name":{"from":"Launch","to":"Launch v2"}}`; got != want {
		t.Fatalf("update details = %s, want %s", got, want)
	}

	res = do(t, app, "PATCH", "/organizations/"+org+"/projects/"+p.ID, `{"name":"Launch v2"}`)
	if got := res.Header.Get(audit.DetailsHeader); got != "" {
		t.Fatalf("no-op update details = %s, want none", got)
	}

	res = do(t, app, "DELETE", "/organizations/"+org+"/projects/"+p.ID, "")
	if res.StatusCode != fiber.StatusNoContent || res.Header.Get(audit.DetailsHeader) != "" {
		t.Fatalf("delete: status %d, details %q", res.StatusCode, res.Header.Get(audit.DetailsHeader))
	}
}
