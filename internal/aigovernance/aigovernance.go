// Package aigovernance decides whether an AI feature may run, for whom, and
// what may be kept afterwards.
//
// It sits between a feature and the gateway. The gateway knows about providers,
// budgets, and redaction; this package knows about people. "Is this feature on?"
// and "has this operator spent their allowance?" are questions about an
// installation's policy rather than about a model, and answering them in one
// place is what stops each feature inventing its own answer.
//
// Three rules shape everything here.
//
// Off is the default and it is a row, not an absence. A feature with no
// configuration is disabled, and a feature configured without a provider cannot
// be enabled at all — an installation should never discover its AI settings by
// watching a request fail.
//
// A limit that is checked after the spend is a bill. Every ceiling is evaluated
// before the call, and the narrowest applicable one wins.
//
// Retention is a decision an owner makes per feature, and the safe default is
// not to keep. A marketing draft is the operator's own copy; a support summary
// is somebody's complaint, and those should not share a retention policy by
// accident.
package aigovernance

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"
)

// Features an owner can enable. They are coarser than gateway tasks because a
// feature is what an owner decides about, and a task is what a budget applies
// to — one decision can configure several tasks.
const (
	FeatureSupportSummary   = "support_summary"
	FeatureSupportReply     = "support_reply"
	FeatureSupportRewrite   = "support_rewrite"
	FeatureSupportTranslate = "support_translate"
	FeatureSupportClassify  = "support_classify"
	FeatureMarketingDraft   = "marketing_draft"
	FeatureRiskAnalysis     = "risk_analysis"
	FeatureCopilot          = "copilot"
	FeatureMCPTools         = "mcp_tools"
)

// All lists every feature, so a settings screen renders the complete set rather
// than only the ones somebody has already touched.
func All() []string {
	features := []string{
		FeatureSupportSummary, FeatureSupportReply, FeatureSupportRewrite,
		FeatureSupportTranslate, FeatureSupportClassify, FeatureMarketingDraft,
		FeatureRiskAnalysis, FeatureCopilot, FeatureMCPTools,
	}
	sort.Strings(features)
	return features
}

var (
	// ErrFeatureDisabled reports a feature an owner has not enabled. It is a
	// normal state and callers render it as "unavailable" — every AI feature has
	// a manual workflow behind it, and this is how they fall back to it.
	ErrFeatureDisabled = errors.New("this ai feature is not enabled")
	// ErrLimitReached reports an exhausted allowance. It names the scope,
	// because "you have used your allowance" and "the installation has used its
	// allowance" call for different actions from the same operator.
	ErrLimitReached = errors.New("ai usage limit reached")
	// ErrUnknownFeature reports a feature name that is not one of the nine.
	ErrUnknownFeature = errors.New("unknown ai feature")
)

// Feature is one feature's configuration.
type Feature struct {
	Name     string
	Enabled  bool
	Provider string
	Model    string

	// Retention is per feature, because the material differs.
	RetainPrompts bool
	RetainOutputs bool
	RetentionDays int
}

// Usable reports whether the configuration could actually run.
//
// It is separate from Enabled so the settings screen can show "enabled but
// misconfigured" rather than silently treating it as off — a switch that reads
// as on and behaves as off is the worst of both.
func (feature Feature) Usable() bool {
	return feature.Enabled && feature.Provider != "" && feature.Model != ""
}

// Provider is what an owner has approved and what it does with the data.
type Provider struct {
	Slug    string
	Kind    string
	Enabled bool
	// ZeroRetention and TrainsOnData are the owner's answers from the provider's
	// terms. Omniflow cannot verify them and does not pretend to; they are
	// recorded so the panel can warn before a feature is switched on rather than
	// after data has left.
	ZeroRetention  bool
	TrainsOnData   bool
	RetentionNote  string
	DataRegion     string
	LastCheckOK    bool
	LastCheckedAt  time.Time
	LastCheckError string
}

// Warning is something an owner should read before enabling a feature.
//
// The tags are not decoration. This type is serialised straight into the
// settings response, and without them it went out as `Code`, `Text`, and
// `Blocking` while the panel read `code`, `text`, and `blocking`. Every warning
// therefore rendered as an untranslated key, and `blocking` read as undefined —
// so the guard that refuses to switch on a feature with no provider was
// present, rendered, and inert.
type Warning struct {
	Code string `json:"code"`
	// Text is written for an owner rather than a developer, because the person
	// reading it is deciding whether their customers' messages may leave.
	Text string `json:"text"`
	// Blocking separates "you should know this" from "this will not work".
	Blocking bool `json:"blocking"`
}

// Warning codes.
const (
	WarningTrainsOnData      = "provider_trains_on_data"
	WarningNoZeroRetention   = "provider_retains_data"
	WarningRegionUnstated    = "provider_region_unstated"
	WarningProviderUnchecked = "provider_never_checked"
	WarningProviderFailing   = "provider_check_failing"
	WarningPromptsRetained   = "prompts_retained"
	WarningNoProvider        = "feature_has_no_provider"
)

// Warnings describes what enabling a feature on a provider means.
//
// It is computed rather than stored so it cannot go stale against the provider
// row, and it is returned to the panel whether or not the feature is currently
// on: the point is to inform the decision, not to annotate it afterwards.
func Warnings(feature Feature, provider Provider) []Warning {
	warnings := make([]Warning, 0, 4)
	if feature.Provider == "" || feature.Model == "" {
		warnings = append(warnings, Warning{
			Code: WarningNoProvider, Blocking: true,
			Text: "This feature has no provider and model, so it cannot be enabled.",
		})
		return warnings
	}
	if provider.TrainsOnData {
		warnings = append(warnings, Warning{
			Code: WarningTrainsOnData, Blocking: false,
			Text: "This provider may train on the data you send. Customer messages " +
				"reaching it could influence a future model.",
		})
	}
	if !provider.ZeroRetention {
		warnings = append(warnings, Warning{
			Code: WarningNoZeroRetention, Blocking: false,
			Text: "This provider is not configured for zero retention, so requests " +
				"may be stored on its side for a period you do not control.",
		})
	}
	if strings.TrimSpace(provider.DataRegion) == "" {
		warnings = append(warnings, Warning{
			Code: WarningRegionUnstated, Blocking: false,
			Text: "No data region is recorded for this provider. If your installation " +
				"has a jurisdictional requirement, confirm it before enabling.",
		})
	}
	if provider.LastCheckedAt.IsZero() {
		warnings = append(warnings, Warning{
			Code: WarningProviderUnchecked, Blocking: false,
			Text: "This provider has never passed a connection test.",
		})
	} else if !provider.LastCheckOK {
		warnings = append(warnings, Warning{
			Code: WarningProviderFailing, Blocking: false,
			Text: "The last connection test to this provider failed: " + provider.LastCheckError,
		})
	}
	if feature.RetainPrompts {
		warnings = append(warnings, Warning{
			Code: WarningPromptsRetained, Blocking: false,
			Text: fmt.Sprintf("Prompts for this feature are kept in Omniflow for %d days. "+
				"They contain the redacted customer material that was sent.",
				feature.RetentionDays),
		})
	}
	return warnings
}

// Scope names whose allowance a limit applies to.
const (
	ScopeInstallation = "installation"
	ScopeRole         = "role"
	ScopeOperator     = "operator"
	ScopeFeature      = "feature"
)

// Limit is one ceiling.
type Limit struct {
	Scope string
	// Ref is the role name or operator id; empty for installation and feature.
	Ref     string
	Feature string
	Window  time.Duration

	MaxRequests  int64
	MaxTokens    int64
	MaxCostMinor int64
}

// Spend is what a scope has already used in a window.
type Spend struct {
	Requests  int64
	Tokens    int64
	CostMinor int64
}

// UsageReader answers what a scope has spent. It is an interface so the policy
// does not own storage and a test can exercise every ceiling without a database.
type UsageReader interface {
	Spent(ctx context.Context, scope, ref, feature string, window time.Duration) (Spend, error)
}

// Actor is who is asking.
type Actor struct {
	OperatorID string
	Role       string
}

// Policy answers whether a feature may run.
type Policy struct {
	features map[string]Feature
	limits   []Limit
	usage    UsageReader
}

// NewPolicy builds the policy from what an owner configured.
func NewPolicy(features []Feature, limits []Limit, usage UsageReader) *Policy {
	indexed := make(map[string]Feature, len(features))
	for _, feature := range features {
		indexed[feature.Name] = feature
	}
	return &Policy{features: indexed, limits: limits, usage: usage}
}

// Feature returns one feature's configuration. A feature with no row is
// disabled rather than missing, so a caller never has to distinguish the two.
func (policy *Policy) Feature(name string) Feature {
	if feature, known := policy.features[name]; known {
		return feature
	}
	return Feature{Name: name}
}

// Enabled reports whether a feature can run at all. Callers use it to decide
// whether to offer a button, rather than offering one that always fails.
func (policy *Policy) Enabled(name string) bool {
	return policy.Feature(name).Usable()
}

// Allow decides whether one request may proceed.
//
// The order matters: enablement first, then the ceilings from narrowest to
// widest. An operator who has exhausted their own allowance should be told that,
// not told the installation is out — the remedy is different.
func (policy *Policy) Allow(ctx context.Context, feature string, actor Actor) error {
	if !policy.Enabled(feature) {
		return fmt.Errorf("%w: %s", ErrFeatureDisabled, feature)
	}
	if policy.usage == nil {
		// No meter means no enforceable ceiling. Refusing would take every
		// feature offline on an installation that has set no limits, which is
		// the common case and a legitimate one.
		return nil
	}

	for _, limit := range policy.applicable(feature, actor) {
		spend, err := policy.usage.Spent(ctx, limit.Scope, limit.Ref, limit.Feature, limit.Window)
		if err != nil {
			// An unreadable meter fails closed for the same reason the gateway's
			// budget does: spending against a limit nobody can measure is how an
			// installation learns its ceiling from an invoice.
			return fmt.Errorf("%w: usage for %s is unreadable", ErrLimitReached, limit.describe())
		}
		if limit.exceededBy(spend) {
			return fmt.Errorf("%w: %s", ErrLimitReached, limit.describe())
		}
	}
	return nil
}

// applicable returns the limits that bind this request, narrowest first.
func (policy *Policy) applicable(feature string, actor Actor) []Limit {
	matched := make([]Limit, 0, 4)
	for _, limit := range policy.limits {
		switch limit.Scope {
		case ScopeOperator:
			if actor.OperatorID != "" && limit.Ref == actor.OperatorID {
				matched = append(matched, limit)
			}
		case ScopeRole:
			if actor.Role != "" && limit.Ref == actor.Role {
				matched = append(matched, limit)
			}
		case ScopeFeature:
			if limit.Feature == feature {
				matched = append(matched, limit)
			}
		case ScopeInstallation:
			matched = append(matched, limit)
		}
	}
	// Narrowest first, so the message an operator sees names the ceiling they
	// can do something about.
	rank := map[string]int{ScopeOperator: 0, ScopeRole: 1, ScopeFeature: 2, ScopeInstallation: 3}
	sort.SliceStable(matched, func(left, right int) bool {
		return rank[matched[left].Scope] < rank[matched[right].Scope]
	})
	return matched
}

func (limit Limit) exceededBy(spend Spend) bool {
	if limit.MaxRequests > 0 && spend.Requests >= limit.MaxRequests {
		return true
	}
	if limit.MaxTokens > 0 && spend.Tokens >= limit.MaxTokens {
		return true
	}
	if limit.MaxCostMinor > 0 && spend.CostMinor >= limit.MaxCostMinor {
		return true
	}
	return false
}

func (limit Limit) describe() string {
	switch limit.Scope {
	case ScopeOperator:
		return "your own allowance"
	case ScopeRole:
		return "the allowance for the " + limit.Ref + " role"
	case ScopeFeature:
		return "the allowance for " + limit.Feature
	default:
		return "the installation-wide allowance"
	}
}

// Retention is what may be kept after a request.
type Retention struct {
	KeepPrompt bool
	KeepOutput bool
	// Until is when the material may be deleted. Zero means "not stored", which
	// is a different statement from "stored forever" and reads differently.
	Until time.Time
}

// RetentionFor decides what one feature's material may keep.
//
// A legal hold overrides the schedule in one direction only: it can extend
// retention of something already kept, and it can never cause something to be
// kept that the policy said not to store. A hold is an instruction not to
// delete, not an instruction to collect.
func (policy *Policy) RetentionFor(feature string, now time.Time, held bool) Retention {
	configured := policy.Feature(feature)
	retention := Retention{
		KeepPrompt: configured.RetainPrompts,
		KeepOutput: configured.RetainOutputs,
	}
	if !retention.KeepPrompt && !retention.KeepOutput {
		return retention
	}
	if held {
		// An indefinite hold is expressed as a zero deletion time on material
		// that is being kept, which the sweeper reads as "never".
		return retention
	}
	days := configured.RetentionDays
	if days <= 0 {
		return retention
	}
	retention.Until = now.AddDate(0, 0, days).UTC()
	return retention
}
