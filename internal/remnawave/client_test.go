package remnawave

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestClientDeleteDeviceUsesOfficialMutation(t *testing.T) {
	t.Parallel()
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodPost || request.URL.Path != "/api/hwid/devices/delete" {
			t.Fatalf("unexpected request: %s %s", request.Method, request.URL.Path)
		}
		var body struct {
			UserID int64  `json:"userId"`
			HWID   string `json:"hwid"`
		}
		if err := json.NewDecoder(request.Body).Decode(&body); err != nil || body.UserID != 42 || body.HWID != "private-hwid" {
			t.Fatalf("unexpected body: %#v, %v", body, err)
		}
		writer.Header().Set("Content-Type", "application/json")
		_, _ = writer.Write([]byte(`{"response":{}}`))
	}))
	defer server.Close()

	client, err := NewClient(server.URL, "secret")
	if err != nil {
		t.Fatal(err)
	}
	if err := client.DeleteDevice(context.Background(), 42, "private-hwid"); err != nil {
		t.Fatal(err)
	}
}

func TestClientProvisioningMatchesRemnawaveV322Contract(t *testing.T) {
	t.Parallel()

	expiresAt := time.Date(2026, time.September, 1, 0, 0, 0, 0, time.UTC)
	squadID := "550e8400-e29b-41d4-a716-446655440000"
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodPatch || request.URL.Path != "/api/users/" {
			t.Fatalf("unexpected request: %s %s", request.Method, request.URL.Path)
		}
		var body struct {
			ID                   int64    `json:"id"`
			Username             string   `json:"username"`
			ActiveInternalSquads []string `json:"activeInternalSquads"`
		}
		if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		if body.ID != 42 || body.Username != "omniflow_customer" || len(body.ActiveInternalSquads) != 1 || body.ActiveInternalSquads[0] != squadID {
			t.Fatalf("unexpected provisioning body: %#v", body)
		}
		writer.Header().Set("Content-Type", "application/json")
		_, _ = writer.Write([]byte(`{"response":{"id":42,"username":"omniflow_customer","status":"ACTIVE","expireAt":"2026-09-01T00:00:00Z","trafficLimitBytes":0,"activeInternalSquads":[{"uuid":"550e8400-e29b-41d4-a716-446655440000","name":"Default"}]}}`))
	}))
	defer server.Close()

	client, err := NewClient(server.URL, "secret")
	if err != nil {
		t.Fatal(err)
	}
	user, err := client.UpdateUser(context.Background(), 42, ProvisionUser{Username: "omniflow_customer", ExpireAt: expiresAt, InternalSquadIDs: []string{squadID}})
	if err != nil {
		t.Fatal(err)
	}
	if user.ID != 42 || len(user.ActiveInternalSquads) != 1 || user.ActiveInternalSquads[0].UUID != squadID {
		t.Fatalf("unexpected provisioned user: %#v", user)
	}
}

func TestClientListUsersMatchesRemnawaveV322Route(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodGet || request.URL.Path != "/api/users/" || request.URL.Query().Get("start") != "20" || request.URL.Query().Get("size") != "10" {
			t.Fatalf("unexpected request: %s %s", request.Method, request.URL.String())
		}
		writer.Header().Set("Content-Type", "application/json")
		_, _ = writer.Write([]byte(`{"response":{"users":[],"total":25}}`))
	}))
	defer server.Close()

	client, err := NewClient(server.URL, "secret")
	if err != nil {
		t.Fatal(err)
	}
	users, total, err := client.ListUsers(context.Background(), 20, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(users) != 0 || total != 25 {
		t.Fatalf("unexpected page: users=%d total=%d", len(users), total)
	}
}

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
