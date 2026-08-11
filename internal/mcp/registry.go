// Package mcp connects Omniflow to Model Context Protocol servers.
//
// An MCP server is a third party that Omniflow gives a model access to. That is
// the whole security problem in one sentence, and every decision here follows
// from it:
//
// A connection is off until an owner turns it on, and reaches only the hosts
// and tools the owner named. Discovery does not grant use — a server that
// advertises forty tools exposes the ones an owner allowlisted and no others.
//
// Tool arguments and tool results are both validated against JSON Schema before
// they reach a model or application code. Validating the way in and not the way
// out would leave the interesting direction unchecked: the result is the part an
// attacker controls.
//
// Everything a server returns is untrusted data, never instruction. Tool output
// is fenced and labelled before it is shown to a model, because the alternative
// is a webpage that tells the copilot to issue a refund.
//
// Nothing external is written without a person confirming it. A read that turns
// out wrong is a wrong answer; a write that turns out wrong happened.
package mcp

import (
	"errors"
	"fmt"
	"net"
	"net/url"
	"slices"
	"sort"
	"strings"
	"time"
)

var (
	// ErrServerUnknown reports a server that is not registered.
	ErrServerUnknown = errors.New("mcp server is not registered")
	// ErrServerDisabled reports a registered server an owner has switched off.
	// It is distinct from unknown so the panel can offer "enable" rather than
	// "add".
	ErrServerDisabled = errors.New("mcp server is disabled")
	// ErrToolNotAllowed reports a tool outside the server's allowlist. A server
	// advertising a tool is not the same as an owner permitting it.
	ErrToolNotAllowed = errors.New("mcp tool is not allowlisted for this server")
	// ErrEgressForbidden reports an endpoint outside the permitted egress. It
	// fires before any request, so a redirected or re-pointed server cannot
	// become a probe of the installation's own network.
	ErrEgressForbidden = errors.New("mcp endpoint is outside the permitted egress")
)

// Server is one owner-registered MCP connection.
//
// Every field that could widen reach is explicit and defaults to the narrow
// value. There is no "allow all tools" flag and no "any host" mode: an
// installation that wants a wide connection names what it wants wide.
type Server struct {
	// Slug is the stable identifier used in permissions, audit, and the panel.
	Slug string
	Name string
	// Endpoint is the Streamable HTTP endpoint. One URL, because the transport
	// is one endpoint that handles both directions.
	Endpoint string
	// Enabled is the owner's switch. A registered server that is off is
	// unreachable rather than merely hidden.
	Enabled bool

	// AllowedTools is the closed set of tools this connection exposes. Empty
	// means none — discovery still works, so an owner can see what is on offer
	// and choose, but nothing is callable until they do.
	AllowedTools []string
	// Permissions maps a tool name to the Omniflow permission an operator must
	// hold to invoke it. A tool with no mapping is unusable: an unmapped tool
	// would otherwise be reachable by anyone who can reach the copilot.
	Permissions map[string]string
	// WriteTools names the tools that change something outside Omniflow. They
	// require explicit confirmation on every call. The list is owner-maintained
	// rather than taken from the server's own annotations, because a server that
	// wants to avoid a confirmation prompt would simply not set the hint.
	WriteTools []string

	// AllowedHosts restricts egress beyond the endpoint's own host, for servers
	// that legitimately redirect. Empty means the endpoint host only.
	AllowedHosts []string
	// AllowPrivateNetwork permits a loopback or RFC 1918 endpoint. It exists
	// because a self-hosted MCP server on the same machine is a real
	// deployment, and it is off by default because it is also how a connection
	// becomes an internal port scanner.
	AllowPrivateNetwork bool

	Timeout time.Duration
	// MaxResponseBytes bounds one tool result. An unbounded result is a memory
	// exhaustion vector and, more mundanely, a way to blow a model's context and
	// the budget with it.
	MaxResponseBytes int64
	// MaxCallsPerRequest bounds how many tool calls one operator question may
	// make. Without it, a tool result that suggests another tool call is an
	// unbounded loop with a bill attached.
	MaxCallsPerRequest int
	// MaxDepth bounds tool-call recursion for the same reason, one level down.
	MaxDepth int
	// CostLimitMinor is the estimated monetary ceiling for one request, in minor
	// units. Zero means the installation-wide AI budget is the only ceiling.
	CostLimitMinor int64
}

// Defaults applied to a server that leaves a limit unset. They are deliberately
// tight: an owner who needs more raises it and knows they did.
const (
	DefaultTimeout            = 20 * time.Second
	DefaultMaxResponseBytes   = 256 * 1024
	DefaultMaxCallsPerRequest = 8
	DefaultMaxDepth           = 3
)

// Normalise fills the unset limits and returns the server, so every caller sees
// the same effective configuration rather than each applying its own default.
func (server Server) Normalise() Server {
	if server.Timeout <= 0 {
		server.Timeout = DefaultTimeout
	}
	if server.MaxResponseBytes <= 0 {
		server.MaxResponseBytes = DefaultMaxResponseBytes
	}
	if server.MaxCallsPerRequest <= 0 {
		server.MaxCallsPerRequest = DefaultMaxCallsPerRequest
	}
	if server.MaxDepth <= 0 {
		server.MaxDepth = DefaultMaxDepth
	}
	return server
}

// Validate refuses a registration that cannot be enforced.
func (server Server) Validate() error {
	if strings.TrimSpace(server.Slug) == "" {
		return errors.New("an mcp server needs a slug")
	}
	if strings.TrimSpace(server.Endpoint) == "" {
		return errors.New("an mcp server needs an endpoint")
	}
	parsed, err := url.Parse(server.Endpoint)
	if err != nil || parsed.Host == "" {
		return fmt.Errorf("mcp server %q has an unusable endpoint", server.Slug)
	}
	// Plaintext is refused rather than warned about. A bearer token on an
	// unencrypted connection is a token somebody else has, and the exception an
	// owner would want — a local server — is served by loopback over HTTP being
	// permitted only when they also set AllowPrivateNetwork.
	if parsed.Scheme != "https" && !(parsed.Scheme == "http" && server.AllowPrivateNetwork) {
		return fmt.Errorf("mcp server %q must use https", server.Slug)
	}
	for _, tool := range server.AllowedTools {
		if strings.TrimSpace(server.Permissions[tool]) == "" {
			// An allowlisted tool with no permission would be callable by
			// anyone who can reach the copilot, which is the failure this
			// package exists to prevent.
			return fmt.Errorf("mcp tool %q on %q has no permission mapping", tool, server.Slug)
		}
	}
	for _, tool := range server.WriteTools {
		if !server.Allows(tool) {
			return fmt.Errorf("mcp server %q marks %q as a write but does not expose it",
				server.Slug, tool)
		}
	}
	return nil
}

// Allows reports whether a tool is allowlisted.
func (server Server) Allows(tool string) bool {
	return slices.Contains(server.AllowedTools, tool)
}

// Writes reports whether a tool changes something outside Omniflow.
func (server Server) Writes(tool string) bool {
	return slices.Contains(server.WriteTools, tool)
}

// PermissionFor returns the Omniflow permission a tool requires, and whether
// one is mapped at all. An unmapped tool is refused rather than defaulted.
func (server Server) PermissionFor(tool string) (string, bool) {
	permission := strings.TrimSpace(server.Permissions[tool])
	return permission, permission != ""
}

// CheckEgress refuses a URL the server may not reach.
//
// It runs before every request rather than once at registration, because the
// address a hostname resolves to can change between the two, and a server that
// re-points at 169.254.169.254 after approval is the textbook version of this
// attack.
func (server Server) CheckEgress(target string) error {
	parsed, err := url.Parse(target)
	if err != nil || parsed.Host == "" {
		return fmt.Errorf("%w: %q is not a usable url", ErrEgressForbidden, target)
	}
	endpoint, err := url.Parse(server.Endpoint)
	if err != nil {
		return fmt.Errorf("%w: the registered endpoint is unusable", ErrEgressForbidden)
	}

	host := parsed.Hostname()
	permitted := host == endpoint.Hostname() ||
		slices.ContainsFunc(server.AllowedHosts, func(allowed string) bool {
			return strings.EqualFold(host, strings.TrimSpace(allowed))
		})
	if !permitted {
		return fmt.Errorf("%w: %s is not an allowed host for %s",
			ErrEgressForbidden, host, server.Slug)
	}
	if server.AllowPrivateNetwork {
		return nil
	}
	addresses, err := net.LookupIP(host)
	if err != nil {
		// An unresolvable host is refused rather than attempted. The request
		// would fail anyway, and failing here produces a message an operator can
		// act on instead of a transport error.
		return fmt.Errorf("%w: %s does not resolve", ErrEgressForbidden, host)
	}
	for _, address := range addresses {
		if isInternal(address) {
			return fmt.Errorf("%w: %s resolves to an internal address", ErrEgressForbidden, host)
		}
	}
	return nil
}

// isInternal reports addresses an MCP server has no business being on unless an
// owner said so: loopback, link-local (which includes cloud metadata), private
// ranges, and the unspecified address.
func isInternal(address net.IP) bool {
	return address.IsLoopback() || address.IsPrivate() ||
		address.IsLinkLocalUnicast() || address.IsLinkLocalMulticast() ||
		address.IsUnspecified() || address.IsInterfaceLocalMulticast()
}

// Registry holds the registered servers.
type Registry struct {
	servers map[string]Server
}

// NewRegistry builds a registry, refusing any server it cannot enforce. It
// refuses the whole set rather than skipping the bad one: a partly-loaded
// registry means an operator sees a tool missing and no explanation.
func NewRegistry(servers ...Server) (*Registry, error) {
	registry := &Registry{servers: make(map[string]Server, len(servers))}
	for _, server := range servers {
		normalised := server.Normalise()
		if err := normalised.Validate(); err != nil {
			return nil, err
		}
		if _, duplicate := registry.servers[normalised.Slug]; duplicate {
			return nil, fmt.Errorf("mcp server %q is registered twice", normalised.Slug)
		}
		registry.servers[normalised.Slug] = normalised
	}
	return registry, nil
}

// Server returns a registered server, refusing a disabled one.
func (registry *Registry) Server(slug string) (Server, error) {
	server, known := registry.servers[slug]
	if !known {
		return Server{}, fmt.Errorf("%w: %s", ErrServerUnknown, slug)
	}
	if !server.Enabled {
		return Server{}, fmt.Errorf("%w: %s", ErrServerDisabled, slug)
	}
	return server, nil
}

// Slugs lists every registered server, enabled or not, sorted.
func (registry *Registry) Slugs() []string {
	slugs := make([]string, 0, len(registry.servers))
	for slug := range registry.servers {
		slugs = append(slugs, slug)
	}
	sort.Strings(slugs)
	return slugs
}
