package main

import (
	"bufio"
	"bytes"
	"context"
	"database/sql"
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
)

const (
	testProject    = "01jd8x3k7q0000000000000000"
	testOtherProj  = "01jd8x3k7q0000000000000001"
	testMember     = "01jd8x3k7q00000000000000mm"
	testAssignee   = "01jd8x3k7q00000000000000aa"
	testUser       = "01jd8x3k7q00000000000000uu"
	testMissing    = "01jd8x3k7q00000000000000zz"
	testPermission = "tasks:assignable"

	tasksPath   = "/projects/" + testProject + "/tasks"
	profilePath = "/users/" + testUser + "/profile"
)

type fakeIdentity struct {
	mu      sync.Mutex
	status  int
	body    string
	calls   []string
	headers []http.Header
}

func (f *fakeIdentity) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if r.Method != http.MethodPost || r.URL.Path != "/access/v1/evaluation" {
		http.NotFound(w, r)
		return
	}
	b, _ := io.ReadAll(r.Body)
	f.calls = append(f.calls, string(b))
	f.headers = append(f.headers, r.Header.Clone())
	w.WriteHeader(f.status)
	_, _ = io.WriteString(w, f.body)
}

func (f *fakeIdentity) setAnswer(status int, body string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.status, f.body = status, body
}

func (f *fakeIdentity) recordedCalls() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.calls)
}

func (f *fakeIdentity) lastHeader() http.Header {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.headers) == 0 {
		return nil
	}
	return f.headers[len(f.headers)-1]
}

type testApp struct {
	t        *testing.T
	db       *sql.DB
	identity *fakeIdentity
	logs     *bytes.Buffer
	send     func(*http.Request) *http.Response
}

func newTestApp(t *testing.T) *testApp {
	t.Helper()
	sqlDB, err := openDB(context.Background(), filepath.Join(t.TempDir(), "app.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { sqlDB.Close() })

	id := &fakeIdentity{status: http.StatusOK, body: `{"decision":true}`}
	srv := httptest.NewServer(id)
	t.Cleanup(srv.Close)

	logs := &bytes.Buffer{}
	prev := slog.Default()
	slog.SetDefault(newLogger(logs))
	otel.SetTextMapPropagator(propagation.TraceContext{})
	t.Cleanup(func() { slog.SetDefault(prev) })

	// Include a trailing slash to verify accessClient trims it.
	app := newApp(
		newTaskHandler(newTaskStore(sqlDB), newAccessClient(srv.URL+"/"), testPermission),
		newProfileHandler(newProfileStore(sqlDB)),
	)
	return &testApp{
		t:        t,
		db:       sqlDB,
		identity: id,
		logs:     logs,
		send: func(req *http.Request) *http.Response {
			t.Helper()
			res, err := app.Test(req)
			if err != nil {
				t.Fatal(err)
			}
			return res
		},
	}
}

type reqOpt func(*http.Request)

func withHeader(k, v string) reqOpt { return func(r *http.Request) { r.Header.Set(k, v) } }
func withoutHeader(k string) reqOpt { return func(r *http.Request) { r.Header.Del(k) } }

func (a *testApp) do(method, path, body string, opts ...reqOpt) *http.Response {
	a.t.Helper()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Plat5-Member-Id", testMember)
	req.Header.Set("X-Plat5-Project-Id", testProject)
	for _, o := range opts {
		o(req)
	}
	return a.send(req)
}

func decodeBody[T any](t *testing.T, res *http.Response) T {
	t.Helper()
	defer res.Body.Close()
	var v T
	if err := json.NewDecoder(res.Body).Decode(&v); err != nil {
		t.Fatalf("decode status %d: %v", res.StatusCode, err)
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
			Fields   []fieldError `json:"fields"`
			Resource string       `json:"resource"`
			ID       any          `json:"id"`
		} `json:"details"`
	} `json:"error"`
}

func (a *testApp) requestLogs() []map[string]any {
	a.t.Helper()
	var out []map[string]any
	for sc := bufio.NewScanner(bytes.NewReader(a.logs.Bytes())); sc.Scan(); {
		var line map[string]any
		if err := json.Unmarshal(sc.Bytes(), &line); err != nil {
			a.t.Fatalf("invalid JSON log line: %q", sc.Text())
		}
		if line["message"] == "request completed" {
			out = append(out, line)
		}
	}
	return out
}

func TestPermissionNameValidation(t *testing.T) {
	for _, ok := range []string{"project:read", "tasks:assignable", "a.b_c-d"} {
		if !permissionName.MatchString(ok) {
			t.Errorf("expected %q to be valid", ok)
		}
	}
	for _, bad := range []string{"", " ", "Project:Read", "project read", strings.Repeat("a", 65)} {
		if permissionName.MatchString(bad) {
			t.Errorf("expected %q to be rejected", bad)
		}
	}
}

func TestValidationErrorZeroFieldsDoesNotPanic(t *testing.T) {
	e := errValidation()
	if e.Status != http.StatusUnprocessableEntity || e.Code != "VALIDATION_ERROR" || e.Message != fallbackValidation {
		t.Fatalf("unexpected error: %+v", e)
	}
}

func TestNotFoundAndMethodNotAllowed(t *testing.T) {
	a := newTestApp(t)

	// Unknown path -> Plat5 404 envelope with request_id.
	res := a.do("GET", "/unknown", "", withHeader("X-Request-ID", "req-404"))
	body := decodeBody[errorBody](t, res)
	if res.StatusCode != http.StatusNotFound || body.Error.Code != "NOT_FOUND" || body.Error.RequestID == nil || *body.Error.RequestID != "req-404" {
		t.Fatalf("404: status=%d body=%+v", res.StatusCode, body.Error)
	}

	// Known path with unsupported method -> Fiber handles 405 naturally (not rewritten to 404).
	res = a.do("DELETE", tasksPath, "")
	if res.StatusCode != http.StatusMethodNotAllowed {
		t.Fatalf("405: got status %d, want 405", res.StatusCode)
	}
	if res.Header.Get("Allow") == "" {
		t.Fatal("405: expected Allow header")
	}
}

func TestErrorEnvelopeRequestID(t *testing.T) {
	a := newTestApp(t)

	res := a.do("GET", tasksPath+"/"+testMissing, "", withoutHeader("X-Request-ID"))
	body := decodeBody[errorBody](t, res)
	if res.StatusCode != http.StatusNotFound || body.Error.RequestID != nil {
		t.Fatalf("expected nil request_id, got %+v", body.Error)
	}

	res = a.do("GET", tasksPath+"/"+testMissing, "", withHeader("X-Request-ID", "req-1"))
	body = decodeBody[errorBody](t, res)
	if body.Error.RequestID == nil || *body.Error.RequestID != "req-1" || body.Error.Details.Resource != "task" || body.Error.Details.ID != testMissing {
		t.Fatalf("unexpected 404 body: %+v", body.Error)
	}
}

func TestTelemetryLogsAndCorrelation(t *testing.T) {
	a := newTestApp(t)
	const traceparent = "00-0af7651916cd43dd8448eb211c80319c-b7ad6b7169203331-01"

	a.do("GET", tasksPath, "", withHeader("X-Request-ID", "req-ok"), withHeader("traceparent", traceparent))
	a.do("GET", tasksPath+"/"+testMissing, "", withHeader("X-Request-ID", "req-404"))
	a.do("GET", profilePath, "", withoutHeader("X-Plat5-Member-Id"), withoutHeader("X-Plat5-Project-Id"), withHeader("X-Plat5-User-Id", testUser))

	logs := a.requestLogs()
	if len(logs) != 3 {
		t.Fatalf("got %d logs, want 3", len(logs))
	}

	ok := logs[0]
	if ok["level"] != "info" || ok["route"] != "/projects/:project_id/tasks" || ok["request_id"] != "req-ok" ||
		ok["node_id"] != testProject || ok["member_id"] != testMember || ok["trace_id"] != "0af7651916cd43dd8448eb211c80319c" ||
		ok["span_id"] == nil || ok["timestamp"] == nil || ok["duration_ms"] == nil {
		t.Fatalf("unexpected ok log: %v", ok)
	}
	if logs[1]["level"] != "warn" || logs[1]["status"] != 404.0 {
		t.Fatalf("unexpected 404 log: %v", logs[1])
	}
	if logs[2]["user_id"] != testUser || logs[2]["node_id"] != nil {
		t.Fatalf("unexpected profile log: %v", logs[2])
	}
}

func TestInternalHealthAndMetrics(t *testing.T) {
	a := newTestApp(t)
	a.do("GET", tasksPath, "")

	h := newInternalHandler(a.db)

	// /health/live -> 200
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/health/live", nil))
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"healthy"`) {
		t.Fatalf("live: %d %s", rec.Code, rec.Body.String())
	}

	// /health/ready -> 200 while DB open
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/health/ready", nil))
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"healthy"`) {
		t.Fatalf("ready: %d %s", rec.Code, rec.Body.String())
	}

	// /metrics -> Prometheus metrics including HTTP & Go/process series
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/metrics", nil))
	body := rec.Body.String()
	for _, metric := range []string{"http_requests_total", "http_request_duration_seconds", "go_goroutines"} {
		if rec.Code != http.StatusOK || !strings.Contains(body, metric) {
			t.Fatalf("metrics missing %s (status %d)", metric, rec.Code)
		}
	}

	// /health/ready -> 503 when DB is closed
	a.db.Close()
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/health/ready", nil))
	if rec.Code != http.StatusServiceUnavailable || !strings.Contains(rec.Body.String(), `"unhealthy"`) {
		t.Fatalf("ready after close: %d %s", rec.Code, rec.Body.String())
	}
}

func TestSetupTelemetryOTLPTraces(t *testing.T) {
	paths := make(chan string, 4)
	collector := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths <- r.URL.Path
		w.WriteHeader(http.StatusOK)
	}))
	defer collector.Close()

	t.Setenv("OTEL_SDK_DISABLED", "")
	t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", collector.URL)
	t.Setenv("OTEL_TRACES_EXPORTER", "")
	t.Setenv("OTEL_METRICS_EXPORTER", "prometheus")

	prevTP := otel.GetTracerProvider()
	t.Cleanup(func() { otel.SetTracerProvider(prevTP) })

	shutdown, err := setupTelemetry(context.Background(), "v1.0.0")
	if err != nil {
		t.Fatal(err)
	}
	_, span := otel.Tracer("test").Start(context.Background(), "op")
	span.End()
	if err := shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := <-paths; got != "/v1/traces" {
		t.Fatalf("exported to %q, want /v1/traces", got)
	}
}

func TestOpenDBWALAndSpecialPathChars(t *testing.T) {
	// Path containing '?' and '#' must open cleanly without breaking the SQLite URI.
	dir := filepath.Join(t.TempDir(), "dir?with#chars")
	path := filepath.Join(dir, "app.db")

	sqlDB, err := openDB(context.Background(), path)
	if err != nil {
		t.Fatalf("openDB(%q): %v", path, err)
	}
	defer sqlDB.Close()

	var mode string
	if err := sqlDB.QueryRow(`PRAGMA journal_mode`).Scan(&mode); err != nil {
		t.Fatal(err)
	}
	if !strings.EqualFold(mode, "wal") {
		t.Fatalf("journal_mode = %q, want wal", mode)
	}
}
