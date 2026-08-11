// Package airedact removes what must never reach a model provider.
//
// Every AI feature in Omniflow sends text somebody else wrote — a support
// ticket, a customer's message, an operator's note — to a third party. That
// text routinely contains things the third party has no business holding: a
// subscription link that grants access, a payment reference, a bot token
// somebody pasted while asking for help.
//
// This package is the one place that decides what goes. It is deliberately
// separate from the gateway, has no network in it, and is tested on its own,
// because "we redact before sending" is a claim an installation makes to its
// customers and a claim that needs to be checkable.
//
// Two design choices follow from that.
//
// Redaction is allow-nothing rather than deny-known-bad for the categories that
// are unambiguous. A subscription link has a shape; anything with that shape
// goes, whether or not it is one of ours.
//
// A redacted span is replaced by a labelled placeholder rather than deleted. A
// model reading "the customer sent [SUBSCRIPTION_LINK]" can still reason about
// the conversation; one reading a sentence with a hole in it cannot, and an
// operator comparing the original with what was sent can see exactly what was
// removed.
package airedact

import (
	"regexp"
	"sort"
	"strings"
)

// Category names what was removed. They are separate because an operator
// reviewing a data-use preview needs to know which kind of thing left, and
// because an installation may reasonably care about one and not another.
const (
	CategorySubscriptionLink = "subscription_link"
	CategoryToken            = "token"
	CategoryPaymentCard      = "payment_card"
	CategoryEmail            = "email"
	CategoryPhone            = "phone"
	CategoryTelegramID       = "telegram_id"
	CategoryIPAddress        = "ip_address"
	CategoryUUID             = "uuid"
)

// rule is one pattern and the label that replaces it.
type rule struct {
	category string
	pattern  *regexp.Regexp
}

// rules run in order, and the order is load-bearing.
//
// A subscription link contains a UUID, and a UUID contains digit-and-dash runs
// that look like a spaced card number. Replacing an inner shape first leaves
// the outer one no longer matching its own pattern, so it survives as a
// partially-redacted string — worse than either outcome alone. The order is
// therefore widest shape first: links, then credentials, then identifiers, and
// only then the loose numeric patterns.
var rules = []rule{
	// Subscription and connection links. Any URL carrying a long opaque path is
	// treated as one, because a link that grants access is the worst thing on
	// this list to leak and the cost of over-redacting a harmless URL is that a
	// model sees a placeholder.
	{CategorySubscriptionLink, regexp.MustCompile(
		`(?i)(?:https?|vless|vmess|trojan|ss)://[^\s<>"']{8,}`)},

	// A Telegram bot token: a numeric account identifier, a colon, and a long
	// opaque secret. It has its own rule because the colon breaks the
	// alphanumeric run the generic key pattern looks for, and it is the
	// credential most likely to be pasted into a ticket by somebody asking why
	// their bot does not work.
	{CategoryToken, regexp.MustCompile(`\d{6,12}:[A-Za-z0-9_-]{30,}`)},

	// Bearer tokens, API keys, and anything shaped like one.
	{CategoryToken, regexp.MustCompile(
		`(?i)(?:sk-|pk-|xox[baprs]-|ghp_|api[_-]?key[=:\s]+)[A-Za-z0-9_-]{16,}`)},
	{CategoryToken, regexp.MustCompile(`[A-Za-z0-9+/]{40,}={0,2}`)},

	// Identifiers before numeric shapes: a UUID's digit runs match the card
	// pattern, and letting the card rule reach it first would leave half a UUID
	// in the prompt.
	{CategoryUUID, regexp.MustCompile(
		`[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}`)},

	{CategoryEmail, regexp.MustCompile(`[A-Za-z0-9._%+-]+@[A-Za-z0-9.-]+\.[A-Za-z]{2,}`)},

	// Telegram numeric identifiers, when labelled as such. An unlabelled
	// nine-digit number is more likely an order reference than an account, and
	// redacting every long integer would make a support conversation
	// unreadable.
	{CategoryTelegramID, regexp.MustCompile(
		`(?i)(?:telegram|tg|chat|user)[\s_-]*id[\s:=]+\d{5,15}`)},

	// International phone shapes. Deliberately broad: a number that turns out to
	// be an order reference becomes a placeholder in a prompt, while a number
	// that turns out to be a phone number is a disclosure.
	{CategoryPhone, regexp.MustCompile(`\+\d[\d\s()-]{7,17}\d`)},

	{CategoryIPAddress, regexp.MustCompile(`(?:\d{1,3}\.){3}\d{1,3}`)},

	// Payment card numbers, spaced or not. A shape test rather than a Luhn check
	// on purpose: a mistyped card number is still a card number as far as what
	// must not leave is concerned. It runs last because its shape is the least
	// specific here.
	{CategoryPaymentCard, regexp.MustCompile(`\d(?:[ -]?\d){12,18}`)},
}

// Result is redacted text and an account of what was taken out.
type Result struct {
	Text string
	// Counts is how many spans of each category were replaced. It drives the
	// data-use preview an operator sees before enabling a feature, and the
	// record of what a request contained without keeping the content.
	Counts map[string]int
}

// Categories lists the categories that were found, sorted, so a preview renders
// the same way twice.
func (result Result) Categories() []string {
	categories := make([]string, 0, len(result.Counts))
	for category := range result.Counts {
		categories = append(categories, category)
	}
	sort.Strings(categories)
	return categories
}

// Total is how many spans were removed altogether.
func (result Result) Total() int {
	total := 0
	for _, count := range result.Counts {
		total += count
	}
	return total
}

// placeholderPattern matches a label this package already wrote, so a second
// pass leaves it alone instead of redacting its own output.
var placeholderPattern = regexp.MustCompile(`^\[[A-Z_]+\]$`)

// Redact removes every category from the text.
//
// It is the only entry point, and it takes no options. A redaction that can be
// partially disabled is one somebody will partially disable, and the first time
// that matters is the time a subscription link reaches a provider.
func Redact(text string) Result {
	result := Result{Text: text, Counts: map[string]int{}}
	for _, current := range rules {
		result.Text = current.pattern.ReplaceAllStringFunc(result.Text, func(match string) string {
			if placeholderPattern.MatchString(match) {
				return match
			}
			result.Counts[current.category]++
			return placeholder(current.category)
		})
	}
	return result
}

// RedactAll applies Redact to several strings and merges the counts, so a
// prompt assembled from a ticket, its notes, and a customer profile reports one
// account of what left.
func RedactAll(texts ...string) ([]string, Result) {
	merged := Result{Counts: map[string]int{}}
	redacted := make([]string, 0, len(texts))
	for _, text := range texts {
		result := Redact(text)
		redacted = append(redacted, result.Text)
		for category, count := range result.Counts {
			merged.Counts[category] += count
		}
	}
	return redacted, merged
}

// placeholder is the labelled replacement. The label is upper-cased and
// bracketed so it is unmistakable in a prompt and greppable in a log.
func placeholder(category string) string {
	return "[" + strings.ToUpper(category) + "]"
}

// Preview describes what would leave the installation, without the text.
//
// It is what the data-use screen renders before an operator enables a feature:
// the categories and counts are enough to judge the exposure, and including the
// text would make the preview a second copy of the customer content it exists
// to protect.
type Preview struct {
	Categories []string       `json:"categories"`
	Counts     map[string]int `json:"counts"`
	// Characters is the size of what would be sent after redaction — the honest
	// measure of how much leaves, and what a cost estimate is built on.
	Characters int `json:"characters"`
}

// Describe builds the preview for a set of texts.
func Describe(texts ...string) Preview {
	redacted, merged := RedactAll(texts...)
	characters := 0
	for _, text := range redacted {
		characters += len(text)
	}
	return Preview{
		Categories: merged.Categories(),
		Counts:     merged.Counts,
		Characters: characters,
	}
}
