// Package aiknowledge answers a support question from sources an operator can
// open.
//
// It exists because an ungrounded suggestion is worse than no suggestion. A
// model asked "how do I answer this?" will produce a confident, plausible reply
// built from nothing, and a support operator under time pressure will send it.
// Everything here is arranged so that cannot happen quietly:
//
// The model is given sources and asked to answer from them. It does not choose
// what to retrieve — retrieval runs first, against an index the installation
// controls, and the model sees only what came back.
//
// Every claim must cite a source it was given. A suggestion citing something
// that was not in the set is discarded rather than shown with a footnote
// removed, because a model that invents a citation has invented the claim too.
//
// A question with no relevant sources produces "I do not have anything on
// this", which is the honest answer and the one that keeps the operator's trust
// in the times it does answer.
package aiknowledge

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"

	"github.com/omniflow/omniflow/internal/aigateway"
)

var (
	// ErrUngrounded reports an answer that cited nothing, or cited something it
	// was not given. Both are the same failure: the answer is not traceable to
	// material the installation holds.
	ErrUngrounded = errors.New("the answer was not grounded in the sources provided")
	// ErrNoSources reports a question nothing matched. It is a normal outcome
	// and callers render it as "nothing found" rather than as a failure.
	ErrNoSources = errors.New("no sources matched the question")
)

// Kinds of source. They are distinguished because an operator reads them
// differently: a resolved ticket is precedent, and documentation is policy.
const (
	KindTicket        = "ticket"
	KindDocumentation = "documentation"
	KindCannedReply   = "canned_response"
)

// Source is one retrievable piece of material.
type Source struct {
	// ID is what the model cites. It is short and stable so a citation is
	// checkable by string comparison rather than by fuzzy matching.
	ID    string `json:"id"`
	Kind  string `json:"kind"`
	Title string `json:"title"`
	// Href is where an operator opens it. A citation nobody can follow is a
	// footnote, not a source.
	Href string `json:"href"`
	// Excerpt is the text shown to the model. It is an excerpt rather than the
	// whole document because the budget is real and because a model given forty
	// pages cites the wrong paragraph.
	Excerpt string `json:"excerpt"`
	// Score is how well it matched, published so an operator can see that the
	// third result was a stretch.
	Score float64 `json:"score"`
}

// Index retrieves candidate sources.
//
// It is an interface because retrieval belongs to the installation's data: the
// PostgreSQL adapter searches resolved tickets and published documentation, and
// a test supplies a fixed set. What matters here is that retrieval happens
// before the model call and is not something the model can steer.
type Index interface {
	// Search returns candidates most relevant first. It takes the operator's
	// grant so a suggestion can never be built from a ticket the asking
	// operator could not open.
	Search(ctx context.Context, query string, permitted Grant, limit int) ([]Source, error)
}

// Grant is what the asking operator may do, the same shape every other AI
// surface uses.
type Grant interface {
	Allows(permission string) bool
}

// Suggestion is a grounded answer.
type Suggestion struct {
	// Text is the answer. It is a draft: no path sends it without a person
	// accepting it.
	Text string `json:"text"`
	// Cited are the sources the answer actually used, in the order it used
	// them. Sources that were retrieved and not cited are dropped, because a
	// list of "things we looked at" trains operators to ignore the list.
	Cited []Source `json:"cited"`
	// Considered is how many were retrieved, so "we found twelve and used two"
	// is visible without listing ten irrelevant links.
	Considered int    `json:"considered"`
	Generated  bool   `json:"generated"`
	Provider   string `json:"provider,omitempty"`
	Model      string `json:"model,omitempty"`
}

// Service produces grounded suggestions.
type Service struct {
	gateway *aigateway.Gateway
	index   Index
	// maxSources bounds what reaches the model. It is small on purpose: the
	// marginal source past about six adds cost and dilutes the answer.
	maxSources int
}

// DefaultMaxSources is how many candidates are shown to the model.
const DefaultMaxSources = 6

// New builds the service.
func New(gateway *aigateway.Gateway, index Index) *Service {
	return &Service{gateway: gateway, index: index, maxSources: DefaultMaxSources}
}

// Available reports whether grounded suggestion can run.
func (service *Service) Available() bool {
	return service.gateway != nil && service.index != nil &&
		service.gateway.Enabled(aigateway.TaskReplySuggest)
}

const knowledgeSystem = "You answer a support operator's question using only the " +
	"sources given to you. Every factual sentence must cite a source by its " +
	"identifier in square brackets, like [S2]. Never cite an identifier that is " +
	"not in the list. If the sources do not answer the question, say exactly " +
	"what is missing and cite nothing. Never invent a price, a date, a policy, " +
	"or a procedure."

// Suggest answers one question from the installation's own material.
func (service *Service) Suggest(
	ctx context.Context, question string, grant Grant,
) (Suggestion, error) {
	if service.gateway == nil || service.index == nil {
		return Suggestion{}, aigateway.ErrDisabled
	}

	// Retrieval runs first and against the asking operator's grant, so a
	// suggestion cannot be assembled from a ticket they could not open.
	candidates, err := service.index.Search(ctx, question, grant, service.maxSources)
	if err != nil {
		return Suggestion{}, err
	}
	if len(candidates) == 0 {
		return Suggestion{}, ErrNoSources
	}
	if len(candidates) > service.maxSources {
		candidates = candidates[:service.maxSources]
	}

	// The labels are positional and short, so a citation is checkable by string
	// comparison. Using the real identifiers would put ticket ids and document
	// slugs into the prompt for no benefit.
	labelled := make(map[string]Source, len(candidates))
	parts := make([]string, 0, len(candidates)+1)
	parts = append(parts, "Operator question: "+clip(question, 800))
	for index, source := range candidates {
		label := fmt.Sprintf("S%d", index+1)
		labelled[label] = source
		parts = append(parts, fmt.Sprintf("[%s] (%s) %s\n%s",
			label, source.Kind, source.Title, clip(source.Excerpt, 1200)))
	}

	result, err := service.gateway.Complete(ctx, aigateway.Call{
		Task:   aigateway.TaskReplySuggest,
		System: knowledgeSystem,
		Instruction: "Answer the operator's question using only the sources below. " +
			"Cite each source you use as [S1], [S2], and so on. If the sources do " +
			"not contain the answer, say so and cite nothing.",
		Parts: parts,
	})
	if err != nil {
		return Suggestion{}, err
	}

	text := strings.TrimSpace(result.Text)
	cited, unknown := citationsIn(text, labelled)
	if len(unknown) > 0 {
		// A model that invented a citation invented the claim attached to it.
		// Stripping the bad footnote and showing the rest would leave the
		// invention on screen looking supported.
		return Suggestion{}, fmt.Errorf("%w: cited %s", ErrUngrounded, strings.Join(unknown, ", "))
	}
	if len(cited) == 0 && !declinesToAnswer(text) {
		// An answer with no citations that does not say it cannot answer is an
		// ungrounded answer wearing a helpful tone.
		return Suggestion{}, ErrUngrounded
	}

	return Suggestion{
		Text: text, Cited: cited, Considered: len(candidates),
		Generated: true, Provider: result.Provider, Model: result.Model,
	}, nil
}

var citationPattern = regexp.MustCompile(`\[(S\d{1,2})\]`)

// citationsIn returns the sources an answer cited, in order of first use, and
// any label it cited that was never provided.
func citationsIn(text string, labelled map[string]Source) (cited []Source, unknown []string) {
	seen := map[string]bool{}
	cited = make([]Source, 0, len(labelled))
	for _, match := range citationPattern.FindAllStringSubmatch(text, -1) {
		label := match[1]
		if seen[label] {
			continue
		}
		seen[label] = true
		source, provided := labelled[label]
		if !provided {
			unknown = append(unknown, label)
			continue
		}
		cited = append(cited, source)
	}
	sort.Strings(unknown)
	return cited, unknown
}

// declinesToAnswer recognises the honest non-answer, so it is not mistaken for
// an ungrounded one.
func declinesToAnswer(text string) bool {
	lowered := strings.ToLower(text)
	for _, phrase := range []string{
		"do not have", "don't have", "not contain", "no information",
		"cannot answer", "can't answer", "nothing on this", "does not cover",
	} {
		if strings.Contains(lowered, phrase) {
			return true
		}
	}
	return false
}

// Similar returns related material without generating anything.
//
// It is exported separately because it is useful on its own: an operator
// looking at a ticket wants "three others like this" whether or not AI is
// configured, and retrieval needs no model. A feature that only worked with AI
// switched on would be one more thing that stops working when it is off.
func (service *Service) Similar(
	ctx context.Context, question string, grant Grant, limit int,
) ([]Source, error) {
	if service.index == nil {
		return nil, ErrNoSources
	}
	if limit <= 0 || limit > 20 {
		limit = service.maxSources
	}
	return service.index.Search(ctx, question, grant, limit)
}

func clip(text string, limit int) string {
	trimmed := strings.TrimSpace(text)
	if len(trimmed) <= limit {
		return trimmed
	}
	return trimmed[:limit] + "…"
}
