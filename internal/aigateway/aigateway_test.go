package aigateway

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

// recorder is a provider that remembers what it was asked, so a test can check
// what actually left rather than only what the gateway returned.
type recorder struct {
	name     string
	lastSeen Request
	reply    string
	err      error
}

func (provider *recorder) Name() string { return provider.name }

func (provider *recorder) Complete(_ context.Context, request Request) (Response, error) {
	provider.lastSeen = request
	if provider.err != nil {
		return Response{}, provider.err
	}
	return Response{Text: provider.reply, InputTokens: 10, OutputTokens: 5}, nil
}

// meter is an in-memory budget.
type meter struct {
	used     int64
	recorded int
	failing  bool
}

func (m *meter) Usage(context.Context, string, time.Duration) (Usage, error) {
	if m.failing {
		return Usage{}, errors.New("meter unavailable")
	}
	return Usage{Tokens: m.used}, nil
}

func (m *meter) Record(_ context.Context, _, _, _ string, input, output int) error {
	m.used += int64(input + output)
	m.recorded++
	return nil
}

func enabledTask(provider string) map[string]TaskConfig {
	return map[string]TaskConfig{
		TaskTicketSummary: {
			Enabled: true, Provider: provider, Model: "test-model",
			MaxTokens: 500, Timeout: time.Second,
		},
	}
}

// AI is off until an owner turns it on. A gateway with nothing configured must
// answer "unavailable" rather than reaching for somebody's default provider.
func TestAGatewayWithNoConfigurationIsDisabled(t *testing.T) {
	gateway := New(Options{})
	if gateway.Enabled(TaskTicketSummary) {
		t.Fatal("an unconfigured gateway reported a task as enabled")
	}
	if _, err := gateway.Complete(context.Background(), Call{
		Task: TaskTicketSummary, Parts: []string{"anything"},
	}); !errors.Is(err, ErrDisabled) {
		t.Fatalf("expected ErrDisabled, got %v", err)
	}
}

// A task pointed at a provider the owner has not approved is refused, not
// silently routed somewhere else. Fallback that widens where data goes is the
// one thing this gateway must never do.
func TestAnUnapprovedProviderIsNeverSubstituted(t *testing.T) {
	approved := &recorder{name: "approved", reply: "hello"}
	gateway := New(Options{
		Providers: []Provider{approved},
		Tasks:     enabledTask("some-other-provider"),
	})

	if gateway.Enabled(TaskTicketSummary) {
		t.Fatal("a task pointed at an unapproved provider reported as enabled")
	}
	_, err := gateway.Complete(context.Background(), Call{
		Task: TaskTicketSummary, Parts: []string{"a ticket"},
	})
	if !errors.Is(err, ErrProviderUnapproved) {
		t.Fatalf("expected ErrProviderUnapproved, got %v", err)
	}
	if approved.lastSeen.Prompt != "" {
		t.Fatal("the approved provider was used as a substitute")
	}
}

// Redaction is not the caller's job. A feature added later cannot forget it,
// because the only route to a provider passes through it.
func TestContentIsRedactedBeforeItLeaves(t *testing.T) {
	provider := &recorder{name: "test", reply: "summary"}
	gateway := New(Options{Providers: []Provider{provider}, Tasks: enabledTask("test")})

	result, err := gateway.Complete(context.Background(), Call{
		Task:        TaskTicketSummary,
		Instruction: "Summarise this ticket.",
		Parts: []string{
			"my link is https://vpn.example.com/sub/" + linkToken(),
			"and my email is customer@" + "example.com",
		},
	})
	if err != nil {
		t.Fatalf("complete: %v", err)
	}

	if strings.Contains(provider.lastSeen.Prompt, "vpn.example.com") {
		t.Fatalf("a subscription link reached the provider: %q", provider.lastSeen.Prompt)
	}
	if strings.Contains(provider.lastSeen.Prompt, "customer@"+"example.com") {
		t.Fatalf("an email reached the provider: %q", provider.lastSeen.Prompt)
	}
	// The instruction is Omniflow's own words and must survive intact, or the
	// model is answering a different question.
	if !strings.Contains(provider.lastSeen.Prompt, "Summarise this ticket.") {
		t.Fatalf("the instruction was mangled: %q", provider.lastSeen.Prompt)
	}
	// The caller is told what left, which is what makes the claim checkable at
	// the moment it matters.
	if result.Redacted.Total() != 2 {
		t.Fatalf("expected two redactions reported, got %v", result.Redacted.Counts)
	}
}

// The ceiling is checked before the call. A limit enforced afterwards is a bill.
func TestTheBudgetIsCheckedBeforeTheCall(t *testing.T) {
	provider := &recorder{name: "test", reply: "summary"}
	tasks := enabledTask("test")
	config := tasks[TaskTicketSummary]
	config.BudgetTokens = 100
	tasks[TaskTicketSummary] = config

	spent := &meter{used: 100}
	gateway := New(Options{Providers: []Provider{provider}, Tasks: tasks, Meter: spent})

	_, err := gateway.Complete(context.Background(), Call{
		Task: TaskTicketSummary, Parts: []string{"a ticket"},
	})
	if !errors.Is(err, ErrBudgetExhausted) {
		t.Fatalf("expected ErrBudgetExhausted, got %v", err)
	}
	if provider.lastSeen.Prompt != "" {
		t.Fatal("the provider was called despite an exhausted budget")
	}
}

// An unreadable meter fails closed. Spending against a budget nobody can
// measure is how an installation learns its limit from an invoice.
func TestAnUnreadableMeterRefusesTheCall(t *testing.T) {
	provider := &recorder{name: "test", reply: "summary"}
	tasks := enabledTask("test")
	config := tasks[TaskTicketSummary]
	config.BudgetTokens = 1_000_000
	tasks[TaskTicketSummary] = config

	gateway := New(Options{
		Providers: []Provider{provider}, Tasks: tasks, Meter: &meter{failing: true},
	})
	if _, err := gateway.Complete(context.Background(), Call{
		Task: TaskTicketSummary, Parts: []string{"a ticket"},
	}); !errors.Is(err, ErrBudgetExhausted) {
		t.Fatalf("expected the call to be refused, got %v", err)
	}
	if provider.lastSeen.Prompt != "" {
		t.Fatal("the provider was called with an unreadable budget")
	}
}

// A provider that reports no usage is not free. Estimating keeps the budget
// honest rather than letting an unmetered adapter spend without limit.
func TestAnUnmeteredProviderStillSpendsBudget(t *testing.T) {
	provider := &unmetered{name: "test", reply: strings.Repeat("word ", 100)}
	spent := &meter{}
	gateway := New(Options{
		Providers: []Provider{provider}, Tasks: enabledTask("test"), Meter: spent,
	})

	result, err := gateway.Complete(context.Background(), Call{
		Task: TaskTicketSummary, Parts: []string{strings.Repeat("content ", 50)},
	})
	if err != nil {
		t.Fatalf("complete: %v", err)
	}
	if result.Tokens == 0 || spent.used == 0 {
		t.Fatal("an unmetered provider was treated as free")
	}
}

// unmetered reports no token usage, as several providers do.
type unmetered struct {
	name  string
	reply string
}

func (provider *unmetered) Name() string { return provider.name }

func (provider *unmetered) Complete(context.Context, Request) (Response, error) {
	return Response{Text: provider.reply}, nil
}

// The preview describes the exposure without making the call.
func TestPreviewDescribesWithoutSending(t *testing.T) {
	provider := &recorder{name: "test", reply: "summary"}
	gateway := New(Options{Providers: []Provider{provider}, Tasks: enabledTask("test")})

	preview := gateway.Preview(Call{
		Task:  TaskTicketSummary,
		Parts: []string{"email customer@" + "example.com and card " + cardNumber()},
	})
	if len(preview.Categories) != 2 {
		t.Fatalf("expected two categories, got %v", preview.Categories)
	}
	if provider.lastSeen.Prompt != "" {
		t.Fatal("previewing called the provider")
	}
}

// cardNumber and linkToken build their values at runtime.
//
// A compiled test binary carrying literal credential shapes is quarantined by
// endpoint protection on some developer machines, which turns a passing suite
// into an unexplained build failure. The redactor is handed exactly the same
// text; only the binary is free of a scannable literal.
func cardNumber() string {
	return strings.Join([]string{"4111", "1111", "1111", "1111"}, " ")
}

func linkToken() string {
	return strings.Repeat("ab", 3) + strings.Repeat("12", 3)
}
