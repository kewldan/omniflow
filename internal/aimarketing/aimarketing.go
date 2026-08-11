// Package aimarketing drafts and checks operator-facing copy.
//
// It can prepare a campaign. It cannot select recipients, schedule, or send —
// that separation is the whole point, and it is why nothing here takes a
// segment or a campaign identifier. A model that could reach the audience is a
// model that can mail your customers.
//
// The checks matter as much as the drafting. Copy that is grammatical and
// on-brand can still break a template variable, exceed a Telegram limit, or
// promise something the operator cannot deliver, and none of those are visible
// until the message has gone.
package aimarketing

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"

	"github.com/omniflow/omniflow/internal/aigateway"
)

// Telegram's own limits. A draft that exceeds them is not a style problem: the
// send fails, or the message is truncated mid-sentence in front of a customer.
const (
	MaxMessageRunes = 4096
	MaxCaptionRunes = 1024
)

// Draft is generated copy with its provenance and its problems attached.
type Draft struct {
	Text      string
	Generated bool
	Provider  string
	Model     string
	// Findings are what is wrong with the text, if anything. They travel with
	// the draft rather than being a separate call, because a draft an operator
	// can accept without seeing its problems is a draft whose problems reach
	// customers.
	Findings []Finding
}

// Acceptable reports whether the draft has any blocking problem.
func (draft Draft) Acceptable() bool {
	for _, finding := range draft.Findings {
		if finding.Blocking {
			return false
		}
	}
	return true
}

// Finding is one problem with a piece of copy.
type Finding struct {
	Code string
	// Detail names the specific thing, so an operator can fix it rather than
	// hunting for it.
	Detail string
	// Blocking separates "this will fail or mislead" from "you may want to look
	// at this". Only the first prevents acceptance, because a checker that
	// blocks on style is one operators learn to override by habit.
	Blocking bool
}

// Finding codes.
const (
	FindingUndeclaredVariable = "undeclared_variable"
	FindingBrokenVariable     = "broken_variable"
	FindingTooLong            = "too_long"
	FindingForbiddenClaim     = "forbidden_claim"
	FindingShouting           = "shouting"
	FindingUrgencyPressure    = "urgency_pressure"
)

var (
	variablePattern = regexp.MustCompile(`\{\{\s*([a-zA-Z][a-zA-Z0-9_]*)\s*\}\}`)
	// brokenVariable catches the near-misses that render literally: a single
	// brace, a stray space inside the name, an unclosed pair.
	brokenVariable = regexp.MustCompile(`\{[^{}]*\}(?:[^}]|$)|\{\{[^}]*$`)
)

// forbiddenClaims are promises an operator cannot keep and a regulator dislikes.
// They are checked because a model asked for "compelling" copy reaches for them
// unprompted, and the operator sending it may not notice.
var forbiddenClaims = map[string]string{
	"guaranteed":     "an absolute guarantee",
	"100% secure":    "an absolute security claim",
	"unlimited free": "an offer that is not actually unlimited or free",
	"no logs ever":   "an absolute logging claim",
	"risk-free":      "an absolute risk claim",
	"lifetime":       "a lifetime promise the operator may not honour",
}

// urgencyPhrases are pressure tactics. They are advisory rather than blocking:
// a genuine deadline is legitimate copy, and only the operator knows whether
// theirs is real.
var urgencyPhrases = []string{
	"act now", "last chance", "hurry", "expires today", "don't miss out",
	"only a few left", "final warning",
}

// ErrUnsupportedInstruction reports an edit outside the closed set.
var ErrUnsupportedInstruction = errors.New("unsupported marketing instruction")

// Edits an operator may ask for. A closed set for the same reason support
// rewrites are: a free-text instruction field is where somebody pastes content.
const (
	EditRewrite  = "rewrite"
	EditShorten  = "shorten"
	EditExpand   = "expand"
	EditSimplify = "simplify"
	EditWarmer   = "warmer"
	EditPlainer  = "plainer"
)

var validEdit = map[string]bool{
	EditRewrite: true, EditShorten: true, EditExpand: true,
	EditSimplify: true, EditWarmer: true, EditPlainer: true,
}

// Service drafts and checks copy.
type Service struct {
	gateway *aigateway.Gateway
	// brandVoice is operator-authored guidance included in every prompt. It is
	// instruction rather than content: it is what the operator is asking for.
	brandVoice string
}

// New builds the service.
func New(gateway *aigateway.Gateway, brandVoice string) *Service {
	return &Service{gateway: gateway, brandVoice: strings.TrimSpace(brandVoice)}
}

// Available reports whether drafting can run.
func (service *Service) Available() bool {
	return service.gateway != nil && service.gateway.Enabled(aigateway.TaskMarketingDraft)
}

const marketingSystem = "You write short marketing and service messages for a " +
	"subscription product. Never promise a guarantee, absolute security, " +
	"absolute privacy, or a lifetime term. Never invent a price, a date, or a " +
	"discount. Preserve {{template_variables}} exactly as given. Write plainly."

// Brief is what the operator wants written.
//
// It carries no audience and no schedule, which is deliberate: this package
// cannot reach customers, and a brief that named a segment would be one step
// from being able to.
type Brief struct {
	// Purpose is operator instruction — what the message is for.
	Purpose string
	// Variables are the template variables the copy may use. Anything the model
	// invents beyond these is a blocking finding, because it renders literally.
	Variables []string
	// Language is "en" or "ru".
	Language string
	// MaxRunes bounds the draft. Zero uses the Telegram message limit.
	MaxRunes int
}

// Draft writes new copy.
func (service *Service) Draft(ctx context.Context, brief Brief) (Draft, error) {
	if service.gateway == nil {
		return Draft{}, aigateway.ErrDisabled
	}
	instruction := fmt.Sprintf(
		"Write a %s message in %s. Purpose: %s.",
		"marketing", languageName(brief.Language), truncate(brief.Purpose, 500),
	)
	if len(brief.Variables) > 0 {
		instruction += " You may use these variables exactly as written: " +
			strings.Join(wrapVariables(brief.Variables), ", ") + "."
	}
	if service.brandVoice != "" {
		instruction += " House style: " + truncate(service.brandVoice, 400)
	}
	instruction += fmt.Sprintf(" Keep it under %d characters.", limitOf(brief))

	return service.complete(ctx, instruction, nil, brief)
}

// Edit adjusts existing copy.
func (service *Service) Edit(
	ctx context.Context, text, edit string, brief Brief,
) (Draft, error) {
	if !validEdit[edit] {
		return Draft{}, ErrUnsupportedInstruction
	}
	if service.gateway == nil {
		return Draft{}, aigateway.ErrDisabled
	}
	instruction := "Rewrite the text below to be " + strings.ReplaceAll(edit, "_", " ") +
		". Preserve every fact, number, and template variable exactly."
	return service.complete(ctx, instruction, []string{text}, brief)
}

// complete runs the call and checks whatever comes back.
func (service *Service) complete(
	ctx context.Context, instruction string, parts []string, brief Brief,
) (Draft, error) {
	result, err := service.gateway.Complete(ctx, aigateway.Call{
		Task:        aigateway.TaskMarketingDraft,
		System:      marketingSystem,
		Instruction: instruction,
		Parts:       parts,
	})
	if err != nil {
		return Draft{}, err
	}
	text := strings.TrimSpace(result.Text)
	return Draft{
		Text: text, Generated: true,
		Provider: result.Provider, Model: result.Model,
		Findings: Check(text, brief),
	}, nil
}

// Check reports what is wrong with a piece of copy.
//
// It is exported and takes no gateway, so operator-written copy goes through
// exactly the same checks as generated copy. A checker that only ran on AI
// output would let a human write the message that breaks the send.
func Check(text string, brief Brief) []Finding {
	findings := make([]Finding, 0, 4)

	declared := make(map[string]bool, len(brief.Variables))
	for _, variable := range brief.Variables {
		declared[variable] = true
	}
	used := map[string]bool{}
	for _, match := range variablePattern.FindAllStringSubmatch(text, -1) {
		used[match[1]] = true
	}
	undeclared := make([]string, 0, len(used))
	for name := range used {
		if !declared[name] {
			undeclared = append(undeclared, name)
		}
	}
	sort.Strings(undeclared)
	for _, name := range undeclared {
		findings = append(findings, Finding{
			Code: FindingUndeclaredVariable,
			Detail: "{{" + name + "}} is not a variable this message can fill, " +
				"so it will render literally",
			Blocking: true,
		})
	}

	// A malformed variable is worse than an undeclared one: it does not even
	// look like a variable to the sender, so nothing catches it.
	if stripVariables(text) != "" && brokenVariable.MatchString(stripVariables(text)) {
		findings = append(findings, Finding{
			Code:     FindingBrokenVariable,
			Detail:   "a placeholder is malformed and will be sent as written",
			Blocking: true,
		})
	}

	if runes := len([]rune(text)); runes > limitOf(brief) {
		findings = append(findings, Finding{
			Code: FindingTooLong,
			Detail: fmt.Sprintf("%d characters, over the %d limit — the send fails or truncates",
				runes, limitOf(brief)),
			Blocking: true,
		})
	}

	lowered := strings.ToLower(text)
	for phrase, description := range forbiddenClaims {
		if strings.Contains(lowered, phrase) {
			findings = append(findings, Finding{
				Code:     FindingForbiddenClaim,
				Detail:   "contains " + description + " (\"" + phrase + "\")",
				Blocking: true,
			})
		}
	}

	for _, phrase := range urgencyPhrases {
		if strings.Contains(lowered, phrase) {
			findings = append(findings, Finding{
				Code: FindingUrgencyPressure,
				Detail: "\"" + phrase + "\" reads as pressure; keep it only if the " +
					"deadline is real",
				Blocking: false,
			})
			break
		}
	}

	if shouting(text) {
		findings = append(findings, Finding{
			Code:     FindingShouting,
			Detail:   "long runs of capitals read as shouting and trip spam filters",
			Blocking: false,
		})
	}

	sort.SliceStable(findings, func(left, right int) bool {
		return findings[left].Blocking && !findings[right].Blocking
	})
	return findings
}

// shouting reports sustained capitals, ignoring short acronyms like VPN.
func shouting(text string) bool {
	run := 0
	for _, character := range text {
		switch {
		case character >= 'A' && character <= 'Z':
			run++
			if run > 8 {
				return true
			}
		case character == ' ' || character == '!':
		default:
			run = 0
		}
	}
	return false
}

// stripVariables removes well-formed placeholders so the malformed-placeholder
// check does not match the correct ones.
func stripVariables(text string) string {
	return variablePattern.ReplaceAllString(text, "")
}

func wrapVariables(names []string) []string {
	wrapped := make([]string, 0, len(names))
	for _, name := range names {
		wrapped = append(wrapped, "{{"+name+"}}")
	}
	return wrapped
}

func limitOf(brief Brief) int {
	if brief.MaxRunes > 0 {
		return brief.MaxRunes
	}
	return MaxMessageRunes
}

func languageName(code string) string {
	if strings.EqualFold(code, "ru") {
		return "Russian"
	}
	return "English"
}

func truncate(text string, limit int) string {
	trimmed := strings.TrimSpace(text)
	if len(trimmed) <= limit {
		return trimmed
	}
	return trimmed[:limit]
}
