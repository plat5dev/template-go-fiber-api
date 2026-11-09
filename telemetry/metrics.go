package telemetry

import (
	"net/http"
	"strconv"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

// The API's series, on Prometheus's default registry, which also has the process
// collector's (process_resident_memory_bytes, process_cpu_seconds_total,
// process_start_time_seconds) and Go's. /metrics serves them all, and OTLP pushes them.
var (
	requestsTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "http_requests_total",
		Help: "HTTP requests answered.",
	}, []string{"route", "method", "status"})
	requestDuration = promauto.NewHistogramVec(prometheus.HistogramOpts{
		Name:    "http_request_duration_seconds",
		Help:    "HTTP request duration.",
		Buckets: []float64{0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1, 2.5, 5},
	}, []string{"route", "method"})

	dbLabels    = []string{"db_system_name", "db_operation_name", "db_namespace"}
	dbOpsTotal  = promauto.NewCounterVec(prometheus.CounterOpts{Name: "db_operations_total", Help: "Database operations."}, dbLabels)
	dbOpsErrors = promauto.NewCounterVec(prometheus.CounterOpts{Name: "db_operation_errors_total", Help: "Database operations that failed."}, dbLabels)
	dbDuration  = promauto.NewHistogramVec(prometheus.HistogramOpts{
		Name:    "db_operation_duration_seconds",
		Help:    "Database operation duration.",
		Buckets: []float64{0.001, 0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1},
	}, dbLabels)
)

// MetricsHandler serves /metrics.
func MetricsHandler() http.Handler { return promhttp.Handler() }

func observeRequest(route, method string, status int, d time.Duration) {
	requestsTotal.WithLabelValues(route, method, strconv.Itoa(status)).Inc()
	requestDuration.WithLabelValues(route, method).Observe(d.Seconds())
}

// DB records one database operation, named like tasks.insert, that began at start:
//
//	defer telemetry.DB("tasks.insert", time.Now(), &err)
func DB(operation string, start time.Time, err *error) {
	labels := []string{"sqlite", operation, "api"}
	dbOpsTotal.WithLabelValues(labels...).Inc()
	dbDuration.WithLabelValues(labels...).Observe(time.Since(start).Seconds())
	if *err != nil {
		dbOpsErrors.WithLabelValues(labels...).Inc()
	}
}
