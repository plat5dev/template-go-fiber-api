package main

import (
	"encoding/json"
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"
)

func (a *testApp) createTask(projectID, title string) Task {
	a.t.Helper()
	b, _ := json.Marshal(map[string]any{"title": title})
	res := a.do("POST", "/projects/"+projectID+"/tasks", string(b))
	if res.StatusCode != http.StatusCreated {
		a.t.Fatalf("create task: status %d", res.StatusCode)
	}
	return decodeBody[Task](a.t, res)
}

func TestTaskCRUDAndTenantIsolation(t *testing.T) {
	a := newTestApp(t)

	// Create
	created := a.createTask(testProject, "  Ship it  ")
	if created.Title != "Ship it" || created.Status != "todo" || created.ProjectID != testProject ||
		created.CreatedBy != testMember || created.AssigneeID != nil || !isID(created.ID) {
		t.Fatalf("unexpected task: %+v", created)
	}

	// Get
	got := decodeBody[Task](t, a.do("GET", tasksPath+"/"+created.ID, ""))
	if got != created {
		t.Fatalf("get mismatch: %+v != %+v", got, created)
	}

	// Cross-tenant isolation: another project gets 404 on GET/PATCH/DELETE and empty list.
	otherTask := a.createTask(testOtherProj, "Other tenant")
	for _, method := range []string{"GET", "PATCH", "DELETE"} {
		if res := a.do(method, tasksPath+"/"+otherTask.ID, `{"title":"hijack"}`); res.StatusCode != http.StatusNotFound {
			t.Fatalf("%s cross-tenant: got %d, want 404", method, res.StatusCode)
		}
	}

	// Delete
	if res := a.do("DELETE", tasksPath+"/"+created.ID, ""); res.StatusCode != http.StatusNoContent {
		t.Fatalf("delete: got %d, want 204", res.StatusCode)
	}
	if res := a.do("DELETE", tasksPath+"/"+created.ID, ""); res.StatusCode != http.StatusNotFound {
		t.Fatalf("delete again: got %d, want 404", res.StatusCode)
	}
}

func TestCreateRequiresMemberHeader(t *testing.T) {
	a := newTestApp(t)
	res := a.do("POST", tasksPath, `{"title":"Launch"}`, withoutHeader("X-Plat5-Member-Id"))
	body := decodeBody[errorBody](t, res)
	if res.StatusCode != http.StatusBadRequest || body.Error.Code != "INVALID_REQUEST" {
		t.Fatalf("expected 400 INVALID_REQUEST, got %d %+v", res.StatusCode, body.Error)
	}
}

func TestTaskListPaginationAndValidation(t *testing.T) {
	a := newTestApp(t)
	var ids []string
	for i := range 6 {
		ids = append(ids, a.createTask(testProject, string(rune('a'+i))).ID)
	}

	type listResp struct {
		Tasks   []Task `json:"tasks"`
		HasMore bool   `json:"has_more"`
	}
	p1 := decodeBody[listResp](t, a.do("GET", tasksPath+"?limit=4", ""))
	if len(p1.Tasks) != 4 || !p1.HasMore {
		t.Fatalf("page 1: len=%d has_more=%v", len(p1.Tasks), p1.HasMore)
	}
	p2 := decodeBody[listResp](t, a.do("GET", tasksPath+"?limit=200&starting_after="+p1.Tasks[3].ID, ""))
	if len(p2.Tasks) != 2 || p2.HasMore {
		t.Fatalf("page 2: len=%d has_more=%v", len(p2.Tasks), p2.HasMore)
	}
	var gotIDs []string
	for _, task := range append(p1.Tasks, p2.Tasks...) {
		gotIDs = append(gotIDs, task.ID)
	}
	if !slices.Equal(gotIDs, ids) {
		t.Fatalf("ids %v != %v", gotIDs, ids)
	}

	// Invalid pagination query params -> 422 naming both fields.
	res := a.do("GET", tasksPath+"?limit=0&starting_after=not-a-ulid", "")
	body := decodeBody[errorBody](t, res)
	if res.StatusCode != http.StatusUnprocessableEntity || len(body.Error.Details.Fields) != 2 {
		t.Fatalf("invalid pagination: %d %+v", res.StatusCode, body.Error)
	}
}

func TestTaskValidationAndJSONErrors(t *testing.T) {
	a := newTestApp(t)

	// Non-JSON Content-Type & malformed JSON -> 400
	for _, tc := range []struct {
		ct, body string
	}{
		{"text/plain", `{"title":"x"}`},
		{"application/json", ``},
		{"application/json", `{`},
		{"application/json", `{"title":"x"} trailing`},
	} {
		res := a.do("POST", tasksPath, tc.body, withHeader("Content-Type", tc.ct))
		if res.StatusCode != http.StatusBadRequest {
			t.Fatalf("(%q, %q): got %d, want 400", tc.ct, tc.body, res.StatusCode)
		}
	}

	// Non-object JSON, unknown field, wrong types, and field constraints -> 422
	for _, tc := range []struct {
		body, path string
	}{
		{`[]`, "body"},
		{`{"title":"x","extra":1}`, "extra"},
		{`{"title":123}`, "title"},
		{`{}`, "title"},
		{`{"title":"   "}`, "title"},
		{`{"title":"` + strings.Repeat("é", 256) + `"}`, "title"},
		{`{"title":"ok","status":"unknown"}`, "status"},
		{`{"title":"ok","assignee_id":"NOT_ULID"}`, "assignee_id"},
	} {
		res := a.do("POST", tasksPath, tc.body)
		body := decodeBody[errorBody](t, res)
		if res.StatusCode != http.StatusUnprocessableEntity || body.Error.Code != "VALIDATION_ERROR" ||
			len(body.Error.Details.Fields) == 0 || body.Error.Details.Fields[0].Path != tc.path {
			t.Fatalf("body %s: got %d %+v (want path %q)", tc.body, res.StatusCode, body.Error, tc.path)
		}
	}
}

func TestPatchNoOpDoesNotWriteOrBumpUpdatedAt(t *testing.T) {
	a := newTestApp(t)
	task := a.createTask(testProject, "Initial")

	// Wait past the 1ms timestamp resolution to ensure a DB write would bump updated_at.
	time.Sleep(5 * time.Millisecond)

	for _, payload := range []string{`{}`, `{"title":"Initial"}`, `{"status":"todo"}`, `{"assignee_id":null}`} {
		res := a.do("PATCH", tasksPath+"/"+task.ID, payload)
		got := decodeBody[Task](t, res)
		if res.StatusCode != http.StatusOK {
			t.Fatalf("no-op %s: status %d", payload, res.StatusCode)
		}
		if got.UpdatedAt != task.UpdatedAt {
			t.Fatalf("no-op %s bumped updated_at: %s -> %s", payload, task.UpdatedAt, got.UpdatedAt)
		}
		if res.Header.Get(auditDetailsHeader) != "" {
			t.Fatalf("no-op %s emitted audit header: %q", payload, res.Header.Get(auditDetailsHeader))
		}
	}

	// Real update bumps updated_at and emits audit diff.
	res := a.do("PATCH", tasksPath+"/"+task.ID, `{"title":"Renamed","status":"done"}`)
	updated := decodeBody[Task](t, res)
	if updated.Title != "Renamed" || updated.Status != "done" || updated.UpdatedAt == task.UpdatedAt {
		t.Fatalf("real update failed: %+v", updated)
	}
	wantAudit := `{"status":{"from":"todo","to":"done"},"title":{"from":"Initial","to":"Renamed"}}`
	if got := res.Header.Get(auditDetailsHeader); got != wantAudit {
		t.Fatalf("audit header = %q, want %q", got, wantAudit)
	}
}

func TestAuditHeaderEncoding(t *testing.T) {
	a := newTestApp(t)

	// HTML chars (<, >, &) stay readable; non-ASCII is \uXXXX-escaped.
	res := a.do("POST", tasksPath, `{"title":"<é😀&>"}`)
	task := decodeBody[Task](t, res)
	if res.Header.Get(auditCreatedHeader) != task.ID {
		t.Fatalf("audit created = %q, want %q", res.Header.Get(auditCreatedHeader), task.ID)
	}
	want := `{"assignee_id":null,"status":"todo","task_id":"` + task.ID + `","title":"<\u00e9\ud83d\ude00&>"}`
	if got := res.Header.Get(auditDetailsHeader); got != want {
		t.Fatalf("audit details = %q, want %q", got, want)
	}

	// Exceeding 4096 bytes returns an error from encodeAuditDetails.
	if _, err := encodeAuditDetails(auditDetails{"bio": strings.Repeat("x", auditMaxBytes)}); err == nil {
		t.Fatal("expected error for oversized audit details")
	}
}

func TestAccessEvaluationAssigneeFlow(t *testing.T) {
	a := newTestApp(t)
	const traceparent = "00-0af7651916cd43dd8448eb211c80319c-b7ad6b7169203331-01"

	// Allowed assignee on POST forwards traceparent and omits X-Request-ID when empty.
	res := a.do("POST", tasksPath, `{"title":"Assigned","assignee_id":"`+testAssignee+`"}`,
		withHeader("traceparent", traceparent), withoutHeader("X-Request-ID"))
	task := decodeBody[Task](t, res)
	if res.StatusCode != http.StatusCreated || task.AssigneeID == nil || *task.AssigneeID != testAssignee {
		t.Fatalf("create assigned: %d %+v", res.StatusCode, task)
	}
	hdr := a.identity.lastHeader()
	if !strings.HasPrefix(hdr.Get("traceparent"), "00-0af7651916cd43dd8448eb211c80319c-") {
		t.Fatalf("traceparent not propagated: %q", hdr.Get("traceparent"))
	}
	if _, sent := hdr["X-Request-Id"]; sent {
		t.Fatalf("expected X-Request-ID omitted when empty, got %v", hdr["X-Request-Id"])
	}

	// Unassign with null does not call identity; reassign forwards non-empty X-Request-ID.
	res = a.do("PATCH", tasksPath+"/"+task.ID, `{"assignee_id":null}`)
	unassigned := decodeBody[Task](t, res)
	if unassigned.AssigneeID != nil || len(a.identity.recordedCalls()) != 1 {
		t.Fatalf("unassign: %+v calls=%d", unassigned, len(a.identity.recordedCalls()))
	}

	res = a.do("PATCH", tasksPath+"/"+task.ID, `{"assignee_id":"`+testAssignee+`"}`, withHeader("X-Request-ID", "req-assign"))
	if res.StatusCode != http.StatusOK || a.identity.lastHeader().Get("X-Request-ID") != "req-assign" {
		t.Fatalf("reassign: status=%d hdr=%v", res.StatusCode, a.identity.lastHeader())
	}

	// Denied by identity -> 422 on assignee_id.
	a.identity.setAnswer(http.StatusOK, `{"decision":false}`)
	res = a.do("POST", tasksPath, `{"title":"Denied","assignee_id":"`+testAssignee+`"}`)
	body := decodeBody[errorBody](t, res)
	if res.StatusCode != http.StatusUnprocessableEntity || body.Error.Details.Fields[0].Path != "assignee_id" {
		t.Fatalf("denied: %d %+v", res.StatusCode, body.Error)
	}

	// Identity error / missing decision / unset URL -> fail-closed 503.
	for _, tc := range []struct {
		status int
		body   string
	}{
		{http.StatusServiceUnavailable, `{}`},
		{http.StatusInternalServerError, `{"decision":true}`},
		{http.StatusOK, `{}`},
	} {
		a.identity.setAnswer(tc.status, tc.body)
		res = a.do("POST", tasksPath, `{"title":"Fail","assignee_id":"`+testAssignee+`"}`)
		body = decodeBody[errorBody](t, res)
		if res.StatusCode != http.StatusServiceUnavailable || body.Error.Code != "SERVICE_UNAVAILABLE" {
			t.Fatalf("identity (%d %s): got %d %+v", tc.status, tc.body, res.StatusCode, body.Error)
		}
	}
}
