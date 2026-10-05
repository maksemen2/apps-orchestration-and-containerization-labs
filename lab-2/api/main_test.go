package main

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"testing"
	"time"
)

// Cancellation must stop a real listening server and release its socket.
func TestRunServesHTTPAndShutsDown(t *testing.T) {
	t.Setenv("HTTP_ADDR", "127.0.0.1:0")
	t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", "")
	t.Setenv("OTEL_EXPORTER_OTLP_TRACES_ENDPOINT", "")
	logs := &safeBuffer{}
	logger := slog.New(slog.NewJSONHandler(logs, nil))
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- run(ctx, logger) }()
	var addr string
	deadline := time.Now().Add(3 * time.Second)
	for addr == "" && time.Now().Before(deadline) {
		select {
		case err := <-done:
			t.Fatalf("server exited before listening: %v", err)
		default:
		}
		for _, line := range strings.Split(strings.TrimSpace(logs.contents()), "\n") {
			var record struct {
				Addr string `json:"addr"`
			}
			if json.Unmarshal([]byte(line), &record) == nil {
				addr = record.Addr
				if addr != "" {
					break
				}
			}
		}
		if addr == "" {
			time.Sleep(10 * time.Millisecond)
		}
	}
	if addr == "" {
		t.Fatal("server did not start")
	}
	client := &http.Client{Timeout: time.Second}
	response, err := client.Get("http://" + addr + "/health")
	if err != nil {
		t.Fatal(err)
	}
	body, err := io.ReadAll(response.Body)
	_ = response.Body.Close()
	if err != nil || response.StatusCode != 200 || string(body) != "ok" {
		t.Fatalf("health: body=%q err=%v status=%d", body, err, response.StatusCode)
	}
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("shutdown: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("server did not shut down")
	}
	if response, err := client.Get("http://" + addr + "/health"); err == nil {
		_ = response.Body.Close()
		t.Fatal("server still accepting connections after shutdown")
	}
}
