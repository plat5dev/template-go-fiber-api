package telemetry

import (
	"bufio"
	"bytes"
	"encoding/json"
	"log/slog"
	"net/http/httptest"
	"testing"

	"github.com/gofiber/fiber/v3"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/propagation"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
)

type statusError struct {
	status    int
	errorType string
}

func (e statusError) Error() string     { return "it broke" }
func (e statusError) ErrorType() string { return e.errorType }

// served is what one request left: its span and its log lines.
type served struct {
	span  sdktrace.ReadOnlySpan
	lines []map[string]any
}

func (s served) attrs() map[attribute.Key]string {
	out := map[attribute.Key]string{}
	for _, kv := range s.span.Attributes() {
		out[kv.Key] = kv.Value.Emit()
	}
	return out
}

// serve sends one request through Middleware, on an app whose error handler answers a
// statusError's status.
func serve(t *testing.T, method, target string, header map[string]string) served {
	t.Helper()
	rec := tracetest.NewSpanRecorder()
	prevTP, prevLog := otel.GetTracerProvider(), slog.Default()
	otel.SetTracerProvider(sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(rec)))
	otel.SetTextMapPropagator(propagation.TraceContext{})
	var buf bytes.Buffer
	slog.SetDefault(NewLogger(&buf))
	t.Cleanup(func() { otel.SetTracerProvider(prevTP); slog.SetDefault(prevLog) })

	app := fiber.New(fiber.Config{ErrorHandler: func(c fiber.Ctx, err error) error {
		if e, ok := err.(statusError); ok {
			return c.SendStatus(e.status)
		}
		return c.SendStatus(fiber.StatusNotFound)
	}})
	app.Use(Middleware()...)
	app.Get("/items/:id", func(c fiber.Ctx) error { return c.SendString("ok") })
	app.Get("/missing", func(c fiber.Ctx) error { return statusError{404, ""} })
	app.Get("/broken", func(c fiber.Ctx) error { return statusError{500, "db"} })

	req := httptest.NewRequest(method, target, nil)
	for k, v := range header {
		req.Header.Set(k, v)
	}
	if _, err := app.Test(req); err != nil {
		t.Fatal(err)
	}
	spans := rec.Ended()
	if len(spans) != 1 {
		t.Fatalf("%d spans, want 1", len(spans))
	}
	out := served{span: spans[0]}
	for sc := bufio.NewScanner(&buf); sc.Scan(); {
		var line map[string]any
		if err := json.Unmarshal(sc.Bytes(), &line); err != nil {
			t.Fatalf("not a JSON line: %q", sc.Text())
		}
		out.lines = append(out.lines, line)
	}
	if len(out.lines) != 1 {
		t.Fatalf("%d log lines, want 1", len(out.lines))
	}
	return out
}

func TestSpanNameIsTheRouteTemplate(t *testing.T) {
	s := serve(t, "GET", "/items/42", nil)
	if s.span.Name() != "GET /items/:id" || s.attrs()["http.route"] != "/items/:id" || s.attrs()["url.path"] != "/items/42" {
		t.Fatalf("span %q, attrs %v", s.span.Name(), s.attrs())
	}
	if s.lines[0]["route"] != "/items/:id" {
		t.Fatalf("log route %v", s.lines[0]["route"])
	}
}

// No route matched: the span is {method} alone, never the middleware's "/", and the log's
// route is unknown.
func TestUnmatchedIsTheMethodAlone(t *testing.T) {
	for _, target := range []string{"/", "/nope"} {
		s := serve(t, "GET", target, nil)
		if s.span.Name() != "GET" {
			t.Fatalf("%s: span %q", target, s.span.Name())
		}
		if _, ok := s.attrs()["http.route"]; ok {
			t.Fatalf("%s: http.route %q", target, s.attrs()["http.route"])
		}
		if s.lines[0]["route"] != "unknown" || s.lines[0]["status"] != 404.0 {
			t.Fatalf("%s: log %v", target, s.lines[0])
		}
	}
}

func TestURLQueryIsRedacted(t *testing.T) {
	s := serve(t, "GET", "/items/1?sig=secret&page=2&X-Amz-Signature=s3&SIG=kept", nil)
	a := s.attrs()
	if a["url.query"] != "sig=REDACTED&page=2&X-Amz-Signature=REDACTED&SIG=kept" {
		t.Fatalf("url.query = %q", a["url.query"])
	}
	if _, ok := a["url.full"]; ok {
		t.Fatal("url.full set")
	}
	if a["url.scheme"] != "http" {
		t.Fatalf("url.scheme = %q", a["url.scheme"])
	}
	if q, ok := serve(t, "GET", "/items/1", nil).attrs()["url.query"]; ok {
		t.Fatalf("url.query %q without a query", q)
	}
}

func TestA5xxIsAnError(t *testing.T) {
	s := serve(t, "GET", "/broken", nil)
	if s.span.Status().Code != codes.Error || s.span.Status().Description != "" {
		t.Fatalf("status %v", s.span.Status())
	}
	if s.attrs()["error.type"] != "db" {
		t.Fatalf("error.type %q", s.attrs()["error.type"])
	}
	line := s.lines[0]
	if line["level"] != "error" || line["status"] != 500.0 || line["error_type"] != "db" || line["error_message"] != "it broke" {
		t.Fatalf("log %v", line)
	}
}

func TestA4xxIsNotAnError(t *testing.T) {
	s := serve(t, "GET", "/missing", nil)
	if s.span.Status().Code != codes.Unset {
		t.Fatalf("status %v", s.span.Status())
	}
	if _, ok := s.attrs()["error.type"]; ok {
		t.Fatal("error.type on a 4xx")
	}
	line := s.lines[0]
	if line["level"] != "warn" || line["status"] != 404.0 || line["error_type"] != nil || line["error_message"] != nil {
		t.Fatalf("log %v", line)
	}
}

// The request id and the ids the front door sent are on the log line and the span.
func TestCallerIDs(t *testing.T) {
	s := serve(t, "GET", "/items/1", map[string]string{
		"X-Request-ID":       "req-1",
		"X-Plat5-Project-Id": "p1",
		"X-Plat5-Member-Id":  "m1",
		"X-Plat5-User-Id":    "u1",
		"traceparent":        "00-0af7651916cd43dd8448eb211c80319c-b7ad6b7169203331-01",
	})
	line, a := s.lines[0], s.attrs()
	for field, want := range map[string]string{"request_id": "req-1", "node_id": "p1", "member_id": "m1", "user_id": "u1", "level": "info"} {
		if line[field] != want {
			t.Errorf("log %s = %v, want %s", field, line[field], want)
		}
	}
	for key, want := range map[attribute.Key]string{"request_id": "req-1", "plat5.node_id": "p1", "plat5.member_id": "m1", "plat5.user_id": "u1"} {
		if a[key] != want {
			t.Errorf("span %s = %q, want %s", key, a[key], want)
		}
	}
	if line["trace_id"] != "0af7651916cd43dd8448eb211c80319c" || s.span.Parent().SpanID().String() != "b7ad6b7169203331" {
		t.Errorf("trace %v, parent %v", line["trace_id"], s.span.Parent().SpanID())
	}

	line = serve(t, "GET", "/items/1", nil).lines[0]
	for _, field := range []string{"request_id", "node_id", "member_id", "user_id"} {
		if _, ok := line[field]; ok {
			t.Errorf("%s logged without its header", field)
		}
	}
}
