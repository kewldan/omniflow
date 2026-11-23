package remnawave

import (
	"context"
	"errors"
	"net/http"
	"testing"
	"time"
)

func TestHostsAndRawSubscription(t *testing.T) {
	t.Parallel()
	client := contractClient(t, func(writer http.ResponseWriter, request *http.Request) {
		if request.Header.Get("Authorization") != "Bearer secret" {
			t.Errorf("%s was not authenticated", request.URL.Path)
		}
		switch request.URL.Path {
		case "/api/hosts":
			_, _ = writer.Write([]byte(`{"response":[{"uuid":"h1","remark":"🇫🇮 Finland","address":"fi.example.net","port":443,"isDisabled":false,"isHidden":false,"nodes":["n1"]}]}`))
		case "/api/subscriptions/by-short-uuid/abcDEF_12-x/raw":
			_, _ = writer.Write([]byte(`{"response":{"user":{"id":42},"resolvedProxyConfigs":[{"finalRemark":"🇫🇮 Finland","address":"fi.example.net","port":443,"metadata":{"uuid":"h1","isHidden":false,"isDisabled":false}}]}}`))
		default:
			t.Errorf("unexpected %s", request.URL.Path)
			writer.WriteHeader(http.StatusNotFound)
		}
	})
	hosts, err := client.Hosts(context.Background())
	if err != nil || len(hosts) != 1 || hosts[0].Nodes[0] != "n1" || hosts[0].Port != 443 {
		t.Fatalf("hosts = %+v, %v", hosts, err)
	}
	raw, err := client.RawSubscription(context.Background(), "abcDEF_12-x")
	if err != nil || raw.UserID != 42 || raw.Entries[0].Metadata.UUID != "h1" {
		t.Fatalf("raw = %+v, %v", raw, err)
	}
	if _, err := client.RawSubscription(context.Background(), "../users"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("a malformed short UUID reached the panel: %v", err)
	}
}

func TestFetchSubscriptionForwardsTheClient(t *testing.T) {
	t.Parallel()
	client := contractClient(t, func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/api/sub/abcDEF/json" {
			t.Errorf("path = %s", request.URL.Path)
		}
		if request.Header.Get("Authorization") != "" {
			t.Error("the API token was sent to the public route")
		}
		if request.Header.Get("User-Agent") != "Happ/3.0" || request.Header.Get("X-Hwid") != "device-1" {
			t.Errorf("client headers were not forwarded: %v", request.Header)
		}
		if request.Header.Get("Cookie") != "" || request.Header.Get("X-Other") != "" {
			t.Error("a header outside the list was forwarded")
		}
		if request.Header.Get("X-Forwarded-For") != "203.0.113.9" {
			t.Errorf("client address = %q", request.Header.Get("X-Forwarded-For"))
		}
		writer.Header().Set("Subscription-Userinfo", "upload=0; download=1")
		writer.WriteHeader(http.StatusForbidden)
		_, _ = writer.Write([]byte(`blocked`))
	})
	var operations []string
	client.WithObserver(func(operation, _, _ string, _ time.Duration) { operations = append(operations, operation) })
	from := http.Header{}
	from.Set("User-Agent", "Happ/3.0")
	from.Set("X-Hwid", "device-1")
	from.Set("Cookie", "session=1")
	from.Set("X-Other", "1")
	content, err := client.FetchSubscription(context.Background(), "abcDEF", SubscriptionFormatXrayJSON, from, "203.0.113.9")
	if err != nil {
		t.Fatal(err)
	}
	if content.Status != http.StatusForbidden || string(content.Body) != "blocked" ||
		content.Header.Get("Subscription-Userinfo") == "" {
		t.Errorf("content = %+v", content)
	}
	// The short UUID is a credential: it must never become a metric label.
	if len(operations) != 1 || operations[0] != "GET /api/sub/:id/json" {
		t.Errorf("operations = %v", operations)
	}
}
