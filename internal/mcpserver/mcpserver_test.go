package mcpserver

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/omniflow/omniflow/internal/mcp"
)

// grant is a fixed permission set.
type grant map[string]bool

func (g grant) Allows(permission string) bool { return g[permission] }

// tokens resolves a credential to an operator, the way the admin token scheme
// does in production.
type tokens map[string]Principal

func (registry tokens) Authenticate(_ context.Context, credential string) (Principal, error) {
	principal, known := registry[credential]
	if !known {
		return Principal{}, ErrUnauthorized
	}
	return principal, nil
}

// refunds counts how many times the refund actually executed, which is the only
// number that matters for the retry test.
type refunds struct{ executed atomic.Int64 }

type audits struct{ events []mcp.Event }

func (audit *audits) Record(_ context.Context, event mcp.Event) error {
	audit.events = append(audit.events, event)
	return nil
}

func (audit *audits) outcomes(tool string) []string {
	seen := make([]string, 0, 2)
	for _, event := range audit.events {
		if event.Tool == tool {
			seen = append(seen, event.Outcome)
		}
	}
	return seen
}

const orderArgument = `{"type":"object","properties":{"orderId":{"type":"string"}},` +
	`"required":["orderId"]}`

func newServer(t *testing.T, adjust func(*Options)) (*Server, *refunds, *audits) {
	t.Helper()
	counter := &refunds{}
	audit := &audits{}

	options := Options{
		Authenticator: tokens{
			"support-token": {ID: "op-support", Grant: grant{"support.read": true}},
			"finance-token": {ID: "op-finance", Grant: grant{
				"finance.read": true, "finance.write": true,
			}},
		},
		Audit: audit,
		Tools: []Tool{
			{
				Name: "get-order", Description: "reads one order",
				Permission: "finance.read", InputSchema: json.RawMessage(orderArgument),
				Run: func(_ context.Context, call Call) (Result, error) {
					return Result{
						Text:       "order " + call.Arguments["orderId"].(string) + " is paid",
						Structured: map[string]any{"status": "paid"},
					}, nil
				},
			},
			{
				Name: "refund-order", Description: "refunds one order",
				Permission: "finance.write", Mutates: true,
				InputSchema: json.RawMessage(orderArgument),
				Run: func(_ context.Context, call Call) (Result, error) {
					counter.executed.Add(1)
					return Result{Text: "refunded with key " + call.IdempotencyKey}, nil
				},
			},
			{
				Name: "list-tickets", Description: "lists open tickets",
				Permission: "support.read",
				Run: func(context.Context, Call) (Result, error) {
					return Result{Text: "two open tickets"}, nil
				},
			},
		},
		Resources: []Resource{{
			URI: "omniflow://docs/refund-policy", Name: "Refund policy",
			Permission: "finance.read",
			Read: func(context.Context, Principal) (string, error) {
				return "# Refund policy\n\nRefunds are approved by a person.", nil
			},
		}},
	}
	if adjust != nil {
		adjust(&options)
	}
	server, err := New(options)
	if err != nil {
		t.Fatalf("new server: %v", err)
	}
	return server, counter, audit
}

func finance() Principal {
	return Principal{ID: "op-finance", Grant: grant{"finance.read": true, "finance.write": true}}
}

func support() Principal {
	return Principal{ID: "op-support", Grant: grant{"support.read": true}}
}

// "Connect an assistant to your admin panel" should not be one switch that also
// means "and let it issue refunds".
func TestMutationsAreOffUntilTheOwnerEnablesTheSpecificTool(t *testing.T) {
	server, counter, audit := newServer(t, nil)

	_, err := server.Invoke(context.Background(), finance(), "refund-order",
		map[string]any{"orderId": "4a1c"}, "key-1")
	if !errors.Is(err, ErrMutationsDisabled) {
		t.Fatalf("expected ErrMutationsDisabled, got %v", err)
	}
	if counter.executed.Load() != 0 {
		t.Fatal("a disabled mutation ran")
	}

	// Turning writes on globally is not enough on its own.
	half, _, _ := newServer(t, func(options *Options) { options.AllowMutations = true })
	if _, err := half.Invoke(context.Background(), finance(), "refund-order",
		map[string]any{"orderId": "4a1c"}, "key-1",
	); !errors.Is(err, ErrMutationsDisabled) {
		t.Fatalf("a global switch enabled an un-allowlisted tool: %v", err)
	}

	full, counter, _ := newServer(t, func(options *Options) {
		options.AllowMutations = true
		options.MutationAllowlist = []string{"refund-order"}
	})
	if _, err := full.Invoke(context.Background(), finance(), "refund-order",
		map[string]any{"orderId": "4a1c"}, "key-1"); err != nil {
		t.Fatalf("an explicitly enabled mutation was refused: %v", err)
	}
	if counter.executed.Load() != 1 {
		t.Fatalf("the enabled mutation did not run: %d", counter.executed.Load())
	}
	if len(audit.outcomes("refund-order")) == 0 {
		t.Fatal("the refusal was not audited")
	}
}

// A transport that retries must not turn one refund into two. This is the
// financial-effect test.
func TestARetriedMutationAppliesExactlyOnce(t *testing.T) {
	server, counter, audit := newServer(t, func(options *Options) {
		options.AllowMutations = true
		options.MutationAllowlist = []string{"refund-order"}
	})

	arguments := map[string]any{"orderId": "4a1c"}
	first, err := server.Invoke(context.Background(), finance(), "refund-order", arguments, "key-1")
	if err != nil {
		t.Fatalf("first call: %v", err)
	}
	for range 4 {
		replay, err := server.Invoke(
			context.Background(), finance(), "refund-order", arguments, "key-1")
		if err != nil {
			t.Fatalf("retry: %v", err)
		}
		if replay.Text != first.Text {
			t.Fatalf("a retry returned a different answer: %q vs %q", replay.Text, first.Text)
		}
	}
	if counter.executed.Load() != 1 {
		t.Fatalf("the refund applied %d times", counter.executed.Load())
	}

	replayed := 0
	for _, outcome := range audit.outcomes("refund-order") {
		if outcome == "replayed" {
			replayed++
		}
	}
	if replayed != 4 {
		// A replay that audits as a fresh call makes the trail claim five
		// refunds happened.
		t.Fatalf("expected four audited replays, got %d", replayed)
	}
}

// The same key with different arguments is a caller bug, not a retry. Returning
// the first result would silently answer a question nobody asked.
func TestAReusedKeyWithDifferentArgumentsIsRefused(t *testing.T) {
	server, counter, _ := newServer(t, func(options *Options) {
		options.AllowMutations = true
		options.MutationAllowlist = []string{"refund-order"}
	})

	if _, err := server.Invoke(context.Background(), finance(), "refund-order",
		map[string]any{"orderId": "4a1c"}, "key-1"); err != nil {
		t.Fatalf("first call: %v", err)
	}
	if _, err := server.Invoke(context.Background(), finance(), "refund-order",
		map[string]any{"orderId": "9f2b"}, "key-1",
	); !errors.Is(err, ErrIdempotencyConflict) {
		t.Fatalf("expected ErrIdempotencyConflict, got %v", err)
	}
	if counter.executed.Load() != 1 {
		t.Fatalf("a conflicting key executed anyway: %d", counter.executed.Load())
	}
}

// Without a key a retry is indistinguishable from a second request.
func TestAMutationWithoutAKeyIsRefused(t *testing.T) {
	server, counter, _ := newServer(t, func(options *Options) {
		options.AllowMutations = true
		options.MutationAllowlist = []string{"refund-order"}
	})
	if _, err := server.Invoke(context.Background(), finance(), "refund-order",
		map[string]any{"orderId": "4a1c"}, "  ",
	); !errors.Is(err, ErrIdempotencyRequired) {
		t.Fatalf("expected ErrIdempotencyRequired, got %v", err)
	}
	if counter.executed.Load() != 0 {
		t.Fatal("a keyless mutation ran")
	}
}

// A tool the operator could not use in the panel is a tool they cannot use
// here. This is the RBAC-bypass test for the server side.
func TestAnOperatorCannotReachAToolBeyondTheirPermissions(t *testing.T) {
	server, _, _ := newServer(t, nil)

	if _, err := server.Invoke(context.Background(), support(), "get-order",
		map[string]any{"orderId": "4a1c"}, ""); !errors.Is(err, ErrForbidden) {
		t.Fatalf("expected ErrForbidden, got %v", err)
	}

	// And it is not merely refused — it is not offered, so an assistant never
	// learns it exists.
	for _, tool := range server.Available(support()) {
		if tool.Name != "list-tickets" {
			t.Fatalf("a support operator was offered %q", tool.Name)
		}
	}
	if len(server.Available(finance())) != 1 {
		t.Fatalf("a finance operator was offered the wrong set: %+v", server.Available(finance()))
	}
}

// Resources are gated the same way tools are; one rule is easier to get right
// than an exception.
func TestResourcesAreGatedLikeTools(t *testing.T) {
	server, _, _ := newServer(t, nil)
	if _, err := server.ReadResource(
		context.Background(), support(), "omniflow://docs/refund-policy",
	); !errors.Is(err, ErrForbidden) {
		t.Fatalf("expected ErrForbidden, got %v", err)
	}
	if len(server.Resources(support())) != 0 {
		t.Fatal("an unreadable resource was still listed")
	}
	text, err := server.ReadResource(
		context.Background(), finance(), "omniflow://docs/refund-policy")
	if err != nil || !strings.Contains(text, "Refund policy") {
		t.Fatalf("a permitted resource was not readable: %q %v", text, err)
	}
}

// Arguments are validated on the way in for the same reason they are on the way
// out.
func TestArgumentsAreValidated(t *testing.T) {
	server, _, _ := newServer(t, nil)
	_, err := server.Invoke(context.Background(), finance(), "get-order",
		map[string]any{"order_id": "4a1c", "admin": true}, "")
	if !errors.Is(err, mcp.ErrArgumentsInvalid) {
		t.Fatalf("expected ErrArgumentsInvalid, got %v", err)
	}
}

// A server with no authenticator would answer everyone.
func TestAServerWithoutAnAuthenticatorRefusesToStart(t *testing.T) {
	if _, err := New(Options{}); err == nil {
		t.Fatal("a server with no authenticator started")
	}
}

// The transport is where a security property is most easily lost, so the same
// gates are checked through HTTP.
func TestTheTransportEnforcesTheSameRules(t *testing.T) {
	server, counter, _ := newServer(t, func(options *Options) {
		options.AllowMutations = true
		options.MutationAllowlist = []string{"refund-order"}
	})
	transport := httptest.NewServer(server.Handler())
	t.Cleanup(transport.Close)

	post := func(token, body string, headers map[string]string) *http.Response {
		t.Helper()
		request, err := http.NewRequest(
			http.MethodPost, transport.URL, strings.NewReader(body))
		if err != nil {
			t.Fatalf("request: %v", err)
		}
		request.Header.Set("Content-Type", "application/json")
		if token != "" {
			request.Header.Set("Authorization", "Bearer "+token)
		}
		for name, value := range headers {
			request.Header.Set(name, value)
		}
		response, err := transport.Client().Do(request)
		if err != nil {
			t.Fatalf("do: %v", err)
		}
		return response
	}

	anonymous := post("", `{"jsonrpc":"2.0","id":1,"method":"tools/list"}`, nil)
	if anonymous.StatusCode != http.StatusUnauthorized {
		t.Fatalf("an anonymous request was served: %d", anonymous.StatusCode)
	}
	if anonymous.Header.Get("WWW-Authenticate") == "" {
		t.Fatal("the challenge does not name the scheme")
	}
	_ = anonymous.Body.Close()

	listed := post("support-token", `{"jsonrpc":"2.0","id":1,"method":"tools/list"}`, nil)
	var decoded struct {
		Result struct {
			Tools []struct {
				Name        string         `json:"name"`
				Annotations map[string]any `json:"annotations"`
			} `json:"tools"`
		} `json:"result"`
	}
	if err := json.NewDecoder(listed.Body).Decode(&decoded); err != nil {
		t.Fatalf("decode: %v", err)
	}
	_ = listed.Body.Close()
	if len(decoded.Result.Tools) != 1 || decoded.Result.Tools[0].Name != "list-tickets" {
		t.Fatalf("the listing was not scoped to the operator: %+v", decoded.Result.Tools)
	}
	if decoded.Result.Tools[0].Annotations["readOnlyHint"] != true {
		t.Fatal("a read-only tool was not published as one")
	}

	// The key in the header survives a client that rebuilds the body on retry,
	// which is exactly the case the key exists for.
	call := `{"jsonrpc":"2.0","id":2,"method":"tools/call","params":` +
		`{"name":"refund-order","arguments":{"orderId":"4a1c"}}}`
	for range 3 {
		response := post("finance-token", call,
			map[string]string{"Mcp-Idempotency-Key": "key-http"})
		if response.StatusCode != http.StatusOK {
			t.Fatalf("call failed: %d", response.StatusCode)
		}
		_ = response.Body.Close()
	}
	if counter.executed.Load() != 1 {
		t.Fatalf("the http retries applied %d refunds", counter.executed.Load())
	}

	// A support operator calling a finance tool through the transport is
	// refused, and the refusal carries no hint about what exists.
	forbidden := post("support-token",
		`{"jsonrpc":"2.0","id":3,"method":"tools/call","params":`+
			`{"name":"refund-order","arguments":{"orderId":"4a1c"}}}`, nil)
	var refusal struct {
		Error *struct {
			Code int `json:"code"`
		} `json:"error"`
	}
	if err := json.NewDecoder(forbidden.Body).Decode(&refusal); err != nil {
		t.Fatalf("decode: %v", err)
	}
	_ = forbidden.Body.Close()
	if refusal.Error == nil {
		t.Fatal("a forbidden call through the transport succeeded")
	}
	if counter.executed.Load() != 1 {
		t.Fatalf("a forbidden call executed: %d", counter.executed.Load())
	}
}
