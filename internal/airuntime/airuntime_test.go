package airuntime

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/omniflow/omniflow/internal/aigateway"
	"github.com/omniflow/omniflow/internal/aigovernance"
	"github.com/omniflow/omniflow/internal/panelpg"
)

// stubStore is the configuration a test installs, without a database.
type stubStore struct {
	features  []panelpg.AIFeature
	providers []panelpg.AIProvider

	kind, baseURL, key string
	credentialErr      error

	spend    aigovernance.Spend
	spendErr error

	// spentFor records what the budget check asked about, so a test can assert
	// the ceiling is read for the feature rather than for the task.
	spentFor string
}

func (store *stubStore) AIFeatures(context.Context) ([]panelpg.AIFeature, error) {
	return store.features, nil
}

func (store *stubStore) AIProviders(context.Context) ([]panelpg.AIProvider, error) {
	return store.providers, nil
}

func (store *stubStore) AIProviderCredential(
	_ context.Context, _ string,
) (string, string, string, error) {
	if store.credentialErr != nil {
		return "", "", "", store.credentialErr
	}
	return store.kind, store.baseURL, store.key, nil
}

func (store *stubStore) Spent(
	_ context.Context, _, _, feature string, _ time.Duration,
) (aigovernance.Spend, error) {
	store.spentFor = feature
	return store.spend, store.spendErr
}

// recordingProvider answers without a network and remembers what it was asked.
type recordingProvider struct {
	name     string
	request  aigateway.Request
	response aigateway.Response
	err      error
}

func (provider *recordingProvider) Name() string { return provider.name }

func (provider *recordingProvider) Complete(
	_ context.Context, request aigateway.Request,
) (aigateway.Response, error) {
	provider.request = request
	return provider.response, provider.err
}

// runtimeWith builds a runtime whose adapter is the given provider, so nothing
// in these tests needs a key or an endpoint.
func runtimeWith(store Store, provider aigateway.Provider) *Runtime {
	return New(store).WithAdapter(
		func(_, name, _, _ string) (aigateway.Provider, error) {
			if recorder, ok := provider.(*recordingProvider); ok {
				recorder.name = name
			}
			return provider, nil
		},
	)
}

func enabledFeature(name, provider, model string) panelpg.AIFeature {
	return panelpg.AIFeature{Feature: name, Enabled: true, Provider: provider, Model: model}
}

func enabledProvider(slug string) panelpg.AIProvider {
	return panelpg.AIProvider{Slug: slug, Kind: "anthropic", Enabled: true, KeyConfigured: true}
}

// A configured feature reaches the provider, and the answer carries the
// provider and model the owner chose rather than anything the caller supplied.
func TestGatewayRoutesAConfiguredFeature(t *testing.T) {
	store := &stubStore{
		features:  []panelpg.AIFeature{enabledFeature(aigovernance.FeatureSupportSummary, "acme", "claude-x")},
		providers: []panelpg.AIProvider{enabledProvider("acme")},
		key:       "secret",
	}
	provider := &recordingProvider{response: aigateway.Response{
		Text: "a summary", InputTokens: 10, OutputTokens: 5,
	}}

	gateway, binding, err := runtimeWith(store, provider).Gateway(
		context.Background(), aigovernance.FeatureSupportSummary)
	if err != nil {
		t.Fatalf("build the gateway: %v", err)
	}
	if binding.Provider != "acme" || binding.Model != "claude-x" {
		t.Fatalf("binding = %+v, want provider acme and model claude-x", binding)
	}
	if binding.Task != aigateway.TaskTicketSummary {
		t.Fatalf("binding.Task = %q, want %q", binding.Task, aigateway.TaskTicketSummary)
	}

	result, err := gateway.Complete(context.Background(), aigateway.Call{
		Task: aigateway.TaskTicketSummary, Instruction: "Summarise.", Parts: []string{"hello"},
	})
	if err != nil {
		t.Fatalf("complete: %v", err)
	}
	if result.Text != "a summary" {
		t.Fatalf("Text = %q, want %q", result.Text, "a summary")
	}
	if provider.request.Model != "claude-x" {
		t.Fatalf("the provider was asked for model %q, want claude-x", provider.request.Model)
	}
}

// Redaction is the gateway's job and it still happens through this path. A
// runtime that assembled the gateway wrongly could route a call while skipping
// it, and nothing downstream would notice.
func TestGatewayRedactsBeforeTheProviderSeesAnything(t *testing.T) {
	store := &stubStore{
		features:  []panelpg.AIFeature{enabledFeature(aigovernance.FeatureSupportSummary, "acme", "m")},
		providers: []panelpg.AIProvider{enabledProvider("acme")},
		key:       "secret",
	}
	provider := &recordingProvider{response: aigateway.Response{Text: "ok"}}

	gateway, _, err := runtimeWith(store, provider).Gateway(
		context.Background(), aigovernance.FeatureSupportSummary)
	if err != nil {
		t.Fatalf("build the gateway: %v", err)
	}
	if _, err = gateway.Complete(context.Background(), aigateway.Call{
		Task:  aigateway.TaskTicketSummary,
		Parts: []string{"write to somebody@example.com about it"},
	}); err != nil {
		t.Fatalf("complete: %v", err)
	}
	if strings.Contains(provider.request.Prompt, "somebody@example.com") {
		t.Fatalf("an address reached the provider: %q", provider.request.Prompt)
	}
}

// Every way a feature can be unusable answers with the sentinel that names the
// remedy, because the panel renders a different sentence for each.
func TestGatewayRefusesWhatIsNotConfigured(t *testing.T) {
	for _, testCase := range []struct {
		name  string
		store *stubStore
		want  error
	}{
		{
			name: "the feature is off",
			store: &stubStore{
				features:  []panelpg.AIFeature{{Feature: aigovernance.FeatureSupportSummary}},
				providers: []panelpg.AIProvider{enabledProvider("acme")},
				key:       "secret",
			},
			want: aigateway.ErrDisabled,
		},
		{
			name: "the feature is on with no model",
			store: &stubStore{
				features: []panelpg.AIFeature{
					{Feature: aigovernance.FeatureSupportSummary, Enabled: true, Provider: "acme"},
				},
				providers: []panelpg.AIProvider{enabledProvider("acme")},
				key:       "secret",
			},
			want: aigateway.ErrDisabled,
		},
		{
			name: "the provider is switched off",
			store: &stubStore{
				features: []panelpg.AIFeature{
					enabledFeature(aigovernance.FeatureSupportSummary, "acme", "m"),
				},
				providers: []panelpg.AIProvider{{Slug: "acme", Kind: "anthropic"}},
				key:       "secret",
			},
			want: aigateway.ErrProviderUnapproved,
		},
		{
			name: "the provider was never registered",
			store: &stubStore{
				features: []panelpg.AIFeature{
					enabledFeature(aigovernance.FeatureSupportSummary, "ghost", "m"),
				},
				providers: []panelpg.AIProvider{enabledProvider("acme")},
				key:       "secret",
			},
			want: aigateway.ErrProviderUnapproved,
		},
		{
			name: "the provider holds no key",
			store: &stubStore{
				features: []panelpg.AIFeature{
					enabledFeature(aigovernance.FeatureSupportSummary, "acme", "m"),
				},
				providers: []panelpg.AIProvider{enabledProvider("acme")},
				key:       "",
			},
			want: ErrProviderUnconfigured,
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			provider := &recordingProvider{}
			_, _, err := runtimeWith(testCase.store, provider).Gateway(
				context.Background(), aigovernance.FeatureSupportSummary)
			if !errors.Is(err, testCase.want) {
				t.Fatalf("err = %v, want %v", err, testCase.want)
			}
		})
	}
}

// A feature that names no completion task is a programming error rather than a
// configuration one, and it must not resolve to some other feature's task.
func TestGatewayRefusesAFeatureThatIsNotACompletionTask(t *testing.T) {
	store := &stubStore{}
	_, _, err := runtimeWith(store, &recordingProvider{}).Gateway(
		context.Background(), aigovernance.FeatureMCPTools)
	if !errors.Is(err, ErrFeatureUnknown) {
		t.Fatalf("err = %v, want ErrFeatureUnknown", err)
	}
}

// The ceiling is enforced before the request, and it is read against the
// feature rather than the task — two features share `reply_suggest`, and
// budgeting them as one would let either spend the other's allowance.
func TestBudgetRefusesBeforeReachingTheProvider(t *testing.T) {
	budget := int64(100)
	feature := enabledFeature(aigovernance.FeatureSupportReply, "acme", "m")
	feature.BudgetTokens = &budget
	store := &stubStore{
		features:  []panelpg.AIFeature{feature},
		providers: []panelpg.AIProvider{enabledProvider("acme")},
		key:       "secret",
		spend:     aigovernance.Spend{Tokens: 100},
	}
	provider := &recordingProvider{response: aigateway.Response{Text: "should not happen"}}

	gateway, _, err := runtimeWith(store, provider).Gateway(
		context.Background(), aigovernance.FeatureSupportReply)
	if err != nil {
		t.Fatalf("build the gateway: %v", err)
	}
	_, err = gateway.Complete(context.Background(), aigateway.Call{
		Task: aigateway.TaskReplySuggest, Parts: []string{"anything"},
	})
	if !errors.Is(err, aigateway.ErrBudgetExhausted) {
		t.Fatalf("err = %v, want ErrBudgetExhausted", err)
	}
	if provider.request.Prompt != "" {
		t.Fatal("the provider was called despite an exhausted budget")
	}
	if store.spentFor != aigovernance.FeatureSupportReply {
		t.Fatalf("the ceiling was read for %q, want the feature", store.spentFor)
	}
}

// An unreadable meter fails closed. Spending against a limit nobody can measure
// is how an installation discovers its ceiling from an invoice.
func TestAnUnreadableMeterRefuses(t *testing.T) {
	budget := int64(100)
	feature := enabledFeature(aigovernance.FeatureSupportReply, "acme", "m")
	feature.BudgetTokens = &budget
	store := &stubStore{
		features:  []panelpg.AIFeature{feature},
		providers: []panelpg.AIProvider{enabledProvider("acme")},
		key:       "secret",
		spendErr:  errors.New("the usage table is unreachable"),
	}
	provider := &recordingProvider{}

	gateway, _, err := runtimeWith(store, provider).Gateway(
		context.Background(), aigovernance.FeatureSupportReply)
	if err != nil {
		t.Fatalf("build the gateway: %v", err)
	}
	if _, err = gateway.Complete(context.Background(), aigateway.Call{
		Task: aigateway.TaskReplySuggest, Parts: []string{"anything"},
	}); !errors.Is(err, aigateway.ErrBudgetExhausted) {
		t.Fatalf("err = %v, want ErrBudgetExhausted", err)
	}
}

// The meter records nothing. Its Record writing a row as well as the caller's
// would double every figure in the cost report.
func TestTheMeterDoesNotRecord(t *testing.T) {
	store := &stubStore{}
	meter := budgetMeter{store: store, feature: aigovernance.FeatureSupportReply}
	if err := meter.Record(context.Background(), "t", "p", "m", 1, 2); err != nil {
		t.Fatalf("Record = %v, want nil", err)
	}
	if store.spentFor != "" {
		t.Fatal("Record touched the store")
	}
}

// Available answers without opening a credential, so a screen can ask it on
// every render.
func TestAvailableReportsWhatWouldRun(t *testing.T) {
	store := &stubStore{
		features: []panelpg.AIFeature{
			enabledFeature(aigovernance.FeatureSupportSummary, "acme", "m"),
			enabledFeature(aigovernance.FeatureSupportReply, "acme", "m"),
		},
		providers:     []panelpg.AIProvider{enabledProvider("acme")},
		credentialErr: errors.New("Available must not open a credential"),
	}
	runtime := New(store)
	if !runtime.Available(context.Background(), aigovernance.FeatureSupportSummary) {
		t.Fatal("a configured feature reported unavailable")
	}
	if runtime.Available(context.Background(), aigovernance.FeatureRiskAnalysis) {
		t.Fatal("a feature with no row reported available")
	}

	store.providers = []panelpg.AIProvider{{Slug: "acme", Kind: "anthropic", Enabled: true}}
	if runtime.Available(context.Background(), aigovernance.FeatureSupportSummary) {
		t.Fatal("a provider holding no key reported available")
	}
}

// A probe runs against a provider the owner has not switched on yet, because
// paste the key, test it, then enable it is the order the work happens in.
func TestProbeRunsBeforeAProviderIsEnabled(t *testing.T) {
	store := &stubStore{
		features:  []panelpg.AIFeature{enabledFeature(aigovernance.FeatureSupportSummary, "acme", "claude-x")},
		providers: []panelpg.AIProvider{{Slug: "acme", Kind: "anthropic"}},
		key:       "secret",
	}
	provider := &recordingProvider{response: aigateway.Response{Text: "OK"}}

	detail, err := runtimeWith(store, provider).Probe(context.Background(), "acme", "")
	if err != nil {
		t.Fatalf("probe: %v", err)
	}
	if provider.request.Model != "claude-x" {
		t.Fatalf("probed with model %q, want the one a feature names", provider.request.Model)
	}
	if !strings.Contains(detail, "claude-x") {
		t.Fatalf("detail = %q, want it to name the model", detail)
	}
}

// A provider nothing points at yet has no model to infer, and inventing one
// would test something the installation does not use.
func TestProbeNeedsAModelWhenNothingNamesOne(t *testing.T) {
	store := &stubStore{
		providers: []panelpg.AIProvider{{Slug: "acme", Kind: "anthropic"}},
		key:       "secret",
	}
	if _, err := runtimeWith(store, &recordingProvider{}).Probe(
		context.Background(), "acme", "",
	); !errors.Is(err, ErrModelUnknown) {
		t.Fatalf("err = %v, want ErrModelUnknown", err)
	}
}

// An empty answer is a failed test. A provider that returns 200 and no text has
// not demonstrated that anything works.
func TestProbeTreatsAnEmptyAnswerAsAFailure(t *testing.T) {
	store := &stubStore{
		providers: []panelpg.AIProvider{{Slug: "acme", Kind: "anthropic"}},
		key:       "secret",
	}
	if _, err := runtimeWith(store, &recordingProvider{}).Probe(
		context.Background(), "acme", "m",
	); !errors.Is(err, aigateway.ErrProviderRejected) {
		t.Fatalf("err = %v, want ErrProviderRejected", err)
	}
}

// Every stored kind constructs, and an unknown one refuses rather than falling
// back to a default provider.
func TestAdapterForCoversEveryStoredKind(t *testing.T) {
	for _, kind := range []string{"openai_compatible", "anthropic", "gemini"} {
		provider, err := adapterFor(kind, "acme", "", "secret")
		if err != nil {
			t.Fatalf("%s: %v", kind, err)
		}
		if provider.Name() != "acme" {
			t.Fatalf("%s: Name = %q, want the slug", kind, provider.Name())
		}
	}
	if _, err := adapterFor("mystery", "acme", "", "secret"); !errors.Is(err, ErrProviderKindUnknown) {
		t.Fatalf("err = %v, want ErrProviderKindUnknown", err)
	}
}

// Each optional column maps onto the gateway's own "unset" rather than onto a
// value nobody chose.
func TestTaskConfigLeavesUnsetColumnsUnset(t *testing.T) {
	bare := taskConfig(enabledFeature(aigovernance.FeatureSupportSummary, "acme", "m"))
	if bare.Timeout != 0 || bare.BudgetTokens != 0 || bare.MaxTokens != 0 {
		t.Fatalf("an unconfigured feature invented limits: %+v", bare)
	}

	temperature := 0.4
	maxTokens := int32(900)
	timeout := int32(4000)
	budget := int64(50)
	window := int32(3600)
	feature := enabledFeature(aigovernance.FeatureSupportSummary, "acme", "m")
	feature.Temperature = &temperature
	feature.MaxTokens = &maxTokens
	feature.TimeoutMS = &timeout
	feature.BudgetTokens = &budget
	feature.BudgetWindow = &window

	config := taskConfig(feature)
	if config.Temperature != 0.4 || config.MaxTokens != 900 {
		t.Fatalf("config = %+v, want the stored temperature and ceiling", config)
	}
	if config.Timeout != 4*time.Second || config.BudgetWindow != time.Hour {
		t.Fatalf("config = %+v, want milliseconds and seconds converted", config)
	}
}
