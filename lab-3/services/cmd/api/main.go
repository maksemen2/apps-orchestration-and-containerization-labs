package main

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"lab3/services/internal/app"
)

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	logger.Info("starting service", "service", "api", "version", app.Version)
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	a, err := app.New(ctx, logger)
	if err != nil {
		logger.Error("startup failed", "error", err)
		os.Exit(1)
	}
	defer a.Store.Close()
	if err := app.RunHTTP(ctx, logger, a.Instrument(handler(a))); err != nil {
		logger.Error("http server failed", "error", err)
		os.Exit(1)
	}
}

func handler(a *app.App) http.Handler {
	mux := http.NewServeMux()
	a.BaseRoutes(mux)
	mux.HandleFunc("POST /order", func(w http.ResponseWriter, r *http.Request) {
		r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
		var input struct {
			Item     string `json:"item"`
			Quantity int    `json:"quantity"`
		}
		decoder := json.NewDecoder(r.Body)
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&input); err != nil {
			http.Error(w, "invalid JSON order", http.StatusBadRequest)
			return
		}
		var extra any
		if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
			http.Error(w, "one JSON object required", http.StatusBadRequest)
			return
		}
		input.Item = strings.TrimSpace(input.Item)
		if input.Item == "" || len(input.Item) > 200 || input.Quantity < 1 || input.Quantity > 10000 {
			http.Error(w, "item must be 1-200 characters and quantity 1-10000", http.StatusBadRequest)
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
		defer cancel()
		order, err := a.Store.Create(ctx, input.Item, input.Quantity)
		if err != nil {
			a.Log.Warn("create order failed")
			http.Error(w, "database unavailable", http.StatusServiceUnavailable)
			return
		}
		app.JSON(w, http.StatusCreated, order)
	})
	mux.HandleFunc("GET /orders", func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
		defer cancel()
		orders, err := a.Store.List(ctx)
		if err != nil {
			a.Log.Warn("list orders failed")
			http.Error(w, "database unavailable", http.StatusServiceUnavailable)
			return
		}
		app.JSON(w, http.StatusOK, orders)
	})
	return mux
}
