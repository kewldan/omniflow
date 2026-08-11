// Package aisupport builds the support desk's AI prompts and shapes the
// answers.
//
// Everything here produces a draft. Nothing sends, files, or decides — an
// authorised operator does that, from a surface that says plainly what came
// from a model. That is a product constraint rather than a setting, and it is
// why this package returns a Draft type that carries its own provenance instead
// of returning a bare string that a caller could pass straight to a customer.
//
// The prompts are here rather than in the panel for one practical reason: what
// a model is asked is the thing most likely to need correcting after a bad
// answer, and it should be reviewable in one file rather than spread through
// handlers.
package aisupport

import (
	"context"
	"errors"
	"strings"

	"github.com/omniflow/omniflow/internal/aigateway"
	"github.com/omniflow/omniflow/internal/airedact"
)

// Tone options for a rewrite. They are a closed set because a free-text tone
// instruction is an injection surface: an operator pasting a customer's words
// into a tone field would be handing the customer the prompt.
const (
	ToneShorter    = "shorter"
	ToneClearer    = "clearer"
	ToneFriendlier = "friendlier"
	ToneMoreFormal = "more_formal"
	ToneTechnical  = "more_technical"
)

var validTone = map[string]bool{
	ToneShorter: true, ToneClearer: true, ToneFriendlier: true,
	ToneMoreFormal: true, ToneTechnical: true,
}

// ErrUnsupportedTone reports a rewrite instruction outside the closed set.
var ErrUnsupportedTone = errors.New("unsupported rewrite tone")

// Draft is a model answer and everything needed to render it honestly.
//
// The provenance fields are not decoration. An operator about to send text to a
// customer needs to know it was generated, by which provider and model, and
// what left the installation to produce it — and a reviewer afterwards needs
// the same.
type Draft struct {
	Text string
	// Generated is always true. It exists so a caller cannot render a draft
	// through the same path as operator-written text by forgetting a flag.
	Generated bool
	Provider  string
	Model     string
	// Redacted is what was removed from the prompt on the way out.
	Redacted airedact.Result
}

// Service turns support content into drafts.
type Service struct {
	gateway *aigateway.Gateway
}

// New builds the service. A nil gateway is valid and reports every feature as
// unavailable, which is what an installation with AI switched off gets.
func New(gateway *aigateway.Gateway) *Service {
	return &Service{gateway: gateway}
}

// Available reports whether a task can run, so the panel can omit a button
// rather than offering one that always fails.
func (service *Service) Available(task string) bool {
	return service.gateway != nil && service.gateway.Enabled(task)
}

// Conversation is what the model is shown.
//
// Notes are deliberately included: an operator's private note is often where
// the actual diagnosis is, and the model summarising a ticket without it
// produces a summary that misses the point. They are redacted like everything
// else, and the resulting draft is never sent to the customer without a person
// reading it.
type Conversation struct {
	Subject  string
	Messages []Message
	Notes    []string
}

// Message is one turn.
type Message struct {
	Sender string
	Body   string
}

// parts flattens a conversation into the untrusted texts the gateway redacts.
func (conversation Conversation) parts() []string {
	parts := make([]string, 0, len(conversation.Messages)+len(conversation.Notes)+1)
	if conversation.Subject != "" {
		parts = append(parts, "Subject: "+conversation.Subject)
	}
	for _, message := range conversation.Messages {
		parts = append(parts, message.Sender+": "+message.Body)
	}
	for _, note := range conversation.Notes {
		parts = append(parts, "Internal note: "+note)
	}
	return parts
}

const summarySystem = "You summarise customer support conversations for an " +
	"operator who has not read them. Be factual and brief. Do not invent " +
	"details, do not guess at causes, and do not suggest actions."

// Summarise condenses a thread.
//
// The instruction asks for three specific things rather than "a summary",
// because the operator's actual question is always the same: what does this
// person want, what has already been tried, and what is still unanswered.
func (service *Service) Summarise(
	ctx context.Context, conversation Conversation,
) (Draft, error) {
	return service.run(ctx, aigateway.Call{
		Task:   aigateway.TaskTicketSummary,
		System: summarySystem,
		Instruction: "Summarise the conversation below in three short sections: " +
			"what the customer wants, what has already been attempted, and what " +
			"questions remain unanswered. If a section has nothing in it, say so " +
			"rather than inventing content.",
		Parts: conversation.parts(),
	})
}

const replySystem = "You draft replies for a customer support operator to " +
	"review and edit. Write only what the conversation supports. If the " +
	"conversation does not contain the answer, say that the operator needs to " +
	"check, rather than guessing. Never promise a refund, a credit, or a date."

// SuggestReply drafts an answer.
//
// The system prompt forbids promising a refund, a credit, or a date. Those are
// commitments only a person can make, and a model that makes one in a draft an
// operator sends without reading has committed the operator to it.
func (service *Service) SuggestReply(
	ctx context.Context, conversation Conversation, guidance string,
) (Draft, error) {
	instruction := "Draft a reply to the customer's most recent message, in the " +
		"language the customer used."
	if strings.TrimSpace(guidance) != "" {
		// Operator guidance is instruction rather than untrusted content: it is
		// what this operator is asking for. It is still length-bounded, because
		// a pasted conversation in this field would be content masquerading as
		// instruction.
		instruction += " The operator adds: " + truncate(guidance, 500)
	}
	return service.run(ctx, aigateway.Call{
		Task:        aigateway.TaskReplySuggest,
		System:      replySystem,
		Instruction: instruction,
		Parts:       conversation.parts(),
	})
}

const rewriteSystem = "You rewrite a support reply. Preserve every fact, name, " +
	"number, link placeholder, and template variable exactly. Change only the " +
	"wording."

// Rewrite adjusts an existing draft's tone.
//
// The tone is chosen from a closed set rather than typed, because a free-text
// instruction field is where an operator would paste a customer's words — and
// that hands the customer the prompt.
func (service *Service) Rewrite(
	ctx context.Context, text, tone string,
) (Draft, error) {
	if !validTone[tone] {
		return Draft{}, ErrUnsupportedTone
	}
	return service.run(ctx, aigateway.Call{
		Task:        aigateway.TaskReplySuggest,
		System:      rewriteSystem,
		Instruction: "Rewrite the text below to be " + strings.ReplaceAll(tone, "_", " ") + ".",
		Parts:       []string{text},
	})
}

const translateSystem = "You translate customer support text between Russian " +
	"and English. Preserve product names, plan names, currency amounts, links, " +
	"and {{template_variables}} exactly as they appear. Translate meaning " +
	"rather than words."

// Translate moves text between the two supported languages.
func (service *Service) Translate(
	ctx context.Context, text, target string,
) (Draft, error) {
	language := "English"
	if strings.EqualFold(target, "ru") {
		language = "Russian"
	}
	return service.run(ctx, aigateway.Call{
		Task:        aigateway.TaskTranslate,
		System:      translateSystem,
		Instruction: "Translate the text below into " + language + ".",
		Parts:       []string{text},
	})
}

const classifySystem = "You classify support tickets. Answer with short labels " +
	"only. If the conversation does not support a confident label, answer " +
	"'unclear' rather than guessing."

// Classification is a suggestion, never an assignment.
type Classification struct {
	Draft
	// Raw is the model's answer as returned. The panel parses it for display
	// and an operator picks what to apply — nothing here writes a priority or a
	// tag onto a ticket, because a mis-classified urgent ticket that nobody
	// chose is worse than an unclassified one.
	Raw string
}

// Classify suggests a category, priority, tags, sentiment, and whether the
// ticket looks like it needs escalating.
func (service *Service) Classify(
	ctx context.Context, conversation Conversation,
) (Classification, error) {
	draft, err := service.run(ctx, aigateway.Call{
		Task:   aigateway.TaskClassify,
		System: classifySystem,
		Instruction: "For the conversation below, give: a one-word category, a " +
			"priority from low/normal/high/urgent, up to three lowercase tags, the " +
			"customer's sentiment, and whether it should be escalated with a one-" +
			"sentence reason. Label each line.",
		Parts: conversation.parts(),
	})
	if err != nil {
		return Classification{}, err
	}
	return Classification{Draft: draft, Raw: draft.Text}, nil
}

// run is the single path to the gateway, so every answer carries provenance and
// no feature can accidentally return a bare string.
func (service *Service) run(ctx context.Context, call aigateway.Call) (Draft, error) {
	if service.gateway == nil {
		return Draft{}, aigateway.ErrDisabled
	}
	result, err := service.gateway.Complete(ctx, call)
	if err != nil {
		return Draft{}, err
	}
	return Draft{
		Text: strings.TrimSpace(result.Text), Generated: true,
		Provider: result.Provider, Model: result.Model, Redacted: result.Redacted,
	}, nil
}

// Preview describes what a task would send without sending it.
func (service *Service) Preview(conversation Conversation) airedact.Preview {
	return airedact.Describe(conversation.parts()...)
}

// truncate bounds operator-supplied instruction text.
func truncate(text string, limit int) string {
	trimmed := strings.TrimSpace(text)
	if len(trimmed) <= limit {
		return trimmed
	}
	return trimmed[:limit]
}
