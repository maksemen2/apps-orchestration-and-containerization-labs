package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	"go.opentelemetry.io/otel"
)

func run(ctx context.Context, logger *slog.Logger) (result error) {
	provider, err := newTracerProvider(ctx, logger)
	if err != nil {
		return err
	}
	defer func() {
		flushCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		result = errors.Join(result, provider.Shutdown(flushCtx))
	}()
	addr := os.Getenv("HTTP_ADDR")
	if addr == "" {
		addr = ":8080"
	}
	listener, err := net.Listen("tcp", addr)
	if err != nil {
		return fmt.Errorf("listen on %s: %w", addr, err)
	}
	defer listener.Close()
	a := newAPI(logger, provider)
	localAddr := listener.Addr().(*net.TCPAddr)
	localIP := localAddr.IP
	if localIP.IsUnspecified() {
		if localIP.To4() != nil {
			localIP = net.ParseIP("127.0.0.1")
		} else {
			localIP = net.ParseIP("::1")
		}
	}
	a.selfURL = "http://" + net.JoinHostPort(localIP.String(), strconv.Itoa(localAddr.Port))
	defer a.client.CloseIdleConnections()
	server := &http.Server{
		Handler: a.handler(), ReadHeaderTimeout: 5 * time.Second, IdleTimeout: 60 * time.Second,
		ErrorLog: slog.NewLogLogger(logger.Handler(), slog.LevelError),
	}
	done := make(chan error, 1)
	go func() { done <- server.Serve(listener) }()
	logger.Info("api listening", "addr", listener.Addr().String())
	select {
	case err := <-done:
		return err
	case <-ctx.Done():
		logger.Info("shutting down")
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := server.Shutdown(shutdownCtx); err != nil {
			_ = server.Close()
			return fmt.Errorf("shutdown HTTP server: %w", err)
		}
		return nil
	}
}

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	otel.SetErrorHandler(otel.ErrorHandlerFunc(func(err error) {
		logger.Error("telemetry error", "error", err.Error())
	}))
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := run(ctx, logger); err != nil {
		logger.Error("api stopped", "error", err.Error())
		os.Exit(1)
	}
}
