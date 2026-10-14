package main

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

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
