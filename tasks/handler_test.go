package tasks

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

const (
	project = "01jd8x3k7q0000000000000000"
	other   = "01jd8x3k7q0000000000000001"
	member  = "01jd8x3k7q00000000000000mm"
)

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
	r := app.Group("/projects/:project_id/tasks")
	r.Get("/", h.List)
	r.Post("/", h.Create)
	r.Get("/:task_id", h.Get)
	r.Patch("/:task_id", h.Update)
	r.Delete("/:task_id", h.Delete)
	return app
}

func do(t *testing.T, app *fiber.App, method, path, body string) *http.Response {
	t.Helper()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set(MemberHeader, member)
	res, err := app.Test(req)
	if err != nil {
		t.Fatal(err)
	}
	return res
}

func create(t *testing.T, app *fiber.App, projectID, title string) Task {
	t.Helper()
	res := do(t, app, "POST", "/projects/"+projectID+"/tasks", `{"title":"`+title+`"}`)
	if res.StatusCode != fiber.StatusCreated {
		t.Fatalf("create: status %d", res.StatusCode)
	}
	var task Task
	if err := json.NewDecoder(res.Body).Decode(&task); err != nil {
		t.Fatal(err)
	}
	return task
}

type listBody struct {
	Tasks   []Task `json:"tasks"`
	HasMore bool   `json:"has_more"`
}

func list(t *testing.T, app *fiber.App, query string) listBody {
	t.Helper()
	res := do(t, app, "GET", "/projects/"+project+"/tasks"+query, "")
	if res.StatusCode != 200 {
		t.Fatalf("list%s: status %d", query, res.StatusCode)
	}
	var out listBody
	if err := json.NewDecoder(res.Body).Decode(&out); err != nil {
		t.Fatal(err)
	}
	return out
}

func TestCreate(t *testing.T) {
	app := newApp(t)
	task := create(t, app, project, "Launch")
	if task.ProjectID != project || task.CreatedBy != member || task.Status != "todo" {
		t.Fatalf("task = %+v", task)
	}
	if len(task.ID) != 26 || task.ID != strings.ToLower(task.ID) {
		t.Fatalf("id %q isn't a lowercase ULID", task.ID)
	}
}

func TestCreateNeedsMember(t *testing.T) {
	app := newApp(t)
	req := httptest.NewRequest("POST", "/projects/"+project+"/tasks", strings.NewReader(`{"title":"Launch"}`))
	req.Header.Set("Content-Type", "application/json")
	res, err := app.Test(req)
	if err != nil {
		t.Fatal(err)
	}
	var body struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	if err := json.NewDecoder(res.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if res.StatusCode != fiber.StatusBadRequest || body.Error.Code != "INVALID_REQUEST" {
		t.Fatalf("status %d, code %q", res.StatusCode, body.Error.Code)
	}
}

func TestScopedByProject(t *testing.T) {
	app := newApp(t)
	task := create(t, app, other, "Elsewhere")
	for _, method := range []string{"GET", "PATCH", "DELETE"} {
		res := do(t, app, method, "/projects/"+project+"/tasks/"+task.ID, `{"title":"x"}`)
		if res.StatusCode != fiber.StatusNotFound {
			t.Fatalf("%s from another project: status %d", method, res.StatusCode)
		}
	}
	if all := list(t, app, ""); len(all.Tasks) != 0 {
		t.Fatalf("list = %+v", all)
	}
}

func TestListPages(t *testing.T) {
	app := newApp(t)
	var ids []string
	for _, title := range []string{"a", "b", "c"} {
		ids = append(ids, create(t, app, project, title).ID)
	}

	first := list(t, app, "?limit=2")
	if len(first.Tasks) != 2 || !first.HasMore || first.Tasks[0].ID != ids[0] {
		t.Fatalf("first page = %+v", first)
	}
	second := list(t, app, "?limit=2&starting_after="+first.Tasks[1].ID)
	if len(second.Tasks) != 1 || second.HasMore || second.Tasks[0].ID != ids[2] {
		t.Fatalf("second page = %+v", second)
	}
	if all := list(t, app, ""); len(all.Tasks) != 3 || all.HasMore {
		t.Fatalf("default page = %+v", all)
	}
}

func TestAuditDetails(t *testing.T) {
	app := newApp(t)

	res := do(t, app, "POST", "/projects/"+project+"/tasks", `{"title":"Launch"}`)
	var task Task
	if err := json.NewDecoder(res.Body).Decode(&task); err != nil {
		t.Fatal(err)
	}
	if got := res.Header.Get(audit.CreatedHeader); got != task.ID {
		t.Fatalf("created = %q, want %q", got, task.ID)
	}
	if got, want := res.Header.Get(audit.DetailsHeader), `{"status":"todo","task_id":"`+task.ID+`","title":"Launch"}`; got != want {
		t.Fatalf("create details = %s, want %s", got, want)
	}

	path := "/projects/" + project + "/tasks/" + task.ID
	res = do(t, app, "PATCH", path, `{"status":"done"}`)
	if got, want := res.Header.Get(audit.DetailsHeader), `{"status":{"from":"todo","to":"done"}}`; got != want {
		t.Fatalf("update details = %s, want %s", got, want)
	}

	res = do(t, app, "PATCH", path, `{"status":"done"}`)
	if got := res.Header.Get(audit.DetailsHeader); got != "" {
		t.Fatalf("no-op update details = %s, want none", got)
	}

	res = do(t, app, "DELETE", path, "")
	if res.StatusCode != fiber.StatusNoContent || res.Header.Get(audit.DetailsHeader) != "" {
		t.Fatalf("delete: status %d, details %q", res.StatusCode, res.Header.Get(audit.DetailsHeader))
	}
}
