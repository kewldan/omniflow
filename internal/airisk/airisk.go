// Package airisk produces explainable abuse assessments.
//
// The whole design follows from one rule: this can recommend an adverse action
// against a customer and can never take one. No suspension, refund denial,
// wallet correction, or entitlement change happens because of anything here — a
// person decides, from a surface that shows them the evidence.
//
// That rule is why an assessment is a structured verdict with evidence,
// confidence, and stated uncertainty rather than a score. A number between zero
// and one tells an operator nothing they can check, nothing they can explain to
// the customer, and nothing they can disagree with specifically. "Three
// accounts share a payment method, first seen four minutes apart" is all three.
package airisk

import (
	"context"
	"encoding/json"
	"errors"
	"sort"
	"strings"
	"time"

	"github.com/omniflow/omniflow/internal/aigateway"
	"github.com/omniflow/omniflow/internal/airedact"
)

// Categories an assessment can be about. They are separate because the evidence
// that supports one says nothing about another, and an operator reviewing a
// payment dispute should not be shown a phishing verdict.
const (
	CategoryScam           = "scam"
	CategoryPhishing       = "phishing"
	CategoryImpersonation  = "impersonation"
	CategorySocialEngineer = "social_engineering"
	CategoryPaymentFraud   = "payment_fraud"
	CategoryReferralAbuse  = "referral_abuse"
)

var validCategory = map[string]bool{
	CategoryScam: true, CategoryPhishing: true, CategoryImpersonation: true,
	CategorySocialEngineer: true, CategoryPaymentFraud: true, CategoryReferralAbuse: true,
}

// Confidence is deliberately coarse. Three levels are as fine as a model's
// self-reported certainty is worth treating, and a percentage invites an
// operator to act on a difference between 71 and 68 that means nothing.
const (
	ConfidenceLow    = "low"
	ConfidenceMedium = "medium"
	ConfidenceHigh   = "high"
)

var validConfidence = map[string]bool{
	ConfidenceLow: true, ConfidenceMedium: true, ConfidenceHigh: true,
}

// ErrUnusableAssessment reports an answer that cannot be shown to an operator.
//
// A malformed assessment is discarded rather than partially rendered: half a
// verdict with no evidence is exactly the unexplained score this package exists
// to avoid.
var ErrUnusableAssessment = errors.New("the model returned an unusable assessment")

// Assessment is one explainable verdict.
type Assessment struct {
	Category string `json:"category"`
	// Concern is one sentence an operator can read and a customer could be told.
	Concern string `json:"concern"`
	// Evidence is what the model actually saw, in checkable terms. An
	// assessment with none is refused: a verdict nobody can verify is an
	// accusation.
	Evidence []string `json:"evidence"`
	// Uncertainty is what would change the answer. It is required for the same
	// reason evidence is — an operator needs to know what to go and check.
	Uncertainty string `json:"uncertainty"`
	Confidence  string `json:"confidence"`
	// Signals are the deterministic checks that fired, passed in by the caller
	// rather than produced by the model. They are the part an auditor can
	// reproduce.
	Signals []string `json:"signals"`

	// Provenance, so a decision made on this can be traced later.
	Generated     bool            `json:"generated"`
	Provider      string          `json:"provider"`
	Model         string          `json:"model"`
	PolicyVersion string          `json:"policyVersion"`
	AssessedAt    time.Time       `json:"assessedAt"`
	Redacted      airedact.Result `json:"-"`
}

// RecommendsReview reports whether a person should look.
//
// It is the strongest thing this package returns, and it is deliberately not
// called "RecommendsSuspension". The recommendation is always to look.
func (assessment Assessment) RecommendsReview() bool {
	return assessment.Confidence != ConfidenceLow || len(assessment.Signals) > 0
}

// PolicyVersion identifies the prompt and rules an assessment was made under.
//
// It is recorded with every assessment because a verdict reached under one
// version of the prompt cannot be defended by reading the current one, and
// "why did the system flag this in March?" is a question that gets asked.
const PolicyVersion = "risk-2026-08-1"

const riskSystem = "You assess customer support conversations for signs of " +
	"abuse. You produce evidence, not judgements. Quote only what is present in " +
	"the material you are given; never infer intent from tone alone; never " +
	"speculate about a person's identity, nationality, or circumstances. If the " +
	"material does not support a concern, say so with high confidence. Answer " +
	"only with the JSON object described."

// Subject is what is being assessed.
//
// It is deliberately narrow. The model sees the conversation and the
// deterministic signals, and nothing else: no order history, no payment
// details, no other customers' data. Widening it would make the assessment
// better and the disclosure worse, and the disclosure is the thing that has to
// be defensible.
type Subject struct {
	// Content is the untrusted material — messages, notes, a claim under
	// dispute. It is redacted before it leaves.
	Content []string
	// Signals are deterministic checks that already fired, in terms an operator
	// can reproduce: "three accounts share a payment token", not a score.
	Signals []string
}

// Service produces assessments.
type Service struct {
	gateway *aigateway.Gateway
	clock   func() time.Time
}

// New builds the service. A nil gateway reports the feature as unavailable.
func New(gateway *aigateway.Gateway) *Service {
	return &Service{gateway: gateway, clock: time.Now}
}

// Available reports whether assessment can run.
func (service *Service) Available() bool {
	return service.gateway != nil && service.gateway.Enabled(aigateway.TaskRiskAnalysis)
}

// Assess produces one verdict.
//
// A malformed answer is an error rather than a partial assessment. The point of
// this package is that an operator is shown evidence they can check; rendering
// a verdict with the evidence missing would deliver exactly the unexplained
// score it exists to avoid.
func (service *Service) Assess(
	ctx context.Context, category string, subject Subject,
) (Assessment, error) {
	if !validCategory[category] {
		return Assessment{}, ErrUnusableAssessment
	}
	if service.gateway == nil {
		return Assessment{}, aigateway.ErrDisabled
	}

	parts := make([]string, 0, len(subject.Content)+1)
	parts = append(parts, subject.Content...)
	if len(subject.Signals) > 0 {
		// The signals are shown to the model as findings rather than as
		// conclusions, so it can explain them or contradict them.
		parts = append(parts, "Automated checks that fired: "+strings.Join(subject.Signals, "; "))
	}

	result, err := service.gateway.Complete(ctx, aigateway.Call{
		Task:   aigateway.TaskRiskAnalysis,
		System: riskSystem,
		Instruction: `Assess the material below for ` + strings.ReplaceAll(category, "_", " ") +
			`. Answer with a single JSON object and nothing else: ` +
			`{"concern": "one sentence", "evidence": ["quoted or described facts"], ` +
			`"uncertainty": "what would change this answer", "confidence": "low|medium|high"}. ` +
			`If the material does not support a concern, say so in "concern" with ` +
			`confidence "high" and an empty evidence list.`,
		Parts: parts,
	})
	if err != nil {
		return Assessment{}, err
	}

	assessment, err := parseAssessment(result.Text)
	if err != nil {
		return Assessment{}, err
	}
	assessment.Category = category
	assessment.Signals = subject.Signals
	assessment.Generated = true
	assessment.Provider = result.Provider
	assessment.Model = result.Model
	assessment.PolicyVersion = PolicyVersion
	assessment.AssessedAt = service.clock().UTC()
	assessment.Redacted = result.Redacted
	return assessment, nil
}

// parseAssessment reads the model's JSON and refuses anything unusable.
func parseAssessment(text string) (Assessment, error) {
	trimmed := strings.TrimSpace(text)
	// Models routinely wrap JSON in a fenced block despite being asked not to.
	// Tolerating that is worth it; tolerating a missing verdict is not.
	if start := strings.Index(trimmed, "{"); start >= 0 {
		if end := strings.LastIndex(trimmed, "}"); end > start {
			trimmed = trimmed[start : end+1]
		}
	}

	var parsed struct {
		Concern     string   `json:"concern"`
		Evidence    []string `json:"evidence"`
		Uncertainty string   `json:"uncertainty"`
		Confidence  string   `json:"confidence"`
	}
	if err := json.Unmarshal([]byte(trimmed), &parsed); err != nil {
		return Assessment{}, ErrUnusableAssessment
	}
	if strings.TrimSpace(parsed.Concern) == "" {
		return Assessment{}, ErrUnusableAssessment
	}
	confidence := strings.ToLower(strings.TrimSpace(parsed.Confidence))
	if !validConfidence[confidence] {
		// An unrecognised confidence becomes low rather than being rejected.
		// The verdict is still readable, and understating certainty is the safe
		// direction for something that leads to a person being investigated.
		confidence = ConfidenceLow
	}

	evidence := make([]string, 0, len(parsed.Evidence))
	for _, item := range parsed.Evidence {
		if strings.TrimSpace(item) != "" {
			evidence = append(evidence, strings.TrimSpace(item))
		}
	}
	sort.Strings(evidence)

	return Assessment{
		Concern:     strings.TrimSpace(parsed.Concern),
		Evidence:    evidence,
		Uncertainty: strings.TrimSpace(parsed.Uncertainty),
		Confidence:  confidence,
	}, nil
}

// Feedback is an operator's verdict on a verdict.
//
// It exists because an assessment that nobody can mark wrong is one that never
// improves, and because "the system said so" is not a defensible reason for an
// adverse decision six months later.
const (
	FeedbackFalsePositive = "false_positive"
	FeedbackConfirmed     = "confirmed_abuse"
	FeedbackInsufficient  = "insufficient_evidence"
)

// ValidFeedback reports whether an operator verdict is one of the three.
func ValidFeedback(value string) bool {
	switch value {
	case FeedbackFalsePositive, FeedbackConfirmed, FeedbackInsufficient:
		return true
	default:
		return false
	}
}
