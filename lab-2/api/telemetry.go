package main

import (
	"context"
	"fmt"
	"log/slog"
	"net/url"
	"os"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
)

func newTracerProvider(ctx context.Context, logger *slog.Logger) (*sdktrace.TracerProvider, error) {
	serviceName := os.Getenv("OTEL_SERVICE_NAME")
	if serviceName == "" {
		serviceName = "api"
	}
	res, err := resource.New(ctx, resource.WithFromEnv(), resource.WithTelemetrySDK(),
		resource.WithAttributes(attribute.String("service.name", serviceName)))
	if err != nil {
		return nil, fmt.Errorf("create telemetry resource: %w", err)
	}
	// Sample every lab request so /fail and /slow are easy to find in Jaeger.
	options := []sdktrace.TracerProviderOption{
		sdktrace.WithResource(res), sdktrace.WithSampler(sdktrace.AlwaysSample()),
	}
	endpoint := os.Getenv("OTEL_EXPORTER_OTLP_TRACES_ENDPOINT")
	if endpoint == "" {
		endpoint = os.Getenv("OTEL_EXPORTER_OTLP_ENDPOINT")
	}
	if endpoint == "" {
		logger.Info("trace export disabled; set OTEL_EXPORTER_OTLP_ENDPOINT to enable", "service", serviceName)
		return sdktrace.NewTracerProvider(options...), nil
	}
	parsed, err := url.Parse(endpoint)
	if err != nil || parsed.Host == "" || (parsed.Scheme != "http" && parsed.Scheme != "https") {
		return nil, fmt.Errorf("OTLP endpoint must be an http(s) URL, got %q", endpoint)
	}
	protocol := os.Getenv("OTEL_EXPORTER_OTLP_TRACES_PROTOCOL")
	if protocol == "" {
		protocol = os.Getenv("OTEL_EXPORTER_OTLP_PROTOCOL")
	}
	if protocol != "" && protocol != "http/protobuf" {
		return nil, fmt.Errorf("this service uses OTLP http/protobuf, got protocol %q", protocol)
	}
	// The exporter reads standard OTEL_* variables, including trace endpoint overrides.
	exporter, err := otlptracehttp.New(ctx, otlptracehttp.WithTimeout(3*time.Second))
	if err != nil {
		return nil, fmt.Errorf("create OTLP exporter: %w", err)
	}
	options = append(options, sdktrace.WithBatcher(exporter,
		sdktrace.WithBatchTimeout(time.Second), sdktrace.WithMaxQueueSize(32768)))
	logger.Info("trace export enabled", "service", serviceName, "protocol", "http/protobuf")
	return sdktrace.NewTracerProvider(options...), nil
}
