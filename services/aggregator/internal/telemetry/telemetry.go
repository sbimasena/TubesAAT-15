// Package telemetry gives a service RED metrics on /metrics and OpenTelemetry traces
// (context-m2.md §8.1). It imports nothing from this service, so other services copy
// this file into their own internal/telemetry and call the same three functions:
//
//	shutdown := telemetry.Setup(ctx, "client-api", logger) // once, in main
//	handler = telemetry.Middleware("client-api", handler)  // outermost HTTP handler
//	mux.Handle("GET /metrics", telemetry.MetricsHandler())
//
// and annotate the pod with prometheus.io/scrape: "true" and prometheus.io/port.
package telemetry

import (
	"context"
	"log/slog"
	"net/http"
	"os"
	"strconv"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.26.0"
	"go.opentelemetry.io/otel/trace"
)

var (
	requests = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "http_requests_total", Help: "HTTP requests served, by route pattern and status code.",
	}, []string{"route", "method", "code"})
	duration = promauto.NewHistogramVec(prometheus.HistogramOpts{
		Name: "http_request_duration_seconds", Help: "HTTP request latency by route pattern.",
		Buckets: []float64{.005, .01, .025, .05, .1, .25, .5, 1, 2.5, 5, 10},
	}, []string{"route", "method"})
)

// Setup exports traces over OTLP/HTTP to OTEL_EXPORTER_OTLP_ENDPOINT (Jaeger) and makes
// outgoing calls through http.DefaultTransport carry the trace. Without the variable
// (Compose, tests) tracing stays a no-op. The returned function flushes pending spans.
func Setup(ctx context.Context, service string, logger *slog.Logger) func(context.Context) error {
	otel.SetTextMapPropagator(propagation.TraceContext{})
	http.DefaultTransport = otelhttp.NewTransport(http.DefaultTransport)
	if os.Getenv("OTEL_EXPORTER_OTLP_ENDPOINT") == "" {
		return func(context.Context) error { return nil }
	}
	exporter, err := otlptracehttp.New(ctx)
	if err != nil {
		logger.Warn("tracing disabled", "error", err)
		return func(context.Context) error { return nil }
	}
	provider := sdktrace.NewTracerProvider(
		sdktrace.WithBatcher(exporter),
		sdktrace.WithResource(resource.NewSchemaless(semconv.ServiceName(service),
			semconv.K8SPodName(os.Getenv("HOSTNAME")), semconv.K8SNodeName(os.Getenv("NODE_NAME")))),
	)
	otel.SetTracerProvider(provider)
	return provider.Shutdown
}

// Middleware records metrics per route pattern (not raw path, which would explode label
// cardinality) and opens a server span continuing an incoming traceparent.
func Middleware(service string, next http.Handler) http.Handler {
	measured := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		recorder := &statusRecorder{ResponseWriter: w, code: http.StatusOK}
		started := time.Now()
		next.ServeHTTP(recorder, r)
		route := r.Pattern // set by ServeMux on this request while routing
		if route == "" {
			route = "unmatched"
		}
		requests.WithLabelValues(route, r.Method, strconv.Itoa(recorder.code)).Inc()
		duration.WithLabelValues(route, r.Method).Observe(time.Since(started).Seconds())
	})
	return otelhttp.NewHandler(measured, service,
		otelhttp.WithFilter(func(r *http.Request) bool { return r.URL.Path != "/metrics" && r.URL.Path != "/health" }),
		otelhttp.WithSpanNameFormatter(func(_ string, r *http.Request) string { return r.Method + " " + r.URL.Path }))
}

// MetricsHandler serves every metric registered with promauto, including the Go runtime.
func MetricsHandler() http.Handler { return promhttp.Handler() }

// TraceID returns the current trace ID for log lines, or "" outside a trace.
func TraceID(ctx context.Context) string {
	if sc := trace.SpanContextFromContext(ctx); sc.HasTraceID() {
		return sc.TraceID().String()
	}
	return ""
}

type statusRecorder struct {
	http.ResponseWriter
	code int
}

func (r *statusRecorder) WriteHeader(code int) {
	r.code = code
	r.ResponseWriter.WriteHeader(code)
}

// Flush keeps streaming handlers (SSE) working behind the middleware.
func (r *statusRecorder) Flush() {
	if f, ok := r.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}
