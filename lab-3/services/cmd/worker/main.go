package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"lab3/services/internal/app"
	"lab3/services/internal/store"
)

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	logger.Info("starting service", "service", "worker", "version", app.Version)
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	a, err := app.New(ctx, logger)
	if err != nil {
		logger.Error("startup failed", "error", err)
		os.Exit(1)
	}
	defer a.Store.Close()
	go process(ctx, a)
	mux := http.NewServeMux()
	a.BaseRoutes(mux)
	if err := app.RunHTTP(ctx, logger, a.Instrument(mux)); err != nil {
		logger.Error("http server failed", "error", err)
		os.Exit(1)
	}
}

func process(ctx context.Context, a *app.App) {
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		if a.Store.Ready() {
			workCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
			id, found, err := a.Store.ProcessOne(workCtx)
			cancel()
			if err != nil && !errors.Is(err, store.ErrUnavailable) {
				a.WorkerErrors.Inc()
				a.Log.Warn("process order failed")
			} else if found {
				a.Processed.Inc()
				a.Log.Info("order processed", "order_id", id)
				continue
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}
