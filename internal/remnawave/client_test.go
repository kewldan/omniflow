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
		_, _ = writer.Write([]byte(`{"response":{"total":0,"devices":[]}}`))
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

// The contract's update addresses the user by exactly one of id or username.
// Omniflow always knows the numeric ID, so that is the one it sends; the panel
// cannot rename a user through this call anyway.
func TestClientUpdateAddressesTheUserByIDAlone(t *testing.T) {
	t.Parallel()

	expiresAt := time.Date(2026, time.September, 1, 0, 0, 0, 0, time.UTC)
	squadID := "550e8400-e29b-41d4-a716-446655440000"
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodPatch || request.URL.Path != "/api/users/" {
			t.Fatalf("unexpected request: %s %s", request.Method, request.URL.Path)
		}
		var body map[string]json.RawMessage
		if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		if _, present := body["username"]; present {
			t.Fatalf("an update must address the user by id alone, got username %s", body["username"])
		}
		var squads []string
		_ = json.Unmarshal(body["activeInternalSquads"], &squads)
		if string(body["id"]) != "42" || len(squads) != 1 || squads[0] != squadID {
			t.Fatalf("unexpected provisioning body: %v", body)
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

// The limits are always on the wire. Remnawave reads an absent field as "leave
// it as it is", which is how a plan with no limit used to inherit the previous
// plan's limit after a renewal; a nil limit has to arrive as the explicit
// unlimited value, which is zero for both limits. A null device limit is not
// unlimited — the panel applies its global fallback limit to it — and a create
// rejects null outright, because the field is optional but not nullable there.
func TestClientAlwaysSendsTheLimitsEvenWhenUnlimited(t *testing.T) {
	t.Parallel()
	var bodies []map[string]json.RawMessage
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		var body map[string]json.RawMessage
		if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
			t.Errorf("decode body: %v", err)
		}
		bodies = append(bodies, body)
		writer.Header().Set("Content-Type", "application/json")
		_, _ = writer.Write([]byte(`{"response":{"id":42,"username":"u","status":"ACTIVE"}}`))
	}))
	defer server.Close()
	client, err := NewClient(server.URL, "secret")
	if err != nil {
		t.Fatal(err)
	}

	limit, devices := int64(1024), 3
	limited := ProvisionUser{Username: "u", TrafficLimitBytes: &limit, HWIDDeviceLimit: &devices}
	cases := []struct {
		name      string
		call      func() error
		wantID    string
		wantName  bool
		traffic   string
		deviceCap string
	}{
		{"unlimited update", func() error {
			_, err := client.UpdateUser(context.Background(), 42, ProvisionUser{Username: "u"})
			return err
		}, "42", false, "0", "0"},
		{"unlimited create", func() error {
			_, err := client.CreateUser(context.Background(), ProvisionUser{Username: "u"})
			return err
		}, "", true, "0", "0"},
		{"limited update", func() error {
			_, err := client.UpdateUser(context.Background(), 42, limited)
			return err
		}, "42", false, "1024", "3"},
		{"limited create", func() error {
			_, err := client.CreateUser(context.Background(), limited)
			return err
		}, "", true, "1024", "3"},
	}
	for _, testCase := range cases {
		if err := testCase.call(); err != nil {
			t.Fatalf("%s: %v", testCase.name, err)
		}
	}
	if len(bodies) != len(cases) {
		t.Fatalf("expected %d requests, got %d", len(cases), len(bodies))
	}
	for index, testCase := range cases {
		body := bodies[index]
		if string(body["id"]) != testCase.wantID {
			t.Errorf("%s: id = %s, want %q", testCase.name, body["id"], testCase.wantID)
		}
		if _, named := body["username"]; named != testCase.wantName {
			t.Errorf("%s: username present = %v, want %v", testCase.name, named, testCase.wantName)
		}
		if string(body["trafficLimitBytes"]) != testCase.traffic || string(body["hwidDeviceLimit"]) != testCase.deviceCap {
			t.Errorf("%s: traffic=%s devices=%s, want %s and %s", testCase.name, body["trafficLimitBytes"], body["hwidDeviceLimit"], testCase.traffic, testCase.deviceCap)
		}
	}
}

func TestClientListUsersUsesOffsetPagination(t *testing.T) {
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
		_, _ = writer.Write([]byte(`{"response":{"isFound":true,"user":{"shortUuid":"abcdefgh12345678","daysLeft":12,"trafficUsed":"1 GB","trafficLimit":"10 GB","lifetimeTrafficUsed":"3 GB","trafficUsedBytes":"1073741824","trafficLimitBytes":"10737418240","lifetimeTrafficUsedBytes":"3221225472","username":"demo","expiresAt":"2026-09-01T00:00:00Z","isActive":true,"userStatus":"ACTIVE","trafficLimitStrategy":"NO_RESET"},"links":[],"ssConfLinks":{},"subscriptionUrl":"https://sub.example/test"}}`))
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
	if subscription.User.DaysLeft != 12 || subscription.User.Status != "ACTIVE" || subscription.SubscriptionURL != "https://sub.example/test" {
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
		_, _ = writer.Write([]byte(`{"response":{"users":[{"id":73,"username":"linked","status":"ACTIVE","trafficLimitBytes":0,"expireAt":"2026-09-01T00:00:00Z","hwidDeviceLimit":null,"subscriptionUrl":"https://sub.example/test","userTraffic":{"usedTrafficBytes":0,"lifetimeUsedTrafficBytes":0,"onlineAt":null,"firstConnectedAt":null,"lastConnectedNodeUuid":null}}],"nextCursor":null,"hasMore":false}}`))
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
