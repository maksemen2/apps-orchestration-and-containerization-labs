package main

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"lab3/services/internal/app"
)

func TestOrderValidationAndUnavailableDB(t *testing.T) {
	t.Setenv("DATABASE_URL", "postgres://user:password@127.0.0.1:1/lab")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	a, err := app.New(ctx, slog.New(slog.NewJSONHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	defer a.Store.Close()
	h := handler(a)
	for _, tc := range []struct {
		body string
		want int
	}{
		{`{"item":"","quantity":1}`, 400},
		{`{"item":"book","quantity":0}`, 400},
		{`{"item":"book","quantity":1,"extra":true}`, 400},
		{`{"item":"book","quantity":1}`, 503},
	} {
		response := httptest.NewRecorder()
		h.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/order", strings.NewReader(tc.body)))
		if response.Code != tc.want {
			t.Errorf("body=%s: got %d, want %d", tc.body, response.Code, tc.want)
		}
	}
}

func TestAPIConditioningKeepsProbesAndVersion(t *testing.T) {
	t.Setenv("DATABASE_URL", "postgres://user:password@127.0.0.1:1/lab")
	t.Setenv("FAIL", "true")
	t.Setenv("FLAKY", "false")
	t.Setenv("FLAKY_PERCENT", "0")
	t.Setenv("DELAY_MS", "0")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	a, err := app.New(ctx, slog.New(slog.NewJSONHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	defer a.Store.Close()
	h := a.Instrument(handler(a))

	for _, tc := range []struct {
		method string
		path   string
		body   string
	}{
		{http.MethodPost, "/order", `{"item":"book","quantity":1}`},
		{http.MethodGet, "/orders", ""},
	} {
		response := httptest.NewRecorder()
		h.ServeHTTP(response, httptest.NewRequest(tc.method, tc.path, strings.NewReader(tc.body)))
		if response.Code != http.StatusInternalServerError {
			t.Errorf("%s %s: got %d, want %d", tc.method, tc.path, response.Code, http.StatusInternalServerError)
		}
	}

	health := httptest.NewRecorder()
	h.ServeHTTP(health, httptest.NewRequest(http.MethodGet, "/health", nil))
	if health.Code != http.StatusOK {
		t.Errorf("/health: got %d, want %d", health.Code, http.StatusOK)
	}
	ready := httptest.NewRecorder()
	h.ServeHTTP(ready, httptest.NewRequest(http.MethodGet, "/ready", nil))
	if ready.Code != http.StatusServiceUnavailable {
		t.Errorf("/ready: got %d, want %d", ready.Code, http.StatusServiceUnavailable)
	}

	version := httptest.NewRecorder()
	h.ServeHTTP(version, httptest.NewRequest(http.MethodGet, "/version", nil))
	var body struct {
		Version string `json:"version"`
	}
	if err := json.Unmarshal(version.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.Version != app.Version {
		t.Errorf("/version = %q, want %q", body.Version, app.Version)
	}
}

func TestAPIFaultPercentageAndDelay(t *testing.T) {
	t.Setenv("FAIL", "false")
	t.Setenv("FLAKY", "true")
	t.Setenv("FLAKY_PERCENT", "100")
	t.Setenv("DELAY_MS", "15")
	start := time.Now()
	response := httptest.NewRecorder()
	handler(&app.App{}).ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/orders", nil))
	if response.Code != http.StatusInternalServerError {
		t.Fatalf("got %d, want %d", response.Code, http.StatusInternalServerError)
	}
	if time.Since(start) < 15*time.Millisecond {
		t.Fatal("configured API delay was not applied")
	}
}
