package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func newClient(t *testing.T, fake *fakeServer, adjust func(*Server)) (*Client, *fakeServer) {
	t.Helper()
	if fake == nil {
		fake = &fakeServer{}
	}
	transport := httptest.NewServer(fake.handler())
	t.Cleanup(transport.Close)

	server := Server{
		Slug: "acme", Endpoint: transport.URL, Enabled: true,
		AllowedTools: []string{"search-orders"},
		Permissions:  map[string]string{"search-orders": "finance.read"},
		// The test server is on loopback, which the egress rule refuses unless an
		// owner opts in — the same opt-in a self-hosted deployment makes.
		AllowPrivateNetwork: true,
	}
	if adjust != nil {
		adjust(&server)
	}
	return NewClient(server.Normalise(), transport.Client(), StaticToken("")), fake
}

// A server may answer the same request with JSON or with a stream. A client
// that handles only one works against some servers and not others, and the ones
// it fails against are the ones doing anything interesting.
func TestBothResponseFormsAreUnderstood(t *testing.T) {
	for _, streaming := range []bool{false, true} {
		client, _ := newClient(t, &fakeServer{sse: streaming}, nil)
		info, err := client.Initialize(context.Background())
		if err != nil {
			t.Fatalf("sse=%v initialize: %v", streaming, err)
		}
		if info.ProtocolVersion != ProtocolVersion {
			t.Fatalf("sse=%v negotiated %q", streaming, info.ProtocolVersion)
		}
		tools, err := client.ListTools(context.Background())
		if err != nil || len(tools) != 4 {
			t.Fatalf("sse=%v tools: %d %v", streaming, len(tools), err)
		}
	}
}

// A server that could write the system prompt is a server that controls the
// assistant, so its instructions are captured for display and never forwarded.
func TestServerInstructionsAreNeverTreatedAsInstruction(t *testing.T) {
	client, _ := newClient(t, nil, nil)
	info, err := client.Initialize(context.Background())
	if err != nil {
		t.Fatalf("initialize: %v", err)
	}
	if info.Instructions == "" {
		t.Fatal("the fixture's instructions were dropped, so this proves nothing")
	}
	// The only path from a server to a model is a tool result, and Wrap fences
	// it. Instructions have no path at all: nothing in the package reads
	// ServerInfo.Instructions into a prompt.
	wrapped := Wrap("mcp:acme", info.Instructions)
	if !wrapped.Suspicious() {
		t.Fatalf("the fixture's instructions were not recognised as an attempt: %v",
			wrapped.Findings)
	}
}

// An unbounded result is a memory-exhaustion vector and a way to blow a model's
// context and the budget with it.
func TestAnOversizedResponseIsRefusedRatherThanTruncated(t *testing.T) {
	client, _ := newClient(t, &fakeServer{oversize: true}, func(server *Server) {
		server.MaxResponseBytes = 512
	})
	if _, err := client.Initialize(context.Background()); err != nil {
		t.Fatalf("initialize: %v", err)
	}
	_, err := client.CallTool(context.Background(), "search-orders", map[string]any{"query": "x"})
	if !errors.Is(err, ErrResponseTooLarge) {
		t.Fatalf("expected ErrResponseTooLarge, got %v", err)
	}
}

// A support operator needs an answer or a failure, not an open connection.
func TestASlowServerTimesOutRatherThanHanging(t *testing.T) {
	client, _ := newClient(t, &fakeServer{delay: 300 * time.Millisecond}, func(server *Server) {
		server.Timeout = 50 * time.Millisecond
	})
	started := time.Now()
	_, err := client.Initialize(context.Background())
	if !errors.Is(err, ErrTransport) {
		t.Fatalf("expected a transport failure, got %v", err)
	}
	if time.Since(started) > time.Second {
		t.Fatal("the timeout did not bound the call")
	}
}

// Matching on the id is what stops a server answering a question nobody asked,
// including one it invented mid-stream.
func TestAResponseForADifferentRequestIsRefused(t *testing.T) {
	transport := httptest.NewServer(http.HandlerFunc(
		func(writer http.ResponseWriter, _ *http.Request) {
			writer.Header().Set("Content-Type", "application/json")
			_, _ = writer.Write([]byte(
				`{"jsonrpc":"2.0","id":9999,"result":{"protocolVersion":"x"}}`))
		}))
	t.Cleanup(transport.Close)

	server := Server{
		Slug: "acme", Endpoint: transport.URL, Enabled: true, AllowPrivateNetwork: true,
	}
	client := NewClient(server.Normalise(), transport.Client(), StaticToken(""))
	if _, err := client.Initialize(context.Background()); !errors.Is(err, ErrProtocol) {
		t.Fatalf("expected ErrProtocol, got %v", err)
	}
}

// Malformed output is refused rather than partly believed.
func TestMalformedOutputIsRefused(t *testing.T) {
	for _, body := range []string{
		`not json at all`,
		`{"jsonrpc":"2.0","id":1,"result":{"protocolVersion":""}}`,
		`{"jsonrpc":"2.0","id":1,"error":{"code":-32000,"message":"boom"}}`,
	} {
		transport := httptest.NewServer(http.HandlerFunc(
			func(writer http.ResponseWriter, request *http.Request) {
				var message rpcRequest
				_ = json.NewDecoder(request.Body).Decode(&message)
				writer.Header().Set("Content-Type", "application/json")
				id := int64(1)
				if message.ID != nil {
					id = *message.ID
				}
				fmt.Fprint(writer, strings.Replace(body, `"id":1`, fmt.Sprintf(`"id":%d`, id), 1))
			}))
		server := Server{
			Slug: "acme", Endpoint: transport.URL, Enabled: true, AllowPrivateNetwork: true,
		}
		client := NewClient(server.Normalise(), transport.Client(), StaticToken(""))
		if _, err := client.Initialize(context.Background()); err == nil {
			t.Fatalf("%q was accepted", body)
		}
		transport.Close()
	}
}

// A bearer token belongs on the wire and nowhere else. This checks it is sent;
// nothing in the package writes it to a log or a preview.
func TestTheBearerTokenIsSentAndNotStored(t *testing.T) {
	fake := &fakeServer{}
	transport := httptest.NewServer(fake.handler())
	t.Cleanup(transport.Close)

	server := Server{
		Slug: "acme", Endpoint: transport.URL, Enabled: true, AllowPrivateNetwork: true,
	}
	secret := strings.Join([]string{"tok", "abc", "123"}, "-")
	client := NewClient(server.Normalise(), transport.Client(), StaticToken(secret))
	if _, err := client.Initialize(context.Background()); err != nil {
		t.Fatalf("initialize: %v", err)
	}

	fake.mutex.Lock()
	sent := fake.authorization
	fake.mutex.Unlock()
	if sent != "Bearer "+secret {
		t.Fatalf("the token was not presented: %q", sent)
	}

	preview := Preview{Server: "acme", Endpoint: server.Endpoint}
	if strings.Contains(fmt.Sprintf("%+v", preview), secret) {
		t.Fatal("a credential appeared in an operator-facing preview")
	}
}

// A server that keeps handing back a cursor holds the connection forever, and
// the honest response is to stop rather than to keep paying for it.
func TestEndlessPaginationStops(t *testing.T) {
	page := 0
	transport := httptest.NewServer(http.HandlerFunc(
		func(writer http.ResponseWriter, request *http.Request) {
			var message rpcRequest
			_ = json.NewDecoder(request.Body).Decode(&message)
			writer.Header().Set("Content-Type", "application/json")
			page++
			result := fmt.Sprintf(
				`{"tools":[{"name":"t%d","inputSchema":{"type":"object"}}],"nextCursor":"c%d"}`,
				page, page)
			encoded, _ := json.Marshal(rpcResponse{
				JSONRPC: "2.0", ID: message.ID, Result: json.RawMessage(result),
			})
			_, _ = writer.Write(encoded)
		}))
	t.Cleanup(transport.Close)

	server := Server{
		Slug: "acme", Endpoint: transport.URL, Enabled: true, AllowPrivateNetwork: true,
	}
	client := NewClient(server.Normalise(), transport.Client(), StaticToken(""))
	if _, err := client.ListTools(context.Background()); !errors.Is(err, ErrProtocol) {
		t.Fatalf("expected ErrProtocol, got %v", err)
	}
	if page > maxPages {
		t.Fatalf("the client followed %d pages", page)
	}
}
