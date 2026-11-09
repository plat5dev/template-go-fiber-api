package main

import (
	"encoding/json"
	"net/http"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/example/go-fiber-api/plat5"
	"github.com/example/go-fiber-api/tasks"
)

const statusSentence = "Status must be todo, in progress, or done."

func (a *testApp) create(projectID, title string) tasks.Task {
	a.t.Helper()
	res := a.do("POST", "/projects/"+projectID+"/tasks", `{"title":`+quote(title)+`}`)
	if res.StatusCode != http.StatusCreated {
		a.t.Fatalf("create: status %d", res.StatusCode)
	}
	return decode[tasks.Task](a.t, res)
}

func (a *testApp) get(id string) tasks.Task {
	a.t.Helper()
	return decode[tasks.Task](a.t, a.do("GET", tasksPath+"/"+id, ""))
}

type taskList struct {
	Tasks   []tasks.Task `json:"tasks"`
	HasMore bool         `json:"has_more"`
}

func (a *testApp) list(query string) taskList {
	a.t.Helper()
	return decode[taskList](a.t, a.do("GET", tasksPath+query, ""))
}

func quote(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}

func assigneeOf(t tasks.Task) string {
	if t.AssigneeID == nil {
		return "null"
	}
	return *t.AssigneeID
}

// Both routes that take a task's body.
func writes(a *testApp) [][2]string {
	task := a.create(project, "Launch")
	return [][2]string{{"POST", tasksPath}, {"PATCH", tasksPath + "/" + task.ID}}
}

var millis = regexp.MustCompile(`^\d{4}-\d\d-\d\dT\d\d:\d\d:\d\d\.\d{3}Z$`)

func TestCreate(t *testing.T) {
	a := newTestApp(t)
	task := a.create(project, "Launch")
	if task.ProjectID != project || task.CreatedBy != member || task.Status != "todo" || task.AssigneeID != nil || !plat5.IsID(task.ID) {
		t.Fatalf("task = %+v", task)
	}
	if !millis.MatchString(task.CreatedAt) || task.UpdatedAt != task.CreatedAt {
		t.Fatalf("times %q, %q", task.CreatedAt, task.UpdatedAt)
	}
	if got := a.get(task.ID); got != task {
		t.Fatalf("read back %+v", got)
	}
}

// Without X-Plat5-Member-Id a create is a 400, before its body is read.
func TestCreateNeedsTheMember(t *testing.T) {
	a := newTestApp(t)
	for _, body := range []string{`{"title":"Launch"}`, `{"title":""}`, `{`} {
		expectMalformed(t, a.do("POST", tasksPath, body, without("X-Plat5-Member-Id")), body)
	}
}

func TestScopedByProject(t *testing.T) {
	a := newTestApp(t)
	task := a.create(other, "Elsewhere")
	for _, method := range []string{"GET", "PATCH", "DELETE"} {
		if res := a.do(method, tasksPath+"/"+task.ID, `{"title":"x"}`); res.StatusCode != http.StatusNotFound {
			t.Fatalf("%s: status %d", method, res.StatusCode)
		}
	}
	if got := a.list(""); len(got.Tasks) != 0 {
		t.Fatalf("listed %+v", got.Tasks)
	}
}

func TestListPages(t *testing.T) {
	a := newTestApp(t)
	var ids []string
	// Made in the same millisecond or not, they list in the order they were made.
	for i := range 10 {
		ids = append(ids, a.create(project, string(rune('a'+i))).ID)
	}
	first := a.list("?limit=4")
	second := a.list("?limit=100&starting_after=" + first.Tasks[3].ID)
	var got []string
	for _, task := range append(first.Tasks, second.Tasks...) {
		got = append(got, task.ID)
	}
	if !slices.Equal(got, ids) || !first.HasMore || second.HasMore {
		t.Fatalf("pages %v (%v, %v), want %v", got, first.HasMore, second.HasMore, ids)
	}
}

func TestListParameters(t *testing.T) {
	a := newTestApp(t)
	a.create(project, "a")
	a.create(project, "b")
	for query, want := range map[string]int{"": 2, "?limit=1": 1, "?limit=001": 1, "?limit=1000": 2, "?starting_after=7zzzzzzzzzzzzzzzzzzzzzzzzz": 0} {
		if got := a.list(query); len(got.Tasks) != want {
			t.Fatalf("%s: %d tasks, want %d", query, len(got.Tasks), want)
		}
	}
	for _, limit := range []string{"", "abc", "1.5", "0", "-1", "%201"} {
		expectField(t, a.do("GET", tasksPath+"?limit="+limit, ""), limit, "limit", fallback)
	}
	for _, after := range []string{"", "nope", "01JD8X3K7Q0000000000000000", "81jd8x3k7q0000000000000000", "01jd8x3k7q000000000000000i"} {
		expectField(t, a.do("GET", tasksPath+"?starting_after="+after, ""), after, "starting_after", fallback)
	}
	expectFields(t, a.do("GET", tasksPath+"?limit=x&starting_after=x", ""), "both",
		plat5.FieldError{Path: "limit", Message: fallback}, plat5.FieldError{Path: "starting_after", Message: fallback})
}

func TestTitleAndStatus(t *testing.T) {
	a := newTestApp(t)
	for _, w := range writes(a) {
		for _, title := range []string{"", "   ", "\t\u3000\n"} {
			expectField(t, a.do(w[0], w[1], `{"title":`+quote(title)+`}`), w[0], "title", "Title is required.")
		}
		expectField(t, a.do(w[0], w[1], `{"title":"`+strings.Repeat("x", 256)+`"}`), w[0], "title", "Title is too long.")
		for _, status := range []string{`"TODO"`, `"in progress"`, `" done"`, `""`, `5`} {
			expectField(t, a.do(w[0], w[1], `{"title":"Launch","status":`+status+`}`), w[0]+" "+status, "status", statusSentence)
		}
	}
	// A create without a title; a null one, or a null status, is the wrong type.
	expectField(t, a.do("POST", tasksPath, `{}`), "no title", "title", "Title is required.")
	expectField(t, a.do("POST", tasksPath, `{"title":null}`), "null title", "title", fallback)
	expectField(t, a.do("POST", tasksPath, `{"title":"a","status":null}`), "null status", "status", statusSentence)
}

func TestTitlesAreTrimmed(t *testing.T) {
	a := newTestApp(t)
	if task := a.create(project, " \t\u00a0Launch\u3000 "); task.Title != "Launch" {
		t.Fatalf("title %q", task.Title)
	}
	task := a.create(project, "Launch")
	if got := decode[tasks.Task](t, a.do("PATCH", tasksPath+"/"+task.ID, `{"title":"  Renamed  "}`)); got.Title != "Renamed" {
		t.Fatalf("title %q", got.Title)
	}
}

// A title's limit counts characters, not bytes.
func TestTitleLimitIsCharacters(t *testing.T) {
	a := newTestApp(t)
	for _, title := range []string{strings.Repeat("é", 255), strings.Repeat("😀", 255)} {
		if got := a.get(a.create(project, title).ID); got.Title != title {
			t.Fatalf("read back %q", got.Title)
		}
	}
	for _, title := range []string{strings.Repeat("é", 256), strings.Repeat("😀", 256)} {
		expectField(t, a.do("POST", tasksPath, `{"title":`+quote(title)+`}`), "long", "title", "Title is too long.")
	}
}

func TestWrongTypes(t *testing.T) {
	a := newTestApp(t)
	for _, w := range writes(a) {
		for _, tc := range []struct {
			body   string
			fields []plat5.FieldError
		}{
			{`{"title":123}`, []plat5.FieldError{{Path: "title", Message: fallback}}},
			{`{"title":true}`, []plat5.FieldError{{Path: "title", Message: fallback}}},
			{`{"title":["a"]}`, []plat5.FieldError{{Path: "title", Message: fallback}}},
			{`{"title":"Launch","status":5}`, []plat5.FieldError{{Path: "status", Message: statusSentence}}},
			// Every wrong field, title, status, then assignee_id.
			{`{"assignee_id":5,"status":5,"title":""}`, []plat5.FieldError{
				{Path: "title", Message: "Title is required."},
				{Path: "status", Message: statusSentence},
				{Path: "assignee_id", Message: fallback},
			}},
		} {
			expectFields(t, a.do(w[0], w[1], tc.body), w[0]+" "+tc.body, tc.fields...)
		}
	}
}

// PATCH: an absent or null title or status leaves it, and a no-op still answers the task.
func TestUpdateNullLeaves(t *testing.T) {
	a := newTestApp(t)
	task := a.create(project, "Launch")
	for _, body := range []string{`{"title":null,"status":null}`, `{}`} {
		res := a.do("PATCH", tasksPath+"/"+task.ID, body)
		got := decode[tasks.Task](t, res)
		if res.StatusCode != http.StatusOK || got.Title != "Launch" || got.Status != "todo" || res.Header.Get(plat5.AuditDetailsHeader) != "" {
			t.Fatalf("%s: %d %+v", body, res.StatusCode, got)
		}
	}
	got := decode[tasks.Task](t, a.do("PATCH", tasksPath+"/"+task.ID, `{"title":"Ship","status":"done"}`))
	if got.Title != "Ship" || got.Status != "done" || !millis.MatchString(got.UpdatedAt) {
		t.Fatalf("%+v", got)
	}
}

// The body must be application/json (any parameters, any case), and JSON.
func TestBodyMustBeJSON(t *testing.T) {
	a := newTestApp(t)
	ws := writes(a)
	for _, ct := range []string{"", "text/plain", "application/x-www-form-urlencoded", "application/jsonx", "application/merge-patch+json", "not a type"} {
		for _, w := range ws {
			expectMalformed(t, a.do(w[0], w[1], `{"title":"Other"}`, header("Content-Type", ct)), w[0]+" "+ct)
		}
	}
	for _, body := range []string{"", " ", "{", `{"title":"a"} x`} {
		for _, w := range ws {
			expectMalformed(t, a.do(w[0], w[1], body), w[0]+" "+body)
		}
	}
	for _, body := range []string{`[{"title":"a"}]`, `"a"`, "5", "true", "null"} {
		for _, w := range ws {
			expectField(t, a.do(w[0], w[1], body), w[0]+" "+body, "body", fallback)
		}
	}
	for _, ct := range []string{"application/json", "Application/JSON", "application/json;charset=UTF-8;", `application/json; charset="utf-8"`} {
		if res := a.do("POST", tasksPath, `{"title":"Launch"}`, header("Content-Type", ct)); res.StatusCode != http.StatusCreated {
			t.Fatalf("%s: %d", ct, res.StatusCode)
		}
	}
	if got := a.list(""); len(got.Tasks) != 5 || got.Tasks[0].Title != "Launch" {
		t.Fatalf("written: %+v", got.Tasks)
	}
}

// Keys match exactly: an unknown one is a 422 naming it, before any value is checked.
func TestUnknownFields(t *testing.T) {
	a := newTestApp(t)
	task := a.create(project, "Launch")
	for _, tc := range []struct{ method, path, body, key string }{
		{"POST", tasksPath, `{"Title":"x"}`, "Title"},
		{"POST", tasksPath, `{"title":"x","priority":1}`, "priority"},
		{"POST", tasksPath, `{"Status":"done","title":"","Assignee_ID":"` + assignee + `"}`, "Status"},
		{"POST", tasksPath, `{"title":5,"x":1}`, "x"},
		{"PATCH", tasksPath + "/" + task.ID, `{"Title":"x"}`, "Title"},
		// A missing task: the body first.
		{"PATCH", tasksPath + "/" + missing, `{"title":"x","extra":{"a":1}}`, "extra"},
	} {
		expectField(t, a.do(tc.method, tc.path, tc.body), tc.body, tc.key, tc.key+" isn't a field.")
	}
	if got := a.get(task.ID); got != task || len(a.list("").Tasks) != 1 || len(a.identity.calls()) != 0 {
		t.Fatalf("written: %+v", got)
	}
	// A repeated key is allowed: the last one counts.
	if got := decode[tasks.Task](t, a.do("PATCH", tasksPath+"/"+task.ID, `{"title":"a","title":"Ship"}`)); got.Title != "Ship" {
		t.Fatalf("title %q", got.Title)
	}
}

func TestAuditDetails(t *testing.T) {
	a := newTestApp(t)
	res := a.do("POST", tasksPath, `{"title":"<é😀&>"}`)
	task := decode[tasks.Task](t, res)
	if res.Header.Get(plat5.AuditCreatedHeader) != task.ID {
		t.Fatalf("created %q", res.Header.Get(plat5.AuditCreatedHeader))
	}
	// Sorted keys, HTML and what isn't ASCII escaped.
	want := `{"assignee_id":null,"status":"todo","task_id":"` + task.ID + `","title":"\u003c\u00e9\ud83d\ude00\u0026\u003e"}`
	if got := res.Header.Get(plat5.AuditDetailsHeader); got != want {
		t.Fatalf("details %s, want %s", got, want)
	}
	path := tasksPath + "/" + task.ID
	for _, tc := range []struct{ body, want string }{
		{`{"status":"done"}`, `{"status":{"from":"todo","to":"done"}}`},
		{`{"assignee_id":"` + assignee + `"}`, `{"assignee_id":{"from":null,"to":"` + assignee + `"}}`},
		{`{"assignee_id":null}`, `{"assignee_id":{"from":"` + assignee + `","to":null}}`},
		{`{"status":"done"}`, ``},
	} {
		if got := a.do("PATCH", path, tc.body).Header.Get(plat5.AuditDetailsHeader); got != tc.want {
			t.Fatalf("%s: details %s, want %s", tc.body, got, tc.want)
		}
	}
	res = a.do("DELETE", path, "")
	if res.StatusCode != http.StatusNoContent || res.Header.Get(plat5.AuditDetailsHeader) != "" {
		t.Fatalf("delete: %d %q", res.StatusCode, res.Header.Get(plat5.AuditDetailsHeader))
	}
	if res := a.do("DELETE", path, ""); res.StatusCode != http.StatusNotFound {
		t.Fatalf("delete again: %d", res.StatusCode)
	}
}

// A DELETE never reads its body.
func TestDeleteIgnoresItsBody(t *testing.T) {
	a := newTestApp(t)
	for _, tc := range [][2]string{{"application/json", ""}, {"application/json", "{"}, {"text/plain", "hello"}, {"", "x"}} {
		task := a.create(project, "Launch")
		if res := a.do("DELETE", tasksPath+"/"+task.ID, tc[1], header("Content-Type", tc[0])); res.StatusCode != http.StatusNoContent {
			t.Fatalf("%v: %d", tc, res.StatusCode)
		}
	}
}

// An assignee is asked about, on the project in the path, for the configured permission,
// with the request's X-Request-ID and trace.
func TestAssign(t *testing.T) {
	a := newTestApp(t)
	const traceparent = "00-0af7651916cd43dd8448eb211c80319c-b7ad6b7169203331-01"
	res := a.do("POST", tasksPath, `{"title":"Launch","assignee_id":"`+assignee+`"}`, header("traceparent", traceparent))
	task := decode[tasks.Task](t, res)
	if res.StatusCode != http.StatusCreated || assigneeOf(task) != assignee || assigneeOf(a.get(task.ID)) != assignee {
		t.Fatalf("create: %d %+v", res.StatusCode, task)
	}
	if got := a.identity.calls(); !slices.Equal(got, []string{evaluation(assignee)}) {
		t.Fatalf("identity asked %q", got)
	}
	if !strings.HasPrefix(a.identity.traceparent, "00-0af7651916cd43dd8448eb211c80319c-") {
		t.Fatalf("traceparent %q", a.identity.traceparent)
	}

	path := tasksPath + "/" + task.ID
	// Absent leaves the assignee, and null unassigns: neither asks.
	if got := decode[tasks.Task](t, a.do("PATCH", path, `{"title":"Renamed"}`)); assigneeOf(got) != assignee {
		t.Fatalf("rename: %+v", got)
	}
	if got := decode[tasks.Task](t, a.do("PATCH", path, `{"assignee_id":null}`)); got.AssigneeID != nil || a.get(task.ID).AssigneeID != nil {
		t.Fatalf("unassign: %+v", got)
	}
	if got := decode[tasks.Task](t, a.do("PATCH", path, `{"assignee_id":"`+assignee+`"}`)); assigneeOf(got) != assignee {
		t.Fatalf("assign: %+v", got)
	}
	a.do("POST", tasksPath, `{"title":"b","assignee_id":null}`)
	if got := a.identity.calls(); len(got) != 2 {
		t.Fatalf("identity asked %d times, want 2", len(got))
	}
}

func TestBadAssignee(t *testing.T) {
	a := newTestApp(t)
	for _, w := range writes(a) {
		for _, value := range []string{`""`, `"someone"`, `"01JD8X3K7Q00000000000000AA"`, `5`, `{}`} {
			expectField(t, a.do(w[0], w[1], `{"title":"Other","assignee_id":`+value+`}`), w[0]+" "+value, "assignee_id", fallback)
		}
	}
	if got := a.identity.calls(); len(got) != 0 {
		t.Fatalf("identity asked %q", got)
	}
}

// identity's false is a 422; no answer is a 503. Either way nothing is written.
func TestAssigneeRefused(t *testing.T) {
	for _, tc := range []struct {
		status int
		body   string
		want   int
	}{
		{http.StatusOK, `{"decision":false}`, http.StatusUnprocessableEntity},
		{http.StatusServiceUnavailable, `{"error":{"code":"SERVICE_UNAVAILABLE"}}`, http.StatusServiceUnavailable},
		{http.StatusInternalServerError, `{}`, http.StatusServiceUnavailable},
		{http.StatusOK, `{}`, http.StatusServiceUnavailable},
	} {
		a := newTestApp(t)
		ws := writes(a)
		a.identity.answer(tc.status, tc.body)
		for _, w := range ws {
			res := a.do(w[0], w[1], `{"title":"Other","assignee_id":"`+assignee+`"}`)
			if tc.want == http.StatusUnprocessableEntity {
				expectField(t, res, w[0], "assignee_id", "That person can't see this project.")
				continue
			}
			if body := decode[errorBody](t, res); res.StatusCode != tc.want || body.Error.Code != "SERVICE_UNAVAILABLE" || body.Error.Type != "api_error" {
				t.Fatalf("identity %d %s, %s: %d %+v", tc.status, tc.body, w[0], res.StatusCode, body.Error)
			}
		}
		if tc.want == http.StatusServiceUnavailable {
			lines := a.requestLines()
			if line := lines[len(lines)-1]; line["level"] != "error" || line["error_type"] != "network" {
				t.Fatalf("log %v", line)
			}
		}
		if got := a.list(""); len(got.Tasks) != 1 || got.Tasks[0].Title != "Launch" || got.Tasks[0].AssigneeID != nil {
			t.Fatalf("identity %d %s: tasks %+v", tc.status, tc.body, got.Tasks)
		}
	}
}

// On an update: the body, then the task's 404, then identity.
func TestUpdateOrder(t *testing.T) {
	a := newTestApp(t)
	expectField(t, a.do("PATCH", tasksPath+"/"+missing, `{"title":"","assignee_id":"`+assignee+`"}`), "body", "title", "Title is required.")
	if res := a.do("PATCH", tasksPath+"/"+missing, `{"assignee_id":"`+assignee+`"}`); res.StatusCode != http.StatusNotFound {
		t.Fatalf("missing task: %d", res.StatusCode)
	}
	if got := a.identity.calls(); len(got) != 0 {
		t.Fatalf("identity asked %q", got)
	}
}

// A body's size is the gateway's to limit: a large one is read like any other.
func TestNoBodyLimit(t *testing.T) {
	a := newTestApp(t)
	expectField(t, a.do("POST", tasksPath, `{"title":"`+strings.Repeat("x", 5<<20)+`"}`), "5 MiB", "title", "Title is too long.")
}
