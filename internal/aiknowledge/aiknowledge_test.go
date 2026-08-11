package aiknowledge

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

type grant map[string]bool

func (g grant) Allows(permission string) bool { return g[permission] }

// fixedIndex records what it was asked and with whose authority.
type fixedIndex struct {
	sources []Source
	asked   string
	grant   Grant
}

func (index *fixedIndex) Search(
	_ context.Context, query string, permitted Grant, limit int,
) ([]Source, error) {
	index.asked, index.grant = query, permitted
	if len(index.sources) > limit {
		return index.sources[:limit], nil
	}
	return index.sources, nil
}

func sources() []Source {
	return []Source{
		{
			ID: "t-1", Kind: KindTicket, Title: "Refund after a double charge",
			Href: "/admin/support/t-1", Excerpt: "We refunded the second charge.", Score: 0.9,
		},
		{
			ID: "d-1", Kind: KindDocumentation, Title: "Refund policy",
			Href: "/docs/refunds", Excerpt: "Refunds are approved by a person.", Score: 0.8,
		},
	}
}

func newService(t *testing.T, reply string, index *fixedIndex) (*Service, *spy) {
	t.Helper()
	provider := &spy{reply: reply}
	gateway := aigateway.New(aigateway.Options{
		Providers: []aigateway.Provider{provider},
		Tasks: map[string]aigateway.TaskConfig{
			aigateway.TaskReplySuggest: {
				Enabled: true, Provider: "spy", Model: "spy-model", MaxTokens: 600,
			},
		},
	})
	return New(gateway, index), provider
}

// The model does not choose what to retrieve, and retrieval runs against the
// asking operator's grant.
func TestRetrievalRunsFirstAndOnTheOperatorsAuthority(t *testing.T) {
	index := &fixedIndex{sources: sources()}
	service, provider := newService(t, "Refund it. [S1][S2]", index)

	authority := grant{"support.read": true}
	if _, err := service.Suggest(
		context.Background(), "customer was charged twice", authority,
	); err != nil {
		t.Fatalf("suggest: %v", err)
	}
	if index.asked != "customer was charged twice" {
		t.Fatalf("the index was asked something else: %q", index.asked)
	}
	if index.grant == nil || !index.grant.Allows("support.read") {
		t.Fatal("retrieval did not receive the operator's grant")
	}
	// The sources reach the prompt; nothing else does.
	if !strings.Contains(provider.seen.Prompt, "Refunds are approved by a person") {
		t.Fatalf("the sources did not reach the model: %q", provider.seen.Prompt)
	}
}

// A model that invents a citation invented the claim attached to it.
func TestAnInventedCitationDiscardsTheAnswer(t *testing.T) {
	index := &fixedIndex{sources: sources()}
	service, _ := newService(t, "Our policy is a full refund within 90 days. [S7]", index)

	_, err := service.Suggest(context.Background(), "refund window?", grant{})
	if !errors.Is(err, ErrUngrounded) {
		t.Fatalf("expected ErrUngrounded, got %v", err)
	}
	if !strings.Contains(err.Error(), "S7") {
		t.Fatalf("the invented citation was not named: %v", err)
	}
}

// An answer with no citations that does not say it cannot answer is an
// ungrounded answer wearing a helpful tone.
func TestAnUncitedAnswerIsRefused(t *testing.T) {
	index := &fixedIndex{sources: sources()}
	service, _ := newService(t, "Just refund them, it is usually fine.", index)

	if _, err := service.Suggest(
		context.Background(), "refund?", grant{},
	); !errors.Is(err, ErrUngrounded) {
		t.Fatalf("expected ErrUngrounded, got %v", err)
	}
}

// The honest non-answer is the one that keeps an operator's trust in the times
// it does answer, so it is not mistaken for an ungrounded one.
func TestDecliningToAnswerIsAllowedWithoutCitations(t *testing.T) {
	index := &fixedIndex{sources: sources()}
	service, _ := newService(t,
		"The sources do not contain anything about crypto payments.", index)

	suggestion, err := service.Suggest(context.Background(), "do we take crypto?", grant{})
	if err != nil {
		t.Fatalf("an honest non-answer was refused: %v", err)
	}
	if len(suggestion.Cited) != 0 {
		t.Fatalf("a non-answer cited something: %+v", suggestion.Cited)
	}
	if suggestion.Considered != 2 {
		t.Fatalf("the number considered was not reported: %d", suggestion.Considered)
	}
}

// A list of "things we looked at" trains operators to ignore the list, so only
// what was used is returned — with somewhere to open it.
func TestOnlyCitedSourcesAreReturnedAndEachIsOpenable(t *testing.T) {
	index := &fixedIndex{sources: sources()}
	service, _ := newService(t, "We refunded the duplicate charge. [S1]", index)

	suggestion, err := service.Suggest(context.Background(), "double charge", grant{})
	if err != nil {
		t.Fatalf("suggest: %v", err)
	}
	if len(suggestion.Cited) != 1 || suggestion.Cited[0].ID != "t-1" {
		t.Fatalf("the wrong sources were returned: %+v", suggestion.Cited)
	}
	if suggestion.Cited[0].Href == "" {
		t.Fatal("a citation nobody can follow is a footnote, not a source")
	}
	if !suggestion.Generated || suggestion.Provider != "spy" {
		t.Fatalf("provenance missing: %+v", suggestion)
	}
}

// A repeated citation is one source, in the order it was first used.
func TestCitationsAreDeduplicatedAndOrdered(t *testing.T) {
	index := &fixedIndex{sources: sources()}
	service, _ := newService(t, "Policy says [S2]. Precedent agrees [S1]. See [S2] again.", index)

	suggestion, err := service.Suggest(context.Background(), "refund", grant{})
	if err != nil {
		t.Fatalf("suggest: %v", err)
	}
	if len(suggestion.Cited) != 2 {
		t.Fatalf("citations were not deduplicated: %+v", suggestion.Cited)
	}
	if suggestion.Cited[0].ID != "d-1" || suggestion.Cited[1].ID != "t-1" {
		t.Fatalf("citations are not in order of first use: %+v", suggestion.Cited)
	}
}

// A question nothing matched produces "nothing found" rather than a model call.
func TestNoSourcesMeansNoModelCall(t *testing.T) {
	index := &fixedIndex{}
	service, provider := newService(t, "should not be reached", index)

	if _, err := service.Suggest(
		context.Background(), "anything", grant{},
	); !errors.Is(err, ErrNoSources) {
		t.Fatalf("expected ErrNoSources, got %v", err)
	}
	if provider.seen.Prompt != "" {
		t.Fatal("a question with no sources still reached the provider")
	}
}

// A feature that only worked with AI switched on would be one more thing that
// stops working when it is off.
func TestSimilarWorksWithoutAModel(t *testing.T) {
	index := &fixedIndex{sources: sources()}
	service := New(nil, index)

	if service.Available() {
		t.Fatal("a nil gateway reported the feature as available")
	}
	similar, err := service.Similar(context.Background(), "double charge", grant{}, 5)
	if err != nil {
		t.Fatalf("similar: %v", err)
	}
	if len(similar) != 2 {
		t.Fatalf("retrieval did not work without a model: %+v", similar)
	}
}

// The system prompt carries the two rules the checker also enforces.
func TestThePromptForbidsInventingAndUncitedClaims(t *testing.T) {
	index := &fixedIndex{sources: sources()}
	service, provider := newService(t, "Answer. [S1]", index)
	if _, err := service.Suggest(context.Background(), "q", grant{}); err != nil {
		t.Fatalf("suggest: %v", err)
	}
	system := strings.ToLower(provider.seen.System)
	for _, must := range []string{
		"never cite an identifier that is not in the list",
		"must cite a source", "never invent a price",
	} {
		if !strings.Contains(system, must) {
			t.Fatalf("the system prompt is missing %q: %q", must, provider.seen.System)
		}
	}
}
