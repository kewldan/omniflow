// Package adtracking holds the operator's own advertising measurement.
//
// It is the operator's analytics and never the project's. Nothing here is
// reported anywhere by default, nothing is sent to Anthropic or to the Omniflow
// project, and an installation that configures none of it renders nothing and
// stores nothing. That distinction is the whole reason this is a separate
// package from `internal/telemetry`: the two would otherwise look similar and
// mean opposite things.
//
// # Why a counter is a named identifier and never a snippet
//
// The obvious design is a textarea an operator pastes their counter code into.
// It is also a stored-cross-site-scripting hole with an operator-write gate in
// front of it: anybody who can write settings could put a script into every
// customer's browser, and a customer's browser holds subscription links, which
// are credentials.
//
// So a counter is a provider this package knows and an identifier validated
// against that provider's shape. The script that ends up in the page is written
// here, in this repository, and the only thing an operator supplies is a number.
// It is less flexible, and the flexibility it gives up is precisely the ability
// to run arbitrary code on a customer's session.
//
// # Why verification tags are not counters
//
// A `<meta name="google-site-verification">` sets no cookie, loads no script,
// and observes nobody. It exists so a webmaster tool can confirm that whoever
// pasted the tag controls the domain, and it has to be present for an anonymous
// fetch — before any consent could have been given, because the fetcher is not
// a person. They are therefore rendered unconditionally and validated as
// opaque tokens rather than as anything executable.
package adtracking

import (
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"
)

// Provider is a counter this package knows how to render.
type Provider string

const (
	// ProviderMetrica is Yandex Metrica, whose counter is a number.
	ProviderMetrica Provider = "yandex_metrica"
	// ProviderGA4 is Google Analytics 4, whose measurement ID is `G-` and a
	// token.
	ProviderGA4 Provider = "google_analytics"
)

// Settings is what an operator configured.
//
// Enabled is separate from "has an identifier" on purpose. Turning measurement
// off should not require deleting the identifiers, because an operator turning
// it off in a hurry — a regulator asked, a customer complained — should not
// have to find the numbers again to turn it back on.
type Settings struct {
	// Enabled is false by default and on a fresh installation. Nothing renders
	// and nothing is captured until somebody decides otherwise.
	Enabled bool `json:"enabled"`
	// Counters are the identifiers, by provider. An absent or empty value
	// renders nothing for that provider.
	Counters map[Provider]string `json:"counters,omitempty"`
	// Verifications are `<meta>` tags for webmaster tools. They are not subject
	// to Enabled or to consent: they observe nobody, and a verification that
	// only appears for consenting visitors verifies nothing.
	Verifications []Verification `json:"verifications,omitempty"`
}

// Verification is one `<meta name content>` pair.
type Verification struct {
	Name    string `json:"name"`
	Content string `json:"content"`
}

var (
	ErrUnknownProvider     = errors.New("unknown counter provider")
	ErrMalformedIdentifier = errors.New("malformed counter identifier")
	ErrUnknownVerification = errors.New("unknown verification tag")
	ErrMalformedToken      = errors.New("malformed verification token")
	ErrTooManyVerification = errors.New("too many verification tags")
)

// counterShapes are what each provider's identifier looks like.
//
// They are anchored and narrow because the value is interpolated into a script
// this package emits. A pattern that admitted a quote or a bracket would hand
// the injection back that naming the providers took away.
var counterShapes = map[Provider]*regexp.Regexp{
	ProviderMetrica: regexp.MustCompile(`^[0-9]{5,12}$`),
	ProviderGA4:     regexp.MustCompile(`^G-[A-Z0-9]{6,16}$`),
}

// verificationNames are the webmaster tools whose tags this will render.
//
// It is an allowlist rather than free text because `name` lands in an attribute
// on every page, and "any meta tag an operator wants" is a different and much
// larger feature than "prove you own this domain".
var verificationNames = map[string]bool{
	"google-site-verification":     true,
	"yandex-verification":          true,
	"msvalidate.01":                true,
	"facebook-domain-verification": true,
}

// verificationToken is what a webmaster tool issues: an opaque token. None of
// them contain a quote, a space, or a bracket.
var verificationToken = regexp.MustCompile(`^[A-Za-z0-9_.=-]{8,128}$`)

// maxVerifications bounds the head. Four tools are supported and an
// installation needs at most one tag each; the limit is generous rather than
// tight because the cost of a wrong guess is an operator who cannot add a tag.
const maxVerifications = 8

// CheckSettings validates what an operator saved.
func CheckSettings(settings Settings) error {
	for provider, identifier := range settings.Counters {
		shape, known := counterShapes[provider]
		if !known {
			return fmt.Errorf("%w: %q", ErrUnknownProvider, provider)
		}
		if identifier == "" {
			continue
		}
		if !shape.MatchString(identifier) {
			return fmt.Errorf("%w: %s expects %s", ErrMalformedIdentifier, provider, shapeHint(provider))
		}
	}
	if len(settings.Verifications) > maxVerifications {
		return fmt.Errorf("%w: %d, the limit is %d",
			ErrTooManyVerification, len(settings.Verifications), maxVerifications)
	}
	for _, verification := range settings.Verifications {
		if !verificationNames[verification.Name] {
			return fmt.Errorf("%w: %q", ErrUnknownVerification, verification.Name)
		}
		if !verificationToken.MatchString(verification.Content) {
			return fmt.Errorf("%w: %q", ErrMalformedToken, verification.Name)
		}
	}
	return nil
}

func shapeHint(provider Provider) string {
	switch provider {
	case ProviderMetrica:
		return "a counter number"
	case ProviderGA4:
		return "a measurement ID such as G-XXXXXXX"
	default:
		return "a valid identifier"
	}
}

// Normalise trims and drops what is empty, so a saved document never carries a
// provider with no identifier or a verification with no token.
func Normalise(settings Settings) Settings {
	counters := map[Provider]string{}
	for provider, identifier := range settings.Counters {
		identifier = strings.TrimSpace(identifier)
		if identifier != "" {
			counters[provider] = identifier
		}
	}
	verifications := make([]Verification, 0, len(settings.Verifications))
	seen := map[string]bool{}
	for _, verification := range settings.Verifications {
		verification.Name = strings.TrimSpace(strings.ToLower(verification.Name))
		verification.Content = strings.TrimSpace(verification.Content)
		if verification.Name == "" || verification.Content == "" || seen[verification.Name] {
			continue
		}
		seen[verification.Name] = true
		verifications = append(verifications, verification)
	}
	sort.Slice(verifications, func(first, second int) bool {
		return verifications[first].Name < verifications[second].Name
	})
	return Settings{
		Enabled: settings.Enabled, Counters: counters, Verifications: verifications,
	}
}

// Measurable reports whether anything would be rendered for a consenting
// visitor. It is what the public endpoint answers with when measurement is off:
// there is nothing to ask consent for, so no banner is shown.
func Measurable(settings Settings) bool {
	if !settings.Enabled {
		return false
	}
	for _, identifier := range settings.Counters {
		if identifier != "" {
			return true
		}
	}
	return false
}

// ProviderNames is the closed set of counters this build can render, so the
// settings screen offers what exists rather than a text field that fails on
// save.
func ProviderNames() []string {
	names := make([]string, 0, len(counterShapes))
	for provider := range counterShapes {
		names = append(names, string(provider))
	}
	sort.Strings(names)
	return names
}

// VerificationNames is the same for webmaster tags.
func VerificationNames() []string {
	names := make([]string, 0, len(verificationNames))
	for name := range verificationNames {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}
