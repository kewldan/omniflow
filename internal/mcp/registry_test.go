package mcp

import (
	"errors"
	"strings"
	"testing"
)

func base(adjust func(*Server)) Server {
	server := Server{
		Slug: "acme", Endpoint: "https://mcp.example.test/rpc", Enabled: true,
		AllowedTools: []string{"search"},
		Permissions:  map[string]string{"search": "finance.read"},
	}
	if adjust != nil {
		adjust(&server)
	}
	return server
}

// An allowlisted tool with no permission would be callable by anyone who can
// reach the copilot, which is the failure this package exists to prevent.
func TestAToolWithoutAPermissionMappingIsRefused(t *testing.T) {
	_, err := NewRegistry(base(func(server *Server) {
		server.AllowedTools = []string{"search", "danger"}
	}))
	if err == nil || !strings.Contains(err.Error(), "danger") {
		t.Fatalf("an unmapped tool was accepted: %v", err)
	}
}

// A bearer token on an unencrypted connection is a token somebody else has.
func TestPlaintextIsRefusedUnlessTheOwnerOptedIntoALocalServer(t *testing.T) {
	if _, err := NewRegistry(base(func(server *Server) {
		server.Endpoint = "http://mcp.example.test/rpc"
	})); err == nil {
		t.Fatal("a plaintext endpoint was accepted")
	}
	if _, err := NewRegistry(base(func(server *Server) {
		server.Endpoint = "http://127.0.0.1:9000/rpc"
		server.AllowPrivateNetwork = true
	})); err != nil {
		t.Fatalf("a deliberately local server was refused: %v", err)
	}
}

// Marking a tool as a write without exposing it means a confirmation rule that
// silently applies to nothing.
func TestAWriteMustBeAToolTheServerActuallyExposes(t *testing.T) {
	if _, err := NewRegistry(base(func(server *Server) {
		server.WriteTools = []string{"refund"}
	})); err == nil {
		t.Fatal("a write marker for an unexposed tool was accepted")
	}
}

// A partly-loaded registry means an operator sees a tool missing and no
// explanation.
func TestABadServerRejectsTheWholeRegistration(t *testing.T) {
	_, err := NewRegistry(
		base(nil),
		base(func(server *Server) { server.Slug = "broken"; server.Endpoint = "" }),
	)
	if err == nil {
		t.Fatal("a registry loaded around a broken server")
	}
}

// A server that re-points at an internal address after approval is the textbook
// version of this attack, which is why egress is checked per request.
func TestEgressRefusesInternalAddressesAndForeignHosts(t *testing.T) {
	server := base(nil).Normalise()

	if err := server.CheckEgress("https://elsewhere.example/rpc"); !errors.Is(err, ErrEgressForbidden) {
		t.Fatalf("a foreign host was allowed: %v", err)
	}

	local := base(func(candidate *Server) {
		candidate.Endpoint = "http://127.0.0.1:9000/rpc"
	}).Normalise()
	if err := local.CheckEgress(local.Endpoint); !errors.Is(err, ErrEgressForbidden) {
		t.Fatalf("loopback was allowed without an opt-in: %v", err)
	}

	local.AllowPrivateNetwork = true
	if err := local.CheckEgress(local.Endpoint); err != nil {
		t.Fatalf("an opted-in local server was refused: %v", err)
	}

	// The metadata address is the one that matters most, and it is link-local
	// rather than private — a check that only covered RFC 1918 would miss it.
	metadata := base(func(candidate *Server) {
		candidate.Endpoint = "http://169.254.169.254/latest/meta-data"
	}).Normalise()
	if err := metadata.CheckEgress(metadata.Endpoint); !errors.Is(err, ErrEgressForbidden) {
		t.Fatalf("the cloud metadata address was reachable: %v", err)
	}
}

// An owner who needs a wider limit raises it and knows they did.
func TestLimitsDefaultToSomethingTight(t *testing.T) {
	server := Server{Slug: "acme", Endpoint: "https://x.test/rpc"}.Normalise()
	if server.Timeout != DefaultTimeout ||
		server.MaxResponseBytes != DefaultMaxResponseBytes ||
		server.MaxCallsPerRequest != DefaultMaxCallsPerRequest ||
		server.MaxDepth != DefaultMaxDepth {
		t.Fatalf("a server with no limits was left unbounded: %+v", server)
	}
}

// Empty means none. Discovery still works so an owner can see what is on offer,
// but nothing is callable until they choose.
func TestAnEmptyAllowlistExposesNothing(t *testing.T) {
	server := base(func(candidate *Server) {
		candidate.AllowedTools = nil
		candidate.Permissions = nil
	}).Normalise()
	if err := server.Validate(); err != nil {
		t.Fatalf("a server with no allowlisted tools was refused: %v", err)
	}
	if server.Allows("search") {
		t.Fatal("an empty allowlist allowed a tool")
	}
}
