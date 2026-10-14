package app

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"lab3/services/internal/store"
)

type App struct {
	Store        *store.Store
	Log          *slog.Logger
	Registry     *prometheus.Registry
	DBReady      prometheus.Gauge
	Requests     *prometheus.CounterVec
	Errors       *prometheus.CounterVec
	Duration     *prometheus.HistogramVec
	Processed    prometheus.Counter
	WorkerErrors prometheus.Counter
}

// Version is set by the image build and is exposed to clients and Prometheus.
var Version = "dev"

func New(ctx context.Context, logger *slog.Logger) (*App, error) {
	url := os.Getenv("DATABASE_URL")
	if url == "" {
		return nil, errors.New("DATABASE_URL is required")
	}
	db, err := store.New(ctx, url)
	if err != nil {
		return nil, err
	}
	r := prometheus.NewRegistry()
	a := &App{
		Store: db, Log: logger, Registry: r,
		DBReady:      prometheus.NewGauge(prometheus.GaugeOpts{Name: "database_ready", Help: "1 when PostgreSQL is available and initialized."}),
		Requests:     prometheus.NewCounterVec(prometheus.CounterOpts{Name: "http_requests_total", Help: "HTTP requests by route, method and status."}, []string{"route", "method", "status"}),
		Errors:       prometheus.NewCounterVec(prometheus.CounterOpts{Name: "http_errors_total", Help: "HTTP requests with a 5xx response by route and method."}, []string{"route", "method"}),
		Duration:     prometheus.NewHistogramVec(prometheus.HistogramOpts{Name: "http_request_duration_seconds", Help: "HTTP request duration by route and method."}, []string{"route", "method"}),
		Processed:    prometheus.NewCounter(prometheus.CounterOpts{Name: "worker_orders_processed_total", Help: "Orders processed by this worker."}),
		WorkerErrors: prometheus.NewCounter(prometheus.CounterOpts{Name: "worker_errors_total", Help: "Database errors seen by this worker."}),
	}
	buildInfo := prometheus.NewGauge(prometheus.GaugeOpts{Name: "app_build_info", Help: "Build version of this process.", ConstLabels: prometheus.Labels{"version": Version}})
	buildInfo.Set(1)
	r.MustRegister(a.DBReady, a.Requests, a.Errors, a.Duration, a.Processed, a.WorkerErrors, buildInfo)
	// Pre-initialize business error series so a first failure is visible to rate().
	a.Errors.WithLabelValues("/order", "POST").Add(0)
	a.Errors.WithLabelValues("/orders", "GET").Add(0)
	go db.Monitor(ctx, logger, func(ready bool) {
		if ready {
			a.DBReady.Set(1)
		} else {
			a.DBReady.Set(0)
		}
	})
	return a, nil
}

func (a *App) BaseRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /health", func(w http.ResponseWriter, r *http.Request) {
		if os.Getenv("HEALTH_FAIL") == "true" {
			http.Error(w, "unhealthy", http.StatusServiceUnavailable)
			return
		}
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.Write([]byte("ok\n"))
	})
	mux.HandleFunc("GET /ready", func(w http.ResponseWriter, r *http.Request) {
		if !a.Store.Ready() {
			http.Error(w, "database unavailable", http.StatusServiceUnavailable)
			return
		}
		w.Write([]byte("ok\n"))
	})
	mux.Handle("GET /metrics", promhttp.HandlerFor(a.Registry, promhttp.HandlerOpts{}))
}

func (a *App) Instrument(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-App-Version", Version)
		if r.URL.Path == "/metrics" {
			next.ServeHTTP(w, r)
			return
		}
		start := time.Now()
		ww := &statusWriter{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(ww, r)
		route := r.URL.Path
		switch route {
		case "/health", "/ready", "/order", "/orders":
		default:
			route = "other"
		}
		status := strconv.Itoa(ww.status)
		method := requestMethod(r.Method)
		a.Requests.WithLabelValues(route, method, status).Inc()
		a.Errors.WithLabelValues(route, method).Add(0)
		if ww.status >= 500 {
			a.Errors.WithLabelValues(route, method).Inc()
		}
		a.Duration.WithLabelValues(route, method).Observe(time.Since(start).Seconds())
		a.Log.Info("http request", "method", method, "route", route, "status", ww.status, "duration_ms", time.Since(start).Milliseconds())
	})
}

func requestMethod(method string) string {
	switch method {
	case "GET", "HEAD", "POST", "PUT", "PATCH", "DELETE", "OPTIONS":
		return method
	default:
		return "other"
	}
}

type statusWriter struct {
	http.ResponseWriter
	status int
}

func (w *statusWriter) WriteHeader(status int) {
	w.status = status
	w.ResponseWriter.WriteHeader(status)
}

func JSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(value)
}

func RunHTTP(ctx context.Context, logger *slog.Logger, handler http.Handler) error {
	port := os.Getenv("PORT")
	if port == "" {
		port = "8080"
	}
	if strings.Contains(port, ":") {
		return errors.New("PORT must be a port number")
	}
	server := &http.Server{Addr: ":" + port, Handler: handler, ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 10 * time.Second, WriteTimeout: 10 * time.Second, IdleTimeout: 60 * time.Second}
	result := make(chan error, 1)
	go func() { result <- server.ListenAndServe() }()
	logger.Info("http listening", "port", port)
	select {
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		return server.Shutdown(shutdownCtx)
	case err := <-result:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	}
}
