package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"strconv"
	"strings"
	"time"

	fiberotel "github.com/gofiber/contrib/v3/otel"
	"github.com/gofiber/fiber/v3"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
	otelprom "go.opentelemetry.io/contrib/bridges/prometheus"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/exporters/otlp/otlpmetric/otlpmetrichttp"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp"
	"go.opentelemetry.io/otel/propagation"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.43.0"
	"go.opentelemetry.io/otel/trace"
)

const errorTypeLocal = "telemetry.error_type"

var (
	httpRequestsTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "http_requests_total",
		Help: "HTTP requests answered.",
	}, []string{"route", "method", "status"})

	httpRequestDuration = promauto.NewHistogramVec(prometheus.HistogramOpts{
		Name:    "http_request_duration_seconds",
		Help:    "HTTP request duration.",
		Buckets: []float64{0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1, 2.5, 5},
	}, []string{"route", "method"})

	callerHeaders = [...]struct{ header, field string }{
		{"X-Plat5-Project-Id", "node_id"},
		{"X-Plat5-Member-Id", "member_id"},
		{"X-Plat5-User-Id", "user_id"},
	}
)

func newLogger(w io.Writer) *slog.Logger {
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

// setupTelemetry configures W3C trace propagation and optional OTLP HTTP export.
// Standard OTEL_* environment variables are handled directly by the OTel SDK.
func setupTelemetry(ctx context.Context, version string) (func(context.Context) error, error) {
	otel.SetTextMapPropagator(propagation.TraceContext{})
	otel.SetErrorHandler(otel.ErrorHandlerFunc(func(err error) {
		slog.Error("telemetry error", "error_message", err.Error())
	}))

	noop := func(context.Context) error { return nil }
	if strings.EqualFold(strings.TrimSpace(os.Getenv("OTEL_SDK_DISABLED")), "true") {
		return noop, nil
	}

	baseEndpoint := strings.TrimSpace(os.Getenv("OTEL_EXPORTER_OTLP_ENDPOINT"))
	pushTraces := (baseEndpoint != "" || strings.TrimSpace(os.Getenv("OTEL_EXPORTER_OTLP_TRACES_ENDPOINT")) != "") &&
		!strings.EqualFold(strings.TrimSpace(os.Getenv("OTEL_TRACES_EXPORTER")), "none")
	metricsExporter := strings.ToLower(strings.TrimSpace(os.Getenv("OTEL_METRICS_EXPORTER")))
	pushMetrics := (baseEndpoint != "" || strings.TrimSpace(os.Getenv("OTEL_EXPORTER_OTLP_METRICS_ENDPOINT")) != "") &&
		metricsExporter != "none" && metricsExporter != "prometheus"

	if !pushTraces && !pushMetrics {
		return noop, nil
	}

	res, err := resource.New(ctx,
		resource.WithAttributes(
			semconv.ServiceName("api"),
			semconv.ServiceNamespace("api"),
			semconv.ServiceVersion(version),
		),
		resource.WithFromEnv(),
		resource.WithTelemetrySDK(),
	)
	if err != nil {
		return nil, fmt.Errorf("telemetry resource: %w", err)
	}

	var shutdowns []func(context.Context) error
	if pushTraces {
		exp, err := otlptracehttp.New(ctx)
		if err != nil {
			return nil, fmt.Errorf("trace exporter: %w", err)
		}
		tp := sdktrace.NewTracerProvider(
			sdktrace.WithBatcher(exp),
			sdktrace.WithResource(res),
		)
		otel.SetTracerProvider(tp)
		shutdowns = append(shutdowns, tp.Shutdown)
	}
	if pushMetrics {
		exp, err := otlpmetrichttp.New(ctx)
		if err != nil {
			return nil, fmt.Errorf("metric exporter: %w", err)
		}
		mp := sdkmetric.NewMeterProvider(
			sdkmetric.WithResource(res),
			sdkmetric.WithReader(sdkmetric.NewPeriodicReader(exp,
				sdkmetric.WithProducer(otelprom.NewMetricProducer()))),
		)
		otel.SetMeterProvider(mp)
		shutdowns = append(shutdowns, mp.Shutdown)
	}

	return func(ctx context.Context) error {
		var errs []error
		for _, fn := range shutdowns {
			errs = append(errs, fn(ctx))
		}
		return errors.Join(errs...)
	}, nil
}

func telemetryMiddleware() []any {
	return []any{
		fiberotel.New(
			fiberotel.WithoutMetrics(true),
			fiberotel.WithResponsePropagators(propagation.NewCompositeTextMapPropagator()),
			fiberotel.WithCustomResponseAttributes(func(c fiber.Ctx) []attribute.KeyValue {
				if errType, ok := c.Locals(errorTypeLocal).(string); ok {
					return []attribute.KeyValue{attribute.String("error.type", errType)}
				}
				return nil
			}),
		),
		logRequest,
	}
}

func logRequest(c fiber.Ctx) error {
	start := time.Now()
	err := c.Next()
	if err != nil {
		if herr := c.App().ErrorHandler(c, err); herr != nil {
			_ = c.SendStatus(fiber.StatusInternalServerError)
		}
	}

	status := c.Response().StatusCode()
	duration := time.Since(start)
	route := "unknown"
	if c.Matched() {
		route = c.Route().Path
	}

	httpRequestsTotal.WithLabelValues(route, c.Method(), strconv.Itoa(status)).Inc()
	httpRequestDuration.WithLabelValues(route, c.Method()).Observe(duration.Seconds())

	ctx := c.Context()
	span := trace.SpanFromContext(ctx)
	attrs := []slog.Attr{
		slog.String("route", route),
		slog.String("method", c.Method()),
		slog.Int("status", status),
		slog.Float64("duration_ms", float64(duration.Microseconds())/1000),
	}
	if sc := span.SpanContext(); sc.IsValid() {
		attrs = append(attrs,
			slog.String("trace_id", sc.TraceID().String()),
			slog.String("span_id", sc.SpanID().String()),
		)
	}
	if id := c.Get("X-Request-ID"); id != "" {
		attrs = append(attrs, slog.String("request_id", id))
		span.SetAttributes(attribute.String("request_id", id))
	}
	for _, h := range callerHeaders {
		if id := c.Get(h.header); id != "" {
			attrs = append(attrs, slog.String(h.field, id))
			span.SetAttributes(attribute.String("plat5."+h.field, id))
		}
	}

	level := slog.LevelInfo
	switch {
	case status >= 500:
		level = slog.LevelError
		errType, msg := "internal", "request failed"
		var ae *apiError
		if errors.As(err, &ae) && ae.errorType != "" {
			errType = ae.errorType
		}
		if err != nil {
			msg = err.Error()
		}
		c.Locals(errorTypeLocal, errType)
		attrs = append(attrs, slog.String("error_type", errType), slog.String("error_message", msg))
	case status >= 400:
		level = slog.LevelWarn
	}
	slog.LogAttrs(ctx, level, "request completed", attrs...)
	return nil
}
