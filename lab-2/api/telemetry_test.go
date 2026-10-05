package main

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	collectortrace "go.opentelemetry.io/proto/otlp/collector/trace/v1"
	"google.golang.org/protobuf/proto"
)

// Exporter wiring must deliver actual OTLP spans with the configured service name.
func TestExportsOTLPWithServiceName(t *testing.T) {
	for _, explicit := range []bool{false, true} {
		t.Run(map[bool]string{false: "generic endpoint", true: "trace endpoint override"}[explicit], func(t *testing.T) {
			received := make(chan *collectortrace.ExportTraceServiceRequest, 1)
			path := "/v1/traces"
			if explicit {
				path = "/custom/traces"
			}
			collector := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != path || r.Method != http.MethodPost {
					t.Errorf("OTLP request: %s %s", r.Method, r.URL.Path)
				}
				payload, err := io.ReadAll(r.Body)
				if err != nil {
					t.Error(err)
					w.WriteHeader(400)
					return
				}
				message := &collectortrace.ExportTraceServiceRequest{}
				if err := proto.Unmarshal(payload, message); err != nil {
					t.Error(err)
					w.WriteHeader(400)
					return
				}
				received <- message
				w.Header().Set("Content-Type", "application/x-protobuf")
			}))
			defer collector.Close()
			t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", collector.URL)
			t.Setenv("OTEL_EXPORTER_OTLP_TRACES_ENDPOINT", "")
			t.Setenv("OTEL_EXPORTER_OTLP_PROTOCOL", "http/protobuf")
			t.Setenv("OTEL_EXPORTER_OTLP_TRACES_PROTOCOL", "http/protobuf")
			t.Setenv("OTEL_EXPORTER_OTLP_COMPRESSION", "none")
			t.Setenv("OTEL_EXPORTER_OTLP_TRACES_COMPRESSION", "none")
			t.Setenv("OTEL_SERVICE_NAME", "lab2-test")
			t.Setenv("OTEL_RESOURCE_ATTRIBUTES", "")
			if explicit {
				t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", "http://127.0.0.1:1")
				t.Setenv("OTEL_EXPORTER_OTLP_TRACES_ENDPOINT", collector.URL+path)
			}
			logger := slog.New(slog.NewJSONHandler(io.Discard, nil))
			provider, err := newTracerProvider(context.Background(), logger)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = provider.Shutdown(context.Background()) })
			a := newAPI(logger, provider)
			response := request(t, a.handler(), "/health")
			if err := provider.ForceFlush(context.Background()); err != nil {
				t.Fatal(err)
			}
			select {
			case message := <-received:
				if len(message.ResourceSpans) != 1 {
					t.Fatalf("resource spans: %v", message.ResourceSpans)
				}
				resource := message.ResourceSpans[0]
				var serviceName string
				for _, attr := range resource.Resource.Attributes {
					if attr.Key == "service.name" {
						serviceName = attr.Value.GetStringValue()
					}
				}
				if serviceName != "lab2-test" || len(resource.ScopeSpans) != 1 || len(resource.ScopeSpans[0].Spans) != 1 {
					t.Fatalf("unexpected exported resource: %+v", resource)
				}
				span := resource.ScopeSpans[0].Spans[0]
				if span.Name != "GET /health" || len(span.TraceId) != 16 || response.Header().Get("X-Trace-ID") == "" {
					t.Fatalf("unexpected exported span: %+v", span)
				}
			default:
				t.Fatal("no OTLP request received after flush")
			}
		})
	}
}

func TestTracingWithoutCollectorStillCreatesTraceIDs(t *testing.T) {
	t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", "")
	t.Setenv("OTEL_EXPORTER_OTLP_TRACES_ENDPOINT", "")
	logger := slog.New(slog.NewJSONHandler(io.Discard, nil))
	provider, err := newTracerProvider(context.Background(), logger)
	if err != nil {
		t.Fatal(err)
	}
	defer provider.Shutdown(context.Background())
	response := request(t, newAPI(logger, provider).handler(), "/health")
	if response.Code != 200 || len(response.Header().Get("X-Trace-ID")) != 32 {
		t.Fatal("local mode has no valid request trace")
	}
}

func TestRejectsMalformedOTLPEndpoint(t *testing.T) {
	t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", "collector:4318")
	t.Setenv("OTEL_EXPORTER_OTLP_TRACES_ENDPOINT", "")
	logger := slog.New(slog.NewJSONHandler(io.Discard, nil))
	if provider, err := newTracerProvider(context.Background(), logger); err == nil {
		_ = provider.Shutdown(context.Background())
		t.Fatal("invalid OTLP endpoint silently accepted")
	}
}
