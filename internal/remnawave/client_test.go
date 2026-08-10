package remnawave

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestClientSubscription(t *testing.T) {
	t.Parallel()
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/api/subscriptions/by-id/42" {
			t.Fatalf("unexpected path %q", request.URL.Path)
		}
		if request.Header.Get("Authorization") != "Bearer secret" {
			t.Fatal("missing bearer token")
		}
		writer.Header().Set("Content-Type", "application/json")
		_, _ = writer.Write([]byte(`{"response":{"isFound":true,"user":{"daysLeft":12,"trafficUsed":"1 GB","trafficLimit":"10 GB","username":"demo","expiresAt":"2026-09-01T00:00:00Z","isActive":true,"userStatus":"ACTIVE","trafficUsedBytes":"1073741824","trafficLimitBytes":"10737418240"},"subscriptionUrl":"https://sub.example/test"}}`))
	}))
	defer server.Close()

	client, err := NewClient(server.URL, "secret")
	if err != nil {
		t.Fatal(err)
	}
	subscription, err := client.Subscription(context.Background(), 42)
	if err != nil {
		t.Fatal(err)
	}
	if subscription.User.DaysLeft != 12 || subscription.User.Status != "ACTIVE" {
		t.Fatalf("unexpected subscription: %#v", subscription)
	}
}

func TestClientNotFound(t *testing.T) {
	t.Parallel()
	server := httptest.NewServer(http.NotFoundHandler())
	defer server.Close()
	client, err := NewClient(server.URL, "secret")
	if err != nil {
		t.Fatal(err)
	}
	_, err = client.User(context.Background(), 9)
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("expected not found, got %v", err)
	}
}

func TestClientUserByTelegramIDUsesExactStreamFilter(t *testing.T) {
	t.Parallel()
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/api/users/stream" || request.URL.Query().Get("telegramId") != "987654321" || request.URL.Query().Get("size") != "2" {
			t.Fatalf("unexpected lookup URL %s", request.URL.String())
		}
		writer.Header().Set("Content-Type", "application/json")
		_, _ = writer.Write([]byte(`{"response":{"users":[{"id":73,"username":"linked","status":"ACTIVE","trafficLimitBytes":0,"expireAt":"2026-09-01T00:00:00Z","hwidDeviceLimit":null,"subscriptionUrl":"https://sub.example/test","userTraffic":{"usedTrafficBytes":0,"lifetimeUsedTrafficBytes":0,"onlineAt":null}}]}}`))
	}))
	defer server.Close()

	client, err := NewClient(server.URL, "secret")
	if err != nil {
		t.Fatal(err)
	}
	user, err := client.UserByTelegramID(context.Background(), 987654321)
	if err != nil {
		t.Fatal(err)
	}
	if user.ID != 73 || user.Username != "linked" {
		t.Fatalf("unexpected user: %#v", user)
	}
}
