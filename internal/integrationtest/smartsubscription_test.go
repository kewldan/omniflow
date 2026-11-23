//go:build integration

package integrationtest

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/omniflow/omniflow/internal/panelpg"
	"github.com/omniflow/omniflow/internal/remnawave"
	"github.com/omniflow/omniflow/internal/smartsub"
	"github.com/omniflow/omniflow/internal/smartsubpg"
)

const (
	smartShortUUID   = "Abc123short"
	smartFinlandHost = "11111111-1111-4111-8111-111111111111"
	smartWhiteHost   = "22222222-2222-4222-8222-222222222222"
)

// fakeSmartPanel answers the four Remnawave routes the smart subscription
// reads, for one user (42) with a Finnish server, a German one, and a
// whitelist-bypass server.
func fakeSmartPanel(t *testing.T, panelURL *string) *httptest.Server {
	t.Helper()
	server := func(remark, address string) map[string]any {
		return map[string]any{
			"remarks": remark,
			"outbounds": []any{
				map[string]any{"tag": "proxy", "protocol": "vless",
					"settings": map[string]any{"vnext": []any{map[string]any{"address": address, "port": 443}}}},
				map[string]any{"tag": "direct", "protocol": "freedom"},
			},
		}
	}
	answer, _ := json.Marshal([]any{
		server("🇫🇮 Finland 1", "fi.example.net"),
		server("🇩🇪 Germany 1", "de.example.net"),
		server("Russia WHITE", "ru.example.net"),
	})
	return httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/api/hosts":
			_, _ = writer.Write([]byte(`{"response":[
				{"uuid":"` + smartFinlandHost + `","remark":"🇫🇮 Finland 1","address":"fi.example.net","port":443,"nodes":[],
				 "inbound":{"configProfileInboundUuid":"in-1"},"internalSquads":{"mode":"EXCLUDE","squads":[]}},
				{"uuid":"33333333-3333-4333-8333-333333333333","remark":"🇩🇪 Germany 1","address":"de.example.net","port":443,"nodes":[],
				 "inbound":{"configProfileInboundUuid":"in-1"},"internalSquads":{"mode":"EXCLUDE","squads":[]}},
				{"uuid":"44444444-4444-4444-8444-444444444444","remark":"🇺🇸 Other squad","address":"us.example.net","port":443,"nodes":[],
				 "inbound":{"configProfileInboundUuid":"in-9"},"internalSquads":{"mode":"EXCLUDE","squads":[]}},
				{"uuid":"` + smartWhiteHost + `","remark":"Russia WHITE","address":"ru.example.net","port":443,"nodes":[],
				 "inbound":{"configProfileInboundUuid":"in-1"},"internalSquads":{"mode":"EXCLUDE","squads":[]}}]}`))
		case "/api/internal-squads":
			_, _ = writer.Write([]byte(`{"response":{"internalSquads":[{"uuid":"sq-a","inbounds":[{"uuid":"in-1"}]}]}}`))
		case "/api/users/42":
			_, _ = writer.Write([]byte(`{"response":{"id":42,"username":"u","status":"ACTIVE","activeInternalSquads":[{"uuid":"sq-a","name":"Main"}]}}`))
		case "/api/users/by-short-uuid/" + smartShortUUID:
			_, _ = writer.Write([]byte(`{"response":{"id":42,"username":"u","status":"ACTIVE","activeInternalSquads":[{"uuid":"sq-a","name":"Main"}]}}`))
		case "/api/nodes":
			_, _ = writer.Write([]byte(`{"response":[]}`))
		case "/api/subscriptions/by-id/42":
			_, _ = writer.Write([]byte(`{"response":{"isFound":true,"subscriptionUrl":"` + *panelURL + `/` + smartShortUUID + `"}}`))
		case "/api/sub/" + smartShortUUID + "/json":
			if request.Header.Get("X-Hwid") != "device-1" {
				t.Errorf("the device header did not reach the panel")
			}
			writer.Header().Set("Subscription-Userinfo", "upload=0; download=0; total=0; expire=0")
			_, _ = writer.Write(answer)
		case "/api/sub/" + smartShortUUID:
			_, _ = writer.Write([]byte("panel-own-answer"))
		default:
			writer.WriteHeader(http.StatusNotFound)
		}
	}))
}

func TestSmartSubscriptionEndToEnd(t *testing.T) {
	ctx := context.Background()
	harness := newHarness(t)
	panel := newOperations(t, harness)
	actor := harness.operator(ctx, t, "smart@example.test")

	var panelURL string
	fake := fakeSmartPanel(t, &panelURL)
	t.Cleanup(fake.Close)
	panelURL = fake.URL
	client, err := remnawave.NewClient(fake.URL, "token")
	if err != nil {
		t.Fatal(err)
	}
	service := smartsubpg.New(harness.pool, client, "https://vpn.example.test", nil)

	customerID := harness.customer(ctx, t)
	var subscriptionID string
	if err := harness.pool.QueryRow(ctx, `INSERT INTO subscriptions (user_id, slot, label, remnawave_user_id)
		VALUES ($1, 1, 'Main', 42) RETURNING id::text`, customerID).Scan(&subscriptionID); err != nil {
		t.Fatalf("create subscription: %v", err)
	}
	happ := http.Header{}
	happ.Set("User-Agent", "Happ/3.1")
	happ.Set("X-Hwid", "device-1")

	// Off: the link answers with the panel's own subscription, and the
	// customer panel keeps handing out the panel's link.
	response, err := service.Serve(ctx, smartShortUUID, happ, "203.0.113.5")
	if err != nil || string(response.Body) != "panel-own-answer" || response.Rewritten {
		t.Fatalf("off: %d %q %v", response.Status, response.Body, err)
	}
	linked := smartsubpg.LinkedPanel{Client: client, Links: service}
	subscription, err := linked.Subscription(ctx, 42)
	if err != nil || subscription.SubscriptionURL != fake.URL+"/"+smartShortUUID {
		t.Fatalf("link while off = %q, %v", subscription.SubscriptionURL, err)
	}
	if _, err := service.CustomerView(ctx, customerID, subscriptionID); !errors.Is(err, smartsubpg.ErrDisabled) {
		t.Fatalf("view while off: %v", err)
	}

	// The operator turns it on and marks the WHITE host as the bypass.
	current, err := panel.SmartSubscription(ctx)
	if err != nil {
		t.Fatal(err)
	}
	current.Enabled = true
	current.Hosts = map[string]smartsub.HostOverride{smartWhiteHost: {Role: smartsub.RoleReserve}}
	saved, err := panel.SaveSmartSubscription(ctx, current, actor)
	if err != nil || !saved.Enabled || saved.Version != current.Version+1 {
		t.Fatalf("save: %+v, %v", saved, err)
	}
	if _, err := panel.SaveSmartSubscription(ctx, current, actor); !errors.Is(err, panelpg.ErrConflict) {
		t.Fatalf("a stale version was not a conflict: %v", err)
	}
	broken := saved
	broken.ProbeIntervalSeconds = 1
	var field *smartsub.FieldError
	if _, err := panel.SaveSmartSubscription(ctx, broken, actor); !errors.As(err, &field) || field.Field != "probeIntervalSeconds" {
		t.Fatalf("an invalid setting: %v", err)
	}
	var audited int
	if err := harness.pool.QueryRow(ctx, `SELECT count(*) FROM audit_events WHERE action = 'panel.smart_subscription.updated'`).Scan(&audited); err != nil || audited != 1 {
		t.Fatalf("audit events = %d, %v", audited, err)
	}
	service.Invalidate()

	subscription, err = linked.Subscription(ctx, 42)
	if err != nil || subscription.SubscriptionURL != "https://vpn.example.test/v1/sub/"+smartShortUUID {
		t.Fatalf("link while on = %q, %v", subscription.SubscriptionURL, err)
	}

	response, err = service.Serve(ctx, smartShortUUID, happ, "203.0.113.5")
	if err != nil || !response.Rewritten || response.Header.Get("Subscription-Userinfo") == "" {
		t.Fatalf("on: %+v, %v", response, err)
	}
	if got := groupNames(t, response.Body); got != "⚡ Fastest | 🇫🇮 Finland | 🇩🇪 Germany | 🛡 Whitelist bypass" {
		t.Fatalf("groups = %s", got)
	}

	// The customer sees their servers and hides Germany.
	view, err := service.CustomerView(ctx, customerID, subscriptionID)
	if err != nil || len(view.Servers.Countries) != 2 || len(view.Servers.Reserve) != 1 || view.Customized {
		t.Fatalf("view = %+v, %v", view, err)
	}
	if _, err := service.CustomerView(ctx, harness.customer(ctx, t), subscriptionID); !errors.Is(err, smartsubpg.ErrNotFound) {
		t.Fatalf("another customer's subscription: %v", err)
	}
	preferences := view.Preferences
	preferences.HiddenCountries = []string{"de"}
	preferences.Reserve = false
	preferences.Rules = []smartsub.Rule{{Domain: "https://Example.com/x", Action: smartsub.ActionDirect}}
	stored, err := service.SavePreferences(ctx, customerID, subscriptionID, preferences)
	if err != nil || stored.HiddenCountries[0] != "DE" || stored.Rules[0].Domain != "example.com" {
		t.Fatalf("save preferences: %+v, %v", stored, err)
	}
	response, err = service.Serve(ctx, smartShortUUID, happ, "203.0.113.5")
	if err != nil {
		t.Fatal(err)
	}
	if got := groupNames(t, response.Body); got != "⚡ Fastest | 🇫🇮 Finland" {
		t.Fatalf("groups after hiding = %s", got)
	}
	if !strings.Contains(string(response.Body), "domain:example.com") {
		t.Fatal("the customer's rule is not in the subscription")
	}

	if err := service.ResetPreferences(ctx, customerID, subscriptionID); err != nil {
		t.Fatal(err)
	}
	view, err = service.CustomerView(ctx, customerID, subscriptionID)
	if err != nil || view.Customized {
		t.Fatalf("after reset: %+v, %v", view, err)
	}
}

func groupNames(t *testing.T, body []byte) string {
	t.Helper()
	var configs []struct {
		Remarks string `json:"remarks"`
	}
	if err := json.Unmarshal(body, &configs); err != nil {
		t.Fatalf("decode %s: %v", body, err)
	}
	names := make([]string, 0, len(configs))
	for _, config := range configs {
		names = append(names, config.Remarks)
	}
	return strings.Join(names, " | ")
}
