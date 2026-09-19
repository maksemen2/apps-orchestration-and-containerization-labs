package main

import (
	"net/http"
	"net/http/httptest"
	"runtime"
	"testing"
)

func TestRoutes(t *testing.T) {
	handler := (&api{}).handler()
	for _, tc := range []struct {
		method string
		path   string
		status int
	}{
		{http.MethodGet, "/health", http.StatusOK},
		{http.MethodGet, "/eat?mb=0", http.StatusOK},
		{http.MethodGet, "/eat", http.StatusBadRequest},
		{http.MethodGet, "/eat?mb=-1", http.StatusBadRequest},
		{http.MethodGet, "/eat?mb=abc", http.StatusBadRequest},
		{http.MethodGet, "/eat?mb=1.5", http.StatusBadRequest},
		{http.MethodGet, "/eat?mb=9223372036854775807", http.StatusBadRequest},
		{http.MethodPost, "/eat?mb=1", http.StatusMethodNotAllowed},
		{http.MethodPost, "/burn", http.StatusMethodNotAllowed},
		{http.MethodGet, "/missing", http.StatusNotFound},
	} {
		t.Run(tc.method+tc.path, func(t *testing.T) {
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, httptest.NewRequest(tc.method, tc.path, nil))
			if response.Code != tc.status {
				t.Fatalf("status = %d, want %d", response.Code, tc.status)
			}
			if tc.status == http.StatusOK && response.Body.String() != "ok" {
				t.Fatalf("body = %q, want ok", response.Body.String())
			}
		})
	}
}

func TestEatRetainsAllocations(t *testing.T) {
	a := &api{}
	handler := a.handler()
	for _, path := range []string{"/eat?mb=1", "/eat?mb=2"} {
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, path, nil))
		if response.Code != http.StatusOK {
			t.Fatalf("status = %d", response.Code)
		}
	}
	runtime.GC()
	if len(a.memory) != 2 {
		t.Fatalf("retained %d allocations, want 2", len(a.memory))
	}
	for i, block := range a.memory {
		if len(block) != (i+1)*megabyte {
			t.Fatalf("allocation %d: size = %d", i, len(block))
		}
		for offset, value := range block {
			if value != 1 {
				t.Fatalf("allocation %d: byte %d was not touched", i, offset)
			}
		}
	}
}
