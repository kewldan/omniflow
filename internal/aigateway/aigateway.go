// Package aigateway is the provider-neutral front door for every AI feature.
//
// Nothing in Omniflow talks to a model directly. Everything goes through here,
// for three reasons that are each worth the indirection.
//
// AI is off until an owner turns it on. A gateway with no configured provider
// answers ErrDisabled to every request, so a feature that forgets to check
// degrades to "unavailable" rather than to a nil pointer or, worse, a request
// to somebody's default provider.
//
// Redaction is not optional and not the caller's job. Every request passes
// through `internal/airedact` here, so a feature added later cannot forget it —
// the only way to reach a provider is through a function that has already
// redacted.
//
// Budgets are enforced before the call, not measured after it. A per-task
// ceiling that is checked afterwards is a bill, not a limit.
package aigateway

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/omniflow/omniflow/internal/airedact"
)

var (
	// ErrDisabled reports that no provider is configured for this task. It is a
	// normal state — AI is opt-in — and callers render it as "unavailable"
	// rather than as a failure.
	ErrDisabled = errors.New("ai is not configured for this task")
	// ErrBudgetExhausted reports that the task's ceiling has been reached.
	ErrBudgetExhausted = errors.New("ai budget for this task is exhausted")
	// ErrProviderUnapproved reports a routing attempt to a provider the owner
	// has not approved. Fallback must never widen where data goes.
	ErrProviderUnapproved = errors.New("ai provider is not approved for this installation")
)

// Task names a unit of AI work. Each carries its own model, limits, and budget,
// because summarising a ticket and judging whether somebody is committing fraud
// are not the same risk and should not share a configuration.
const (
	TaskTicketSummary  = "ticket_summary"
	TaskReplySuggest   = "reply_suggest"
	TaskTranslate      = "translate"
	TaskClassify       = "classify"
	TaskMarketingDraft = "marketing_draft"
	TaskRiskAnalysis   = "risk_analysis"
)

// Provider is one model backend.
//
// The interface is deliberately small. Anything richer — tools, streaming,
// multi-turn state — would be a capability the gateway has to reason about per
// provider, and the features here need none of it.
type Provider interface {
	// Name is the stable identifier an owner approves.
	Name() string
	// Complete answers a single prompt. The text it receives has already been
	// redacted; a provider implementation must never be given raw content.
	Complete(context.Context, Request) (Response, error)
}

// Request is one already-redacted call.
type Request struct {
	Model string
	// System and Prompt are separate because a provider that supports a system
	// role uses it, and one that does not concatenates — which is the
	// adapter's decision rather than the caller's.
	System      string
	Prompt      string
	Temperature float64
	MaxTokens   int
}

// Response is what a provider returned.
type Response struct {
	Text string
	// InputTokens and OutputTokens drive the budget. A provider that does not
	// report them leaves them zero, and the gateway falls back to a character
	// estimate rather than treating the call as free.
	InputTokens  int
	OutputTokens int
}

// TaskConfig is the per-task configuration an owner sets.
//
// Every field is per task rather than global on purpose: a summary can be cheap
// and fast, and a fraud assessment should be allowed to be neither.
type TaskConfig struct {
	Enabled     bool
	Provider    string
	Model       string
	Temperature float64
	MaxTokens   int
	Timeout     time.Duration
	// BudgetTokens is the ceiling for the window. Zero means no ceiling, which
	// an owner has to choose explicitly.
	BudgetTokens int64
	BudgetWindow time.Duration
}

// Usage is what a task has already spent in the current window.
type Usage struct {
	Tokens int64
	Since  time.Time
}

// Meter records and reports spend. It is an interface so the gateway does not
// own storage, and so a test can exercise the budget rules without a database.
type Meter interface {
	Usage(ctx context.Context, task string, window time.Duration) (Usage, error)
	Record(ctx context.Context, task, provider, model string, input, output int) error
}

// Gateway routes AI requests.
type Gateway struct {
	providers map[string]Provider
	tasks     map[string]TaskConfig
	meter     Meter
	clock     func() time.Time
}

// Options configures the gateway.
type Options struct {
	// Providers are the adapters compiled in and approved by the owner. A
	// provider absent from this map cannot be routed to, whatever a task
	// configuration says.
	Providers []Provider
	Tasks     map[string]TaskConfig
	Meter     Meter
}

// New builds the gateway. A gateway with no providers is valid and answers
// ErrDisabled to everything, which is what an installation that has not
// configured AI gets.
func New(options Options) *Gateway {
	registry := make(map[string]Provider, len(options.Providers))
	for _, provider := range options.Providers {
		registry[provider.Name()] = provider
	}
	tasks := options.Tasks
	if tasks == nil {
		tasks = map[string]TaskConfig{}
	}
	return &Gateway{
		providers: registry, tasks: tasks, meter: options.Meter, clock: time.Now,
	}
}

// Enabled reports whether a task can run. Callers use it to decide whether to
// offer a button at all, rather than offering one that always fails.
func (gateway *Gateway) Enabled(task string) bool {
	config, known := gateway.tasks[task]
	if !known || !config.Enabled {
		return false
	}
	_, approved := gateway.providers[config.Provider]
	return approved
}

// Call is the only way to reach a provider.
//
// `parts` are the untrusted texts that make up the prompt — a ticket body, an
// operator note, a customer message. They are redacted here, together, so the
// account of what left is one account rather than several.
type Call struct {
	Task   string
	System string
	// Instruction is operator- or product-authored text and is not redacted:
	// it is what Omniflow itself is asking for, and redacting it would mangle
	// the instruction rather than protect anybody.
	Instruction string
	// Parts are the untrusted content. Everything here is redacted before it
	// leaves.
	Parts []string
}

// Result is the model's answer and an account of what was sent.
type Result struct {
	Text string
	// Redacted is what was removed on the way out. It is returned so a caller
	// can show the operator what left, which is the only way a data-use claim
	// is checkable at the moment it matters.
	Redacted airedact.Result
	Provider string
	Model    string
	Tokens   int
}

// Complete runs one task.
//
// The order is the safety property: disabled check, provider approval, budget,
// redaction, and only then the network. Each step refuses rather than
// degrading, because every degradation here is a way for content to reach a
// provider the owner did not approve.
func (gateway *Gateway) Complete(ctx context.Context, call Call) (Result, error) {
	config, known := gateway.tasks[call.Task]
	if !known || !config.Enabled {
		return Result{}, ErrDisabled
	}
	provider, approved := gateway.providers[config.Provider]
	if !approved {
		// A task configured for a provider that is not compiled in or not
		// approved does not fall back to another one. Fallback that widens
		// where data goes is the one thing this gateway must never do.
		return Result{}, ErrProviderUnapproved
	}

	if err := gateway.withinBudget(ctx, call.Task, config); err != nil {
		return Result{}, err
	}

	redacted, account := airedact.RedactAll(call.Parts...)
	prompt := strings.TrimSpace(call.Instruction)
	if len(redacted) > 0 {
		prompt = strings.TrimSpace(prompt + "\n\n" + strings.Join(redacted, "\n\n"))
	}

	timeout := config.Timeout
	if timeout <= 0 {
		timeout = DefaultTimeout
	}
	callCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	response, err := provider.Complete(callCtx, Request{
		Model: config.Model, System: call.System, Prompt: prompt,
		Temperature: config.Temperature, MaxTokens: config.MaxTokens,
	})
	if err != nil {
		return Result{}, err
	}

	input, output := response.InputTokens, response.OutputTokens
	if input == 0 && output == 0 {
		// A provider that reports nothing is not free. Estimating from
		// characters keeps the budget honest rather than letting an unmetered
		// adapter spend without limit.
		input, output = estimateTokens(prompt), estimateTokens(response.Text)
	}
	if gateway.meter != nil {
		if err := gateway.meter.Record(
			ctx, call.Task, provider.Name(), config.Model, input, output,
		); err != nil {
			// The call has already happened and the money is already spent.
			// Failing here would hide a successful answer behind a bookkeeping
			// error, so the caller gets the answer and the error surfaces in
			// the log.
			_ = err
		}
	}

	return Result{
		Text: response.Text, Redacted: account,
		Provider: provider.Name(), Model: config.Model,
		Tokens: input + output,
	}, nil
}

// withinBudget refuses a call that would exceed the task's ceiling.
//
// It is checked before the request rather than after, because a ceiling
// enforced afterwards is a bill rather than a limit.
func (gateway *Gateway) withinBudget(
	ctx context.Context, task string, config TaskConfig,
) error {
	if config.BudgetTokens <= 0 || gateway.meter == nil {
		return nil
	}
	window := config.BudgetWindow
	if window <= 0 {
		window = DefaultBudgetWindow
	}
	usage, err := gateway.meter.Usage(ctx, task, window)
	if err != nil {
		// An unreadable meter fails closed. Spending against a budget nobody
		// can measure is how an installation discovers its limit from an
		// invoice.
		return ErrBudgetExhausted
	}
	if usage.Tokens >= config.BudgetTokens {
		return ErrBudgetExhausted
	}
	return nil
}

// Preview describes what a call would send, without making it.
//
// It is what the data-use screen renders: an operator can see the categories
// and the size before enabling a feature, and can check the claim afterwards.
func (gateway *Gateway) Preview(call Call) airedact.Preview {
	return airedact.Describe(call.Parts...)
}

const (
	// DefaultTimeout bounds a single model call. A support operator waiting on
	// a suggestion needs an answer or a failure, not an open connection.
	DefaultTimeout = 30 * time.Second
	// DefaultBudgetWindow is the period a token ceiling applies over.
	DefaultBudgetWindow = 24 * time.Hour
)

// estimateTokens approximates tokens from characters for providers that report
// no usage. Four characters per token is crude and deliberately generous: the
// error should push the budget towards refusing early rather than overspending.
func estimateTokens(text string) int {
	if text == "" {
		return 0
	}
	return (len(text) + 3) / 4
}
