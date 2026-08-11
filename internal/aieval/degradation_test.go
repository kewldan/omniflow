package aieval_test

import (
	"context"
	"errors"
	"testing"

	"github.com/omniflow/omniflow/internal/aicopilot"
	"github.com/omniflow/omniflow/internal/aidraft"
	"github.com/omniflow/omniflow/internal/aigateway"
	"github.com/omniflow/omniflow/internal/aigovernance"
	"github.com/omniflow/omniflow/internal/aiknowledge"
	"github.com/omniflow/omniflow/internal/aimarketing"
	"github.com/omniflow/omniflow/internal/airisk"
	"github.com/omniflow/omniflow/internal/aisupport"
)

// Graceful degradation.
//
// Every AI feature in Omniflow sits in front of a manual workflow that predates
// it: an operator can write a reply, judge a dispute, and compose a campaign
// without any of this. The property under test is that switching AI off — or
// never switching it on — leaves those workflows exactly as they were, and
// produces a clear "unavailable" rather than a panic, a hang, or an error that
// reads like a bug.
//
// The test lives here rather than in each package because it is a statement
// about all of them together. One package quietly panicking on a nil gateway
// would be a support desk that stops working when a provider's bill goes
// unpaid.

type grant map[string]bool

func (g grant) Allows(permission string) bool { return g[permission] }

type emptyIndex struct{}

func (emptyIndex) Search(
	context.Context, string, aiknowledge.Grant, int,
) ([]aiknowledge.Source, error) {
	return nil, nil
}

// An installation that has never configured AI must be fully usable.
func TestEveryFeatureReportsUnavailableWithNoGateway(t *testing.T) {
	ctx := context.Background()

	support := aisupport.New(nil)
	for _, task := range []string{
		aigateway.TaskTicketSummary, aigateway.TaskReplySuggest,
		aigateway.TaskTranslate, aigateway.TaskClassify,
	} {
		if support.Available(task) {
			t.Fatalf("support reported %s as available with no gateway", task)
		}
	}
	if _, err := support.Summarise(ctx, aisupport.Conversation{Subject: "x"}); !errors.Is(err, aigateway.ErrDisabled) {
		t.Fatalf("summarise: expected ErrDisabled, got %v", err)
	}

	risk := airisk.New(nil)
	if risk.Available() {
		t.Fatal("risk reported as available with no gateway")
	}
	if _, err := risk.Assess(
		ctx, airisk.CategoryScam, airisk.Subject{Content: []string{"anything"}},
	); !errors.Is(err, aigateway.ErrDisabled) {
		t.Fatalf("assess: expected ErrDisabled, got %v", err)
	}

	marketing := aimarketing.New(nil, "")
	if marketing.Available() {
		t.Fatal("marketing reported as available with no gateway")
	}
	if _, err := marketing.Draft(
		ctx, aimarketing.Brief{Purpose: "x"},
	); !errors.Is(err, aigateway.ErrDisabled) {
		t.Fatalf("draft: expected ErrDisabled, got %v", err)
	}

	registry, err := aicopilot.NewRegistry(aicopilot.Tool{
		Name: "orders", Permission: "finance.read", Describe: "reads orders",
		Run: func(context.Context, map[string]string) ([]aicopilot.Record, error) {
			return nil, nil
		},
	})
	if err != nil {
		t.Fatalf("registry: %v", err)
	}
	copilot := aicopilot.New(nil, registry)
	if copilot.Available() {
		t.Fatal("the copilot reported as available with no gateway")
	}
	if _, err := copilot.Ask(
		ctx, grant{"finance.read": true}, "question", nil,
	); !errors.Is(err, aigateway.ErrDisabled) {
		t.Fatalf("ask: expected ErrDisabled, got %v", err)
	}

	knowledge := aiknowledge.New(nil, emptyIndex{})
	if knowledge.Available() {
		t.Fatal("grounded answers reported as available with no gateway")
	}
	if _, err := knowledge.Suggest(
		ctx, "question", grant{},
	); !errors.Is(err, aigateway.ErrDisabled) {
		t.Fatalf("suggest: expected ErrDisabled, got %v", err)
	}
}

// The checks that protect a customer are not AI features and must not become
// unavailable with the model.
func TestTheCheckersKeepWorkingWithoutAModel(t *testing.T) {
	// Marketing copy is checked by a function that takes no gateway, so a human
	// writing the message that breaks the send is still caught.
	findings := aimarketing.Check("Guaranteed results, {{unknown}}!", aimarketing.Brief{
		Variables: []string{"first_name"},
	})
	if len(findings) == 0 {
		t.Fatal("operator-written copy went unchecked with AI off")
	}

	// Cross-ticket correlation is arithmetic on fingerprints, not a model.
	patterns := airisk.DetectPatterns([]airisk.Observation{
		{Kind: airisk.SignalDeviceID, Fingerprint: "fp", TicketID: "t-1", CustomerRef: "c-1"},
		{Kind: airisk.SignalDeviceID, Fingerprint: "fp", TicketID: "t-2", CustomerRef: "c-2"},
		{Kind: airisk.SignalDeviceID, Fingerprint: "fp", TicketID: "t-3", CustomerRef: "c-3"},
	}, airisk.PatternOptions{})
	if len(patterns) != 1 {
		t.Fatalf("pattern detection stopped working with AI off: %+v", patterns)
	}

	// Similar-material retrieval needs no model either.
	if _, err := aiknowledge.New(nil, emptyIndex{}).Similar(
		context.Background(), "double charge", grant{}, 5,
	); err != nil {
		t.Fatalf("retrieval failed with no model: %v", err)
	}
}

// A feature that vanishes when it is off teaches operators that it is
// unreliable. The surface renders the same component with an explanation.
func TestAnUnavailableFeatureStillProducesADraft(t *testing.T) {
	for _, failure := range []string{
		aidraft.FailureDisabled, aidraft.FailureUnconfigured,
		aidraft.FailureBudget, aidraft.FailureLimit,
	} {
		draft := aidraft.Unavailable("d-1", aigovernance.FeatureSupportReply, failure)
		if draft.State != aidraft.StateUnavailable {
			t.Fatalf("%s produced state %q", failure, draft.State)
		}
		if draft.Retryable {
			t.Fatalf("%s offered a retry that would produce the same answer", failure)
		}
		if _, err := draft.Sendable(); !errors.Is(err, aidraft.ErrNotAccepted) {
			t.Fatalf("%s was sendable: %v", failure, err)
		}
	}
}

// A disabled feature and an exhausted allowance both refuse before any network
// call, and both say which it was — the operator's next action differs.
func TestGovernanceRefusesBeforeAnythingIsSpent(t *testing.T) {
	policy := aigovernance.NewPolicy(nil, nil, nil)
	if policy.Enabled(aigovernance.FeatureSupportReply) {
		t.Fatal("an unconfigured feature reported as enabled")
	}
	err := policy.Allow(
		context.Background(), aigovernance.FeatureSupportReply,
		aigovernance.Actor{OperatorID: "op-1"},
	)
	if !errors.Is(err, aigovernance.ErrFeatureDisabled) {
		t.Fatalf("expected ErrFeatureDisabled, got %v", err)
	}
	if errors.Is(err, aigovernance.ErrLimitReached) {
		t.Fatal("a disabled feature was reported as an exhausted allowance")
	}
}
