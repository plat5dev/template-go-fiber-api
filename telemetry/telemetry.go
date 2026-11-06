package telemetry

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/rs/zerolog"
	"go.opentelemetry.io/contrib/bridges/prometheus"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/exporters/otlp/otlpmetric/otlpmetrichttp"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp"
	"go.opentelemetry.io/otel/propagation"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.24.0"
	"go.opentelemetry.io/otel/trace"
	tracenoop "go.opentelemetry.io/otel/trace/noop"
)

// Telemetry bundles tracing, optional OTLP metrics bridge, and logging utilities.
// Prometheus scrape (/metrics) lives in the metrics package and is always on.
type Telemetry struct {
	tracerProvider trace.TracerProvider
	sdkProvider    *sdktrace.TracerProvider
	meterProvider  *sdkmetric.MeterProvider
	propagator     propagation.TextMapPropagator
	logger         zerolog.Logger
}

// Init configures OpenTelemetry per plat5 docs/reference/telemetry.md.
//
// Defaults when an OTLP destination is set:
//   - traces → OTLP on (unless OTEL_TRACES_EXPORTER excludes otlp)
//   - metrics OTLP → on when dest exists (OTEL_METRICS_EXPORTER unset defaults to otlp)
//   - /metrics scrape → always on (metrics package; independent of this Init)
//
// A telemetry variable that is set but invalid is an error, whether or not OTLP is on.
// An export that fails (the collector unreachable) is an error log line; the API keeps
// serving.
func Init(ctx context.Context) (*Telemetry, error) {
	zerolog.TimeFieldFormat = time.RFC3339Nano
	zerolog.MessageFieldName = "message"
	zerolog.LevelFieldName = "level"
	zerolog.TimestampFieldName = "timestamp"

	baseLogger := zerolog.New(os.Stdout).
		With().
		Timestamp().
		Logger()

	propagator := propagation.NewCompositeTextMapPropagator(
		propagation.TraceContext{},
	)
	otel.SetTextMapPropagator(propagator)
	otel.SetErrorHandler(otel.ErrorHandlerFunc(func(err error) {
		baseLogger.Error().Str("error_message", err.Error()).Msg("telemetry error")
	}))

	t := &Telemetry{
		propagator: propagator,
		logger:     baseLogger,
	}

	sdkDisabled, err := envBool("OTEL_SDK_DISABLED")
	if err != nil {
		return nil, err
	}
	if err := checkProtocol(); err != nil {
		return nil, err
	}
	tracesExporters, err := envExporterList("OTEL_TRACES_EXPORTER", "otlp", "none")
	if err != nil {
		return nil, err
	}
	metricsExporters, err := envExporterList("OTEL_METRICS_EXPORTER", "otlp", "prometheus", "none")
	if err != nil {
		return nil, err
	}
	sampleRatio, err := samplerRatio()
	if err != nil {
		return nil, err
	}
	interval, err := metricExportInterval()
	if err != nil {
		return nil, err
	}

	tracesDest, err := resolveOTLPEndpoint("traces", "/v1/traces")
	if err != nil {
		return nil, fmt.Errorf("invalid OTLP traces endpoint: %w", err)
	}
	metricsDest, err := resolveOTLPEndpoint("metrics", "/v1/metrics")
	if err != nil {
		return nil, fmt.Errorf("invalid OTLP metrics endpoint: %w", err)
	}

	res, err := buildResource(ctx)
	if err != nil {
		return nil, err
	}

	enableTraces := !sdkDisabled && tracesDest != nil && exporterIncludesOTLP(tracesExporters, true)
	enableMetricsOTLP := !sdkDisabled && metricsDest != nil && exporterIncludesOTLP(metricsExporters, true)

	if !enableTraces && !enableMetricsOTLP {
		tp := tracenoop.NewTracerProvider()
		otel.SetTracerProvider(tp)
		t.tracerProvider = tp
		return t, nil
	}

	if enableTraces {
		clientOpts := []otlptracehttp.Option{
			otlptracehttp.WithEndpoint(tracesDest.host),
			otlptracehttp.WithURLPath(tracesDest.path),
		}
		if tracesDest.insecure {
			clientOpts = append(clientOpts, otlptracehttp.WithInsecure())
		}
		exporter, err := otlptracehttp.New(ctx, clientOpts...)
		if err != nil {
			return nil, fmt.Errorf("failed to create trace exporter: %w", err)
		}
		tp := sdktrace.NewTracerProvider(
			sdktrace.WithSampler(sdktrace.ParentBased(sdktrace.TraceIDRatioBased(sampleRatio))),
			sdktrace.WithBatcher(exporter),
			sdktrace.WithResource(res),
		)
		otel.SetTracerProvider(tp)
		t.tracerProvider = tp
		t.sdkProvider = tp
	} else {
		tp := tracenoop.NewTracerProvider()
		otel.SetTracerProvider(tp)
		t.tracerProvider = tp
	}

	if enableMetricsOTLP {
		metricOpts := []otlpmetrichttp.Option{
			otlpmetrichttp.WithEndpoint(metricsDest.host),
			otlpmetrichttp.WithURLPath(metricsDest.path),
		}
		if metricsDest.insecure {
			metricOpts = append(metricOpts, otlpmetrichttp.WithInsecure())
		}
		metricExporter, err := otlpmetrichttp.New(ctx, metricOpts...)
		if err != nil {
			return nil, fmt.Errorf("failed to create metric exporter: %w", err)
		}
		reader := sdkmetric.NewPeriodicReader(metricExporter,
			sdkmetric.WithProducer(prometheus.NewMetricProducer()),
			sdkmetric.WithInterval(interval),
		)
		mp := sdkmetric.NewMeterProvider(
			sdkmetric.WithResource(res),
			sdkmetric.WithReader(reader),
		)
		otel.SetMeterProvider(mp)
		t.meterProvider = mp
	}

	return t, nil
}

func buildResource(ctx context.Context) (*resource.Resource, error) {
	serviceName := getEnv("OTEL_SERVICE_NAME", "api")
	serviceNamespace := getEnv("OTEL_SERVICE_NAMESPACE", "api")
	serviceInstanceID := getEnv("OTEL_SERVICE_INSTANCE_ID", getEnv("HOSTNAME", "api-local"))
	deploymentEnv := getEnv("OTEL_DEPLOYMENT_ENV", getEnv("DEPLOYMENT_ENV", "development"))
	serviceVersion := getEnv("OTEL_SERVICE_VERSION", getEnv("CI_COMMIT_TAG", "0.0.0"))

	res, err := resource.New(ctx,
		resource.WithFromEnv(),
		resource.WithProcess(),
		resource.WithTelemetrySDK(),
		resource.WithAttributes(
			semconv.ServiceNameKey.String(serviceName),
			semconv.ServiceNamespaceKey.String(serviceNamespace),
			semconv.ServiceInstanceIDKey.String(serviceInstanceID),
			semconv.ServiceVersionKey.String(serviceVersion),
			semconv.DeploymentEnvironmentKey.String(deploymentEnv),
		),
	)
	if err != nil {
		return nil, fmt.Errorf("failed to build resource: %w", err)
	}
	return res, nil
}

// Shutdown flushes exporters when OTLP is enabled.
func (t *Telemetry) Shutdown(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	var firstErr error
	if t.meterProvider != nil {
		if err := t.meterProvider.Shutdown(ctx); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	if t.sdkProvider != nil {
		if err := t.sdkProvider.Shutdown(ctx); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

// TracerProvider returns the underlying provider.
func (t *Telemetry) TracerProvider() trace.TracerProvider {
	return t.tracerProvider
}

// Propagator returns the configured propagator.
func (t *Telemetry) Propagator() propagation.TextMapPropagator {
	return t.propagator
}

// Logger returns the base zerolog logger.
func (t *Telemetry) Logger() zerolog.Logger {
	return t.logger
}

// LoggerWithContext enriches the logger with trace/span IDs if available.
func (t *Telemetry) LoggerWithContext(ctx context.Context) zerolog.Logger {
	spanContext := trace.SpanContextFromContext(ctx)
	if !spanContext.IsValid() {
		return t.logger
	}
	return t.logger.With().
		Str("trace_id", spanContext.TraceID().String()).
		Str("span_id", spanContext.SpanID().String()).
		Logger()
}

type otlpDest struct {
	host     string
	path     string
	insecure bool
}

// resolveOTLPEndpoint resolves OTEL_EXPORTER_OTLP_{SIGNAL}_ENDPOINT or base + suffix.
// Does not apply SDK_DISABLED or exporter lists — callers gate those.
func resolveOTLPEndpoint(signal, defaultPath string) (*otlpDest, error) {
	envKey := "OTEL_EXPORTER_OTLP_" + strings.ToUpper(signal) + "_ENDPOINT"
	target := strings.TrimSpace(os.Getenv(envKey))
	if target == "" {
		base := strings.TrimSpace(os.Getenv("OTEL_EXPORTER_OTLP_ENDPOINT"))
		if base == "" {
			return nil, nil
		}
		target = strings.TrimSuffix(base, "/") + defaultPath
	}

	parsed, err := url.Parse(target)
	if err != nil {
		return nil, err
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return nil, fmt.Errorf("OTLP endpoint %q: want an http:// or https:// URL", target)
	}
	if parsed.Host == "" {
		return nil, fmt.Errorf("OTLP endpoint missing host: %q", target)
	}

	// A per-signal URL without a path gets the signal's.
	path := parsed.Path
	if path == "" || path == "/" {
		path = defaultPath
	}
	return &otlpDest{
		host:     parsed.Host,
		path:     path,
		insecure: parsed.Scheme != "https",
	}, nil
}

// envExporterList parses a comma-separated OTEL_*_EXPORTER value.
// Returns nil when unset (caller applies defaults); a value outside allowed is an error.
func envExporterList(key string, allowed ...string) ([]string, error) {
	raw := strings.TrimSpace(os.Getenv(key))
	if raw == "" {
		return nil, nil
	}
	parts := strings.Split(raw, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		p = strings.ToLower(strings.TrimSpace(p))
		if p == "" {
			continue
		}
		if !slices.Contains(allowed, p) {
			return nil, fmt.Errorf("%s is %q: want a comma-separated list of %s", key, raw, strings.Join(allowed, ", "))
		}
		out = append(out, p)
	}
	if len(out) == 0 {
		return nil, nil
	}
	return out, nil
}

// exporterIncludesOTLP reports whether OTLP export is allowed.
// defaultWhenUnset: traces and metrics default true (otlp when dest exists).
func exporterIncludesOTLP(list []string, defaultWhenUnset bool) bool {
	if list == nil {
		return defaultWhenUnset
	}
	for _, e := range list {
		if e == "otlp" {
			return true
		}
	}
	return false
}

// envBool is a boolean OTEL_* variable: unset or empty is false, else true or false.
func envBool(key string) (bool, error) {
	switch v := strings.ToLower(strings.TrimSpace(os.Getenv(key))); v {
	case "", "false":
		return false, nil
	case "true":
		return true, nil
	default:
		return false, fmt.Errorf("%s is %q: want true or false", key, os.Getenv(key))
	}
}

// checkProtocol refuses any OTLP protocol but http/protobuf, the one the exporters speak.
func checkProtocol() error {
	for _, key := range []string{"OTEL_EXPORTER_OTLP_PROTOCOL", "OTEL_EXPORTER_OTLP_TRACES_PROTOCOL", "OTEL_EXPORTER_OTLP_METRICS_PROTOCOL"} {
		if v := strings.TrimSpace(os.Getenv(key)); v != "" && v != "http/protobuf" {
			return fmt.Errorf("%s is %q: only http/protobuf is supported", key, v)
		}
	}
	return nil
}

func getEnv(key, fallback string) string {
	if value := strings.TrimSpace(os.Getenv(key)); value != "" {
		return value
	}
	return fallback
}

// samplerRatio is OTEL_TRACES_SAMPLER_RATIO: unset is 1, else a number from 0 to 1.
func samplerRatio() (float64, error) {
	v := strings.TrimSpace(os.Getenv("OTEL_TRACES_SAMPLER_RATIO"))
	if v == "" {
		return 1, nil
	}
	f, err := strconv.ParseFloat(v, 64)
	if err != nil || f < 0 || f > 1 {
		return 0, fmt.Errorf("OTEL_TRACES_SAMPLER_RATIO is %q: want a number from 0 to 1", v)
	}
	return f, nil
}

// metricExportInterval is OTEL_METRIC_EXPORT_INTERVAL, milliseconds per the OTEL env spec:
// unset is 60000, else a positive whole number.
func metricExportInterval() (time.Duration, error) {
	v := strings.TrimSpace(os.Getenv("OTEL_METRIC_EXPORT_INTERVAL"))
	if v == "" {
		return 60 * time.Second, nil
	}
	ms, err := strconv.Atoi(v)
	if err != nil || ms <= 0 {
		return 0, fmt.Errorf("OTEL_METRIC_EXPORT_INTERVAL is %q: want a positive number of milliseconds", v)
	}
	return time.Duration(ms) * time.Millisecond, nil
}
