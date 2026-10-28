package access

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/rs/zerolog"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/trace"
)

const (
	member  = "01jd8x3k7q00000000000000mm"
	project = "01jd8x3k7q00000000000000pp"
)

// identity answers every evaluation with status and body, and records the headers and
// body it was sent.
type identity struct {
	mu     sync.Mutex
	status int
	body   string
	header http.Header
	sent   string
}

func (f *identity) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if r.Method != http.MethodPost || r.URL.Path != "/access/v1/evaluation" {
		http.NotFound(w, r)
		return
	}
	b, _ := io.ReadAll(r.Body)
	f.header, f.sent = r.Header.Clone(), string(b)
	w.WriteHeader(f.status)
	_, _ = io.WriteString(w, f.body)
}

func client(t *testing.T, f *identity) *Client {
	t.Helper()
	srv := httptest.NewServer(f)
	t.Cleanup(srv.Close)
	return New(srv.URL, propagation.TraceContext{}, zerolog.Nop())
}

func TestEvaluate(t *testing.T) {
	sc := trace.NewSpanContext(trace.SpanContextConfig{
		TraceID:    trace.TraceID{1},
		SpanID:     trace.SpanID{2},
		TraceFlags: trace.FlagsSampled,
	})
	ctx := trace.ContextWithRemoteSpanContext(context.Background(), sc)

	for _, tc := range []struct {
		body string
		want bool
	}{{`{"decision":true}`, true}, {`{"decision":false}`, false}} {
		f := &identity{status: http.StatusOK, body: tc.body}
		got, err := client(t, f).Evaluate(ctx, "req-1", member, "project", project, "project:read")
		if err != nil || got != tc.want {
			t.Fatalf("%s: decision = %v, %v", tc.body, got, err)
		}
		f.mu.Lock()
		sent, header := f.sent, f.header
		f.mu.Unlock()
		want := `{"subject":{"type":"member","id":"` + member + `"},"resource":{"type":"project","id":"` + project + `"},"action":{"name":"project:read"}}`
		if sent != want {
			t.Fatalf("sent %s, want %s", sent, want)
		}
		if got := header.Get("X-Request-ID"); got != "req-1" {
			t.Fatalf("X-Request-ID = %q", got)
		}
		if got := header.Get("traceparent"); got != "00-01000000000000000000000000000000-0200000000000000-01" {
			t.Fatalf("traceparent = %q", got)
		}
	}
}

func TestNoAnswerIsAnError(t *testing.T) {
	for _, tc := range []struct {
		name   string
		status int
		body   string
	}{
		{"not 200", http.StatusInternalServerError, `{"decision":true}`},
		{"no decision", http.StatusOK, `{}`},
		{"decision not a boolean", http.StatusOK, `{"decision":"true"}`},
		{"not JSON", http.StatusOK, `yes`},
	} {
		f := &identity{status: tc.status, body: tc.body}
		if got, err := client(t, f).Evaluate(context.Background(), "req-1", member, "project", project, "project:read"); err == nil || got {
			t.Fatalf("%s: decision = %v, %v; want an error", tc.name, got, err)
		}
	}

	if _, err := New("", propagation.TraceContext{}, zerolog.Nop()).Evaluate(context.Background(), "req-1", member, "project", project, "project:read"); err == nil {
		t.Fatal("unset identity url: want an error")
	}
}
