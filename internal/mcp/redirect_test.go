package mcp

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

// A registered server that answers with a redirect must not be able to send
// the request — and the bearer token riding on it — to a host the owner never
// allowlisted. Redirects used to be followed with no check at all.
func TestARedirectToAnUnapprovedHostIsNotFollowed(t *testing.T) {
	var reached atomic.Int32
	elsewhere := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		reached.Add(1)
		writer.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(elsewhere.Close)
	// Same loopback server, different host name: it is not the endpoint's host
	// and not on the allowlist, which is the property under test.
	foreign := strings.Replace(elsewhere.URL, "127.0.0.1", "localhost", 1)

	registered := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		http.Redirect(writer, request, foreign+"/steal", http.StatusTemporaryRedirect)
	}))
	t.Cleanup(registered.Close)

	server := Server{
		Slug: "acme", Endpoint: registered.URL, Enabled: true, AllowPrivateNetwork: true,
	}
	client := NewClient(server.Normalise(), registered.Client(), StaticToken("tok-abc"))
	if _, err := client.Initialize(context.Background()); err == nil {
		t.Fatal("initialize succeeded through a redirect to an unapproved host")
	}
	if reached.Load() != 0 {
		t.Fatal("the redirect was followed to a host the owner never approved")
	}
}

// Without the private-network opt-in the default client refuses a loopback
// endpoint, and the refusal is the egress rule rather than a transport error.
func TestTheDefaultClientRefusesAnInternalEndpoint(t *testing.T) {
	target := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(target.Close)
	server := Server{Slug: "acme", Endpoint: target.URL, Enabled: true}
	client := NewClient(server.Normalise(), nil, StaticToken(""))
	if _, err := client.Initialize(context.Background()); err == nil ||
		!strings.Contains(err.Error(), ErrEgressForbidden.Error()) {
		t.Fatalf("initialize against loopback = %v, want an egress refusal", err)
	}
}
