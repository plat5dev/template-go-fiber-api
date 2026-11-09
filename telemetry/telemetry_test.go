package telemetry

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"go.opentelemetry.io/otel"
	semconv "go.opentelemetry.io/otel/semconv/v1.43.0"
)

// unsetAll clears every telemetry variable for the test.
func unsetAll(t *testing.T) {
	t.Helper()
	for _, key := range []string{
		"OTEL_SDK_DISABLED", "OTEL_TRACES_EXPORTER", "OTEL_METRICS_EXPORTER",
		"OTEL_EXPORTER_OTLP_PROTOCOL", "OTEL_EXPORTER_OTLP_TRACES_PROTOCOL", "OTEL_EXPORTER_OTLP_METRICS_PROTOCOL",
		"OTEL_EXPORTER_OTLP_ENDPOINT", "OTEL_EXPORTER_OTLP_TRACES_ENDPOINT", "OTEL_EXPORTER_OTLP_METRICS_ENDPOINT",
		"OTEL_TRACES_SAMPLER_RATIO", "OTEL_METRIC_EXPORT_INTERVAL", "OTEL_RESOURCE_ATTRIBUTES",
		"OTEL_SERVICE_VERSION",
	} {
		t.Setenv(key, "")
	}
}

// Each set-but-invalid value in telemetry.md's "Invalid values" table stops Setup (and so
// the API) with an error naming the variable.
func TestSetupRefusesInvalidValues(t *testing.T) {
	for _, tt := range []struct{ key, value string }{
		{"OTEL_TRACES_EXPORTER", "jaeger"},
		{"OTEL_TRACES_EXPORTER", "otlp,prometheus"},
		{"OTEL_TRACES_EXPORTER", "otlp,"},
		{"OTEL_METRICS_EXPORTER", "otlp,statsd"},
		{"OTEL_EXPORTER_OTLP_PROTOCOL", "grpc"},
		{"OTEL_EXPORTER_OTLP_TRACES_PROTOCOL", "http/json"},
		{"OTEL_EXPORTER_OTLP_METRICS_PROTOCOL", "grpc"},
		{"OTEL_EXPORTER_OTLP_ENDPOINT", "localhost:4318"},
		{"OTEL_EXPORTER_OTLP_ENDPOINT", "grpc://collector:4317"},
		{"OTEL_EXPORTER_OTLP_TRACES_ENDPOINT", "ftp://collector/v1/traces"},
		{"OTEL_EXPORTER_OTLP_METRICS_ENDPOINT", "collector"},
		{"OTEL_TRACES_SAMPLER_RATIO", "1.5"},
		{"OTEL_TRACES_SAMPLER_RATIO", "-0.1"},
		{"OTEL_TRACES_SAMPLER_RATIO", "half"},
		{"OTEL_METRIC_EXPORT_INTERVAL", "0"},
		{"OTEL_METRIC_EXPORT_INTERVAL", "10s"},
		{"OTEL_SDK_DISABLED", "yes"},
		{"OTEL_RESOURCE_ATTRIBUTES", "service.namespace"},
		{"OTEL_RESOURCE_ATTRIBUTES", "a=b,=c"},
	} {
		t.Run(tt.key+"="+tt.value, func(t *testing.T) {
			unsetAll(t)
			t.Setenv(tt.key, tt.value)
			_, err := Setup(context.Background(), "dev")
			if err == nil || !strings.Contains(err.Error(), tt.key) {
				t.Fatalf("Setup with %s=%q: %v, want an error naming it", tt.key, tt.value, err)
			}
		})
	}
}

func TestSetupAcceptsValidValues(t *testing.T) {
	unsetAll(t)
	for key, value := range map[string]string{
		"OTEL_TRACES_EXPORTER":        "otlp,none",
		"OTEL_METRICS_EXPORTER":       "Prometheus, none",
		"OTEL_EXPORTER_OTLP_PROTOCOL": "http/protobuf",
		"OTEL_EXPORTER_OTLP_ENDPOINT": "https://collector.example",
		"OTEL_TRACES_SAMPLER_RATIO":   "0.25",
		"OTEL_METRIC_EXPORT_INTERVAL": "5000",
		"OTEL_SDK_DISABLED":           "TRUE",
		"OTEL_RESOURCE_ATTRIBUTES":    "service.namespace=edge,deployment.environment=dev",
	} {
		t.Setenv(key, value)
	}
	if _, err := Setup(context.Background(), "dev"); err != nil {
		t.Fatal(err)
	}
}

// What the variables turn on (plat5 docs/reference/telemetry.md#exporter-selection).
func TestWhatIsPushed(t *testing.T) {
	for _, tt := range []struct {
		env             map[string]string
		traces, metrics bool
	}{
		{map[string]string{}, false, false},
		{map[string]string{"OTEL_EXPORTER_OTLP_ENDPOINT": "http://c:4318"}, true, true},
		{map[string]string{"OTEL_EXPORTER_OTLP_ENDPOINT": "http://c:4318", "OTEL_METRICS_EXPORTER": "prometheus"}, true, false},
		{map[string]string{"OTEL_EXPORTER_OTLP_ENDPOINT": "http://c:4318", "OTEL_TRACES_EXPORTER": "none"}, false, true},
		{map[string]string{"OTEL_EXPORTER_OTLP_ENDPOINT": "http://c:4318", "OTEL_SDK_DISABLED": "true"}, false, false},
		{map[string]string{"OTEL_EXPORTER_OTLP_TRACES_ENDPOINT": "http://c:4318/v1/traces"}, true, false},
		{map[string]string{"OTEL_EXPORTER_OTLP_METRICS_ENDPOINT": "http://c:4318/v1/metrics", "OTEL_METRICS_EXPORTER": "otlp,prometheus"}, false, true},
	} {
		unsetAll(t)
		for k, v := range tt.env {
			t.Setenv(k, v)
		}
		s, err := readSettings()
		if err != nil || s.traces != tt.traces || s.metrics != tt.metrics {
			t.Errorf("%v: traces %v, metrics %v, %v; want %v, %v", tt.env, s.traces, s.metrics, err, tt.traces, tt.metrics)
		}
	}
}

// With a base endpoint, traces go to its /v1/traces, as the exporter reads the standard
// variables.
func TestTracesGoToTheEndpoint(t *testing.T) {
	unsetAll(t)
	paths := make(chan string, 10)
	collector := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths <- r.URL.Path
	}))
	defer collector.Close()
	t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", collector.URL+"/otlp")
	t.Setenv("OTEL_METRICS_EXPORTER", "none")
	prev := otel.GetTracerProvider()
	t.Cleanup(func() { otel.SetTracerProvider(prev) })

	shutdown, err := Setup(context.Background(), "dev")
	if err != nil {
		t.Fatal(err)
	}
	_, span := otel.Tracer("test").Start(context.Background(), "x")
	span.End()
	if err := shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := <-paths; got != "/otlp/v1/traces" {
		t.Fatalf("exported to %q", got)
	}
}

// The logger writes Plat5's field names, and OpenTelemetry's errors (a failed export) are
// one JSON line through it.
func TestLogLines(t *testing.T) {
	var buf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(NewLogger(&buf))
	t.Cleanup(func() { slog.SetDefault(prev) })
	unsetAll(t)
	if _, err := Setup(context.Background(), "dev"); err != nil {
		t.Fatal(err)
	}

	otel.Handle(errors.New("traces export: connection refused"))

	var line map[string]any
	if err := json.Unmarshal(buf.Bytes(), &line); err != nil {
		t.Fatalf("not one JSON line: %q", buf.String())
	}
	if line["level"] != "error" || line["message"] != "telemetry error" || line["error_message"] != "traces export: connection refused" || line["timestamp"] == nil {
		t.Fatalf("line = %v", line)
	}
}

// service.version: OTEL_SERVICE_VERSION, then OTEL_RESOURCE_ATTRIBUTES, then the build's.
func TestServiceVersion(t *testing.T) {
	unsetAll(t)
	version := func(build string) string {
		t.Helper()
		res, err := newResource(context.Background(), build)
		if err != nil {
			t.Fatal(err)
		}
		v, _ := res.Set().Value(semconv.ServiceVersionKey)
		return v.AsString()
	}
	if got := version("dev"); got != "dev" {
		t.Fatalf("without VERSION: %q", got)
	}
	if got := version("v1.2.3"); got != "v1.2.3" {
		t.Fatalf("with VERSION: %q", got)
	}
	t.Setenv("OTEL_RESOURCE_ATTRIBUTES", "service.version=v2.0.0")
	if got := version("v1.2.3"); got != "v2.0.0" {
		t.Fatalf("OTEL_RESOURCE_ATTRIBUTES: %q", got)
	}
	t.Setenv("OTEL_SERVICE_VERSION", "v3.0.0")
	if got := version("v1.2.3"); got != "v3.0.0" {
		t.Fatalf("OTEL_SERVICE_VERSION: %q", got)
	}
}
