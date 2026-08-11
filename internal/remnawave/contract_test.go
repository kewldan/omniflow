package remnawave

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// The tests below pin the supported Remnawave 3.2.x surface: the routes
// Omniflow calls, the envelope it expects back, and how each failure is
// classified. A panel upgrade that changes any of them fails here rather than
// in the fulfillment worker.

func contractClient(t *testing.T, handler http.HandlerFunc) *Client {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	client, err := NewClient(server.URL, "secret")
	if err != nil {
		t.Fatalf("configure client: %v", err)
	}
	return client
}

func TestSupportedRoutesAreCalledExactly(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name   string
		method string
		path   string
		body   string
		call   func(*Client) error
	}{
		{"create user", http.MethodPost, "/api/users/", `{"response":{"id":1,"username":"u","status":"ACTIVE"}}`,
			func(client *Client) error {
				_, err := client.CreateUser(context.Background(), ProvisionUser{Username: "u"})
				return err
			}},
		{"read user", http.MethodGet, "/api/users/7", `{"response":{"id":7,"username":"u","status":"ACTIVE"}}`,
			func(client *Client) error { _, err := client.User(context.Background(), 7); return err }},
		{"enable user", http.MethodPost, "/api/users/7/actions/enable", `{"response":{}}`,
			func(client *Client) error { return client.EnableUser(context.Background(), 7) }},
		{"disable user", http.MethodPost, "/api/users/7/actions/disable", `{"response":{}}`,
			func(client *Client) error { return client.DisableUser(context.Background(), 7) }},
		{"reset traffic", http.MethodPost, "/api/users/7/actions/reset-traffic", `{"response":{}}`,
			func(client *Client) error { return client.ResetUserTraffic(context.Background(), 7) }},
		{"revoke subscription", http.MethodPost, "/api/users/7/actions/revoke", `{"response":{}}`,
			func(client *Client) error { return client.RevokeSubscription(context.Background(), 7) }},
		{"list devices", http.MethodGet, "/api/hwid/devices/7", `{"response":{"total":0,"devices":[]}}`,
			func(client *Client) error { _, err := client.Devices(context.Background(), 7); return err }},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			var seenMethod, seenPath string
			client := contractClient(t, func(writer http.ResponseWriter, request *http.Request) {
				seenMethod, seenPath = request.Method, request.URL.Path
				writer.Header().Set("Content-Type", "application/json")
				_, _ = writer.Write([]byte(testCase.body))
			})
			if err := testCase.call(client); err != nil {
				t.Fatalf("call failed: %v", err)
			}
			if seenMethod != testCase.method || seenPath != testCase.path {
				t.Fatalf("expected %s %s, got %s %s", testCase.method, testCase.path, seenMethod, seenPath)
			}
		})
	}
}

// Every call must carry the bearer token. A panel that starts answering
// unauthenticated requests would otherwise hide a misconfiguration.
func TestEveryCallIsAuthenticated(t *testing.T) {
	t.Parallel()
	authorization := ""
	client := contractClient(t, func(writer http.ResponseWriter, request *http.Request) {
		authorization = request.Header.Get("Authorization")
		writer.Header().Set("Content-Type", "application/json")
		_, _ = writer.Write([]byte(`{"response":{"users":[],"total":0}}`))
	})
	if err := client.Ping(context.Background()); err != nil {
		t.Fatalf("ping: %v", err)
	}
	if authorization != "Bearer secret" {
		t.Fatalf("expected a bearer token, got %q", authorization)
	}
}

func TestPingReportsAnUnhealthyPanel(t *testing.T) {
	t.Parallel()
	client := contractClient(t, func(writer http.ResponseWriter, _ *http.Request) {
		writer.WriteHeader(http.StatusBadGateway)
	})
	err := client.Ping(context.Background())
	if err == nil {
		t.Fatal("a failing panel must not report as healthy")
	}
	var apiError *APIError
	if !errors.As(err, &apiError) || apiError.StatusCode != http.StatusBadGateway {
		t.Fatalf("expected a classified API error, got %v", err)
	}
}

// An expired or revoked token must surface as an error, never as an empty but
// successful result that would look like a customer with no subscription.
func TestUnauthorizedIsAnError(t *testing.T) {
	t.Parallel()
	client := contractClient(t, func(writer http.ResponseWriter, _ *http.Request) {
		writer.WriteHeader(http.StatusUnauthorized)
	})
	_, err := client.User(context.Background(), 7)
	var apiError *APIError
	if !errors.As(err, &apiError) || apiError.StatusCode != http.StatusUnauthorized {
		t.Fatalf("expected a 401 API error, got %v", err)
	}
}

// A panel that returns an oversized body must not be able to exhaust memory.
func TestResponseBodiesAreBounded(t *testing.T) {
	t.Parallel()
	client := contractClient(t, func(writer http.ResponseWriter, _ *http.Request) {
		writer.Header().Set("Content-Type", "application/json")
		_, _ = writer.Write([]byte(`{"response":{"id":7,"username":"` + strings.Repeat("a", 4<<20) + `"}}`))
	})
	if _, err := client.User(context.Background(), 7); err == nil {
		t.Fatal("an oversized response must be rejected")
	}
}

// A redirect must not be followed: an authenticated call that lands somewhere
// else would leak the bearer token to that host.
func TestRedirectsAreNotFollowed(t *testing.T) {
	t.Parallel()
	leaked := false
	elsewhere := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, request *http.Request) {
		if request.Header.Get("Authorization") != "" {
			leaked = true
		}
	}))
	defer elsewhere.Close()
	client := contractClient(t, func(writer http.ResponseWriter, _ *http.Request) {
		http.Redirect(writer, &http.Request{}, elsewhere.URL, http.StatusFound)
	})
	if _, err := client.User(context.Background(), 7); err == nil {
		t.Fatal("a redirected call must not be treated as a success")
	}
	if leaked {
		t.Fatal("the bearer token was sent to the redirect target")
	}
}
