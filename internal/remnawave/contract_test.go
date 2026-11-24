package remnawave

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// The tests below pin the supported Remnawave 3.4.11 surface (backend-contract
// 3.4.17, the contract that release ships): the routes Omniflow calls, the
// envelope it expects back, and how each failure is classified. A panel upgrade
// that changes any of them fails here rather than in the fulfillment worker.
//
// Every user, subscription, and HWID route addresses the user by the panel's
// numeric ID. Nodes are addressed by UUID, but Omniflow only lists them.

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
		query  string
		body   string
		call   func(*Client) error
	}{
		{"list users", http.MethodGet, "/api/users/", "size=25&start=50", `{"response":{"users":[],"total":0}}`,
			func(client *Client) error { _, _, err := client.ListUsers(context.Background(), 50, 25); return err }},
		{"create user", http.MethodPost, "/api/users/", "", `{"response":{"id":1,"username":"u","status":"ACTIVE"}}`,
			func(client *Client) error {
				_, err := client.CreateUser(context.Background(), ProvisionUser{Username: "u"})
				return err
			}},
		{"update user", http.MethodPatch, "/api/users/", "", `{"response":{"id":7,"username":"u","status":"ACTIVE"}}`,
			func(client *Client) error {
				_, err := client.UpdateUser(context.Background(), 7, ProvisionUser{Username: "u"})
				return err
			}},
		{"read user", http.MethodGet, "/api/users/7", "", `{"response":{"id":7,"username":"u","status":"ACTIVE"}}`,
			func(client *Client) error { _, err := client.User(context.Background(), 7); return err }},
		{"read user by username", http.MethodGet, "/api/users/by-username/omniflow_0123", "", `{"response":{"id":7,"username":"omniflow_0123","status":"ACTIVE"}}`,
			func(client *Client) error {
				_, err := client.UserByUsername(context.Background(), "omniflow_0123")
				return err
			}},
		{"find user by telegram id", http.MethodGet, "/api/users/stream", "size=2&telegramId=42", `{"response":{"users":[{"id":7}],"nextCursor":null,"hasMore":false}}`,
			func(client *Client) error { _, err := client.UserByTelegramID(context.Background(), 42); return err }},
		{"enable user", http.MethodPost, "/api/users/7/actions/enable", "", `{"response":{}}`,
			func(client *Client) error { return client.EnableUser(context.Background(), 7) }},
		{"disable user", http.MethodPost, "/api/users/7/actions/disable", "", `{"response":{}}`,
			func(client *Client) error { return client.DisableUser(context.Background(), 7) }},
		{"reset traffic", http.MethodPost, "/api/users/7/actions/reset-traffic", "", `{"response":{}}`,
			func(client *Client) error { return client.ResetUserTraffic(context.Background(), 7) }},
		{"revoke subscription", http.MethodPost, "/api/users/7/actions/revoke", "", `{"response":{}}`,
			func(client *Client) error { return client.RevokeSubscription(context.Background(), 7) }},
		{"read subscription", http.MethodGet, "/api/subscriptions/by-id/7", "", `{"response":{"isFound":true}}`,
			func(client *Client) error { _, err := client.Subscription(context.Background(), 7); return err }},
		{"list devices", http.MethodGet, "/api/hwid/devices/7", "", `{"response":{"total":0,"devices":[]}}`,
			func(client *Client) error { _, err := client.Devices(context.Background(), 7); return err }},
		{"delete device", http.MethodPost, "/api/hwid/devices/delete", "", `{"response":{"total":0,"devices":[]}}`,
			func(client *Client) error { return client.DeleteDevice(context.Background(), 7, "hwid") }},
		{"delete all devices", http.MethodPost, "/api/hwid/devices/delete-all", "", `{"response":{"total":0,"devices":[]}}`,
			func(client *Client) error { return client.DeleteAllDevices(context.Background(), 7) }},
		{"list nodes", http.MethodGet, "/api/nodes", "", `{"response":[]}`,
			func(client *Client) error { _, err := client.Nodes(context.Background()); return err }},
		{"delete user", http.MethodDelete, "/api/users/7", "", ``,
			func(client *Client) error { return client.DeleteUser(context.Background(), 7) }},
		{"list internal squads", http.MethodGet, "/api/internal-squads", "", `{"response":{"total":0,"internalSquads":[]}}`,
			func(client *Client) error { _, err := client.InternalSquads(context.Background()); return err }},
		{"list external squads", http.MethodGet, "/api/external-squads", "", `{"response":{"total":0,"externalSquads":[]}}`,
			func(client *Client) error { _, err := client.ExternalSquads(context.Background()); return err }},
		{"request a node geocheck", http.MethodPost, "/api/connections/geocheck/0f0e0d0c-0000-4000-8000-000000000001", "",
			`{"response":{"jobId":"42"}}`,
			func(client *Client) error {
				job, err := client.RequestGeocheck(context.Background(), "0f0e0d0c-0000-4000-8000-000000000001")
				if err == nil && job != "42" {
					return errors.New("job id not decoded")
				}
				return err
			}},
		{"read a node geocheck", http.MethodGet, "/api/connections/geocheck/42", "",
			`{"response":{"isCompleted":true,"isFailed":false,"result":{"success":true,"nodeUuid":"0f0e0d0c-0000-4000-8000-000000000001","image":null,"rawReport":{"schema":1},"message":null}}}`,
			func(client *Client) error {
				job, err := client.Geocheck(context.Background(), "42")
				if err == nil && (!job.Completed || !job.Success || string(job.Report) != `{"schema":1}`) {
					return errors.New("geocheck not decoded")
				}
				return err
			}},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			var seenMethod, seenPath, seenQuery string
			client := contractClient(t, func(writer http.ResponseWriter, request *http.Request) {
				seenMethod, seenPath = request.Method, request.URL.Path
				seenQuery = request.URL.Query().Encode()
				writer.Header().Set("Content-Type", "application/json")
				_, _ = writer.Write([]byte(testCase.body))
			})
			if err := testCase.call(client); err != nil {
				t.Fatalf("call failed: %v", err)
			}
			if seenMethod != testCase.method || seenPath != testCase.path || seenQuery != testCase.query {
				t.Fatalf("expected %s %s?%s, got %s %s?%s", testCase.method, testCase.path, testCase.query, seenMethod, seenPath, seenQuery)
			}
		})
	}
}

// The HWID mutations take the numeric user ID as a JSON number. The contract
// validates `userId` as a number, so a string ID would be a 400.
func TestDeviceMutationsSendANumericUserID(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		call func(*Client) error
		want string
	}{
		{"delete one", func(client *Client) error { return client.DeleteDevice(context.Background(), 7, "hwid-1") },
			`{"hwid":"hwid-1","userId":7}`},
		{"delete all", func(client *Client) error { return client.DeleteAllDevices(context.Background(), 7) },
			`{"userId":7}`},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			var seen map[string]any
			client := contractClient(t, func(writer http.ResponseWriter, request *http.Request) {
				if err := json.NewDecoder(request.Body).Decode(&seen); err != nil {
					t.Errorf("decode body: %v", err)
				}
				writer.Header().Set("Content-Type", "application/json")
				_, _ = writer.Write([]byte(`{"response":{"total":0,"devices":[]}}`))
			})
			if err := testCase.call(client); err != nil {
				t.Fatalf("call failed: %v", err)
			}
			encoded, _ := json.Marshal(seen)
			if string(encoded) != testCase.want {
				t.Fatalf("body = %s, want %s", encoded, testCase.want)
			}
		})
	}
}

// A user payload exactly as a 3.4.11 panel returns it (ExtendedUsersSchema):
// the fields Omniflow reads decode, and the ones it does not are ignored.
func TestAFullUserPayloadDecodes(t *testing.T) {
	t.Parallel()
	client := contractClient(t, func(writer http.ResponseWriter, _ *http.Request) {
		writer.Header().Set("Content-Type", "application/json")
		_, _ = writer.Write([]byte(`{"response":{
			"id":7,"shortUuid":"abcdefgh12345678","username":"omniflow_0123","status":"ACTIVE",
			"trafficLimitBytes":10737418240,"trafficLimitStrategy":"NO_RESET",
			"expireAt":"2026-12-01T00:00:00.000Z","telegramId":987654321,"email":null,
			"description":null,"tag":null,"hwidDeviceLimit":0,"externalSquadUuid":null,
			"trojanPassword":"secret-secret","vlessUuid":"550e8400-e29b-41d4-a716-446655440001",
			"ssPassword":"secret-secret","lastTriggeredThreshold":0,"subRevokedAt":null,
			"lastTrafficResetAt":null,"createdAt":"2026-01-01T00:00:00.000Z",
			"updatedAt":"2026-11-01T00:00:00.000Z","subscriptionUrl":"https://sub.example/abc",
			"activeInternalSquads":[{"uuid":"550e8400-e29b-41d4-a716-446655440000","name":"Default"}],
			"userTraffic":{"usedTrafficBytes":1024,"lifetimeUsedTrafficBytes":4096,
				"onlineAt":"2026-11-18T10:00:00.000Z","firstConnectedAt":null,"lastConnectedNodeUuid":null}
		}}`))
	})
	user, err := client.User(context.Background(), 7)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if user.ID != 7 || user.Status != "ACTIVE" || user.TrafficLimitBytes != 10737418240 ||
		user.TelegramID == nil || *user.TelegramID != 987654321 ||
		user.HWIDDeviceLimit == nil || *user.HWIDDeviceLimit != 0 ||
		len(user.ActiveInternalSquads) != 1 || user.Traffic.UsedBytes != 1024 || user.Traffic.OnlineAt == nil {
		t.Fatalf("decoded %+v", user)
	}
}

// The panel answers enable on an enabled user, and disable on a disabled one,
// with a 400 carrying its own error code. For a retried operation whose first
// attempt landed that is the desired state, not a failure; any other 400 still
// is one.
func TestEnableAndDisableAreIdempotent(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name    string
		code    string
		call    func(*Client) error
		wantErr bool
	}{
		{"enable an enabled user", "A030", func(client *Client) error { return client.EnableUser(context.Background(), 7) }, false},
		{"disable a disabled user", "A029", func(client *Client) error { return client.DisableUser(context.Background(), 7) }, false},
		{"enable with the disable code", "A029", func(client *Client) error { return client.EnableUser(context.Background(), 7) }, true},
		{"disable with the enable code", "A030", func(client *Client) error { return client.DisableUser(context.Background(), 7) }, true},
		{"enable rejected otherwise", "A038", func(client *Client) error { return client.EnableUser(context.Background(), 7) }, true},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			client := contractClient(t, func(writer http.ResponseWriter, request *http.Request) {
				writer.Header().Set("Content-Type", "application/json")
				writer.WriteHeader(http.StatusBadRequest)
				_, _ = writer.Write([]byte(`{"timestamp":"2026-11-19T00:00:00.000Z","path":"` + request.URL.Path + `","message":"x","errorCode":"` + testCase.code + `"}`))
			})
			err := testCase.call(client)
			if (err != nil) != testCase.wantErr {
				t.Fatalf("err = %v, want error %v", err, testCase.wantErr)
			}
		})
	}
}

// A failure keeps the panel's error code for diagnosis and drops its message,
// which can echo request values. A body that is not the panel's error shape —
// a schema validation failure or a proxy page — leaves the code empty.
func TestFailuresCarryThePanelErrorCode(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name   string
		status int
		body   string
		code   string
	}{
		{"panel error", http.StatusInternalServerError, `{"timestamp":"t","path":"/api/users/","message":"Update user error","errorCode":"A039"}`, "A039"},
		{"validation error", http.StatusBadRequest, `{"statusCode":400,"message":"Validation failed","errors":[]}`, ""},
		{"proxy page", http.StatusBadGateway, `<html>bad gateway</html>`, ""},
		{"forbidden", http.StatusForbidden, `{"message":"Forbidden","errorCode":"E000"}`, "E000"},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			client := contractClient(t, func(writer http.ResponseWriter, _ *http.Request) {
				writer.WriteHeader(testCase.status)
				_, _ = writer.Write([]byte(testCase.body))
			})
			_, err := client.UpdateUser(context.Background(), 7, ProvisionUser{Username: "u"})
			var apiError *APIError
			if !errors.As(err, &apiError) || apiError.StatusCode != testCase.status || apiError.Code != testCase.code {
				t.Fatalf("got %#v (%v), want HTTP %d code %q", apiError, err, testCase.status, testCase.code)
			}
			if strings.Contains(err.Error(), "Update user error") {
				t.Fatalf("the panel message leaked into the error: %v", err)
			}
		})
	}
}

// The panel drops any request that did not arrive through an HTTPS reverse
// proxy. A plain-HTTP base URL is a direct in-network connection, so the
// adapter supplies the proxy headers itself; over HTTPS the real proxy does.
func TestPlainHTTPConnectionsCarryTheProxyHeaders(t *testing.T) {
	t.Parallel()
	var proto, forwarded string
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		proto, forwarded = request.Header.Get("X-Forwarded-Proto"), request.Header.Get("X-Forwarded-For")
		writer.Header().Set("Content-Type", "application/json")
		_, _ = writer.Write([]byte(`{"response":{"users":[],"total":0}}`))
	}))
	defer server.Close()
	client, err := NewClient(server.URL, "secret")
	if err != nil {
		t.Fatal(err)
	}
	if err := client.Ping(context.Background()); err != nil {
		t.Fatalf("ping: %v", err)
	}
	if proto != "https" || forwarded == "" {
		t.Fatalf("a plain-HTTP call sent X-Forwarded-Proto=%q X-Forwarded-For=%q", proto, forwarded)
	}

	secure := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		proto, forwarded = request.Header.Get("X-Forwarded-Proto"), request.Header.Get("X-Forwarded-For")
		writer.Header().Set("Content-Type", "application/json")
		_, _ = writer.Write([]byte(`{"response":{"users":[],"total":0}}`))
	}))
	defer secure.Close()
	tlsClient, err := NewClient(secure.URL, "secret")
	if err != nil {
		t.Fatal(err)
	}
	tlsClient.http.Transport = secure.Client().Transport
	if err := tlsClient.Ping(context.Background()); err != nil {
		t.Fatalf("ping over TLS: %v", err)
	}
	if proto != "" || forwarded != "" {
		t.Fatalf("an HTTPS call must leave the proxy headers to the proxy, got %q %q", proto, forwarded)
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

// The node listing is the one route in this adapter whose absence is a normal
// outcome rather than a fault.
//
// It is read for a report, not for a purchase, and a panel that does not expose
// it must produce "no node data" on the screen rather than a page of zeros —
// those two look identical and mean opposite things. A panel that shapes the
// payload differently degrades the same way: only the fields Omniflow renders
// are decoded, and an absent one is a zero rather than a decode failure.
func TestTheNodeListingDegradesRatherThanFailing(t *testing.T) {
	t.Parallel()

	missing := contractClient(t, func(writer http.ResponseWriter, _ *http.Request) {
		writer.WriteHeader(http.StatusNotFound)
	})
	if _, err := missing.Nodes(context.Background()); !errors.Is(err, ErrNotFound) {
		t.Fatalf("a panel without the route returned %v, want ErrNotFound", err)
	}

	// A payload carrying fields this build does not know about, and missing one
	// it does.
	extra := contractClient(t, func(writer http.ResponseWriter, _ *http.Request) {
		writer.Header().Set("Content-Type", "application/json")
		_, _ = writer.Write([]byte(`{"response":[{
			"uuid":"n1","name":"Frankfurt","isConnected":true,
			"trafficUsedBytes":1024,"trafficLimitBytes":null,"somethingNew":{"nested":true}
		}]}`))
	})
	nodes, err := extra.Nodes(context.Background())
	if err != nil {
		t.Fatalf("an unfamiliar payload failed to decode: %v", err)
	}
	if len(nodes) != 1 || nodes[0].Name != "Frankfurt" || nodes[0].TrafficUsedBytes != 1024 {
		t.Fatalf("decoded %+v", nodes)
	}
	if nodes[0].TrafficLimitBytes != 0 || nodes[0].UsersOnline != nil {
		t.Fatalf("an absent field became something other than a zero: %+v", nodes[0])
	}
}

// A squad's member count sits under `info` in the 3.4 contract. The plan
// editor shows it beside the name, so a squad nobody is in is visible as such.
func TestSquadListsDecodeTheNameAndMemberCount(t *testing.T) {
	t.Parallel()
	client := contractClient(t, func(writer http.ResponseWriter, _ *http.Request) {
		writer.Header().Set("Content-Type", "application/json")
		_, _ = writer.Write([]byte(`{"response":{"total":1,"internalSquads":[{"uuid":"9d2c5c34-0f5e-4c43-9d5c-5d8c1c4f3a10","name":"Germany","viewPosition":1,"info":{"membersCount":12,"inboundsCount":2},"inbounds":[]}]}}`))
	})
	squads, err := client.InternalSquads(context.Background())
	if err != nil {
		t.Fatalf("list squads: %v", err)
	}
	if len(squads) != 1 || squads[0].Name != "Germany" || squads[0].Members != 12 ||
		squads[0].UUID != "9d2c5c34-0f5e-4c43-9d5c-5d8c1c4f3a10" {
		t.Fatalf("squads = %+v", squads)
	}
}
