package adtracking

import (
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"strings"
)

// The click identifier, and why it has to be carried rather than observed.
//
// An advertising platform tags the click it sold: Google appends `gclid`,
// Yandex `yclid`, Meta `fbclid`, Microsoft `msclkid`, TikTok `ttclid`. The
// platform then wants to be told which of those clicks turned into a paid
// order, so it can attribute the money and optimise the bidding.
//
// Nothing in a normal analytics setup can tell it. Payment here happens on the
// backend, often a day after the click, sometimes through a bank transfer an
// operator confirms by hand, and always outside the browser session that
// carried the identifier. A counter script watching the storefront sees the
// visit and never sees the money. That is why "no advertising channel can be
// attributed at all" was true even for an installation that had pasted a
// counter in.
//
// So the identifier is carried: captured on the first visit, held first-party,
// attached to the order, and exported when that order settles. What the
// operator uploads to the platform is a file — this repository makes no request
// to any advertising network, with or without consent. Sending a customer's
// purchase to Google because a settings toggle was on is not a thing an
// operator can be asked to discover from behaviour.

// Attribution is one order's advertising origin.
//
// It hangs off the order rather than the customer, deliberately. An order is a
// conversion and has an amount and a date, which is exactly what an offline
// upload needs. A customer is a person, and a click identifier attached to a
// person for the life of their account is a profile — a different thing, with
// different obligations, that nothing here needs.
type Attribution struct {
	// ClickID is the platform's own identifier for the click it sold.
	ClickID string `json:"clickId,omitempty"`
	// ClickSource names which platform issued it, so an export can be filtered
	// per network without parsing the identifier's shape.
	ClickSource string `json:"clickSource,omitempty"`

	Source   string `json:"source,omitempty"`
	Medium   string `json:"medium,omitempty"`
	Campaign string `json:"campaign,omitempty"`
	Content  string `json:"content,omitempty"`
	Term     string `json:"term,omitempty"`
}

// clickParameters maps a query parameter to the platform that issues it.
//
// The list is closed. An unrecognised parameter is not captured, because "store
// whatever the URL carried" is how a session identifier, an e-mail address, or
// somebody's password reset token ends up in an analytics table.
var clickParameters = map[string]string{
	"gclid":   "google",
	"gbraid":  "google",
	"wbraid":  "google",
	"yclid":   "yandex",
	"fbclid":  "meta",
	"msclkid": "microsoft",
	"ttclid":  "tiktok",
	"twclid":  "x",
}

// clickIdentifier is what every platform's identifier looks like: an opaque
// token. Anchored and bounded, because it is stored and later written into a
// CSV an operator uploads.
var clickIdentifier = regexp.MustCompile(`^[A-Za-z0-9_.-]{6,200}$`)

// campaignField bounds a UTM value. They are operator-authored strings that
// travel through a customer's URL bar, so they are length-bounded and stripped
// of anything that is not printable — a campaign name is a label, not a payload.
const maxCampaignField = 120

var (
	ErrNoAttribution      = errors.New("no advertising parameters")
	ErrMalformedClickID   = errors.New("malformed click identifier")
	ErrUnknownClickSource = errors.New("unknown click source")
)

// Capture reads the advertising parameters out of a landing URL's query.
//
// It returns ErrNoAttribution when there is nothing to record, which is the
// common case: most visits are not from an advertisement, and an attribution
// row for every visit would be a log of who arrived rather than a record of
// what an advertisement bought.
func Capture(rawQuery string) (Attribution, error) {
	values, err := url.ParseQuery(rawQuery)
	if err != nil {
		return Attribution{}, ErrNoAttribution
	}
	return CaptureValues(values)
}

// CaptureValues is Capture over already-parsed values.
func CaptureValues(values url.Values) (Attribution, error) {
	var attribution Attribution
	for parameter, platform := range clickParameters {
		candidate := strings.TrimSpace(values.Get(parameter))
		if candidate == "" {
			continue
		}
		if !clickIdentifier.MatchString(candidate) {
			// A malformed identifier is dropped rather than refused. The visitor
			// did nothing wrong and is trying to buy something; failing their
			// landing page over an advertising parameter would be a worse
			// outcome than not attributing one click.
			continue
		}
		attribution.ClickID = candidate
		attribution.ClickSource = platform
		break
	}

	attribution.Source = campaignField(values.Get("utm_source"))
	attribution.Medium = campaignField(values.Get("utm_medium"))
	attribution.Campaign = campaignField(values.Get("utm_campaign"))
	attribution.Content = campaignField(values.Get("utm_content"))
	attribution.Term = campaignField(values.Get("utm_term"))

	if attribution.Empty() {
		return Attribution{}, ErrNoAttribution
	}
	return attribution, nil
}

// CheckAttribution validates an attribution that arrived from a browser rather
// than being captured here, which is what the account endpoint receives.
func CheckAttribution(attribution Attribution) error {
	if attribution.ClickID != "" {
		if !clickIdentifier.MatchString(attribution.ClickID) {
			return ErrMalformedClickID
		}
		if _, known := knownSources()[attribution.ClickSource]; !known {
			return fmt.Errorf("%w: %q", ErrUnknownClickSource, attribution.ClickSource)
		}
	}
	if attribution.Empty() {
		return ErrNoAttribution
	}
	return nil
}

// Clean bounds the campaign fields of an attribution that arrived from a
// browser, so a caller cannot store two kilobytes of anything under `utm_term`.
func Clean(attribution Attribution) Attribution {
	return Attribution{
		ClickID:     strings.TrimSpace(attribution.ClickID),
		ClickSource: strings.TrimSpace(attribution.ClickSource),
		Source:      campaignField(attribution.Source),
		Medium:      campaignField(attribution.Medium),
		Campaign:    campaignField(attribution.Campaign),
		Content:     campaignField(attribution.Content),
		Term:        campaignField(attribution.Term),
	}
}

// Empty reports whether there is nothing worth storing.
func (attribution Attribution) Empty() bool {
	return attribution.ClickID == "" && attribution.Source == "" &&
		attribution.Medium == "" && attribution.Campaign == "" &&
		attribution.Content == "" && attribution.Term == ""
}

func knownSources() map[string]bool {
	sources := map[string]bool{}
	for _, platform := range clickParameters {
		sources[platform] = true
	}
	return sources
}

// campaignField trims, bounds, and strips control characters.
//
// The control-character strip is what keeps a newline out of a value that later
// becomes a CSV cell an operator uploads to an advertising platform.
func campaignField(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	cleaned := strings.Map(func(symbol rune) rune {
		if symbol < 0x20 || symbol == 0x7f {
			return -1
		}
		return symbol
	}, value)
	runes := []rune(cleaned)
	if len(runes) > maxCampaignField {
		runes = runes[:maxCampaignField]
	}
	return strings.TrimSpace(string(runes))
}
