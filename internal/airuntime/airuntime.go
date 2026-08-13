// Package airuntime turns AI configuration into something that can make a call.
//
// Everything an owner sets in the panel — an approved provider, a sealed key, a
// per-feature model, a temperature, a budget — is stored by `internal/panelpg`,
// and every AI feature package takes an `*aigateway.Gateway`. Nothing joined the
// two. That gap is why an installation could configure AI completely and
// correctly and never make a single request: the settings were real, the
// features were real, and no code path led from one to the other.
//
// It resolves on every call rather than caching, for the same reason
// `internal/goodsdelivery` does. A key an owner rotates in the panel takes
// effect on the next click instead of at the next restart, and the cost is two
// indexed reads on a path that is about to make a network request anyway.
package airuntime

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/omniflow/omniflow/internal/aigateway"
	"github.com/omniflow/omniflow/internal/aigovernance"
	"github.com/omniflow/omniflow/internal/panelpg"
)

// featureTask maps a feature onto the gateway task it runs as.
//
// The feature names come from `internal/aigovernance` rather than being
// restated here. They are the same nine names the migration seeds and the
// settings screen renders, and a second list of them would be a second thing to
// keep in step with the database's CHECK constraint.
//
// It is not a bijection, and that is the reason a gateway is built per feature
// rather than one gateway holding every task at once. Two features — drafting a
// reply and rewriting one — are both `reply_suggest`, so a single task-keyed map
// could hold only one of their two configurations, and an owner who pointed the
// two at different models would silently get one of them for both.
//
// `mcp_tools` is deliberately absent. It is a feature an owner enables, but it
// gates tool calls rather than naming a completion task, so it has no entry here
// and reaching for one is a bug rather than a missing line.
var featureTask = map[string]string{
	aigovernance.FeatureSupportSummary:   aigateway.TaskTicketSummary,
	aigovernance.FeatureSupportReply:     aigateway.TaskReplySuggest,
	aigovernance.FeatureSupportRewrite:   aigateway.TaskReplySuggest,
	aigovernance.FeatureSupportTranslate: aigateway.TaskTranslate,
	aigovernance.FeatureSupportClassify:  aigateway.TaskClassify,
	aigovernance.FeatureMarketingDraft:   aigateway.TaskMarketingDraft,
	aigovernance.FeatureRiskAnalysis:     aigateway.TaskRiskAnalysis,
	aigovernance.FeatureCopilot:          aigateway.TaskClassify,
}

// Reasons a feature cannot run right now. Each is a configuration state rather
// than a failure, and the panel renders each differently: an owner whose key is
// missing needs a different sentence from one whose provider was never approved.
var (
	// ErrFeatureUnknown reports a feature name with no task behind it. It is a
	// programming error rather than a configuration one.
	ErrFeatureUnknown = errors.New("ai feature is not a completion task")
	// ErrProviderUnconfigured reports an approved provider with no stored key.
	ErrProviderUnconfigured = errors.New("ai provider has no credential")
	// ErrProviderKindUnknown reports a stored kind this build cannot construct.
	ErrProviderKindUnknown = errors.New("ai provider kind is not implemented")
	// ErrModelUnknown reports a probe with no model to test against.
	ErrModelUnknown = errors.New("no model is configured for this provider")
)

// Store is the configuration this package reads.
//
// It is an interface so gateway construction can be exercised without a
// database, and it is narrow so that widening it is a visible decision.
// `*panelpg.Service` satisfies it.
type Store interface {
	AIFeatures(ctx context.Context) ([]panelpg.AIFeature, error)
	AIProviders(ctx context.Context) ([]panelpg.AIProvider, error)
	AIProviderCredential(ctx context.Context, slug string) (kind, baseURL, key string, err error)
	Spent(ctx context.Context, scope, ref, feature string, window time.Duration) (aigovernance.Spend, error)
}

// Binding is what a built gateway resolved to.
//
// The caller needs every field to record the usage event afterwards, and asking
// it to look the provider and model up a second time would be a second chance to
// record something the call did not actually use.
type Binding struct {
	Feature  string
	Task     string
	Provider string
	Model    string
}

// Runtime builds gateways from stored configuration.
type Runtime struct {
	store Store
	// adapt constructs a provider. It is a field rather than a function call so
	// a test can exercise routing, budgets, and recording without a model
	// endpoint, and so no test needs a real key to prove the wiring.
	adapt func(kind, name, baseURL, key string) (aigateway.Provider, error)
}

// New builds the runtime.
func New(store Store) *Runtime {
	return &Runtime{store: store, adapt: adapterFor}
}

// WithAdapter replaces the provider constructor. It exists for tests.
func (runtime *Runtime) WithAdapter(
	adapt func(kind, name, baseURL, key string) (aigateway.Provider, error),
) *Runtime {
	return &Runtime{store: runtime.store, adapt: adapt}
}

// Gateway builds the gateway for one feature.
//
// It answers `aigateway.ErrDisabled` for a feature an owner has not switched on
// and `aigateway.ErrProviderUnapproved` for one pointed at a provider that is
// missing or switched off, which are the same two sentinels a caller would get
// from the gateway itself. Callers therefore compare against one vocabulary
// rather than two, and a feature that forgets to check degrades to "unavailable"
// instead of to a nil pointer.
func (runtime *Runtime) Gateway(
	ctx context.Context, feature string,
) (*aigateway.Gateway, Binding, error) {
	task, known := featureTask[feature]
	if !known {
		return nil, Binding{}, fmt.Errorf("%w: %s", ErrFeatureUnknown, feature)
	}

	configured, err := runtime.feature(ctx, feature)
	if err != nil {
		return nil, Binding{}, err
	}
	if !configured.Enabled || configured.Provider == "" || configured.Model == "" {
		return nil, Binding{}, aigateway.ErrDisabled
	}

	provider, err := runtime.provider(ctx, configured.Provider)
	if err != nil {
		return nil, Binding{}, err
	}

	binding := Binding{
		Feature: feature, Task: task,
		Provider: configured.Provider, Model: configured.Model,
	}
	gateway := aigateway.New(aigateway.Options{
		Providers: []aigateway.Provider{provider},
		Tasks:     map[string]aigateway.TaskConfig{task: taskConfig(configured)},
		Meter:     budgetMeter{store: runtime.store, feature: feature},
	})
	return gateway, binding, nil
}

// Available reports whether a feature would run, without building anything that
// could make a call.
//
// The panel uses it to omit a button rather than offer one that always fails.
// It deliberately does not open the credential: "is this on?" is a question a
// screen asks on every render, and unsealing a key to answer it would put a
// decryption on a path that never sends anything.
func (runtime *Runtime) Available(ctx context.Context, feature string) bool {
	if _, known := featureTask[feature]; !known {
		return false
	}
	configured, err := runtime.feature(ctx, feature)
	if err != nil || !configured.Enabled || configured.Provider == "" || configured.Model == "" {
		return false
	}
	providers, err := runtime.store.AIProviders(ctx)
	if err != nil {
		return false
	}
	for _, provider := range providers {
		if provider.Slug == configured.Provider {
			return provider.Enabled && provider.KeyConfigured
		}
	}
	return false
}

// Probe makes the smallest real request a provider will answer.
//
// It is a real completion rather than a reachability check because the two fail
// differently and only the first one answers the operator's question: an address
// that resolves and a key that is accepted are separate facts, and a probe that
// only proved the first would report a revoked key as healthy.
//
// The prompt is Omniflow's own text, so nothing customer-shaped leaves in it.
func (runtime *Runtime) Probe(
	ctx context.Context, slug, model string,
) (detail string, err error) {
	if strings.TrimSpace(model) == "" {
		model, err = runtime.modelFor(ctx, slug)
		if err != nil {
			return "", err
		}
	}
	// A probe deliberately does not require the provider to be enabled. Pasting
	// a key, testing it, and only then switching the provider on is the order an
	// owner actually works in, and a test that refused until the thing was
	// already live would be a test nobody could use.
	provider, err := runtime.adapter(ctx, slug)
	if err != nil {
		return "", err
	}

	probeCtx, cancel := context.WithTimeout(ctx, probeTimeout)
	defer cancel()

	started := time.Now()
	response, err := provider.Complete(probeCtx, aigateway.Request{
		Model:  model,
		System: "You are a connection test. Answer with one word.",
		Prompt: "Reply with the single word OK.",
		// Small on purpose. A connection test that can spend a paragraph of
		// output is a connection test somebody will be billed for.
		MaxTokens: 16,
	})
	if err != nil {
		return "", err
	}
	answer := strings.TrimSpace(response.Text)
	if answer == "" {
		return "", fmt.Errorf("%w: the provider answered with no text", aigateway.ErrProviderRejected)
	}
	return fmt.Sprintf("%s answered in %d ms", model, time.Since(started).Milliseconds()), nil
}

// probeTimeout bounds a connection test. It is shorter than a feature call's
// default: an operator pressing "test" is waiting at the screen, and a provider
// that needs thirty seconds to say OK has failed the test in every sense that
// matters.
const probeTimeout = 15 * time.Second

// modelFor picks the model to probe with when the operator named none.
//
// A provider row carries no model — models belong to features — so the answer is
// whichever feature already points at this provider. An owner testing a provider
// nothing uses yet has to name a model, which is the honest outcome: there is
// nothing to infer.
func (runtime *Runtime) modelFor(ctx context.Context, slug string) (string, error) {
	features, err := runtime.store.AIFeatures(ctx)
	if err != nil {
		return "", err
	}
	for _, feature := range features {
		if feature.Provider == slug && strings.TrimSpace(feature.Model) != "" {
			return feature.Model, nil
		}
	}
	return "", fmt.Errorf("%w: %s", ErrModelUnknown, slug)
}

// feature reads one feature's configuration.
func (runtime *Runtime) feature(ctx context.Context, name string) (panelpg.AIFeature, error) {
	features, err := runtime.store.AIFeatures(ctx)
	if err != nil {
		return panelpg.AIFeature{}, err
	}
	for _, feature := range features {
		if feature.Feature == name {
			return feature, nil
		}
	}
	// A feature with no row is off rather than missing, so a caller never has to
	// distinguish the two. It matches how aigovernance.Policy answers.
	return panelpg.AIFeature{Feature: name}, nil
}

// provider resolves a slug an owner has approved.
func (runtime *Runtime) provider(ctx context.Context, slug string) (aigateway.Provider, error) {
	approved, err := runtime.store.AIProviders(ctx)
	if err != nil {
		return nil, err
	}
	enabled := false
	for _, provider := range approved {
		if provider.Slug == slug {
			enabled = provider.Enabled
			break
		}
	}
	if !enabled {
		// A feature pointed at a provider that is missing or switched off does
		// not fall back to another one. Fallback that widens where data goes is
		// the one thing this must never do, and it is refused here as well as in
		// the gateway so that neither is the only place the rule lives.
		return nil, aigateway.ErrProviderUnapproved
	}
	return runtime.adapter(ctx, slug)
}

// adapter opens a slug's credential and constructs its adapter, without asking
// whether the owner has switched the provider on. Only the probe uses it
// directly; everything that can send customer content goes through provider.
func (runtime *Runtime) adapter(ctx context.Context, slug string) (aigateway.Provider, error) {
	kind, baseURL, key, err := runtime.store.AIProviderCredential(ctx, slug)
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(key) == "" {
		return nil, fmt.Errorf("%w: %s", ErrProviderUnconfigured, slug)
	}
	// The adapter's name is the slug, because the slug is what a task
	// configuration names and what the gateway matches a route against.
	return runtime.adapt(kind, slug, baseURL, key)
}

// adapterFor constructs the compiled-in adapter for a stored kind.
func adapterFor(kind, name, baseURL, key string) (aigateway.Provider, error) {
	switch kind {
	case "openai_compatible":
		return aigateway.NewOpenAICompatible(name, baseURL, key)
	case "anthropic":
		return aigateway.NewAnthropic(name, baseURL, key)
	case "gemini":
		return aigateway.NewGemini(name, baseURL, key)
	default:
		return nil, fmt.Errorf("%w: %s", ErrProviderKindUnknown, kind)
	}
}

// taskConfig maps a stored feature row onto the gateway's per-task settings.
//
// Every optional column becomes a zero the gateway already knows how to read as
// "unset": a nil timeout is the gateway's default, and a nil budget is no
// ceiling. Nothing here invents a value an owner did not choose.
func taskConfig(feature panelpg.AIFeature) aigateway.TaskConfig {
	config := aigateway.TaskConfig{
		Enabled: true, Provider: feature.Provider, Model: feature.Model,
	}
	if feature.Temperature != nil {
		config.Temperature = *feature.Temperature
	}
	if feature.MaxTokens != nil {
		config.MaxTokens = int(*feature.MaxTokens)
	}
	if feature.TimeoutMS != nil {
		config.Timeout = time.Duration(*feature.TimeoutMS) * time.Millisecond
	}
	if feature.BudgetTokens != nil {
		config.BudgetTokens = *feature.BudgetTokens
	}
	if feature.BudgetWindow != nil {
		config.BudgetWindow = time.Duration(*feature.BudgetWindow) * time.Second
	}
	return config
}

// budgetMeter answers the gateway's pre-call budget question and deliberately
// records nothing.
//
// Two things could write to `ai_usage_events`: this and the caller that made the
// request. The caller wins, because it is the only one that knows the operator,
// the latency, and whether the call ended in a refusal, and a row missing all
// three is not worth a second insert. Recording in both places would double
// every figure in the cost report — an error nobody finds until they reconcile
// against an invoice.
type budgetMeter struct {
	store   Store
	feature string
}

// Usage reads what this feature has spent in the window.
func (meter budgetMeter) Usage(
	ctx context.Context, _ string, window time.Duration,
) (aigateway.Usage, error) {
	spend, err := meter.store.Spent(ctx, aigovernance.ScopeFeature, "", meter.feature, window)
	if err != nil {
		return aigateway.Usage{}, err
	}
	return aigateway.Usage{Tokens: spend.Tokens, Since: time.Now().Add(-window)}, nil
}

// Record is a no-op. See the type comment: the caller writes the authoritative
// row, including the failures this would never see.
func (budgetMeter) Record(context.Context, string, string, string, int, int) error {
	return nil
}
