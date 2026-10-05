package main

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"go.opentelemetry.io/otel/codes"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
)

type safeBuffer struct {
	sync.Mutex
	bytes.Buffer
}

func (b *safeBuffer) Write(p []byte) (int, error) {
	b.Lock()
	defer b.Unlock()
	return b.Buffer.Write(p)
}

func (b *safeBuffer) contents() string {
	b.Lock()
	defer b.Unlock()
	return b.Buffer.String()
}

func testAPI(t *testing.T) (*api, *safeBuffer, *tracetest.InMemoryExporter) {
	t.Helper()
	logs := &safeBuffer{}
	exporter := tracetest.NewInMemoryExporter()
	provider := sdktrace.NewTracerProvider(sdktrace.WithSyncer(exporter))
	t.Cleanup(func() { _ = provider.Shutdown(context.Background()) })
	return newAPI(slog.New(slog.NewJSONHandler(logs, nil)), provider), logs, exporter
}

func request(t *testing.T, handler http.Handler, target string) *httptest.ResponseRecorder {
	t.Helper()
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, target, nil))
	return response
}

// A scrape before the first burst must provide a baseline for request rates.
func TestKnownRoutesExposeZeroCountersBeforeTraffic(t *testing.T) {
	a, _, _ := testAPI(t)
	metrics := request(t, a.handler(), "/metrics").Body.String()
	for _, route := range []struct {
		path   string
		status string
	}{
		{"/health", "200"},
		{"/fail", "500"},
		{"/slow", "200"},
		{"/load", "200"},
	} {
		t.Run(route.path, func(t *testing.T) {
			for _, line := range []string{
				`http_requests_total{method="GET",route="` + route.path + `",status="` + route.status + `"} 0`,
				`http_errors_total{method="GET",route="` + route.path + `"} 0`,
				`http_request_duration_seconds_count{method="GET",route="` + route.path + `"} 0`,
			} {
				if !strings.Contains(metrics, line+"\n") {
					t.Errorf("missing zero baseline: %s", line)
				}
			}
		})
	}
}

// Missing instrumentation must break the RED counts and log/trace correlation.
func TestFailProducesMetricsAndCorrelatedErrorLog(t *testing.T) {
	a, logs, exporter := testAPI(t)
	handler := a.handler()
	if response := request(t, handler, "/health"); response.Code != 200 || response.Body.String() != "ok" {
		t.Fatalf("health: status=%d body=%q", response.Code, response.Body.String())
	}
	response := request(t, handler, "/fail")
	if response.Code != 500 {
		t.Fatalf("fail: status=%d, want 500", response.Code)
	}
	traceID := response.Header().Get("X-Trace-ID")
	if len(traceID) != 32 || traceID == strings.Repeat("0", 32) {
		t.Fatalf("invalid trace ID: %q", traceID)
	}
	metrics := request(t, handler, "/metrics").Body.String()
	for _, line := range []string{
		`http_requests_total{method="GET",route="/health",status="200"} 1`,
		`http_requests_total{method="GET",route="/fail",status="500"} 1`,
		`http_errors_total{method="GET",route="/fail"} 1`,
		`http_request_duration_seconds_count{method="GET",route="/fail"} 1`,
	} {
		if !strings.Contains(metrics, line) {
			t.Errorf("missing metric: %s", line)
		}
	}
	for _, line := range strings.Split(strings.TrimSpace(logs.contents()), "\n") {
		var record map[string]any
		if err := json.Unmarshal([]byte(line), &record); err != nil {
			t.Fatalf("non-JSON log: %v", err)
		}
		if record["route"] == "/fail" {
			if record["level"] != "ERROR" || record["trace_id"] != traceID || record["status"] != float64(500) {
				t.Fatalf("uncorrelated error log: %v", record)
			}
			spans := exporter.GetSpans()
			for _, span := range spans {
				if span.Name == "GET /fail" && span.SpanContext.TraceID().String() == traceID {
					if span.Status.Code != codes.Error || len(span.Events) == 0 {
						t.Fatalf("fail span has no error status/event: %+v", span)
					}
					return
				}
			}
			t.Fatal("error log has no matching span")
		}
	}
	t.Fatal("no error log for /fail")
}

// The wait must be in a child span, not just disappear into the server span.
func TestSlowHasTimedChildSpan(t *testing.T) {
	a, _, exporter := testAPI(t)
	started := time.Now()
	response := request(t, a.handler(), "/slow?delay=1s")
	if response.Code != 200 || time.Since(started) < time.Second {
		t.Fatalf("slow: status=%d elapsed=%s", response.Code, time.Since(started))
	}
	spans := exporter.GetSpans()
	for _, child := range spans {
		if child.Name != "slow-op" {
			continue
		}
		if child.EndTime.Sub(child.StartTime) < time.Second {
			t.Fatal("slow-op span does not cover the wait")
		}
		for _, parent := range spans {
			if parent.Name == "GET /slow" && child.Parent.SpanID() == parent.SpanContext.SpanID() && child.SpanContext.TraceID() == parent.SpanContext.TraceID() {
				return
			}
		}
		t.Fatal("slow-op is not a child of the request span")
	}
	t.Fatal("missing slow-op span")
}

// /load must issue real HTTP requests that increment target metrics.
func TestLoadGeneratesRequestsWithSharedTrace(t *testing.T) {
	a, _, exporter := testAPI(t)
	server := httptest.NewServer(a.handler())
	defer server.Close()
	a.selfURL = server.URL
	response, err := http.Get(server.URL + "/load?requests=12&concurrency=3&target=/fail")
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != 200 {
		t.Fatalf("load: status=%d, want 200", response.StatusCode)
	}
	var result loadResult
	if err := json.NewDecoder(response.Body).Decode(&result); err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != 200 || result.Completed != 12 || result.Failed != 12 || result.Succeeded != 0 {
		t.Fatalf("load: status=%d result=%+v", response.StatusCode, result)
	}
	metrics := request(t, a.handler(), "/metrics").Body.String()
	if !strings.Contains(metrics, `http_errors_total{method="GET",route="/fail"} 12`) {
		t.Fatal("load did not increment target error metrics")
	}
	traceID := response.Header.Get("X-Trace-ID")
	var incoming int
	for _, span := range exporter.GetSpans() {
		if span.Name == "GET /fail" {
			incoming++
			if span.SpanContext.TraceID().String() != traceID || !span.Parent.IsValid() {
				t.Fatal("load child request lost its parent trace")
			}
		}
	}
	if incoming != 12 {
		t.Fatalf("incoming target spans=%d, want 12", incoming)
	}
}

// Unbounded input or recursive /load calls would make experiments uncontrollable.
func TestRejectsInvalidExperimentParameters(t *testing.T) {
	a, _, _ := testAPI(t)
	for _, target := range []string{
		"/slow?delay=0", "/slow?delay=10s", "/slow?delay=oops",
		"/load?requests=0", "/load?requests=10001", "/load?requests=nope",
		"/load?concurrency=0", "/load?concurrency=101",
		"/load?target=/load", "/load?target=http://example.com/",
	} {
		t.Run(target, func(t *testing.T) {
			if response := request(t, a.handler(), target); response.Code != 400 {
				t.Fatalf("status=%d body=%q, want 400", response.Code, response.Body.String())
			}
		})
	}
}

// Cancelled requests must release the slow operation promptly.
func TestSlowHonorsCancellation(t *testing.T) {
	a, _, _ := testAPI(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	req := httptest.NewRequest(http.MethodGet, "/slow", nil).WithContext(ctx)
	response := httptest.NewRecorder()
	started := time.Now()
	a.handler().ServeHTTP(response, req)
	if response.Code != 408 || time.Since(started) > 500*time.Millisecond {
		t.Fatalf("cancelled slow: status=%d elapsed=%s", response.Code, time.Since(started))
	}
}

// Scrapes must not generate artificial traffic, logs, or traces.
func TestMetricsScrapeDoesNotObserveItself(t *testing.T) {
	a, logs, exporter := testAPI(t)
	handler := a.handler()
	for range 2 {
		if response := request(t, handler, "/metrics"); response.Code != 200 {
			t.Fatalf("metrics status=%d", response.Code)
		}
	}
	if logs.contents() != "" || len(exporter.GetSpans()) != 0 {
		t.Fatal("scrapes created request logs or spans")
	}
	request(t, handler, "/does-not-exist-1")
	request(t, handler, "/does-not-exist-2")
	metrics := request(t, handler, "/metrics").Body.String()
	if !strings.Contains(metrics, `http_requests_total{method="GET",route="unmatched",status="404"} 2`) || strings.Contains(metrics, "does-not-exist") {
		t.Fatal("unknown URLs produce unbounded metric labels")
	}
}
