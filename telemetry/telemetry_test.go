package telemetry

import (
	"testing"
	"time"
)

func TestExporterIncludesOTLP(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name             string
		list             []string
		defaultWhenUnset bool
		want             bool
	}{
		{name: "traces default unset", list: nil, defaultWhenUnset: true, want: true},
		{name: "metrics default unset", list: nil, defaultWhenUnset: false, want: false},
		{name: "explicit otlp", list: []string{"otlp"}, defaultWhenUnset: false, want: true},
		{name: "otlp and prometheus", list: []string{"otlp", "prometheus"}, defaultWhenUnset: false, want: true},
		{name: "prometheus only", list: []string{"prometheus"}, defaultWhenUnset: true, want: false},
		{name: "none", list: []string{"none"}, defaultWhenUnset: true, want: false},
		{name: "empty slice not unset", list: []string{}, defaultWhenUnset: true, want: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := exporterIncludesOTLP(tt.list, tt.defaultWhenUnset)
			if got != tt.want {
				t.Fatalf("exporterIncludesOTLP(%v, %v) = %v, want %v", tt.list, tt.defaultWhenUnset, got, tt.want)
			}
		})
	}
}

func TestEnvExporterList(t *testing.T) {
	t.Setenv("OTEL_METRICS_EXPORTER", " otlp , Prometheus ")
	got, err := envExporterList("OTEL_METRICS_EXPORTER", "otlp", "prometheus", "none")
	if err != nil || len(got) != 2 || got[0] != "otlp" || got[1] != "prometheus" {
		t.Fatalf("envExporterList = %#v, %v", got, err)
	}

	for _, unset := range []string{"", " , "} {
		t.Setenv("OTEL_METRICS_EXPORTER", unset)
		if got, err := envExporterList("OTEL_METRICS_EXPORTER", "otlp"); got != nil || err != nil {
			t.Fatalf("%q: want nil, nil; got %#v, %v", unset, got, err)
		}
	}

	t.Setenv("OTEL_TRACES_EXPORTER", "console")
	if _, err := envExporterList("OTEL_TRACES_EXPORTER", "otlp", "none"); err == nil {
		t.Fatal("console: want an error")
	}
}

// A telemetry variable set but invalid stops the API at boot, with OTLP on or off.
func TestInitRefusesInvalidValues(t *testing.T) {
	for key, value := range map[string]string{
		"OTEL_SDK_DISABLED":                   "yes",
		"OTEL_EXPORTER_OTLP_PROTOCOL":         "grpc",
		"OTEL_EXPORTER_OTLP_TRACES_PROTOCOL":  "http/json",
		"OTEL_EXPORTER_OTLP_METRICS_PROTOCOL": "grpc",
		"OTEL_TRACES_EXPORTER":                "zipkin",
		"OTEL_METRICS_EXPORTER":               "console",
		"OTEL_TRACES_SAMPLER_RATIO":           "1.5",
		"OTEL_METRIC_EXPORT_INTERVAL":         "30s",
		"OTEL_EXPORTER_OTLP_ENDPOINT":         "collector:4318",
		"OTEL_RESOURCE_ATTRIBUTES":            "service.version",
	} {
		t.Run(key, func(t *testing.T) {
			t.Setenv(key, value)
			if _, err := Init(t.Context()); err == nil {
				t.Fatalf("%s=%s: want an error", key, value)
			}
		})
	}
}

func TestDefaults(t *testing.T) {
	for _, key := range []string{"OTEL_SDK_DISABLED", "OTEL_TRACES_SAMPLER_RATIO", "OTEL_METRIC_EXPORT_INTERVAL"} {
		t.Setenv(key, "")
	}
	if disabled, err := envBool("OTEL_SDK_DISABLED"); disabled || err != nil {
		t.Fatalf("OTEL_SDK_DISABLED unset: %v, %v", disabled, err)
	}
	t.Setenv("OTEL_SDK_DISABLED", "TRUE")
	if disabled, err := envBool("OTEL_SDK_DISABLED"); !disabled || err != nil {
		t.Fatalf("OTEL_SDK_DISABLED=TRUE: %v, %v", disabled, err)
	}
	if ratio, err := samplerRatio(); ratio != 1 || err != nil {
		t.Fatalf("sampler ratio: %v, %v", ratio, err)
	}
	if interval, err := metricExportInterval(); interval != 60*time.Second || err != nil {
		t.Fatalf("export interval: %v, %v", interval, err)
	}
}

func TestResolveOTLPEndpoint(t *testing.T) {
	t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", "")
	t.Setenv("OTEL_EXPORTER_OTLP_TRACES_ENDPOINT", "")
	dest, err := resolveOTLPEndpoint("traces", "/v1/traces")
	if err != nil {
		t.Fatal(err)
	}
	if dest != nil {
		t.Fatalf("expected nil dest, got %#v", dest)
	}

	t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", "http://localhost:4318")
	dest, err = resolveOTLPEndpoint("traces", "/v1/traces")
	if err != nil {
		t.Fatal(err)
	}
	if dest == nil || dest.host != "localhost:4318" || dest.path != "/v1/traces" || !dest.insecure {
		t.Fatalf("base endpoint: %#v", dest)
	}

	t.Setenv("OTEL_EXPORTER_OTLP_TRACES_ENDPOINT", "https://collector.example/v1/traces")
	dest, err = resolveOTLPEndpoint("traces", "/v1/traces")
	if err != nil {
		t.Fatal(err)
	}
	if dest == nil || dest.host != "collector.example" || dest.path != "/v1/traces" || dest.insecure {
		t.Fatalf("per-signal override: %#v", dest)
	}
}
