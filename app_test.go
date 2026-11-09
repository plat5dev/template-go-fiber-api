package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/propagation"

	"github.com/example/go-fiber-api/access"
	"github.com/example/go-fiber-api/db"
	"github.com/example/go-fiber-api/plat5"
	"github.com/example/go-fiber-api/profiles"
	"github.com/example/go-fiber-api/tasks"
	"github.com/example/go-fiber-api/telemetry"
)

// The ids the gateway would send, and the permission the API checks an assignee for (not
// the starter's, to show the check asks for the configured one).
const (
	project    = "01jd8x3k7q0000000000000000"
	other      = "01jd8x3k7q0000000000000001"
	member     = "01jd8x3k7q00000000000000mm"
	assignee   = "01jd8x3k7q00000000000000aa"
	user       = "01jd8x3k7q00000000000000uu"
	missing    = "01jd8x3k7q00000000000000zz"
	permission = "tasks:assignable"

	tasksPath = "/projects/" + project + "/tasks"
	fallback  = plat5.FallbackValidation
)

// identity answers every access evaluation with status and body, records each body it's
// sent, and the traceparent it came with.
type identity struct {
	mu          sync.Mutex
	status      int
	body        string
	asked       []string
	traceparent string
}

func (f *identity) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	b, _ := io.ReadAll(r.Body)
	f.asked = append(f.asked, string(b))
	f.traceparent = r.Header.Get("traceparent")
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

// evaluation is what identity should be asked for an assignee on the project in the path.
func evaluation(assigneeID string) string {
	return `{"subject":{"type":"member","id":"` + assigneeID + `"},"resource":{"type":"project","id":"` + project + `"},"action":{"name":"` + permission + `"}}`
}

type testApp struct {
	t        *testing.T
	identity *identity
	logs     *bytes.Buffer
	send     func(req *http.Request) *http.Response
}

// newTestApp serves the API, with identity allowing every assignee until told otherwise,
// and its log lines kept.
func newTestApp(t *testing.T) *testApp {
	t.Helper()
	sqlDB, err := db.Open(context.Background(), filepath.Join(t.TempDir(), "app.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { sqlDB.Close() })
	id := &identity{status: http.StatusOK, body: `{"decision":true}`}
	srv := httptest.NewServer(id)
	t.Cleanup(srv.Close)

	logs := &bytes.Buffer{}
	prev := slog.Default()
	slog.SetDefault(telemetry.NewLogger(logs))
	otel.SetTextMapPropagator(propagation.TraceContext{})
	t.Cleanup(func() { slog.SetDefault(prev) })

	app := newApp(tasks.NewHandler(tasks.NewStore(sqlDB), access.New(srv.URL), permission), profiles.NewHandler(profiles.NewStore(sqlDB)))
	return &testApp{t: t, identity: id, logs: logs, send: func(req *http.Request) *http.Response {
		t.Helper()
		res, err := app.Test(req)
		if err != nil {
			t.Fatal(err)
		}
		return res
	}}
}

// opt changes a request before it's sent.
type opt func(*http.Request)

func header(key, value string) opt { return func(r *http.Request) { r.Header.Set(key, value) } }
func without(key string) opt       { return func(r *http.Request) { r.Header.Del(key) } }

// do sends what the gateway sends: on a project route, the caller's member id; a body as
// application/json.
func (a *testApp) do(method, path, body string, opts ...opt) *http.Response {
	a.t.Helper()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Plat5-Member-Id", member)
	req.Header.Set("X-Plat5-Project-Id", project)
	for _, o := range opts {
		o(req)
	}
	return a.send(req)
}

func decode[T any](t *testing.T, res *http.Response) T {
	t.Helper()
	defer res.Body.Close()
	var v T
	if err := json.NewDecoder(res.Body).Decode(&v); err != nil {
		t.Fatalf("decode %d: %v", res.StatusCode, err)
	}
	return v
}

type errorBody struct {
	Error struct {
		Type      string  `json:"type"`
		Code      string  `json:"code"`
		Message   string  `json:"message"`
		RequestID *string `json:"request_id"`
		Details   struct {
			Fields []plat5.FieldError `json:"fields"`
		} `json:"details"`
	} `json:"error"`
}

// expectFields checks res is a 422 naming fields, the first one's sentence the message.
func expectFields(t *testing.T, res *http.Response, label string, fields ...plat5.FieldError) {
	t.Helper()
	body := decode[errorBody](t, res)
	if res.StatusCode != http.StatusUnprocessableEntity || body.Error.Code != "VALIDATION_ERROR" {
		t.Fatalf("%s: status %d, %+v", label, res.StatusCode, body.Error)
	}
	if !slices.Equal(body.Error.Details.Fields, fields) || body.Error.Message != fields[0].Message {
		t.Fatalf("%s: %q, fields %+v; want %+v", label, body.Error.Message, body.Error.Details.Fields, fields)
	}
}

func expectField(t *testing.T, res *http.Response, label, path, message string) {
	t.Helper()
	expectFields(t, res, label, plat5.FieldError{Path: path, Message: message})
}

// expectMalformed checks res is the 400 for a request malformed before its values are
// read.
func expectMalformed(t *testing.T, res *http.Response, label string) {
	t.Helper()
	body := decode[errorBody](t, res)
	if res.StatusCode != http.StatusBadRequest || body.Error.Code != "INVALID_REQUEST" || body.Error.Message != "Malformed request." {
		t.Fatalf("%s: status %d, %+v", label, res.StatusCode, body.Error)
	}
}

// requestLines are the request lines logged so far.
func (a *testApp) requestLines() []map[string]any {
	a.t.Helper()
	var out []map[string]any
	for sc := bufio.NewScanner(bytes.NewReader(a.logs.Bytes())); sc.Scan(); {
		var line map[string]any
		if err := json.Unmarshal(sc.Bytes(), &line); err != nil {
			a.t.Fatalf("not a JSON line: %q", sc.Text())
		}
		if line["message"] == "request completed" {
			out = append(out, line)
		}
	}
	return out
}

// An unknown route, or a known path in a method it doesn't serve (OPTIONS included), is
// the 404 envelope; HEAD on a GET route is the GET without its body, and a trailing slash
// is the same route.
func TestUnknownRoutes(t *testing.T) {
	a := newTestApp(t)
	task := decode[tasks.Task](t, a.do("POST", tasksPath, `{"title":"Launch"}`))
	taskPath := tasksPath + "/" + task.ID
	for _, r := range [][2]string{
		{"PUT", taskPath}, {"POST", taskPath}, {"DELETE", tasksPath}, {"PATCH", tasksPath},
		{"PATCH", "/users/" + user + "/profile"}, {"OPTIONS", tasksPath}, {"OPTIONS", taskPath},
		{"GET", "/nope"}, {"GET", "/users/" + user}, {"GET", "/"},
	} {
		res := a.do(r[0], r[1], "{}", header("X-Request-ID", "req-9"))
		if res.Header.Get("Allow") != "" || !strings.HasPrefix(res.Header.Get("Content-Type"), "application/json") {
			t.Fatalf("%s %s: Allow %q, Content-Type %q", r[0], r[1], res.Header.Get("Allow"), res.Header.Get("Content-Type"))
		}
		raw, _ := io.ReadAll(res.Body)
		want := `{"error":{"type":"invalid_request_error","code":"NOT_FOUND","message":"Resource not found.","request_id":"req-9","details":{"resource":"resource","id":null}}}`
		if res.StatusCode != http.StatusNotFound || string(raw) != want {
			t.Fatalf("%s %s: %d %s", r[0], r[1], res.StatusCode, raw)
		}
	}
	res := a.do("HEAD", tasksPath, "")
	if raw, _ := io.ReadAll(res.Body); res.StatusCode != http.StatusOK || len(raw) != 0 {
		t.Fatalf("HEAD: %d %q", res.StatusCode, raw)
	}
	if res := a.do("GET", tasksPath+"/", ""); res.StatusCode != http.StatusOK {
		t.Fatalf("trailing slash: %d", res.StatusCode)
	}
}

// request_id is X-Request-ID as sent, or null; the envelope's keys are in its order.
func TestRequestID(t *testing.T) {
	a := newTestApp(t)
	for _, tc := range []struct {
		sent opt
		want string
	}{
		{without("X-Request-ID"), "null"},
		{header("X-Request-ID", ""), "null"},
		{header("X-Request-ID", "req-1"), `"req-1"`},
	} {
		raw, _ := io.ReadAll(a.do("GET", tasksPath+"/"+missing, "", tc.sent).Body)
		want := `{"error":{"type":"invalid_request_error","code":"NOT_FOUND","message":"Resource not found.","request_id":` + tc.want + `,"details":{"resource":"task","id":"` + missing + `"}}}`
		if string(raw) != want {
			t.Fatalf("got %s, want %s", raw, want)
		}
	}
}

// The request line: the route's template or unknown, warn on a 4xx, and the ids the front
// door sent.
func TestRequestLog(t *testing.T) {
	a := newTestApp(t)
	a.do("GET", tasksPath, "")
	a.do("GET", tasksPath+"/"+missing, "", header("X-Request-ID", "req-2"))
	a.do("GET", "/nope/"+missing, "")
	a.do("PATCH", "/users/"+user+"/profile", "{}", without("X-Plat5-Member-Id"), without("X-Plat5-Project-Id"), header("X-Plat5-User-Id", user))
	lines := a.requestLines()
	if len(lines) != 4 {
		t.Fatalf("%d lines", len(lines))
	}
	ok, notFound, unknown, wrongMethod := lines[0], lines[1], lines[2], lines[3]
	if ok["route"] != "/projects/:project_id/tasks" || ok["level"] != "info" || ok["node_id"] != project || ok["member_id"] != member || ok["request_id"] != nil {
		t.Fatalf("ok: %v", ok)
	}
	if notFound["route"] != "/projects/:project_id/tasks/:task_id" || notFound["level"] != "warn" || notFound["request_id"] != "req-2" || notFound["status"] != 404.0 {
		t.Fatalf("not found: %v", notFound)
	}
	if unknown["route"] != "unknown" || unknown["level"] != "warn" {
		t.Fatalf("unknown: %v", unknown)
	}
	if wrongMethod["route"] != "unknown" || wrongMethod["user_id"] != user || wrongMethod["node_id"] != nil {
		t.Fatalf("wrong method: %v", wrongMethod)
	}
}
