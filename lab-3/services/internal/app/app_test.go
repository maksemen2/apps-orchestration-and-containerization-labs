package app

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestHealthAndReadiness(t *testing.T) {
	t.Setenv("DATABASE_URL", "postgres://user:password@127.0.0.1:1/lab")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	a, err := New(ctx, slog.New(slog.NewJSONHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	defer a.Store.Close()
	mux := http.NewServeMux()
	a.BaseRoutes(mux)
	for _, tc := range []struct {
		path string
		fail string
		want int
	}{
		{"/health", "false", 200},
		{"/health", "true", 503},
		{"/ready", "false", 503},
	} {
		t.Setenv("HEALTH_FAIL", tc.fail)
		response := httptest.NewRecorder()
		mux.ServeHTTP(response, httptest.NewRequest(http.MethodGet, tc.path, nil))
		if response.Code != tc.want {
			t.Errorf("%s HEALTH_FAIL=%s: got %d, want %d", tc.path, tc.fail, response.Code, tc.want)
		}
	}
}

func TestVersionAndBusinessMetricBaseline(t *testing.T) {
	previous := Version
	Version = "0.2.0"
	defer func() { Version = previous }()
	t.Setenv("DATABASE_URL", "postgres://user:password@127.0.0.1:1/lab")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	a, err := New(ctx, slog.New(slog.NewJSONHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	defer a.Store.Close()
	mux := http.NewServeMux()
	a.BaseRoutes(mux)
	h := a.Instrument(mux)
	response := httptest.NewRecorder()
	h.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/health", nil))
	if got := response.Header().Get("X-App-Version"); got != "0.2.0" {
		t.Errorf("X-App-Version = %q", got)
	}
	metrics := httptest.NewRecorder()
	h.ServeHTTP(metrics, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	for _, expected := range []string{
		`app_build_info{version="0.2.0"} 1`,
		`http_errors_total{method="POST",route="/order"} 0`,
		`http_errors_total{method="GET",route="/orders"} 0`,
	} {
		if !strings.Contains(metrics.Body.String(), expected) {
			t.Errorf("metrics missing %q", expected)
		}
	}
}
