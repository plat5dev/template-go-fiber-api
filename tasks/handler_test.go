package tasks

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/gofiber/fiber/v3"
	"github.com/rs/zerolog"
	"go.opentelemetry.io/otel/propagation"

	"github.com/example/go-fiber-api/access"
	"github.com/example/go-fiber-api/audit"
	"github.com/example/go-fiber-api/db"
	"github.com/example/go-fiber-api/errors"
)

const (
	environment = "01jd8x3k7q0000000000000000"
	other       = "01jd8x3k7q0000000000000001"
	project     = "01jd8x3k7q00000000000000pp"
	member      = "01jd8x3k7q00000000000000mm"
	assignee    = "01jd8x3k7q00000000000000aa"
	// permission isn't the starter's, to show the check asks for the configured one.
	permission = "tasks:assignable"
)

// identity answers every access evaluation with status and body, and records each body
// it's sent.
type identity struct {
	mu     sync.Mutex
	status int
	body   string
	asked  []string
}

func (f *identity) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if r.Method != http.MethodPost || r.URL.Path != "/access/v1/evaluation" {
		http.NotFound(w, r)
		return
	}
	b, _ := io.ReadAll(r.Body)
	f.asked = append(f.asked, string(b))
	w.WriteHeader(f.status)
	_, _ = io.WriteString(w, f.body)
}

func (f *identity) answer(status int, body string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.status, f.body = status, body
}

func (f *identity) calls() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.asked)
}

// evaluation is what identity should be asked for an assignee on the project.
func evaluation(assigneeID string) string {
	return `{"subject":{"type":"member","id":"` + assigneeID + `"},"resource":{"type":"project","id":"` + project + `"},"action":{"name":"` + permission + `"}}`
}

// newApp serves the task routes, with identity allowing every assignee until told otherwise.
func newApp(t *testing.T) (*fiber.App, *identity) {
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
	id := &identity{status: http.StatusOK, body: `{"decision":true}`}
	srv := httptest.NewServer(id)
	t.Cleanup(srv.Close)
	h := NewHandler(NewStore(sqlDB), nil, access.New(srv.URL, propagation.TraceContext{}, zerolog.Nop()), permission)
	app := fiber.New(fiber.Config{ErrorHandler: errors.FiberErrorHandler})
	r := app.Group("/environments/:environment_id/tasks")
	r.Get("/", h.List)
	r.Post("/", h.Create)
	r.Get("/:task_id", h.Get)
	r.Patch("/:task_id", h.Update)
	r.Delete("/:task_id", h.Delete)
	return app, id
}

// do sends what the gateway sends on an environment route: the caller's member id and the
// environment's project id.
func do(t *testing.T, app *fiber.App, method, path, body string) *http.Response {
	t.Helper()
	return doWithout(t, app, "", method, path, body)
}

// doWithout is do without the header named.
func doWithout(t *testing.T, app *fiber.App, header, method, path, body string) *http.Response {
	t.Helper()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set(MemberHeader, member)
	req.Header.Set(ProjectHeader, project)
	req.Header.Del(header)
	res, err := app.Test(req)
	if err != nil {
		t.Fatal(err)
	}
	return res
}

type errorBody struct {
	Error struct {
		Code    string `json:"code"`
		Message string `json:"message"`
		Details struct {
			Fields []struct {
				Path    string `json:"path"`
				Message string `json:"message"`
			} `json:"fields"`
		} `json:"details"`
	} `json:"error"`
}

func decodeError(t *testing.T, res *http.Response) errorBody {
	t.Helper()
	var body errorBody
	if err := json.NewDecoder(res.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	return body
}

func decodeTask(t *testing.T, res *http.Response) Task {
	t.Helper()
	var task Task
	if err := json.NewDecoder(res.Body).Decode(&task); err != nil {
		t.Fatal(err)
	}
	return task
}

func create(t *testing.T, app *fiber.App, environmentID, title string) Task {
	t.Helper()
	res := do(t, app, "POST", "/environments/"+environmentID+"/tasks", `{"title":"`+title+`"}`)
	if res.StatusCode != fiber.StatusCreated {
		t.Fatalf("create: status %d", res.StatusCode)
	}
	return decodeTask(t, res)
}

type listBody struct {
	Tasks   []Task `json:"tasks"`
	HasMore bool   `json:"has_more"`
}

func list(t *testing.T, app *fiber.App, query string) listBody {
	t.Helper()
	res := do(t, app, "GET", "/environments/"+environment+"/tasks"+query, "")
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
	app, _ := newApp(t)
	task := create(t, app, environment, "Launch")
	if task.EnvironmentID != environment || task.CreatedBy != member || task.Status != "todo" || task.AssigneeID != nil {
		t.Fatalf("task = %+v", task)
	}
	if len(task.ID) != 26 || task.ID != strings.ToLower(task.ID) {
		t.Fatalf("id %q isn't a lowercase ULID", task.ID)
	}
}

func TestCreateNeedsMember(t *testing.T) {
	app, _ := newApp(t)
	res := doWithout(t, app, MemberHeader, "POST", "/environments/"+environment+"/tasks", `{"title":"Launch"}`)
	if body := decodeError(t, res); res.StatusCode != fiber.StatusBadRequest || body.Error.Code != "INVALID_REQUEST" {
		t.Fatalf("status %d, code %q", res.StatusCode, body.Error.Code)
	}
}

func TestScopedByEnvironment(t *testing.T) {
	app, _ := newApp(t)
	task := create(t, app, other, "Elsewhere")
	for _, method := range []string{"GET", "PATCH", "DELETE"} {
		res := do(t, app, method, "/environments/"+environment+"/tasks/"+task.ID, `{"title":"x"}`)
		if res.StatusCode != fiber.StatusNotFound {
			t.Fatalf("%s from another environment: status %d", method, res.StatusCode)
		}
	}
	if all := list(t, app, ""); len(all.Tasks) != 0 {
		t.Fatalf("list = %+v", all)
	}
}

func TestListPages(t *testing.T) {
	app, _ := newApp(t)
	var ids []string
	for _, title := range []string{"a", "b", "c"} {
		ids = append(ids, create(t, app, environment, title).ID)
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
	app, _ := newApp(t)

	res := do(t, app, "POST", "/environments/"+environment+"/tasks", `{"title":"Launch"}`)
	task := decodeTask(t, res)
	if got := res.Header.Get(audit.CreatedHeader); got != task.ID {
		t.Fatalf("created = %q, want %q", got, task.ID)
	}
	if got, want := res.Header.Get(audit.DetailsHeader), `{"assignee_id":null,"status":"todo","task_id":"`+task.ID+`","title":"Launch"}`; got != want {
		t.Fatalf("create details = %s, want %s", got, want)
	}

	path := "/environments/" + environment + "/tasks/" + task.ID
	res = do(t, app, "PATCH", path, `{"status":"done"}`)
	if got, want := res.Header.Get(audit.DetailsHeader), `{"status":{"from":"todo","to":"done"}}`; got != want {
		t.Fatalf("update details = %s, want %s", got, want)
	}

	res = do(t, app, "PATCH", path, `{"assignee_id":"`+assignee+`"}`)
	if got, want := res.Header.Get(audit.DetailsHeader), `{"assignee_id":{"from":null,"to":"`+assignee+`"}}`; got != want {
		t.Fatalf("assign details = %s, want %s", got, want)
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

func get(t *testing.T, app *fiber.App, taskID string) Task {
	t.Helper()
	res := do(t, app, "GET", "/environments/"+environment+"/tasks/"+taskID, "")
	if res.StatusCode != 200 {
		t.Fatalf("get: status %d", res.StatusCode)
	}
	return decodeTask(t, res)
}

func assigned(task Task) string {
	if task.AssigneeID == nil {
		return "null"
	}
	return *task.AssigneeID
}

func TestCreateWithAssignee(t *testing.T) {
	app, id := newApp(t)
	res := do(t, app, "POST", "/environments/"+environment+"/tasks", `{"title":"Launch","assignee_id":"`+assignee+`"}`)
	if res.StatusCode != fiber.StatusCreated {
		t.Fatalf("status %d", res.StatusCode)
	}
	task := decodeTask(t, res)
	if assigned(task) != assignee || assigned(get(t, app, task.ID)) != assignee {
		t.Fatalf("assignee_id = %s", assigned(task))
	}
	// The assignee, on the project from the header, for the configured permission.
	if got := id.calls(); !slices.Equal(got, []string{evaluation(assignee)}) {
		t.Fatalf("identity asked %q", got)
	}
}

func TestRefusedAssignee(t *testing.T) {
	app, id := newApp(t)
	task := create(t, app, environment, "Launch")
	id.answer(http.StatusOK, `{"decision":false}`)

	for _, tc := range []struct{ method, path string }{
		{"POST", "/environments/" + environment + "/tasks"},
		{"PATCH", "/environments/" + environment + "/tasks/" + task.ID},
	} {
		res := do(t, app, tc.method, tc.path, `{"title":"Other","assignee_id":"`+assignee+`"}`)
		body := decodeError(t, res)
		want := "That person can't see this project."
		if res.StatusCode != fiber.StatusUnprocessableEntity || body.Error.Code != "VALIDATION_ERROR" || body.Error.Message != want {
			t.Fatalf("%s: status %d, %+v", tc.method, res.StatusCode, body.Error)
		}
		if f := body.Error.Details.Fields; len(f) != 1 || f[0].Path != "assignee_id" || f[0].Message != want {
			t.Fatalf("%s: fields = %+v", tc.method, f)
		}
	}
	// Nothing written: no second task, and the first unchanged.
	if all := list(t, app, ""); len(all.Tasks) != 1 || all.Tasks[0].Title != "Launch" || all.Tasks[0].AssigneeID != nil {
		t.Fatalf("tasks = %+v", all.Tasks)
	}
	if got := id.calls(); !slices.Equal(got, []string{evaluation(assignee), evaluation(assignee)}) {
		t.Fatalf("identity asked %q", got)
	}
}

func TestNoAnswerIsUnavailable(t *testing.T) {
	for _, tc := range []struct {
		status int
		body   string
	}{
		{http.StatusInternalServerError, `{"error":{}}`},
		{http.StatusOK, `{}`},
	} {
		app, id := newApp(t)
		task := create(t, app, environment, "Launch")
		id.answer(tc.status, tc.body)

		for _, r := range []struct{ method, path string }{
			{"POST", "/environments/" + environment + "/tasks"},
			{"PATCH", "/environments/" + environment + "/tasks/" + task.ID},
		} {
			res := do(t, app, r.method, r.path, `{"title":"Other","assignee_id":"`+assignee+`"}`)
			if body := decodeError(t, res); res.StatusCode != fiber.StatusServiceUnavailable || body.Error.Code != "SERVICE_UNAVAILABLE" {
				t.Fatalf("identity %d %s, %s: status %d, code %q", tc.status, tc.body, r.method, res.StatusCode, body.Error.Code)
			}
		}
		if all := list(t, app, ""); len(all.Tasks) != 1 || all.Tasks[0].Title != "Launch" || all.Tasks[0].AssigneeID != nil {
			t.Fatalf("identity %d %s: tasks = %+v", tc.status, tc.body, all.Tasks)
		}
	}
}

func TestUpdateAssignee(t *testing.T) {
	app, id := newApp(t)
	task := create(t, app, environment, "Launch")
	path := "/environments/" + environment + "/tasks/" + task.ID

	res := do(t, app, "PATCH", path, `{"assignee_id":"`+assignee+`"}`)
	if res.StatusCode != 200 || assigned(decodeTask(t, res)) != assignee {
		t.Fatalf("assign: status %d", res.StatusCode)
	}
	if got := id.calls(); !slices.Equal(got, []string{evaluation(assignee)}) {
		t.Fatalf("identity asked %q", got)
	}

	// Absent leaves the assignee, and null unassigns: neither asks identity.
	res = do(t, app, "PATCH", path, `{"title":"Renamed"}`)
	if res.StatusCode != 200 || assigned(decodeTask(t, res)) != assignee {
		t.Fatalf("rename: status %d", res.StatusCode)
	}
	res = do(t, app, "PATCH", path, `{"assignee_id":null}`)
	if res.StatusCode != 200 || assigned(decodeTask(t, res)) != "null" || assigned(get(t, app, task.ID)) != "null" {
		t.Fatalf("unassign: status %d", res.StatusCode)
	}
	if got := id.calls(); len(got) != 1 {
		t.Fatalf("identity asked %d times, want 1", len(got))
	}
}

func TestNoAssigneeNoCheck(t *testing.T) {
	app, id := newApp(t)
	base := "/environments/" + environment + "/tasks"
	// Without an assignee, the project header isn't needed either.
	for _, body := range []string{`{"title":"a"}`, `{"title":"b","assignee_id":null}`} {
		res := doWithout(t, app, ProjectHeader, "POST", base, body)
		if res.StatusCode != fiber.StatusCreated || decodeTask(t, res).AssigneeID != nil {
			t.Fatalf("create %s: status %d", body, res.StatusCode)
		}
	}
	task := list(t, app, "").Tasks[0]
	for _, body := range []string{`{"status":"done"}`, `{"assignee_id":null}`} {
		if res := doWithout(t, app, ProjectHeader, "PATCH", base+"/"+task.ID, body); res.StatusCode != 200 {
			t.Fatalf("update %s: status %d", body, res.StatusCode)
		}
	}
	if got := id.calls(); len(got) != 0 {
		t.Fatalf("identity asked %q", got)
	}
}

func TestAssigneeNeedsProject(t *testing.T) {
	app, id := newApp(t)
	task := create(t, app, environment, "Launch")
	for _, r := range []struct{ method, path string }{
		{"POST", "/environments/" + environment + "/tasks"},
		{"PATCH", "/environments/" + environment + "/tasks/" + task.ID},
	} {
		res := doWithout(t, app, ProjectHeader, r.method, r.path, `{"title":"Other","assignee_id":"`+assignee+`"}`)
		if body := decodeError(t, res); res.StatusCode != fiber.StatusBadRequest || body.Error.Code != "INVALID_REQUEST" {
			t.Fatalf("%s: status %d, code %q", r.method, res.StatusCode, body.Error.Code)
		}
	}
	if all := list(t, app, ""); len(all.Tasks) != 1 || all.Tasks[0].Title != "Launch" {
		t.Fatalf("tasks = %+v", all.Tasks)
	}
	if got := id.calls(); len(got) != 0 {
		t.Fatalf("identity asked %q", got)
	}
}

func TestBadAssignee(t *testing.T) {
	app, id := newApp(t)
	task := create(t, app, environment, "Launch")
	for _, value := range []string{`""`, `"someone"`, `"01JD8X3K7Q00000000000000AA"`, `5`, `{}`} {
		for _, r := range []struct{ method, path string }{
			{"POST", "/environments/" + environment + "/tasks"},
			{"PATCH", "/environments/" + environment + "/tasks/" + task.ID},
		} {
			res := do(t, app, r.method, r.path, `{"title":"Other","assignee_id":`+value+`}`)
			body := decodeError(t, res)
			if res.StatusCode != fiber.StatusUnprocessableEntity || body.Error.Code != "VALIDATION_ERROR" {
				t.Fatalf("%s %s: status %d, code %q", r.method, value, res.StatusCode, body.Error.Code)
			}
			if f := body.Error.Details.Fields; len(f) != 1 || f[0].Path != "assignee_id" {
				t.Fatalf("%s %s: fields = %+v", r.method, value, f)
			}
		}
	}
	if got := id.calls(); len(got) != 0 {
		t.Fatalf("identity asked %q", got)
	}
}

func TestUpdateOrder(t *testing.T) {
	app, id := newApp(t)
	missing := "/environments/" + environment + "/tasks/01jd8x3k7q00000000000000zz"

	// The body before the task, and the task before identity.
	if res := do(t, app, "PATCH", missing, `{"title":"","assignee_id":"`+assignee+`"}`); res.StatusCode != fiber.StatusUnprocessableEntity {
		t.Fatalf("bad body on a missing task: status %d", res.StatusCode)
	}
	if res := do(t, app, "PATCH", missing, `{"assignee_id":"`+assignee+`"}`); res.StatusCode != fiber.StatusNotFound {
		t.Fatalf("assign a missing task: status %d", res.StatusCode)
	}
	if res := doWithout(t, app, ProjectHeader, "PATCH", missing, `{"assignee_id":"`+assignee+`"}`); res.StatusCode != fiber.StatusNotFound {
		t.Fatalf("assign a missing task without a project: status %d", res.StatusCode)
	}
	if got := id.calls(); len(got) != 0 {
		t.Fatalf("identity asked %q", got)
	}
}

// send is do with the Content-Type given, none when empty.
func send(t *testing.T, app *fiber.App, contentType, method, path, body string) *http.Response {
	t.Helper()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Header.Del("Content-Type")
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	req.Header.Set(MemberHeader, member)
	req.Header.Set(ProjectHeader, project)
	res, err := app.Test(req)
	if err != nil {
		t.Fatal(err)
	}
	return res
}

// A body not sent as JSON is a 422 at body, whatever it holds; JSON with a charset or a
// +json type parses; a DELETE never reads its body.
func TestBodyMustBeJSON(t *testing.T) {
	app, _ := newApp(t)
	task := create(t, app, environment, "Launch")
	path := "/environments/" + environment + "/tasks"
	for _, tc := range []struct{ method, path, contentType, body string }{
		{"POST", path, "", `{"title":"x"}`},
		{"POST", path, "", ""},
		{"POST", path, "text/plain", `{"title":"x"}`},
		{"POST", path, "application/x-www-form-urlencoded", "title=x"},
		{"POST", path, "application/json", ""},
		{"PATCH", path + "/" + task.ID, "", `{"title":"x"}`},
		{"PATCH", path + "/" + task.ID, "", ""},
		{"PATCH", path + "/" + task.ID, "text/plain", `{"title":"x"}`},
	} {
		res := send(t, app, tc.contentType, tc.method, tc.path, tc.body)
		body := decodeError(t, res)
		if res.StatusCode != fiber.StatusUnprocessableEntity || len(body.Error.Details.Fields) != 1 || body.Error.Details.Fields[0].Path != "body" || body.Error.Message != errors.FallbackValidation {
			t.Fatalf("%s %q %q: status %d, error %+v", tc.method, tc.contentType, tc.body, res.StatusCode, body.Error)
		}
	}
	if got := get(t, app, task.ID); got.Title != "Launch" || got.UpdatedAt != task.UpdatedAt {
		t.Fatalf("a refused PATCH wrote: %+v", got)
	}
	for _, contentType := range []string{"application/json; charset=utf-8", "application/merge-patch+json"} {
		if res := send(t, app, contentType, "POST", path, `{"title":"x"}`); res.StatusCode != fiber.StatusCreated {
			t.Fatalf("POST as %q: status %d", contentType, res.StatusCode)
		}
	}
	if res := send(t, app, "application/json", "DELETE", path+"/"+task.ID, ""); res.StatusCode != fiber.StatusNoContent {
		t.Fatalf("DELETE with a JSON Content-Type and no body: status %d", res.StatusCode)
	}
}

// A title's limit counts characters, not bytes.
func TestTitleLimitIsCharacters(t *testing.T) {
	app, _ := newApp(t)
	path := "/environments/" + environment + "/tasks"
	if res := do(t, app, "POST", path, `{"title":"`+strings.Repeat("é", 255)+`"}`); res.StatusCode != fiber.StatusCreated {
		t.Fatalf("255 characters: status %d", res.StatusCode)
	}
	task := create(t, app, environment, "Launch")
	for _, tc := range []struct{ method, path string }{{"POST", path}, {"PATCH", path + "/" + task.ID}} {
		res := do(t, app, tc.method, tc.path, `{"title":"`+strings.Repeat("é", 256)+`"}`)
		if body := decodeError(t, res); res.StatusCode != fiber.StatusUnprocessableEntity || body.Error.Message != "Title is too long." {
			t.Fatalf("%s 256 characters: status %d, error %+v", tc.method, res.StatusCode, body.Error)
		}
	}
}

// A scoped write that finds no row, the task gone since it was read, is a 404.
func TestWriteFindsNoRow(t *testing.T) {
	t.Setenv("DATABASE_PATH", filepath.Join(t.TempDir(), "app.db"))
	sqlDB, err := db.Connect(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { sqlDB.Close() })
	if err := db.Migrate(context.Background(), sqlDB); err != nil {
		t.Fatal(err)
	}
	store := NewStore(sqlDB)
	ctx := context.Background()
	task := &Task{ID: "01jd8x3k7q00000000000000tt", EnvironmentID: environment, Title: "Launch", Status: "todo", CreatedBy: member, CreatedAt: "t", UpdatedAt: "t"}
	if err := store.Insert(ctx, task); err != nil {
		t.Fatal(err)
	}
	moved := *task
	moved.EnvironmentID = other
	if ok, err := store.Update(ctx, &moved); err != nil || ok {
		t.Fatalf("update in another environment: %v, %v", ok, err)
	}
	if ok, err := store.Delete(ctx, other, task.ID); err != nil || ok {
		t.Fatalf("delete in another environment: %v, %v", ok, err)
	}
	if ok, err := store.Update(ctx, task); err != nil || !ok {
		t.Fatalf("update: %v, %v", ok, err)
	}
	if ok, err := store.Delete(ctx, environment, task.ID); err != nil || !ok {
		t.Fatalf("delete: %v, %v", ok, err)
	}
	if ok, err := store.Update(ctx, task); err != nil || ok {
		t.Fatalf("update after delete: %v, %v", ok, err)
	}
	if ok, err := store.Delete(ctx, environment, task.ID); err != nil || ok {
		t.Fatalf("delete after delete: %v, %v", ok, err)
	}
}

// Keys match exactly: a wrong-case or unknown key is a 422 naming each, in the body's order,
// before any other check, and nothing is written.
func TestUnknownFields(t *testing.T) {
	app, id := newApp(t)
	task := create(t, app, environment, "Launch")
	path := "/environments/" + environment + "/tasks"
	for _, tc := range []struct {
		method, path, body string
		paths              []string
	}{
		{"POST", path, `{"Title":"x"}`, []string{"Title"}},
		{"POST", path, `{"title":"x","priority":1}`, []string{"priority"}},
		{"POST", path, `{"Status":"done","title":"","Assignee_ID":"` + assignee + `"}`, []string{"Status", "Assignee_ID"}},
		{"PATCH", path + "/" + task.ID, `{"Title":"x"}`, []string{"Title"}},
		{"PATCH", path + "/01jd8x3k7q00000000000000zz", `{"title":"x","extra":{"a":1}}`, []string{"extra"}},
	} {
		res := do(t, app, tc.method, tc.path, tc.body)
		body := decodeError(t, res)
		if res.StatusCode != fiber.StatusUnprocessableEntity || body.Error.Code != "VALIDATION_ERROR" || body.Error.Message != tc.paths[0]+" isn't a field." {
			t.Fatalf("%s %s: status %d, error %+v", tc.method, tc.body, res.StatusCode, body.Error)
		}
		f := body.Error.Details.Fields
		if len(f) != len(tc.paths) {
			t.Fatalf("%s %s: fields %+v", tc.method, tc.body, f)
		}
		for i, p := range tc.paths {
			if f[i].Path != p || f[i].Message != p+" isn't a field." {
				t.Fatalf("%s %s: fields[%d] = %+v", tc.method, tc.body, i, f[i])
			}
		}
	}
	if all := list(t, app, ""); len(all.Tasks) != 1 {
		t.Fatalf("tasks = %+v", all.Tasks)
	}
	if got := get(t, app, task.ID); got.Title != "Launch" || got.UpdatedAt != task.UpdatedAt {
		t.Fatalf("a refused PATCH wrote: %+v", got)
	}
	if got := id.calls(); len(got) != 0 {
		t.Fatalf("identity asked %q", got)
	}
	if res := do(t, app, "PATCH", path+"/"+task.ID, `{"title":"Ship","status":"done","assignee_id":null}`); res.StatusCode != 200 {
		t.Fatalf("valid PATCH: status %d", res.StatusCode)
	}
}
