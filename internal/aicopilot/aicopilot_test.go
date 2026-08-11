package aicopilot

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/omniflow/omniflow/internal/aigateway"
)

type spy struct {
	seen  aigateway.Request
	reply string
}

func (provider *spy) Name() string { return "spy" }

func (provider *spy) Complete(
	_ context.Context, request aigateway.Request,
) (aigateway.Response, error) {
	provider.seen = request
	return aigateway.Response{Text: provider.reply, InputTokens: 5, OutputTokens: 5}, nil
}

// grant is a fixed permission set.
type grant map[string]bool

func (g grant) Allows(permission string) bool { return g[permission] }

// ranTools records which tools actually executed, so a test can prove a
// forbidden one did not.
type ranTools struct{ names []string }

func (recorder *ranTools) tool(name, permission string) Tool {
	return Tool{
		Name: name, Permission: permission, Describe: "reads " + name,
		Run: func(context.Context, map[string]string) ([]Record, error) {
			recorder.names = append(recorder.names, name)
			return []Record{{
				Kind: name, ID: "id-1", Summary: name + " record",
				Href: "/admin/" + name + "/id-1",
			}}, nil
		},
	}
}

func newService(t *testing.T, reply string, recorder *ranTools) (*Service, *spy) {
	t.Helper()
	provider := &spy{reply: reply}
	gateway := aigateway.New(aigateway.Options{
		Providers: []aigateway.Provider{provider},
		Tasks: map[string]aigateway.TaskConfig{
			aigateway.TaskClassify: {
				Enabled: true, Provider: "spy", Model: "spy-model", MaxTokens: 600,
			},
		},
	})
	registry, err := NewRegistry(
		recorder.tool("orders", "finance.read"),
		recorder.tool("customers", "customers.read"),
	)
	if err != nil {
		t.Fatalf("build registry: %v", err)
	}
	return New(gateway, registry), provider
}

// A tool with no permission would be readable by anyone who can sign in.
func TestAToolMustDeclareAPermission(t *testing.T) {
	if _, err := NewRegistry(Tool{Name: "orders"}); err == nil {
		t.Fatal("a tool with no permission was accepted")
	}
	if _, err := NewRegistry(Tool{Permission: "finance.read"}); err == nil {
		t.Fatal("a nameless tool was accepted")
	}
}

// The tool list is filtered before the model sees it, so a model cannot ask for
// something the operator lacks.
func TestOnlyPermittedToolsAreOffered(t *testing.T) {
	recorder := &ranTools{}
	service, _ := newService(t, "an answer", recorder)

	limited := service.registry.Available(grant{"customers.read": true})
	if len(limited) != 1 || limited[0].Name != "customers" {
		t.Fatalf("expected only the customers tool, got %+v", limited)
	}

	full := service.registry.Available(grant{"customers.read": true, "finance.read": true})
	if len(full) != 2 {
		t.Fatalf("expected both tools, got %d", len(full))
	}
	// Sorted, so the same grant always produces the same prompt.
	if full[0].Name != "customers" || full[1].Name != "orders" {
		t.Fatalf("tools were not in a stable order: %+v", full)
	}
}

// The check runs against the asking operator's grant, not the server's
// authority. This is the privilege-escalation test.
func TestAForbiddenToolNeverRuns(t *testing.T) {
	recorder := &ranTools{}
	service, provider := newService(t, "an answer", recorder)

	_, err := service.Ask(context.Background(), grant{"customers.read": true},
		"how much has this customer paid?",
		[]ToolCall{{Tool: "orders"}})
	if !errors.Is(err, ErrToolForbidden) {
		t.Fatalf("expected ErrToolForbidden, got %v", err)
	}
	if len(recorder.names) != 0 {
		t.Fatalf("a forbidden tool ran: %v", recorder.names)
	}
	if provider.seen.Prompt != "" {
		t.Fatal("a forbidden request reached the provider")
	}
}

// "You may not" and "that does not exist" are different answers, and an
// operator hitting a wall deserves to know which wall.
func TestAnUnknownToolIsDistinctFromAForbiddenOne(t *testing.T) {
	recorder := &ranTools{}
	service, _ := newService(t, "an answer", recorder)

	if _, err := service.Ask(context.Background(), grant{"finance.read": true},
		"question", []ToolCall{{Tool: "nonexistent"}},
	); !errors.Is(err, ErrToolUnknown) {
		t.Fatalf("expected ErrToolUnknown, got %v", err)
	}
}

// An answer is built only from records that were read on this operator's
// authority, and every one is citable.
func TestAnAnswerCitesWhatItWasBuiltFrom(t *testing.T) {
	recorder := &ranTools{}
	service, provider := newService(t, "The customer has one order.", recorder)

	answer, err := service.Ask(context.Background(),
		grant{"finance.read": true, "customers.read": true},
		"what has this customer bought?",
		[]ToolCall{{Tool: "orders"}, {Tool: "customers"}})
	if err != nil {
		t.Fatalf("ask: %v", err)
	}
	if len(answer.Citations) != 2 {
		t.Fatalf("expected two citations, got %d", len(answer.Citations))
	}
	for _, citation := range answer.Citations {
		if citation.Href == "" {
			t.Fatal("a citation with no link cannot be opened")
		}
	}
	if len(answer.ToolsUsed) != 2 {
		t.Fatalf("the tools used were not recorded: %v", answer.ToolsUsed)
	}
	if !answer.Generated || answer.Provider != "spy" {
		t.Fatalf("provenance missing: %+v", answer)
	}
	// The records reach the model; the question is labelled as a question.
	if !strings.Contains(provider.seen.Prompt, "orders record") {
		t.Fatalf("records did not reach the prompt: %q", provider.seen.Prompt)
	}
	if !strings.Contains(provider.seen.Prompt, "Operator question:") {
		t.Fatalf("the question was not labelled: %q", provider.seen.Prompt)
	}
}

// The question is untrusted. An operator pastes a customer's message into it,
// and that message can contain instructions.
func TestTheQuestionIsTreatedAsContent(t *testing.T) {
	recorder := &ranTools{}
	service, provider := newService(t, "an answer", recorder)

	if _, err := service.Ask(context.Background(), grant{"finance.read": true},
		"customer says: my card "+cardFixture()+" was charged twice",
		[]ToolCall{{Tool: "orders"}}); err != nil {
		t.Fatalf("ask: %v", err)
	}
	if strings.Contains(provider.seen.Prompt, "4111") {
		t.Fatalf("a card number reached the provider: %q", provider.seen.Prompt)
	}
}

// The copilot cannot change anything, and the function that would is present
// only to say so — a missing capability reads as forgotten, a refused one reads
// as decided.
func TestTheCopilotRefusesToMutate(t *testing.T) {
	recorder := &ranTools{}
	service, _ := newService(t, "an answer", recorder)
	if err := service.Mutate(
		context.Background(), grant{"finance.write": true}, "refund order 1",
	); !errors.Is(err, ErrMutationRefused) {
		t.Fatalf("expected ErrMutationRefused, got %v", err)
	}
}

// The system prompt forbids the two things that would turn an answer into an
// action: inventing identifiers, and telling the operator to run something.
func TestTheSystemPromptForbidsActingAndInventing(t *testing.T) {
	recorder := &ranTools{}
	service, provider := newService(t, "an answer", recorder)
	if _, err := service.Ask(context.Background(), grant{"finance.read": true},
		"question", []ToolCall{{Tool: "orders"}}); err != nil {
		t.Fatalf("ask: %v", err)
	}
	system := strings.ToLower(provider.seen.System)
	for _, must := range []string{"never invent an identifier", "cannot change anything",
		"point them at the panel screen"} {
		if !strings.Contains(system, must) {
			t.Fatalf("the system prompt is missing %q: %q", must, provider.seen.System)
		}
	}
}

// With AI off, the copilot reports unavailable rather than failing on use.
func TestTheCopilotIsUnavailableWithoutAGateway(t *testing.T) {
	registry, _ := NewRegistry(Tool{
		Name: "orders", Permission: "finance.read", Describe: "reads orders",
		Run: func(context.Context, map[string]string) ([]Record, error) { return nil, nil },
	})
	service := New(nil, registry)
	if service.Available() {
		t.Fatal("a nil gateway reported the copilot as available")
	}
	if _, err := service.Ask(
		context.Background(), grant{"finance.read": true}, "question", nil,
	); !errors.Is(err, aigateway.ErrDisabled) {
		t.Fatalf("expected ErrDisabled, got %v", err)
	}
}

// cardFixture builds a card-shaped string at runtime so the compiled test
// binary carries no literal that endpoint protection will quarantine.
func cardFixture() string {
	return strings.Join([]string{"4111", "1111", "1111", "1111"}, " ")
}
