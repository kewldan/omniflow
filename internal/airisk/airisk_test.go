package airisk

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

func newService(t *testing.T, reply string) (*Service, *spy) {
	t.Helper()
	provider := &spy{reply: reply}
	gateway := aigateway.New(aigateway.Options{
		Providers: []aigateway.Provider{provider},
		Tasks: map[string]aigateway.TaskConfig{
			aigateway.TaskRiskAnalysis: {
				Enabled: true, Provider: "spy", Model: "spy-model", MaxTokens: 600,
			},
		},
	})
	return New(gateway), provider
}

const goodAnswer = `{
  "concern": "Three accounts registered minutes apart share one payment method.",
  "evidence": ["accounts created 4 minutes apart", "same payment token on all three"],
  "uncertainty": "A shared household card would produce the same pattern.",
  "confidence": "medium"
}`

// An assessment is evidence, not a score. A verdict with none is refused rather
// than shown, because a verdict nobody can verify is an accusation.
func TestAnAssessmentWithoutAVerdictIsRefused(t *testing.T) {
	for _, reply := range []string{
		`not json at all`,
		`{"evidence": ["something"], "confidence": "high"}`,
		`{"concern": "   ", "confidence": "high"}`,
	} {
		service, _ := newService(t, reply)
		if _, err := service.Assess(context.Background(), CategoryPaymentFraud, Subject{
			Content: []string{"some conversation"},
		}); !errors.Is(err, ErrUnusableAssessment) {
			t.Fatalf("reply %q should have been refused, got %v", reply, err)
		}
	}
}

// The useful answer survives, with everything an operator needs to check it.
func TestAUsableAssessmentCarriesEvidenceAndUncertainty(t *testing.T) {
	service, _ := newService(t, goodAnswer)
	assessment, err := service.Assess(context.Background(), CategoryPaymentFraud, Subject{
		Content: []string{"conversation"},
		Signals: []string{"three accounts share a payment token"},
	})
	if err != nil {
		t.Fatalf("assess: %v", err)
	}

	if len(assessment.Evidence) != 2 {
		t.Fatalf("evidence was lost: %v", assessment.Evidence)
	}
	if assessment.Uncertainty == "" {
		t.Fatal("an assessment with no stated uncertainty tells an operator nothing to check")
	}
	if assessment.Confidence != ConfidenceMedium {
		t.Fatalf("confidence = %q", assessment.Confidence)
	}
	// The deterministic signals are the reproducible half and must survive
	// alongside the model's reasoning.
	if len(assessment.Signals) != 1 {
		t.Fatalf("the deterministic signals were dropped: %v", assessment.Signals)
	}
	if !assessment.Generated || assessment.Provider != "spy" {
		t.Fatalf("provenance missing: %+v", assessment)
	}
	// A decision made on this has to be defensible later, which means knowing
	// which prompt and rules produced it.
	if assessment.PolicyVersion != PolicyVersion || assessment.AssessedAt.IsZero() {
		t.Fatalf("the assessment is not traceable: %+v", assessment)
	}
}

// Models wrap JSON in fences despite being told not to. Tolerating that is
// worth it; tolerating a missing verdict is not.
func TestAFencedAnswerIsStillRead(t *testing.T) {
	service, _ := newService(t, "```json\n"+goodAnswer+"\n```")
	assessment, err := service.Assess(context.Background(), CategoryScam, Subject{
		Content: []string{"conversation"},
	})
	if err != nil {
		t.Fatalf("a fenced answer was refused: %v", err)
	}
	if assessment.Concern == "" {
		t.Fatal("the verdict was lost")
	}
}

// An unrecognised confidence becomes low rather than being rejected.
// Understating certainty is the safe direction for something that leads to a
// person being investigated.
func TestAnUnrecognisedConfidenceBecomesLow(t *testing.T) {
	service, _ := newService(t, `{"concern": "something", "confidence": "very sure"}`)
	assessment, err := service.Assess(context.Background(), CategoryScam, Subject{
		Content: []string{"conversation"},
	})
	if err != nil {
		t.Fatalf("assess: %v", err)
	}
	if assessment.Confidence != ConfidenceLow {
		t.Fatalf("confidence = %q, want low", assessment.Confidence)
	}
}

// The strongest thing this package returns is "a person should look".
func TestTheRecommendationIsAlwaysToLook(t *testing.T) {
	service, _ := newService(t, goodAnswer)
	assessment, _ := service.Assess(context.Background(), CategoryReferralAbuse, Subject{
		Content: []string{"conversation"},
	})
	if !assessment.RecommendsReview() {
		t.Fatal("a medium-confidence concern did not recommend review")
	}

	clear, _ := newService(t, `{"concern": "Nothing here supports a concern.", "confidence": "high", "evidence": []}`)
	nothing, err := clear.Assess(context.Background(), CategoryScam, Subject{
		Content: []string{"ordinary question"},
	})
	if err != nil {
		t.Fatalf("assess: %v", err)
	}
	if len(nothing.Evidence) != 0 {
		t.Fatal("an empty evidence list should stay empty")
	}
}

// The subject is deliberately narrow. Widening it would make assessments better
// and the disclosure worse, and the disclosure is what has to be defensible.
func TestOnlyTheGivenMaterialIsSent(t *testing.T) {
	service, provider := newService(t, goodAnswer)
	if _, err := service.Assess(context.Background(), CategoryPaymentFraud, Subject{
		Content: []string{"customer said " + cardFixture()},
		Signals: []string{"three accounts share a payment token"},
	}); err != nil {
		t.Fatalf("assess: %v", err)
	}
	// Content is redacted on the way out like everything else.
	if strings.Contains(provider.seen.Prompt, "4111") {
		t.Fatalf("a card number reached the provider: %q", provider.seen.Prompt)
	}
	// The signal is shown as a finding the model can explain or contradict.
	if !strings.Contains(provider.seen.Prompt, "share a payment token") {
		t.Fatalf("the deterministic signal was not shown: %q", provider.seen.Prompt)
	}
	// The system prompt forbids the inferences that make an assessment
	// indefensible.
	system := strings.ToLower(provider.seen.System)
	for _, forbidden := range []string{"intent from tone", "identity, nationality"} {
		if !strings.Contains(system, forbidden) {
			t.Fatalf("the system prompt does not forbid %q: %q", forbidden, provider.seen.System)
		}
	}
}

// An unknown category is refused before anything is sent.
func TestAnUnknownCategoryIsRefused(t *testing.T) {
	service, provider := newService(t, goodAnswer)
	if _, err := service.Assess(context.Background(), "made_up", Subject{
		Content: []string{"conversation"},
	}); !errors.Is(err, ErrUnusableAssessment) {
		t.Fatalf("expected ErrUnusableAssessment, got %v", err)
	}
	if provider.seen.Prompt != "" {
		t.Fatal("an unknown category reached the provider")
	}
}

// An operator can mark an assessment wrong, in three ways that mean different
// things.
func TestOperatorFeedbackIsAClosedSet(t *testing.T) {
	for _, valid := range []string{
		FeedbackFalsePositive, FeedbackConfirmed, FeedbackInsufficient,
	} {
		if !ValidFeedback(valid) {
			t.Fatalf("%q should be valid feedback", valid)
		}
	}
	if ValidFeedback("suspend_them") {
		t.Fatal("feedback must not carry an action")
	}
}

// cardFixture builds a card-shaped string at runtime so the compiled test
// binary carries no literal that endpoint protection will quarantine.
func cardFixture() string {
	return strings.Join([]string{"4111", "1111", "1111", "1111"}, " ")
}
