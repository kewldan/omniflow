// Package aicopilot answers operator questions using authorised tools.
//
// It is read-only by default and permission-aware by construction. Those are
// not two features; they are the same one, because the failure they prevent is
// identical: an operator asking a question and getting an answer built from
// records they are not allowed to see, or an action they are not allowed to
// take.
//
// The permission check happens against the asking operator's own grant, before
// a tool runs and again before its result is used. A copilot that ran tools with
// the server's authority would be a privilege-escalation surface with a chat
// box on it — the most convenient one an attacker could ask for.
package aicopilot

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/omniflow/omniflow/internal/aigateway"
)

var (
	// ErrToolUnknown reports a tool the copilot does not have.
	ErrToolUnknown = errors.New("unknown copilot tool")
	// ErrToolForbidden reports a tool this operator may not use. It is
	// deliberately distinct from ErrToolUnknown so the panel can say "you do
	// not have permission" rather than "that does not exist" — an operator
	// hitting a wall deserves to know which wall.
	ErrToolForbidden = errors.New("this account may not use that tool")
	// ErrMutationRefused reports an attempt to change something. The copilot
	// never mutates: it deep-links to the surface that does, where the ordinary
	// preview, permission check, reason, and confirmation apply.
	ErrMutationRefused = errors.New("the copilot cannot change anything")
)

// Tool is one thing the copilot can look up.
//
// Every tool is read-only. There is no write variant and no flag to make one:
// the way to add a mutating capability is to not add it, and to deep-link to
// the panel surface that already does the job with its own confirmation.
type Tool struct {
	Name string
	// Permission is what the asking operator must hold. It is required — a tool
	// with an empty permission would be readable by anyone who can sign in.
	Permission string
	// Describe is what the model is told the tool does. It is a fixed string
	// rather than operator-editable, because a tool description is part of the
	// prompt and an editable one is an injection surface.
	Describe string
	// Run performs the lookup. It returns records the caller renders as
	// citations, so an answer can always be traced to what produced it.
	Run func(context.Context, map[string]string) ([]Record, error)
}

// Record is one cited fact.
//
// Every field exists so an answer can point at something an operator can open.
// A copilot that summarises without citing is one nobody can check, and an
// uncheckable answer about a customer's money is worse than no answer.
type Record struct {
	Kind string
	ID   string
	// Summary is the one-line rendering the model is shown. It must not contain
	// anything the operator's permission does not already entitle them to see.
	Summary string
	// Href is where the operator can open the record. It is what makes a
	// suggested next action a deep link into the normal workflow rather than a
	// shortcut around it.
	Href string
}

// Registry holds the tools an installation exposes.
type Registry struct {
	tools map[string]Tool
}

// NewRegistry builds a registry, refusing a tool with no permission.
func NewRegistry(tools ...Tool) (*Registry, error) {
	registry := &Registry{tools: make(map[string]Tool, len(tools))}
	for _, tool := range tools {
		if strings.TrimSpace(tool.Name) == "" || strings.TrimSpace(tool.Permission) == "" {
			return nil, fmt.Errorf("copilot tool %q needs a name and a permission", tool.Name)
		}
		registry.tools[tool.Name] = tool
	}
	return registry, nil
}

// Available lists the tools this operator may use, sorted.
//
// The list is filtered before the model sees it, so a model cannot ask for a
// tool the operator lacks — the refusal happens at description time rather than
// as an error the model then has to be told about.
func (registry *Registry) Available(grant Grant) []Tool {
	names := make([]string, 0, len(registry.tools))
	for name, tool := range registry.tools {
		if grant.Allows(tool.Permission) {
			names = append(names, name)
		}
	}
	sort.Strings(names)
	available := make([]Tool, 0, len(names))
	for _, name := range names {
		available = append(available, registry.tools[name])
	}
	return available
}

// Grant is what the asking operator is allowed to do.
//
// It is an interface over the panel's RBAC rather than a copy of it, so there
// is one answer to "may they?" and the copilot cannot drift from the routes.
type Grant interface {
	Allows(permission string) bool
}

// Answer is a copilot response.
type Answer struct {
	Text string
	// Citations are the records the answer was built from. An answer with none
	// is still returned — sometimes the honest answer is "I found nothing" —
	// but the panel renders that differently from one with sources.
	Citations []Record
	// ToolsUsed names what ran, so an audit of a copilot session shows which
	// records were read on whose authority.
	ToolsUsed []string
	Generated bool
	Provider  string
	Model     string
}

// Service answers questions.
type Service struct {
	gateway  *aigateway.Gateway
	registry *Registry
}

// New builds the copilot.
func New(gateway *aigateway.Gateway, registry *Registry) *Service {
	return &Service{gateway: gateway, registry: registry}
}

// Available reports whether the copilot can run at all.
func (service *Service) Available() bool {
	return service.gateway != nil && service.registry != nil &&
		service.gateway.Enabled(aigateway.TaskClassify)
}

const copilotSystem = "You help an operator understand their own system. Answer " +
	"only from the records provided. If the records do not contain the answer, " +
	"say so and name what you would need. Never invent an identifier, an amount, " +
	"or a date. Never instruct the operator to run a command or call an API; " +
	"point them at the panel screen instead. You cannot change anything."

// Ask answers one question.
//
// The shape is deliberate: tools run first, against the operator's own grant,
// and the model is shown only what came back. The model never chooses what to
// read — it is given a set of records and asked to explain them. That gives up
// some flexibility and removes the entire class of failure where a model is
// talked into reading something.
func (service *Service) Ask(
	ctx context.Context, grant Grant, question string, requested []ToolCall,
) (Answer, error) {
	if service.gateway == nil || service.registry == nil {
		return Answer{}, aigateway.ErrDisabled
	}

	citations := make([]Record, 0, 16)
	used := make([]string, 0, len(requested))
	for _, call := range requested {
		tool, known := service.registry.tools[call.Tool]
		if !known {
			return Answer{}, ErrToolUnknown
		}
		// Checked against the asking operator's grant rather than the server's
		// authority. A copilot running with the server's authority would be a
		// privilege-escalation surface with a chat box on it.
		if !grant.Allows(tool.Permission) {
			return Answer{}, ErrToolForbidden
		}
		records, err := tool.Run(ctx, call.Arguments)
		if err != nil {
			return Answer{}, err
		}
		citations = append(citations, records...)
		used = append(used, tool.Name)
	}

	parts := make([]string, 0, len(citations)+1)
	// The question is untrusted: an operator can paste a customer's message
	// into it, and that message can contain instructions. It goes through
	// redaction with everything else and is labelled as a question rather than
	// injected as instruction.
	parts = append(parts, "Operator question: "+truncate(question, 1000))
	for _, record := range citations {
		parts = append(parts, fmt.Sprintf("[%s %s] %s", record.Kind, record.ID, record.Summary))
	}

	result, err := service.gateway.Complete(ctx, aigateway.Call{
		Task:   aigateway.TaskClassify,
		System: copilotSystem,
		Instruction: "Answer the operator's question using only the records listed. " +
			"Cite the records you used by their identifier. If they do not answer " +
			"the question, say what is missing.",
		Parts: parts,
	})
	if err != nil {
		return Answer{}, err
	}

	return Answer{
		Text: strings.TrimSpace(result.Text), Citations: citations, ToolsUsed: used,
		Generated: true, Provider: result.Provider, Model: result.Model,
	}, nil
}

// ToolCall is one requested lookup.
type ToolCall struct {
	Tool      string
	Arguments map[string]string
}

// Mutate exists to be refused.
//
// It is here so that a caller looking for a way to make the copilot act finds a
// function that explains why there is not one, rather than concluding the
// capability was forgotten and adding it.
func (service *Service) Mutate(context.Context, Grant, string) error {
	return ErrMutationRefused
}

func truncate(text string, limit int) string {
	trimmed := strings.TrimSpace(text)
	if len(trimmed) <= limit {
		return trimmed
	}
	return trimmed[:limit]
}
