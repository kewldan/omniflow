// Package notice holds the transactional notices an operator may reword.
//
// The messages this covers are the ones the installation sends on its own
// initiative because something happened to a subscription: it is about to
// expire, the traffic allowance is nearly gone, an automatic charge failed.
// They are the messages an operator most wants to change — they carry the
// installation's voice to every customer, repeatedly — and the ones they
// currently cannot, because the wording is compiled in.
//
// # Why placeholders rather than format verbs
//
// The compiled catalogue uses `fmt.Sprintf` and positional verbs. That is safe
// for text written beside the call that renders it and unsafe for text written
// by somebody in a browser: `%d` where a string arrives prints
// `%!d(string=Pro)`, a verb too many prints `%!(EXTRA ...)`, and neither fails
// loudly — they simply reach the customer looking broken.
//
// So an override names its values: `{days}`, `{plan}`, `{until}`. A placeholder
// that is not declared for that notice is refused when it is saved rather than
// discovered when it is sent, and a notice with no variables cannot acquire one
// by being typed.
//
// # Why this package owns the defaults
//
// The default wording lives here, in the same `{name}` form an override uses,
// rather than in the bot's catalogue. Two copies would drift, and the panel has
// to be able to show an operator the text they are replacing — which means the
// default has to be readable from a process that has no bot in it.
package notice

import (
	"errors"
	"fmt"
	"sort"
	"strings"
	"unicode/utf8"
)

// Code identifies one overridable notice.
type Code string

const (
	CodeExpiry               Code = "expiry"
	CodeTraffic              Code = "traffic"
	CodeRenewal              Code = "renewal"
	CodeGrace                Code = "grace"
	CodeRecovery             Code = "recovery"
	CodeFulfillmentSucceeded Code = "fulfillment_succeeded"
	CodeFulfillmentFailed    Code = "fulfillment_failed"
	CodeDunningRetry         Code = "dunning_retry"
	CodeDunningAbandoned     Code = "dunning_abandoned"
)

// Variable is one value a notice can carry.
//
// Sample is what a preview substitutes. It is part of the definition rather
// than invented by the preview screen so that the panel and any test render the
// same thing, and so an operator judging "does this fit on a phone" is judging
// against a plausible length rather than against `{plan}`.
type Variable struct {
	Name    string `json:"name"`
	Purpose string `json:"purpose"`
	Sample  string `json:"sample"`
}

// Definition is one notice and everything an operator needs to reword it.
type Definition struct {
	Code Code `json:"code"`
	// Variables may be empty. A notice with nothing to substitute is still worth
	// overriding — most of the wording an installation wants to change carries
	// no values at all.
	Variables []Variable `json:"variables"`
	// Default is the compiled wording per locale, which is what an override
	// replaces and what deleting one restores.
	Default map[string]string `json:"default"`
}

// maxBody bounds an override.
//
// Telegram accepts 4096 characters in a message. The limit here is half that,
// because a notice is prefixed with the subscription line and suffixed with a
// keyboard, and a message refused by Telegram for length would fail at delivery
// — long after the operator who wrote it has left the screen.
const maxBody = 2000

var (
	// ErrUnknownNotice is a code that is not overridable.
	ErrUnknownNotice = errors.New("unknown notice")
	// ErrUnknownVariable is a placeholder the notice does not carry. It is the
	// error worth having: `{name}` in a notice that has no customer name would
	// otherwise reach every customer as literal braces.
	ErrUnknownVariable = errors.New("unknown variable")
	// ErrUnsupportedMarkup is a tag Telegram does not accept.
	ErrUnsupportedMarkup = errors.New("unsupported markup")
	ErrEmptyBody         = errors.New("empty body")
	ErrBodyTooLong       = errors.New("body too long")
)

// Locales are the two the product ships. A notice must have both: an
// installation that overrides only English silently keeps the shipped Russian,
// which is defensible, so the store holds one row per locale and either may be
// absent.
var Locales = []string{"en", "ru"}

// Lookup finds a definition by code.
func Lookup(code string) (Definition, bool) {
	for _, definition := range definitions {
		if string(definition.Code) == code {
			return definition, true
		}
	}
	return Definition{}, false
}

// Definitions is every overridable notice, in a stable order.
func Definitions() []Definition {
	out := make([]Definition, len(definitions))
	copy(out, definitions)
	return out
}

// Check validates an operator-authored body.
//
// It is deliberately strict about two things and permissive about everything
// else. A placeholder must be one the notice carries, and the markup must be a
// tag Telegram accepts — both of those produce a message that fails or looks
// broken at the customer, hours later, with nobody watching. Everything else is
// wording, and wording is the operator's business.
func Check(definition Definition, body string) error {
	body = strings.TrimSpace(body)
	if body == "" {
		return ErrEmptyBody
	}
	if utf8.RuneCountInString(body) > maxBody {
		return fmt.Errorf("%w: %d characters, the limit is %d",
			ErrBodyTooLong, utf8.RuneCountInString(body), maxBody)
	}

	declared := make(map[string]bool, len(definition.Variables))
	for _, variable := range definition.Variables {
		declared[variable.Name] = true
	}
	for _, name := range Placeholders(body) {
		if !declared[name] {
			return fmt.Errorf("%w: {%s}", ErrUnknownVariable, name)
		}
	}
	return checkMarkup(body)
}

// Placeholders returns every `{name}` in a body, in order of appearance.
//
// Anything that is not a plain identifier inside the braces is not a
// placeholder at all — it is text that happens to contain a brace, which a
// notice is entitled to do.
func Placeholders(body string) []string {
	var names []string
	for index := 0; index < len(body); index++ {
		if body[index] != '{' {
			continue
		}
		end := strings.IndexByte(body[index:], '}')
		if end < 0 {
			break
		}
		name := body[index+1 : index+end]
		if isIdentifier(name) {
			names = append(names, name)
		}
		index += end
	}
	return names
}

func isIdentifier(value string) bool {
	if value == "" {
		return false
	}
	for _, symbol := range value {
		switch {
		case symbol >= 'a' && symbol <= 'z',
			symbol >= 'A' && symbol <= 'Z',
			symbol >= '0' && symbol <= '9':
		default:
			return false
		}
	}
	return true
}

// Render substitutes declared values into a body.
//
// A placeholder with no value becomes empty rather than staying as literal
// braces. By the time this runs the body has already been checked against the
// definition, so a missing value means the caller had nothing to put there —
// and an empty gap reads better to a customer than `{days}` does.
func Render(body string, values map[string]string) string {
	if len(values) == 0 && !strings.ContainsRune(body, '{') {
		return body
	}
	var out strings.Builder
	out.Grow(len(body))
	for index := 0; index < len(body); index++ {
		if body[index] != '{' {
			out.WriteByte(body[index])
			continue
		}
		end := strings.IndexByte(body[index:], '}')
		if end < 0 {
			out.WriteString(body[index:])
			break
		}
		name := body[index+1 : index+end]
		if !isIdentifier(name) {
			out.WriteString(body[index : index+end+1])
			index += end
			continue
		}
		out.WriteString(values[name])
		index += end
	}
	return out.String()
}

// Samples is the value map a preview renders against.
func Samples(definition Definition) map[string]string {
	values := make(map[string]string, len(definition.Variables))
	for _, variable := range definition.Variables {
		values[variable.Name] = variable.Sample
	}
	return values
}

// telegramTags is the markup Telegram accepts in a message with parse_mode
// HTML. Anything else is refused at save time rather than producing a delivery
// that fails at the customer.
var telegramTags = map[string]bool{
	"b": true, "strong": true, "i": true, "em": true, "u": true,
	"s": true, "strike": true, "del": true, "code": true, "pre": true,
	"a": true,
}

// checkMarkup refuses tags Telegram does not accept, and links that are not
// https.
//
// The tags are checked rather than stripped. Stripping would silently publish
// something other than what the operator wrote, and the point of an override is
// that the operator decides what it says.
func checkMarkup(body string) error {
	var open []string
	for index := 0; index < len(body); index++ {
		if body[index] != '<' {
			continue
		}
		// Every `<` has to open or close a tag. Telegram parses the body as HTML
		// and refuses a message with a stray one, so "5 < 10" is caught here,
		// while the operator is still looking at the screen, rather than at
		// delivery time with nobody watching. The refusal says to write `&lt;`.
		end := strings.IndexByte(body[index:], '>')
		if end < 0 || !opensATag(body[index+1:]) {
			return fmt.Errorf(
				"%w: a stray `<`; write `&lt;` for a literal one", ErrUnsupportedMarkup)
		}
		inner := strings.TrimSpace(body[index+1 : index+end])
		index += end

		closing := strings.HasPrefix(inner, "/")
		inner = strings.TrimPrefix(inner, "/")
		name, attributes, _ := strings.Cut(inner, " ")
		name = strings.ToLower(name)
		if !telegramTags[name] {
			return fmt.Errorf("%w: <%s>", ErrUnsupportedMarkup, name)
		}
		if closing {
			if len(open) == 0 || open[len(open)-1] != name {
				return fmt.Errorf("%w: </%s> closes nothing", ErrUnsupportedMarkup, name)
			}
			open = open[:len(open)-1]
			continue
		}
		if name == "a" {
			if err := checkLink(attributes); err != nil {
				return err
			}
		} else if strings.TrimSpace(attributes) != "" {
			return fmt.Errorf("%w: <%s> takes no attributes", ErrUnsupportedMarkup, name)
		}
		open = append(open, name)
	}
	if len(open) > 0 {
		sort.Strings(open)
		return fmt.Errorf("%w: <%s> is never closed", ErrUnsupportedMarkup, open[0])
	}
	return nil
}

// opensATag reports whether what follows a `<` can begin a tag at all.
//
// A letter opens one and `/` closes one. Anything else — a space, a digit, the
// end of the body — is a stray angle bracket, which is text the operator meant
// literally and Telegram will refuse.
func opensATag(rest string) bool {
	if rest == "" {
		return false
	}
	symbol := rest[0]
	return symbol == '/' ||
		(symbol >= 'a' && symbol <= 'z') ||
		(symbol >= 'A' && symbol <= 'Z')
}

// checkLink allows exactly one attribute and exactly one scheme.
//
// https only, for the same reason the information pages refuse anything else: a
// `tg://` or `javascript:` target in a message the installation sends on its
// own initiative is a link the customer has every reason to trust and no way to
// inspect.
func checkLink(attributes string) error {
	name, value, found := strings.Cut(strings.TrimSpace(attributes), "=")
	if !found || strings.ToLower(strings.TrimSpace(name)) != "href" {
		return fmt.Errorf("%w: <a> needs an href", ErrUnsupportedMarkup)
	}
	target := strings.Trim(strings.TrimSpace(value), `"'`)
	if !strings.HasPrefix(strings.ToLower(target), "https://") {
		return fmt.Errorf("%w: a link must be https", ErrUnsupportedMarkup)
	}
	return nil
}
