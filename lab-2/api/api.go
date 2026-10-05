package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/felixge/httpsnoop"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/trace"
)

type api struct {
	selfURL  string
	logger   *slog.Logger
	provider trace.TracerProvider
	client   *http.Client
	loadSlot chan struct{}
	registry *prometheus.Registry
	requests *prometheus.CounterVec
	errors   *prometheus.CounterVec
	duration *prometheus.HistogramVec
}

type loadResult struct {
	Target    string `json:"target"`
	Requested int64  `json:"requested"`
	Completed int64  `json:"completed"`
	Succeeded int64  `json:"succeeded"`
	Failed    int64  `json:"failed"`
}

func newAPI(logger *slog.Logger, provider trace.TracerProvider) *api {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = nil // Self-requests go directly to this process's loopback listener.
	transport.MaxIdleConnsPerHost = 100
	a := &api{
		logger: logger, provider: provider, loadSlot: make(chan struct{}, 1),
		registry: prometheus.NewRegistry(),
		client: &http.Client{
			Timeout: 5 * time.Second,
			Transport: otelhttp.NewTransport(transport,
				otelhttp.WithTracerProvider(provider),
				otelhttp.WithPropagators(propagation.TraceContext{})),
		},
		requests: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "http_requests_total", Help: "Completed HTTP requests, excluding metrics scrapes.",
		}, []string{"method", "route", "status"}),
		errors: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "http_errors_total", Help: "Completed HTTP requests with a 5xx response.",
		}, []string{"method", "route"}),
		duration: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Name: "http_request_duration_seconds", Help: "HTTP response duration in seconds.",
			Buckets: []float64{.005, .01, .025, .05, .1, .25, .5, 1, 1.5, 2, 2.5, 3, 5, 10},
		}, []string{"method", "route"}),
	}
	a.registry.MustRegister(a.requests, a.errors, a.duration,
		prometheus.NewGoCollector(), prometheus.NewProcessCollector(prometheus.ProcessCollectorOpts{}))
	for _, route := range []string{"/health", "/fail", "/slow", "/load"} {
		a.errors.WithLabelValues("GET", route).Add(0)
		status := "200"
		if route == "/fail" {
			status = "500"
		}
		// Expose a zero baseline before traffic so the first burst is observable.
		a.requests.WithLabelValues("GET", route, status).Add(0)
		a.duration.WithLabelValues("GET", route)
	}
	return a
}

func (a *api) handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /health", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		_, _ = io.WriteString(w, "ok")
	})
	mux.HandleFunc("GET /fail", func(w http.ResponseWriter, r *http.Request) {
		span := trace.SpanFromContext(r.Context())
		span.RecordError(errors.New("intentional failure"))
		span.SetStatus(codes.Error, "intentional failure")
		http.Error(w, "intentional failure", http.StatusInternalServerError)
	})
	mux.HandleFunc("GET /slow", a.slow)
	mux.HandleFunc("GET /load", a.load)
	mux.Handle("GET /metrics", promhttp.HandlerFor(a.registry, promhttp.HandlerOpts{}))
	instrumented := otelhttp.NewHandler(a.observe(mux), "api",
		otelhttp.WithTracerProvider(a.provider),
		otelhttp.WithPropagators(propagation.TraceContext{}),
		otelhttp.WithSpanNameFormatter(func(_ string, r *http.Request) string {
			return r.Method + " " + requestRoute(r.URL.Path)
		}))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/metrics" {
			mux.ServeHTTP(w, r)
			return
		}
		instrumented.ServeHTTP(w, r)
	})
}

func requestRoute(path string) string {
	switch path {
	case "/health", "/fail", "/slow", "/load":
		return path
	default:
		return "unmatched" // Raw URLs would create unbounded Prometheus label values.
	}
}

func requestMethod(method string) string {
	switch method {
	case "GET", "HEAD", "POST", "PUT", "PATCH", "DELETE", "OPTIONS":
		return method
	default:
		return "other"
	}
}

func (a *api) observe(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		route, method := requestRoute(r.URL.Path), requestMethod(r.Method)
		span := trace.SpanFromContext(r.Context())
		span.SetAttributes(attribute.String("http.route", route))
		w.Header().Set("X-Trace-ID", span.SpanContext().TraceID().String())
		measured := httpsnoop.CaptureMetrics(next, w, r)
		a.requests.WithLabelValues(method, route, strconv.Itoa(measured.Code)).Inc()
		a.duration.WithLabelValues(method, route).Observe(measured.Duration.Seconds())
		level := slog.LevelInfo
		if measured.Code >= 500 {
			a.errors.WithLabelValues(method, route).Inc()
			span.SetStatus(codes.Error, http.StatusText(measured.Code))
			level = slog.LevelError
		} else if measured.Code >= 400 {
			level = slog.LevelWarn
		}
		a.logger.Log(r.Context(), level, "http request",
			"method", r.Method, "route", route, "status", measured.Code,
			"duration_ms", float64(measured.Duration)/float64(time.Millisecond),
			"trace_id", span.SpanContext().TraceID().String(),
			"span_id", span.SpanContext().SpanID().String())
	})
}

func (a *api) slow(w http.ResponseWriter, r *http.Request) {
	delay := 2 * time.Second
	if raw := r.URL.Query().Get("delay"); raw != "" {
		var err error
		delay, err = time.ParseDuration(raw)
		if err != nil || delay < time.Second || delay > 3*time.Second {
			http.Error(w, "delay must be a duration between 1s and 3s", http.StatusBadRequest)
			return
		}
	}
	ctx, span := a.provider.Tracer("lab2/api").Start(r.Context(), "slow-op",
		trace.WithAttributes(attribute.Int64("delay_ms", delay.Milliseconds())))
	defer span.End()
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-timer.C:
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		_, _ = fmt.Fprintf(w, "slept %s\n", delay)
	case <-ctx.Done():
		span.RecordError(ctx.Err())
		span.SetStatus(codes.Error, "request cancelled")
		http.Error(w, "request cancelled", http.StatusRequestTimeout)
	}
}

func intParameter(r *http.Request, name string, fallback, maximum int) (int, error) {
	raw := r.URL.Query().Get(name)
	if raw == "" {
		return fallback, nil
	}
	value, err := strconv.Atoi(raw)
	if err != nil || value < 1 || value > maximum {
		return 0, fmt.Errorf("%s must be an integer between 1 and %d", name, maximum)
	}
	return value, nil
}

func (a *api) load(w http.ResponseWriter, r *http.Request) {
	count, err := intParameter(r, "requests", 200, 10000)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	concurrency, err := intParameter(r, "concurrency", 20, 100)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	target := r.URL.Query().Get("target")
	if target == "" {
		target = "/health"
	}
	switch target {
	case "/health", "/fail", "/slow":
	default:
		http.Error(w, "target must be /health, /fail, or /slow", http.StatusBadRequest)
		return
	}
	select {
	case a.loadSlot <- struct{}{}:
		defer func() { <-a.loadSlot }()
	default:
		http.Error(w, "a load burst is already running", http.StatusConflict)
		return
	}
	ctx, span := a.provider.Tracer("lab2/api").Start(r.Context(), "load-burst",
		trace.WithAttributes(attribute.Int("requests", count), attribute.String("target", target)))
	defer span.End()
	var next, succeeded, failed atomic.Int64
	var workers sync.WaitGroup
	for range min(concurrency, count) {
		workers.Go(func() {
			for next.Add(1) <= int64(count) {
				if ctx.Err() != nil {
					return
				}
				req, err := http.NewRequestWithContext(ctx, http.MethodGet, a.selfURL+target, nil)
				if err != nil {
					failed.Add(1)
					continue
				}
				response, err := a.client.Do(req)
				if err != nil {
					failed.Add(1)
					continue
				}
				_, readErr := io.Copy(io.Discard, response.Body)
				_ = response.Body.Close()
				if response.StatusCode >= 400 || readErr != nil {
					failed.Add(1)
				} else {
					succeeded.Add(1)
				}
			}
		})
	}
	workers.Wait()
	if ctx.Err() != nil {
		span.RecordError(ctx.Err())
		span.SetStatus(codes.Error, "load cancelled")
		http.Error(w, "load cancelled", http.StatusRequestTimeout)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(loadResult{
		Target: target, Requested: int64(count), Completed: succeeded.Load() + failed.Load(),
		Succeeded: succeeded.Load(), Failed: failed.Load(),
	})
}
