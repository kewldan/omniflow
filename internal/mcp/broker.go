package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"
)

var (
	// ErrForbidden reports an operator without the permission a tool is mapped
	// to.
	ErrForbidden = errors.New("this account may not use that mcp tool")
	// ErrConfirmationRequired reports a write attempted without a person
	// agreeing to it. It is returned before the call, so the refusal is a
	// prompt rather than an apology.
	ErrConfirmationRequired = errors.New("this mcp tool requires explicit confirmation")
	// ErrReasonRequired reports a confirmed write with no stated reason. The
	// reason is what makes the audit entry answer "why" six months later.
	ErrReasonRequired = errors.New("a confirmed mcp write requires a reason")
	// ErrArgumentsInvalid reports arguments that did not match the tool's
	// declared input schema.
	ErrArgumentsInvalid = errors.New("mcp tool arguments are invalid")
	// ErrResultInvalid reports a result that did not match the tool's declared
	// output schema. It is refused rather than passed through: a result that
	// does not match its contract is the case where a server has been replaced
	// or compromised.
	ErrResultInvalid = errors.New("mcp tool result did not match its schema")
	// ErrLimitExceeded reports a request that hit a call-count, depth, or cost
	// ceiling.
	ErrLimitExceeded = errors.New("mcp request exceeded a configured limit")
	// ErrCapabilitiesUnknown reports a server whose tools have never been
	// discovered. Calling a tool whose schema is unknown would mean forwarding
	// unvalidated arguments, so it is refused.
	ErrCapabilitiesUnknown = errors.New("mcp server capabilities have not been discovered")
	// ErrToolUnknown reports a tool the server does not advertise.
	ErrToolUnknown = errors.New("mcp server does not offer that tool")
)

// Grant is what the asking operator may do. It is the same shape the copilot
// uses, so there is one answer to "may they?" across every AI surface.
type Grant interface {
	Allows(permission string) bool
}

// Invocation is one requested tool call.
type Invocation struct {
	Server string
	Tool   string
	// Arguments are what the caller wants to send. They are validated against
	// the tool's schema before anything is sent, and the validated copy is what
	// goes out — not the original.
	Arguments map[string]any
	// Confirmed records that a person saw the preview and agreed. It is only
	// consulted for tools the owner marked as writes.
	Confirmed bool
	// Reason is the operator's stated why, required alongside a confirmation.
	Reason string
	// Operator identifies who is asking, for the audit trail.
	Operator string
}

// Preview is what an operator is shown before a call happens.
//
// Every field exists because a confirmation prompt that does not say what will
// happen is a click-through. "Call tool X?" trains an operator to say yes;
// "search-orders on acme, reading orders for customer 4a1c, no external change"
// lets them notice when it says something else.
type Preview struct {
	Server string
	Tool   string
	// Endpoint is where the request goes. It is shown because "which third
	// party is this?" is the question a confirmation is really asking.
	Endpoint    string
	Description string
	// Arguments are rendered after validation, so the preview shows what will
	// actually be sent rather than what was proposed.
	Arguments map[string]any
	// Permission is the Omniflow permission this call is authorised by.
	Permission string
	// Writes reports whether this changes something outside Omniflow.
	Writes bool
	// RequiresConfirmation is Writes, restated as the thing the UI acts on. They
	// are separate fields because a future rule that requires confirmation for a
	// read should not have to lie about the read being a write.
	RequiresConfirmation bool
	// SideEffects is the owner-facing statement of what the call does. For a
	// read it says so explicitly, because "no side effects" is information.
	SideEffects string
	// Problems are the schema violations. A preview with problems cannot be
	// confirmed into a call.
	Problems []string
}

// Outcome is a completed call.
type Outcome struct {
	Preview Preview
	// Content is the tool's textual result, wrapped as untrusted data. It is an
	// Untrusted rather than a string so a caller cannot accidentally treat it as
	// instruction — using it in a prompt means calling Prompt(), which fences it.
	Content Untrusted
	// Structured is the parsed result, present only when the tool declared an
	// output schema and the result matched it.
	Structured map[string]any
	// ToolError reports a tool that ran and failed. The connection worked; this
	// is the tool's answer.
	ToolError bool
	Bytes     int64
	Duration  time.Duration
}

// AuditSink records everything that happened.
//
// It is an interface so the broker does not own storage, and it is called for
// refusals as well as successes: an audit trail that only records what happened
// cannot answer "did anyone try?", which is the question asked after an
// incident.
type AuditSink interface {
	Record(context.Context, Event) error
}

// Event is one auditable moment.
type Event struct {
	At       time.Time
	Kind     string
	Server   string
	Tool     string
	Operator string
	// Arguments are recorded as sent. They are the tool's own arguments, which
	// an owner has already seen in the preview.
	Arguments map[string]any
	Confirmed bool
	Reason    string
	// Outcome is "allowed", "refused", or "failed"; Detail says why.
	Outcome  string
	Detail   string
	Bytes    int64
	Duration time.Duration
	// Findings are the injection patterns detected in the result, so a strange
	// answer later has a record of the material that produced it.
	Findings []string
}

// Event kinds.
const (
	EventConnectionChanged = "connection_changed"
	EventDiscovery         = "discovery"
	EventToolCall          = "tool_call"
	EventConfirmation      = "confirmation"
	EventFailure           = "failure"
)

// Capabilities is what a server advertised, cached.
//
// It is cached because discovery is a network round trip on a path an operator
// is waiting on, and it is refreshed explicitly rather than on a timer so that
// "the tools changed" is a thing an owner is told about rather than something
// that silently happens between two calls.
type Capabilities struct {
	Server      string
	Info        ServerInfo
	Tools       []Tool
	Resources   []Resource
	Prompts     []Prompt
	RefreshedAt time.Time

	schemas map[string]*Schema
	outputs map[string]*Schema
}

// Session bounds one operator request across however many tool calls it makes.
//
// The limits live here rather than on the broker because they are per request:
// a ceiling of eight calls that reset on process start is not a ceiling.
type Session struct {
	mutex     sync.Mutex
	calls     int
	depth     int
	costMinor int64
	started   time.Time
}

// NewSession starts a request budget.
func NewSession() *Session { return &Session{started: time.Now()} }

// Broker is the only way a tool call reaches a server.
type Broker struct {
	registry *Registry
	clients  map[string]*Client
	breaker  *Breaker
	audit    AuditSink
	clock    func() time.Time

	mutex        sync.RWMutex
	capabilities map[string]Capabilities
	health       map[string]Health
}

// NewBroker builds the broker.
func NewBroker(registry *Registry, clients map[string]*Client, audit AuditSink) *Broker {
	return &Broker{
		registry: registry, clients: clients, breaker: NewBreaker(), audit: audit,
		clock:        time.Now,
		capabilities: map[string]Capabilities{},
		health:       map[string]Health{},
	}
}

// Discover refreshes one server's capability metadata and health.
func (broker *Broker) Discover(ctx context.Context, slug string) (Capabilities, error) {
	server, err := broker.registry.Server(slug)
	if err != nil {
		return Capabilities{}, err
	}
	client, present := broker.clients[slug]
	if !present {
		return Capabilities{}, fmt.Errorf("%w: %s has no client", ErrServerUnknown, slug)
	}
	if err := broker.breaker.Allow(slug); err != nil {
		return Capabilities{}, err
	}

	started := broker.clock()
	info, err := client.Initialize(ctx)
	if err != nil {
		broker.breaker.Failed(slug)
		broker.recordHealth(slug, Health{
			Slug: slug, Reachable: false, Detail: reason(err),
			CheckedAt: broker.clock(), Latency: broker.clock().Sub(started),
		})
		broker.record(ctx, Event{
			At: broker.clock(), Kind: EventDiscovery, Server: slug,
			Outcome: "failed", Detail: reason(err),
		})
		return Capabilities{}, err
	}

	tools, err := client.ListTools(ctx)
	if err != nil {
		broker.breaker.Failed(slug)
		return Capabilities{}, err
	}
	// Resources and prompts are optional capabilities. A server that offers
	// neither is not broken, so their absence is not a discovery failure.
	resources, _ := client.ListResources(ctx)
	prompts, _ := client.ListPrompts(ctx)

	capabilities := Capabilities{
		Server: slug, Info: info, Tools: tools, Resources: resources,
		Prompts: prompts, RefreshedAt: broker.clock(),
		schemas: map[string]*Schema{}, outputs: map[string]*Schema{},
	}
	// Schemas are compiled at discovery, not at call time. A tool whose schema
	// this validator cannot enforce is refused now, while an owner is looking at
	// the connection, rather than at three in the morning when it is used.
	unusable := make([]string, 0, 2)
	for _, tool := range tools {
		input, err := CompileSchema(tool.InputSchema)
		if err != nil {
			unusable = append(unusable, tool.Name+": "+err.Error())
			continue
		}
		capabilities.schemas[tool.Name] = input
		if len(tool.OutputSchema) > 0 {
			output, err := CompileSchema(tool.OutputSchema)
			if err != nil {
				unusable = append(unusable, tool.Name+" (output): "+err.Error())
				continue
			}
			capabilities.outputs[tool.Name] = output
		}
	}

	broker.breaker.Succeeded(slug)
	broker.mutex.Lock()
	broker.capabilities[slug] = capabilities
	broker.mutex.Unlock()
	broker.recordHealth(slug, Health{
		Slug: slug, Reachable: true, CheckedAt: broker.clock(),
		Latency: broker.clock().Sub(started), Info: info,
	})
	broker.record(ctx, Event{
		At: broker.clock(), Kind: EventDiscovery, Server: server.Slug, Outcome: "allowed",
		Detail: fmt.Sprintf("%d tools, %d resources, %d prompts%s",
			len(tools), len(resources), len(prompts), unusableSuffix(unusable)),
	})
	return capabilities, nil
}

func unusableSuffix(unusable []string) string {
	if len(unusable) == 0 {
		return ""
	}
	return "; unusable schemas: " + strings.Join(unusable, "; ")
}

// Capabilities returns the cached metadata for a server.
func (broker *Broker) Capabilities(slug string) (Capabilities, bool) {
	broker.mutex.RLock()
	defer broker.mutex.RUnlock()
	capabilities, known := broker.capabilities[slug]
	return capabilities, known
}

// Health returns the last-known reachability of every registered server,
// sorted, including servers never contacted.
func (broker *Broker) Health() []Health {
	broker.mutex.RLock()
	defer broker.mutex.RUnlock()
	statuses := make([]Health, 0, len(broker.health))
	for _, slug := range broker.registry.Slugs() {
		status, known := broker.health[slug]
		if !known {
			status = Health{Slug: slug, Detail: "never contacted"}
		}
		if broker.breaker.Open(slug) {
			status.Reachable = false
			status.Detail = "circuit open after repeated failures"
		}
		statuses = append(statuses, status)
	}
	sort.Slice(statuses, func(left, right int) bool {
		return statuses[left].Slug < statuses[right].Slug
	})
	return statuses
}

func (broker *Broker) recordHealth(slug string, status Health) {
	broker.mutex.Lock()
	defer broker.mutex.Unlock()
	broker.health[slug] = status
}

// Preview describes a call without making it.
//
// Every refusal a call would hit is checked here, in the same order, so an
// operator never sees a preview they can confirm into an error.
func (broker *Broker) Preview(grant Grant, invocation Invocation) (Preview, error) {
	server, err := broker.registry.Server(invocation.Server)
	if err != nil {
		return Preview{}, err
	}
	if !server.Allows(invocation.Tool) {
		return Preview{}, fmt.Errorf("%w: %s on %s",
			ErrToolNotAllowed, invocation.Tool, server.Slug)
	}
	permission, mapped := server.PermissionFor(invocation.Tool)
	if !mapped {
		return Preview{}, fmt.Errorf("%w: %s has no permission mapping", ErrForbidden, invocation.Tool)
	}
	if grant == nil || !grant.Allows(permission) {
		// Checked against the asking operator's grant. A broker that used the
		// server's own authority would let any operator reach every tool an
		// owner ever connected.
		return Preview{}, fmt.Errorf("%w: %s requires %s", ErrForbidden, invocation.Tool, permission)
	}

	capabilities, known := broker.Capabilities(server.Slug)
	if !known {
		return Preview{}, fmt.Errorf("%w: %s", ErrCapabilitiesUnknown, server.Slug)
	}
	schema, described := capabilities.schemas[invocation.Tool]
	if !described {
		return Preview{}, fmt.Errorf("%w: %s", ErrToolUnknown, invocation.Tool)
	}

	writes := server.Writes(invocation.Tool)
	preview := Preview{
		Server: server.Slug, Tool: invocation.Tool, Endpoint: server.Endpoint,
		Description: descriptionOf(capabilities, invocation.Tool),
		Arguments:   invocation.Arguments, Permission: permission,
		Writes: writes, RequiresConfirmation: writes,
		SideEffects: sideEffects(writes, server.Slug),
		Problems:    schema.Validate(normaliseArguments(invocation.Arguments)),
	}
	return preview, nil
}

func descriptionOf(capabilities Capabilities, tool string) string {
	for _, candidate := range capabilities.Tools {
		if candidate.Name == tool {
			// A tool description is written by the server and is therefore
			// untrusted text on its way to an operator's screen. It is clipped
			// so a description cannot become a wall of text that hides the
			// arguments below it.
			return clip(candidate.Description, 400)
		}
	}
	return ""
}

func sideEffects(writes bool, slug string) string {
	if writes {
		return "This changes data on " + slug + ", outside Omniflow. " +
			"Omniflow cannot undo it."
	}
	return "This reads from " + slug + " and changes nothing."
}

// Invoke performs one tool call, or refuses it.
//
// The order is the safety property, and it is the same order Preview checks in:
// registration, allowlist, permission, discovery, schema, confirmation, limits,
// breaker, and only then the network. Every step refuses rather than degrading.
func (broker *Broker) Invoke(
	ctx context.Context, session *Session, grant Grant, invocation Invocation,
) (Outcome, error) {
	preview, err := broker.Preview(grant, invocation)
	if err != nil {
		broker.refused(ctx, invocation, err)
		return Outcome{}, err
	}
	if len(preview.Problems) > 0 {
		err := fmt.Errorf("%w: %s", ErrArgumentsInvalid, strings.Join(preview.Problems, "; "))
		broker.refused(ctx, invocation, err)
		return Outcome{}, err
	}
	if preview.RequiresConfirmation {
		if !invocation.Confirmed {
			broker.refused(ctx, invocation, ErrConfirmationRequired)
			return Outcome{}, ErrConfirmationRequired
		}
		if strings.TrimSpace(invocation.Reason) == "" {
			broker.refused(ctx, invocation, ErrReasonRequired)
			return Outcome{}, ErrReasonRequired
		}
		broker.record(ctx, Event{
			At: broker.clock(), Kind: EventConfirmation, Server: invocation.Server,
			Tool: invocation.Tool, Operator: invocation.Operator, Confirmed: true,
			Reason: invocation.Reason, Outcome: "allowed",
		})
	}

	server, err := broker.registry.Server(invocation.Server)
	if err != nil {
		return Outcome{}, err
	}
	if err := session.spend(server); err != nil {
		broker.refused(ctx, invocation, err)
		return Outcome{}, err
	}
	if err := broker.breaker.Allow(server.Slug); err != nil {
		broker.refused(ctx, invocation, err)
		return Outcome{}, err
	}

	client, present := broker.clients[server.Slug]
	if !present {
		return Outcome{}, fmt.Errorf("%w: %s has no client", ErrServerUnknown, server.Slug)
	}

	started := broker.clock()
	result, err := client.CallTool(ctx, invocation.Tool, invocation.Arguments)
	elapsed := broker.clock().Sub(started)
	if err != nil {
		// Only a transport or protocol failure trips the breaker. A tool that
		// ran and refused a bad argument is a working connection.
		broker.breaker.Failed(server.Slug)
		broker.record(ctx, Event{
			At: broker.clock(), Kind: EventFailure, Server: server.Slug, Tool: invocation.Tool,
			Operator: invocation.Operator, Outcome: "failed", Detail: reason(err),
			Duration: elapsed,
		})
		return Outcome{}, err
	}
	broker.breaker.Succeeded(server.Slug)

	capabilities, _ := broker.Capabilities(server.Slug)
	structured := result.Structured
	if output, declared := capabilities.outputs[invocation.Tool]; declared {
		problems := output.Validate(normaliseArguments(structured))
		if len(problems) > 0 {
			// A result that does not match the contract the owner reviewed is
			// refused rather than passed through. This is the case where a
			// server has been replaced, and passing it on would mean the
			// mismatch is discovered by whatever consumes it.
			err := fmt.Errorf("%w: %s", ErrResultInvalid, strings.Join(problems, "; "))
			broker.record(ctx, Event{
				At: broker.clock(), Kind: EventFailure, Server: server.Slug,
				Tool: invocation.Tool, Operator: invocation.Operator,
				Outcome: "refused", Detail: reason(err), Duration: elapsed,
			})
			return Outcome{}, err
		}
	} else {
		// Without a declared output schema there is no contract to check
		// against, so the result stays text and is never handed to application
		// code as values.
		structured = nil
	}

	content := Wrap("mcp:"+server.Slug+"/"+invocation.Tool, result.Text)
	broker.record(ctx, Event{
		At: broker.clock(), Kind: EventToolCall, Server: server.Slug, Tool: invocation.Tool,
		Operator: invocation.Operator, Arguments: invocation.Arguments,
		Confirmed: invocation.Confirmed, Reason: invocation.Reason,
		Outcome: "allowed", Bytes: result.Bytes, Duration: elapsed,
		Findings: content.Findings,
	})

	return Outcome{
		Preview: preview, Content: content, Structured: structured,
		ToolError: result.IsError, Bytes: result.Bytes, Duration: elapsed,
	}, nil
}

// spend charges one call against the request's ceilings.
func (session *Session) spend(server Server) error {
	if session == nil {
		return fmt.Errorf("%w: no session was opened for this request", ErrLimitExceeded)
	}
	session.mutex.Lock()
	defer session.mutex.Unlock()
	if session.calls >= server.MaxCallsPerRequest {
		return fmt.Errorf("%w: %d tool calls is the ceiling for one request",
			ErrLimitExceeded, server.MaxCallsPerRequest)
	}
	if session.depth > server.MaxDepth {
		return fmt.Errorf("%w: tool calls nested more than %d deep",
			ErrLimitExceeded, server.MaxDepth)
	}
	if server.CostLimitMinor > 0 && session.costMinor >= server.CostLimitMinor {
		return fmt.Errorf("%w: the cost ceiling for this request is reached", ErrLimitExceeded)
	}
	session.calls++
	return nil
}

// Enter and Leave bracket a nested tool call, so recursion is bounded by the
// structure of the work rather than by a counter somebody remembers to reset.
func (session *Session) Enter() { session.mutex.Lock(); session.depth++; session.mutex.Unlock() }

// Leave ends a nested tool call.
func (session *Session) Leave() { session.mutex.Lock(); session.depth--; session.mutex.Unlock() }

// Charge records estimated spend against the request's cost ceiling.
func (session *Session) Charge(minor int64) {
	session.mutex.Lock()
	session.costMinor += minor
	session.mutex.Unlock()
}

// Calls reports how many tool calls this request has made.
func (session *Session) Calls() int {
	session.mutex.Lock()
	defer session.mutex.Unlock()
	return session.calls
}

func (broker *Broker) refused(ctx context.Context, invocation Invocation, err error) {
	broker.record(ctx, Event{
		At: broker.clock(), Kind: EventToolCall, Server: invocation.Server,
		Tool: invocation.Tool, Operator: invocation.Operator,
		Confirmed: invocation.Confirmed, Reason: invocation.Reason,
		Outcome: "refused", Detail: reason(err),
	})
}

func (broker *Broker) record(ctx context.Context, event Event) {
	if broker.audit == nil {
		return
	}
	// An audit failure does not fail the call that has already happened, and it
	// does not silently vanish either: the sink is responsible for its own
	// durability, which is why this is an interface and not a log line.
	_ = broker.audit.Record(ctx, event)
}

// reason renders an error for an operator without leaking a wrapped chain.
func reason(err error) string {
	if err == nil {
		return ""
	}
	return clip(err.Error(), 300)
}

// normaliseArguments round-trips a Go map through JSON so the validator sees
// the same types it would see for a decoded request — an int written in Go and
// an integer decoded from JSON are different Go types, and a validator that
// disagreed with itself depending on the caller would be worse than none.
func normaliseArguments(arguments map[string]any) any {
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
