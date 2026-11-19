package mailer

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestNewResendConfiguration(t *testing.T) {
	t.Parallel()
	if _, err := NewResend("", ""); !errors.Is(err, ErrNotConfigured) {
		t.Fatalf("empty settings: %v, want ErrNotConfigured", err)
	}
	if _, err := NewResend("key", ""); err == nil || errors.Is(err, ErrNotConfigured) {
		t.Fatalf("half-configured: %v, want a configuration error", err)
	}
	if _, err := NewResend("key", "Omniflow <support@example.com>"); err != nil {
		t.Fatalf("configured: %v", err)
	}
}

func TestResendSendsAndClassifies(t *testing.T) {
	t.Parallel()
	var got map[string]any
	status := http.StatusOK
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/emails" || request.Header.Get("Authorization") != "Bearer key" {
			t.Errorf("unexpected request %s %s", request.URL.Path, request.Header.Get("Authorization"))
		}
		_ = json.NewDecoder(request.Body).Decode(&got)
		writer.WriteHeader(status)
	}))
	t.Cleanup(server.Close)
	resend, _ := NewResend("key", "support@example.com")
	resend.baseURL = server.URL
	if err := resend.Send(context.Background(), Message{To: "client@example.com", Subject: "Вход", Text: "ссылка"}); err != nil {
		t.Fatal(err)
	}
	if got["from"] != "support@example.com" || got["subject"] != "Вход" || got["to"].([]any)[0] != "client@example.com" {
		t.Fatalf("payload = %v", got)
	}
	status = http.StatusUnprocessableEntity
	err := resend.Send(context.Background(), Message{To: "client@example.com", Subject: "x", Text: "y"})
	if err == nil || strings.Contains(err.Error(), "client@example.com") {
		t.Fatalf("error = %v, want a failure that does not name the recipient", err)
	}
}
