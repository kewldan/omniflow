// Package mcpserver exposes Omniflow's own admin capabilities over MCP.
//
// It is the other half of `internal/mcp`: that package is Omniflow as a client
// of somebody else's server, this one is Omniflow as the server somebody else
// connects to. The threat model is inverted and the conclusions are not
// symmetric.
//
// Every tool is read-only unless an owner separately enables mutations, and
// enabling them does not enable all of them — a mutating tool is named in its
// own allowlist. "Connect an assistant to your admin panel" should not be one
// switch that also means "and let it issue refunds".
//
// Every call is authorised as a person. There is no service identity with its
// own permissions: a connection carries a token that resolves to an operator,
// and a tool the operator could not use in the panel is a tool they cannot use
// here. An MCP surface that had its own authority would be a way around RBAC
// with a nicer interface.
//
// Every mutation is idempotent by key. A transport that retries — and
// Streamable HTTP does — must not turn one refund into two.
package mcpserver

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/omniflow/omniflow/internal/mcp"
)

var (
	// ErrUnauthorized reports a request with no usable credential.
	ErrUnauthorized = errors.New("mcp request is not authenticated")
	// ErrForbidden reports an operator without the tool's permission.
	ErrForbidden = errors.New("this operator may not use that tool")
	// ErrMutationsDisabled reports a mutating tool on an installation that has
	// not enabled mutations. It is distinct from forbidden so an owner reading
	// the audit sees a configuration decision rather than a permission problem.
	ErrMutationsDisabled = errors.New("mcp mutations are not enabled for this installation")
	// ErrIdempotencyRequired reports a mutation with no key. Without one a retry
	// is indistinguishable from a second request.
	ErrIdempotencyRequired = errors.New("a mutating mcp tool requires an idempotency key")
	// ErrIdempotencyConflict reports a key reused with different arguments,
	// which is a bug in the caller rather than a retry.
	ErrIdempotencyConflict = errors.New("this idempotency key was used with different arguments")
)

// Grant is what the authenticated operator may do.
type Grant interface {
	Allows(permission string) bool
}

// Principal is who is calling.
type Principal struct {
	// ID is the operator's identifier, recorded on every audit entry so a tool
	// call is attributable to a person rather than to "the MCP server".
	ID    string
	Label string
	Grant Grant
}

// Authenticator resolves a credential to a principal.
//
// It is an interface so this package does not own the admin token scheme; the
// installation passes in whatever already answers "who is this?" for the admin
// API, and there is one answer rather than two.
type Authenticator interface {
	Authenticate(ctx context.Context, credential string) (Principal, error)
}

// Tool is one capability Omniflow offers.
type Tool struct {
	Name        string
	Description string
	// Permission is the Omniflow permission the caller must hold. Required: a
	// tool with none would be reachable by any authenticated connection.
	Permission string
	// Mutates marks a tool that changes Omniflow state. Mutating tools are off
	// unless an owner enables them and names this tool specifically.
	Mutates bool
	// InputSchema is the JSON Schema arguments are validated against, in the
	// same subset the client enforces. Validating what arrives is the same
	// problem as validating what leaves.
	InputSchema json.RawMessage
	// Run performs the work. For a mutating tool it receives the idempotency key
	// and must use it as the transaction's key, so a retry that gets past the
	// cache still cannot double-apply.
	Run func(ctx context.Context, call Call) (Result, error)

	schema *mcp.Schema
}

// Call is one authorised invocation.
type Call struct {
	Principal      Principal
	Arguments      map[string]any
	IdempotencyKey string
}

// Result is what a tool returned.
type Result struct {
	// Text is the human-readable answer.
	Text string
	// Structured is the machine-readable answer, when the tool has one.
	Structured map[string]any
	// IsError reports a tool that ran and failed.
	IsError bool
}

// Resource is a read-only document the server offers.
//
// Operational documentation is exposed as a resource rather than baked into
// tool descriptions because a resource is fetched when it is needed, and a
// description is carried in every request whether it is relevant or not.
type Resource struct {
	URI         string
	Name        string
	Description string
	MimeType    string
	// Permission gates the resource, like a tool. Documentation about refund
	// policy is not customer data, but a resource that listed queue names would
	// be, and one rule is easier to get right than an exception.
	Permission string
	Read       func(context.Context, Principal) (string, error)
}

// Options configures the server.
type Options struct {
	Authenticator Authenticator
	Tools         []Tool
	Resources     []Resource
	Audit         mcp.AuditSink
	// AllowMutations is the owner's switch. Off by default, and on its own it
	// enables nothing: a mutating tool must also appear in MutationAllowlist.
	AllowMutations bool
	// MutationAllowlist names the mutating tools an owner has enabled. The
	// two-part gate exists because "I turned on writes" and "I turned on this
	// write" are different decisions, and only the second one is informed.
	MutationAllowlist []string
	// Idempotency remembers completed mutations. Nil uses an in-memory store,
	// which is correct for a single process and stated here so an installation
	// running several knows to supply a shared one.
	Idempotency IdempotencyStore
	Clock       func() time.Time
}

// IdempotencyStore remembers what a key already did.
type IdempotencyStore interface {
	// Lookup returns a previous result for a key, and the argument fingerprint
	// it was used with.
	Lookup(ctx context.Context, key string) (Result, string, bool, error)
	Remember(ctx context.Context, key, fingerprint string, result Result) error
}

// Server answers MCP over Streamable HTTP.
type Server struct {
	options   Options
	tools     map[string]Tool
	resources map[string]Resource
	clock     func() time.Time
}

// New builds the server, refusing a tool it cannot enforce.
func New(options Options) (*Server, error) {
	if options.Authenticator == nil {
		// A server with no authenticator would answer everyone. Refusing to
		// start is the only safe reading of that configuration.
		return nil, errors.New("an mcp server requires an authenticator")
	}
	if options.Idempotency == nil {
		options.Idempotency = NewMemoryIdempotency()
	}
	if options.Clock == nil {
		options.Clock = time.Now
	}

	server := &Server{
		options:   options,
		tools:     make(map[string]Tool, len(options.Tools)),
		resources: make(map[string]Resource, len(options.Resources)),
		clock:     options.Clock,
	}
	for _, tool := range options.Tools {
		if strings.TrimSpace(tool.Name) == "" || strings.TrimSpace(tool.Permission) == "" {
			return nil, fmt.Errorf("mcp tool %q needs a name and a permission", tool.Name)
		}
		if tool.Run == nil {
			return nil, fmt.Errorf("mcp tool %q has no implementation", tool.Name)
		}
		schema, err := mcp.CompileSchema(tool.InputSchema)
		if err != nil {
			// A schema this build cannot enforce is refused at startup rather
			// than at call time, so the failure is a deployment problem instead
			// of an unvalidated argument.
			return nil, fmt.Errorf("mcp tool %q: %w", tool.Name, err)
		}
		tool.schema = schema
		server.tools[tool.Name] = tool
	}
	for _, resource := range options.Resources {
		if strings.TrimSpace(resource.URI) == "" || resource.Read == nil {
			return nil, fmt.Errorf("mcp resource %q needs a uri and a reader", resource.Name)
		}
		server.resources[resource.URI] = resource
	}
	return server, nil
}

// Enabled reports whether a mutating tool may run at all.
func (server *Server) Enabled(tool Tool) bool {
	if !tool.Mutates {
		return true
	}
	if !server.options.AllowMutations {
		return false
	}
	return slices.Contains(server.options.MutationAllowlist, tool.Name)
}

// Available lists the tools a principal may use, sorted.
//
// Tools the operator cannot use are omitted rather than listed-and-refused. A
// list that advertises what an operator cannot have is a list that teaches an
// assistant to keep asking.
func (server *Server) Available(principal Principal) []Tool {
	names := make([]string, 0, len(server.tools))
	for name, tool := range server.tools {
		if !server.Enabled(tool) {
			continue
		}
		if principal.Grant == nil || !principal.Grant.Allows(tool.Permission) {
			continue
		}
		names = append(names, name)
	}
	sort.Strings(names)
	available := make([]Tool, 0, len(names))
	for _, name := range names {
		available = append(available, server.tools[name])
	}
	return available
}

// Invoke runs one tool for one principal.
//
// The order is the safety property: known, enabled, permitted, valid, then
// idempotent. Each step refuses rather than degrading.
func (server *Server) Invoke(
	ctx context.Context, principal Principal, name string,
	arguments map[string]any, idempotencyKey string,
) (Result, error) {
	tool, known := server.tools[name]
	if !known {
		return Result{}, fmt.Errorf("%w: %s", mcp.ErrToolUnknown, name)
	}
	if !server.Enabled(tool) {
		server.record(ctx, principal, tool, "refused", ErrMutationsDisabled.Error(), idempotencyKey)
		return Result{}, ErrMutationsDisabled
	}
	if principal.Grant == nil || !principal.Grant.Allows(tool.Permission) {
		// The operator's own grant, not the connection's. A tool they could not
		// use in the panel is a tool they cannot use here.
		server.record(ctx, principal, tool, "refused", "missing "+tool.Permission, idempotencyKey)
		return Result{}, fmt.Errorf("%w: %s requires %s", ErrForbidden, name, tool.Permission)
	}
	if problems := tool.schema.Validate(normalise(arguments)); len(problems) > 0 {
		detail := strings.Join(problems, "; ")
		server.record(ctx, principal, tool, "refused", detail, idempotencyKey)
		return Result{}, fmt.Errorf("%w: %s", mcp.ErrArgumentsInvalid, detail)
	}

	if !tool.Mutates {
		result, err := tool.Run(ctx, Call{Principal: principal, Arguments: arguments})
		server.recordOutcome(ctx, principal, tool, err, idempotencyKey)
		return result, err
	}

	if strings.TrimSpace(idempotencyKey) == "" {
		server.record(ctx, principal, tool, "refused", ErrIdempotencyRequired.Error(), "")
		return Result{}, ErrIdempotencyRequired
	}
	fingerprint := fingerprintOf(name, arguments)
	previous, seen, replayed, err := server.options.Idempotency.Lookup(ctx, idempotencyKey)
	if err != nil {
		return Result{}, err
	}
	if replayed {
		if seen != fingerprint {
			// The same key with different arguments is a caller bug, not a
			// retry. Returning the first result would silently answer a
			// question nobody asked.
			server.record(ctx, principal, tool, "refused",
				ErrIdempotencyConflict.Error(), idempotencyKey)
			return Result{}, ErrIdempotencyConflict
		}
		server.record(ctx, principal, tool, "replayed", "returned the first result", idempotencyKey)
		return previous, nil
	}

	result, err := tool.Run(ctx, Call{
		Principal: principal, Arguments: arguments, IdempotencyKey: idempotencyKey,
	})
	if err != nil {
		server.recordOutcome(ctx, principal, tool, err, idempotencyKey)
		return Result{}, err
	}
	// Only a success is remembered. Remembering a failure would make a retry
	// after a transient error return the failure forever.
	if err := server.options.Idempotency.Remember(
		ctx, idempotencyKey, fingerprint, result,
	); err != nil {
		return Result{}, err
	}
	server.recordOutcome(ctx, principal, tool, nil, idempotencyKey)
	return result, nil
}

// ReadResource returns one document.
func (server *Server) ReadResource(
	ctx context.Context, principal Principal, uri string,
) (string, error) {
	resource, known := server.resources[uri]
	if !known {
		return "", fmt.Errorf("%w: %s", mcp.ErrToolUnknown, uri)
	}
	if resource.Permission != "" &&
		(principal.Grant == nil || !principal.Grant.Allows(resource.Permission)) {
		return "", fmt.Errorf("%w: %s requires %s", ErrForbidden, uri, resource.Permission)
	}
	return resource.Read(ctx, principal)
}

// Resources lists the documents a principal may read, sorted.
func (server *Server) Resources(principal Principal) []Resource {
	uris := make([]string, 0, len(server.resources))
	for uri, resource := range server.resources {
		if resource.Permission != "" &&
			(principal.Grant == nil || !principal.Grant.Allows(resource.Permission)) {
			continue
		}
		uris = append(uris, uri)
	}
	sort.Strings(uris)
	available := make([]Resource, 0, len(uris))
	for _, uri := range uris {
		available = append(available, server.resources[uri])
	}
	return available
}

func (server *Server) recordOutcome(
	ctx context.Context, principal Principal, tool Tool, err error, key string,
) {
	if err != nil {
		server.record(ctx, principal, tool, "failed", err.Error(), key)
		return
	}
	server.record(ctx, principal, tool, "allowed", "", key)
}

func (server *Server) record(
	ctx context.Context, principal Principal, tool Tool, outcome, detail, key string,
) {
	if server.options.Audit == nil {
		return
	}
	_ = server.options.Audit.Record(ctx, mcp.Event{
		At: server.clock(), Kind: mcp.EventToolCall, Server: "omniflow",
		Tool: tool.Name, Operator: principal.ID, Outcome: outcome,
		Detail: strings.TrimSpace(strings.Join([]string{detail, keyNote(key)}, " ")),
	})
}

func keyNote(key string) string {
	if key == "" {
		return ""
	}
	return "(idempotency " + key + ")"
}

// fingerprintOf renders the arguments canonically so the same call produces the
// same fingerprint regardless of map ordering.
func fingerprintOf(name string, arguments map[string]any) string {
	keys := make([]string, 0, len(arguments))
	for key := range arguments {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys)+1)
	parts = append(parts, name)
	for _, key := range keys {
		encoded, _ := json.Marshal(arguments[key])
		parts = append(parts, key+"="+string(encoded))
	}
	return strings.Join(parts, "\x00")
}

func normalise(arguments map[string]any) any {
	if arguments == nil {
		return map[string]any{}
	}
	encoded, err := json.Marshal(arguments)
	if err != nil {
		return arguments
	}
	var decoded any
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		return arguments
	}
	return decoded
}

// MemoryIdempotency is a process-local store.
//
// It is the default because it is correct for the single-process case and
// obviously wrong for several, which is a better failure than a store that
// looks distributed and is not.
type MemoryIdempotency struct {
	mutex   sync.Mutex
	entries map[string]memoryEntry
}

type memoryEntry struct {
	fingerprint string
	result      Result
}

// NewMemoryIdempotency builds the store.
func NewMemoryIdempotency() *MemoryIdempotency {
	return &MemoryIdempotency{entries: map[string]memoryEntry{}}
}

// Lookup returns a remembered result.
func (store *MemoryIdempotency) Lookup(
	_ context.Context, key string,
) (Result, string, bool, error) {
	store.mutex.Lock()
	defer store.mutex.Unlock()
	entry, known := store.entries[key]
	return entry.result, entry.fingerprint, known, nil
}

// Remember records a completed mutation.
func (store *MemoryIdempotency) Remember(
	_ context.Context, key, fingerprint string, result Result,
) error {
	store.mutex.Lock()
	defer store.mutex.Unlock()
	store.entries[key] = memoryEntry{fingerprint: fingerprint, result: result}
	return nil
}

// credentialFrom pulls a bearer token out of a request.
func credentialFrom(request *http.Request) string {
	header := request.Header.Get("Authorization")
	if !strings.HasPrefix(header, "Bearer ") {
		return ""
	}
	return strings.TrimSpace(strings.TrimPrefix(header, "Bearer "))
}
