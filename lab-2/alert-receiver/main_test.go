package main

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
)

func TestNotificationPreservesWebhookDetailsInLog(t *testing.T) {
	var output bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&output, nil))
	payload := `{"receiver":"local-webhook","status":"firing","alerts":[{"status":"firing","labels":{"alertname":"ApiHighErrorRate","severity":"critical"},"annotations":{"summary":"API errors"}}]}`
	response := httptest.NewRecorder()
	handler(logger).ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/alerts", strings.NewReader(payload)))
	if response.Code != http.StatusNoContent {
		t.Fatalf("status = %d, body = %s", response.Code, response.Body.String())
	}
	var record struct {
		Notification map[string]any `json:"notification"`
	}
	if err := json.Unmarshal(output.Bytes(), &record); err != nil {
		t.Fatal(err)
	}
	var expected map[string]any
	if err := json.Unmarshal([]byte(payload), &expected); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(record.Notification, expected) {
		t.Fatalf("notification = %#v, want %#v", record.Notification, expected)
	}
}

func TestInvalidNotificationIsNotAcknowledgedOrLogged(t *testing.T) {
	for _, body := range []string{"", "null", "[]", "{", "{} {}"} {
		t.Run(body, func(t *testing.T) {
			var output bytes.Buffer
			response := httptest.NewRecorder()
			handler(slog.New(slog.NewJSONHandler(&output, nil))).ServeHTTP(response,
				httptest.NewRequest(http.MethodPost, "/alerts", strings.NewReader(body)))
			if response.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400", response.Code)
			}
			if output.Len() != 0 {
				t.Fatal("invalid notification was logged as received")
			}
		})
	}
}
