// Package telemetry is the API's logs, traces and metrics, as plat5
// docs/reference/telemetry.md has them: JSON lines on stdout, Prometheus /metrics always,
// and OTLP over HTTP when a destination is set.
package telemetry

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/url"
	"os"
	"slices"
	"strconv"
	"strings"

	"go.opentelemetry.io/contrib/bridges/prometheus"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/exporters/otlp/otlpmetric/otlpmetrichttp"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp"
	"go.opentelemetry.io/otel/propagation"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.43.0"
)

// serviceName is the default service.name and service.namespace.
const serviceName = "api"

// NewLogger writes each record to w as one JSON line, with the field names Plat5's logs
// use: timestamp, level (lowercase) and message.
func NewLogger(w io.Writer) *slog.Logger {
	return slog.New(slog.NewJSONHandler(w, &slog.HandlerOptions{
		ReplaceAttr: func(groups []string, a slog.Attr) slog.Attr {
			if len(groups) > 0 {
				return a
			}
			switch a.Key {
			case slog.TimeKey:
				a.Key, a.Value = "timestamp", slog.TimeValue(a.Value.Time().UTC())
			case slog.MessageKey:
				a.Key = "message"
			case slog.LevelKey:
				a.Value = slog.StringValue(strings.ToLower(a.Value.String()))
			}
			return a
		},
	}))
}

// Setup installs OpenTelemetry's propagator (W3C trace context), and its tracer and meter
// providers when OTLP is on: traces when a destination is set and OTEL_TRACES_EXPORTER
// (unset is otlp) includes otlp, metrics likewise with OTEL_METRICS_EXPORTER. The
// exporters read their destination and headers from the standard OTEL_EXPORTER_OTLP_*
// variables. version is the build's, the default service.version.
//
// A telemetry variable that is set but invalid is an error naming it, OTLP on or off. Once
// running, a failed export is a JSON error line, and the API keeps serving. shutdown
// flushes what's pending.
func Setup(ctx context.Context, version string) (shutdown func(context.Context) error, err error) {
	otel.SetTextMapPropagator(propagation.TraceContext{})
	otel.SetErrorHandler(otel.ErrorHandlerFunc(func(err error) {
		slog.Error("telemetry error", "error_message", err.Error())
	}))

	s, err := readSettings()
	if err != nil {
		return nil, err
	}
	res, err := newResource(ctx, version)
	if err != nil {
		return nil, err
	}

	var shutdowns []func(context.Context) error
	shutdown = func(ctx context.Context) error {
		var errs []error
		for _, f := range shutdowns {
			errs = append(errs, f(ctx))
		}
		return errors.Join(errs...)
	}
	if s.traces {
		exporter, err := otlptracehttp.New(ctx)
		if err != nil {
			return nil, fmt.Errorf("trace exporter: %w", err)
		}
		tp := sdktrace.NewTracerProvider(
			sdktrace.WithSampler(sdktrace.ParentBased(sdktrace.TraceIDRatioBased(s.samplerRatio))),
			sdktrace.WithBatcher(exporter),
			sdktrace.WithResource(res),
		)
		otel.SetTracerProvider(tp)
		shutdowns = append(shutdowns, tp.Shutdown)
	}
	if s.metrics {
		exporter, err := otlpmetrichttp.New(ctx)
		if err != nil {
			return nil, fmt.Errorf("metric exporter: %w", err)
		}
		// The series /metrics serves, pushed: the Prometheus registry is the one source.
		// The reader takes its interval from OTEL_METRIC_EXPORT_INTERVAL.
		mp := sdkmetric.NewMeterProvider(
			sdkmetric.WithResource(res),
			sdkmetric.WithReader(sdkmetric.NewPeriodicReader(exporter,
				sdkmetric.WithProducer(prometheus.NewMetricProducer()))),
		)
		otel.SetMeterProvider(mp)
		shutdowns = append(shutdowns, mp.Shutdown)
	}
	return shutdown, nil
}

// newResource layers the resource: the service's defaults (version among them), then
// OTEL_RESOURCE_ATTRIBUTES and OTEL_SERVICE_NAME, then the convenience variables that are
// set.
func newResource(ctx context.Context, version string) (*resource.Resource, error) {
	const deploymentEnvironment = attribute.Key("deployment.environment")
	defaults := []attribute.KeyValue{
		semconv.ServiceName(serviceName),
		semconv.ServiceNamespace(serviceName),
		semconv.ServiceInstanceID(env("HOSTNAME", serviceName+"-local")),
		semconv.ServiceVersion(version),
		deploymentEnvironment.String("development"),
	}
	var overrides []attribute.KeyValue
	for key, vars := range map[attribute.Key][]string{
		semconv.ServiceNamespaceKey:  {"OTEL_SERVICE_NAMESPACE"},
		semconv.ServiceVersionKey:    {"OTEL_SERVICE_VERSION"},
		semconv.ServiceInstanceIDKey: {"OTEL_SERVICE_INSTANCE_ID"},
		deploymentEnvironment:        {"OTEL_DEPLOYMENT_ENV", "DEPLOYMENT_ENV"},
	} {
		for _, v := range vars {
			if value := env(v, ""); value != "" {
				overrides = append(overrides, key.String(value))
				break
			}
		}
	}
	return resource.New(ctx,
		resource.WithAttributes(defaults...),
		resource.WithFromEnv(),
		resource.WithTelemetrySDK(),
		resource.WithAttributes(overrides...),
	)
}

// settings are what the telemetry variables decide.
type settings struct {
	traces, metrics bool
	samplerRatio    float64
}

// readSettings reads the telemetry variables strictly: one set but invalid is an error
// naming it (plat5 docs/reference/telemetry.md#invalid-values). Empty is unset.
func readSettings() (s settings, err error) {
	disabled, err := oneOf("OTEL_SDK_DISABLED", "false", "true", "false")
	if err != nil {
		return s, err
	}
	for _, key := range []string{"OTEL_EXPORTER_OTLP_PROTOCOL", "OTEL_EXPORTER_OTLP_TRACES_PROTOCOL", "OTEL_EXPORTER_OTLP_METRICS_PROTOCOL"} {
		if _, err := oneOf(key, "http/protobuf", "http/protobuf"); err != nil {
			return s, err
		}
	}
	tracesTo, err := exporters("OTEL_TRACES_EXPORTER", "otlp", "none")
	if err != nil {
		return s, err
	}
	metricsTo, err := exporters("OTEL_METRICS_EXPORTER", "otlp", "prometheus", "none")
	if err != nil {
		return s, err
	}
	var base, tracesURL, metricsURL bool
	for key, set := range map[string]*bool{
		"OTEL_EXPORTER_OTLP_ENDPOINT":         &base,
		"OTEL_EXPORTER_OTLP_TRACES_ENDPOINT":  &tracesURL,
		"OTEL_EXPORTER_OTLP_METRICS_ENDPOINT": &metricsURL,
	} {
		if *set, err = endpoint(key); err != nil {
			return s, err
		}
	}
	s.samplerRatio = 1
	if v := env("OTEL_TRACES_SAMPLER_RATIO", ""); v != "" {
		if s.samplerRatio, err = strconv.ParseFloat(v, 64); err != nil || s.samplerRatio < 0 || s.samplerRatio > 1 {
			return s, fmt.Errorf("OTEL_TRACES_SAMPLER_RATIO is %q: want a number from 0 to 1", v)
		}
	}
	if v := env("OTEL_METRIC_EXPORT_INTERVAL", ""); v != "" {
		if ms, err := strconv.Atoi(v); err != nil || ms <= 0 {
			return s, fmt.Errorf("OTEL_METRIC_EXPORT_INTERVAL is %q: want a positive number of milliseconds", v)
		}
	}
	if v := env("OTEL_RESOURCE_ATTRIBUTES", ""); v != "" {
		for entry := range strings.SplitSeq(v, ",") {
			if key, _, ok := strings.Cut(entry, "="); !ok || strings.TrimSpace(key) == "" {
				return s, fmt.Errorf("OTEL_RESOURCE_ATTRIBUTES is %q: want key=value entries, comma-separated", v)
			}
		}
	}
	on := disabled == "false"
	s.traces = on && (base || tracesURL) && slices.Contains(tracesTo, "otlp")
	s.metrics = on && (base || metricsURL) && slices.Contains(metricsTo, "otlp")
	return s, nil
}

// env is the variable key, trimmed, or fallback when that's empty.
func env(key, fallback string) string {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		return v
	}
	return fallback
}

// oneOf is key, lowercased, if it's one of allowed; fallback when unset.
func oneOf(key, fallback string, allowed ...string) (string, error) {
	v := strings.ToLower(env(key, fallback))
	if !slices.Contains(allowed, v) {
		return "", fmt.Errorf("%s is %q: want %s", key, os.Getenv(key), strings.Join(allowed, " or "))
	}
	return v, nil
}

// exporters is a comma-separated OTEL_*_EXPORTER list of allowed names; unset is otlp.
func exporters(key string, allowed ...string) ([]string, error) {
	var out []string
	for name := range strings.SplitSeq(strings.ToLower(env(key, "otlp")), ",") {
		if name = strings.TrimSpace(name); !slices.Contains(allowed, name) {
			return nil, fmt.Errorf("%s is %q: want a comma-separated list of %s", key, os.Getenv(key), strings.Join(allowed, ", "))
		}
		out = append(out, name)
	}
	return out, nil
}

// endpoint says whether the OTLP endpoint key is set, to an http:// or https:// URL.
func endpoint(key string) (bool, error) {
	v := env(key, "")
	if v == "" {
		return false, nil
	}
	if u, err := url.Parse(v); err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return false, fmt.Errorf("%s is %q: want an http:// or https:// URL", key, v)
	}
	return true, nil
}
