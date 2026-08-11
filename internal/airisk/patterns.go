package airisk

import (
	"crypto/sha256"
	"encoding/hex"
	"sort"
	"strings"
	"time"
)

// Cross-ticket pattern detection.
//
// One ticket saying "send me your login code" is a customer who has been
// phished. Nine tickets across six accounts saying it within an hour is a
// campaign, and the second is invisible from inside any single conversation.
//
// The detection is deliberately not a model. It runs on structured signals a
// caller has already extracted — a fingerprint, a ticket, a time — and produces
// a count. A count is reproducible, explainable to the person whose account it
// concerns, and cheap enough to run on every new ticket. A model would be none
// of those and would additionally require sending nine customers' messages
// somewhere to learn something arithmetic could tell us.
//
// Nothing here sees content. An Observation carries a fingerprint, never the
// value it was derived from, which is what makes it safe to correlate across
// customers that the operator viewing the result may not all be entitled to
// read: a pattern names how many accounts, and the caller resolves which ones
// under its own permission check.

// Signal kinds that can be correlated. The set is closed because each one is a
// deliberate decision that correlating it is proportionate — an installation
// does not get to start fingerprinting arbitrary fields by passing a new string.
const (
	SignalPaymentMethod = "payment_method"
	SignalDeviceID      = "device"
	SignalIPPrefix      = "ip_prefix"
	SignalMessageShape  = "message_shape"
	SignalLinkHost      = "link_host"
	SignalReferralCode  = "referral_code"
	SignalContactHandle = "contact_handle"
)

var correlatable = map[string]bool{
	SignalPaymentMethod: true, SignalDeviceID: true, SignalIPPrefix: true,
	SignalMessageShape: true, SignalLinkHost: true, SignalReferralCode: true,
	SignalContactHandle: true,
}

// Observation is one minimized data point.
//
// There is no field for content and no field for a customer's name, email, or
// telephone number. The caller fingerprints whatever it observed before
// constructing one of these, so the value never enters this package and cannot
// leak from it.
type Observation struct {
	Kind string
	// Fingerprint is an opaque, stable digest of the observed value. Two
	// observations of the same payment method produce the same fingerprint and
	// nothing else about it.
	Fingerprint string
	TicketID    string
	// CustomerRef is an opaque handle. It is compared and counted, never
	// displayed: the caller maps it back to a customer only for the operators
	// entitled to see that customer.
	CustomerRef string
	At          time.Time
}

// Fingerprint derives the opaque digest for a value.
//
// The salt is the installation's, so fingerprints are not comparable between
// installations and a leaked table of them cannot be reversed with a rainbow
// table of common values — which matters most for exactly the fields worth
// correlating, since there are not many plausible payment tokens.
func Fingerprint(salt, kind, value string) string {
	normalised := strings.ToLower(strings.TrimSpace(value))
	if normalised == "" {
		return ""
	}
	digest := sha256.Sum256([]byte(salt + "\x00" + kind + "\x00" + normalised))
	// Half the digest is ample for correlation and halves what a leak exposes.
	return hex.EncodeToString(digest[:16])
}

// Pattern is a correlation worth an operator's look.
type Pattern struct {
	Kind string `json:"kind"`
	// Fingerprint identifies the shared value without revealing it, so two
	// patterns can be told apart and neither discloses anything.
	Fingerprint string `json:"fingerprint"`

	// TicketIDs are the conversations involved, so an operator can open them —
	// the permission check for each happens where they are opened.
	TicketIDs []string `json:"ticketIds"`
	// Customers is a count rather than a list. The number is what makes the
	// pattern interesting, and the identities are what the operator may not all
	// be entitled to.
	Customers int `json:"customers"`

	First time.Time `json:"first"`
	Last  time.Time `json:"last"`
	// Span is how tightly clustered it is. Six accounts sharing a device over a
	// year is a family; over four minutes it is not.
	Span time.Duration `json:"span"`

	// Confidence is coarse for the same reason an assessment's is: a percentage
	// invites acting on a difference that means nothing.
	Confidence string `json:"confidence"`
	// Explanation is the sentence an operator reads, and it is arithmetic
	// rather than inference — "six accounts, four minutes" is checkable.
	Explanation string `json:"explanation"`
}

// RecommendsReview is the strongest thing a pattern says. As everywhere else
// here, the recommendation is always to look.
func (pattern Pattern) RecommendsReview() bool { return pattern.Confidence != ConfidenceLow }

// PatternOptions tunes detection.
type PatternOptions struct {
	// MinCustomers is how many distinct accounts must share a value. Two is
	// noise — people share households, offices, and payment cards — so the
	// default is three.
	MinCustomers int
	// Window bounds how far apart observations may be and still correlate.
	Window time.Duration
	// MaxPatterns bounds the result, so a busy installation gets the tightest
	// clusters rather than a page of coincidences.
	MaxPatterns int
}

// Detection defaults.
const (
	DefaultMinCustomers = 3
	DefaultWindow       = 24 * time.Hour
	DefaultMaxPatterns  = 20
)

// DetectPatterns correlates observations across tickets.
//
// It returns the tightest clusters first, because an operator reading three
// findings reads the first one properly.
func DetectPatterns(observations []Observation, options PatternOptions) []Pattern {
	if options.MinCustomers <= 0 {
		options.MinCustomers = DefaultMinCustomers
	}
	if options.Window <= 0 {
		options.Window = DefaultWindow
	}
	if options.MaxPatterns <= 0 {
		options.MaxPatterns = DefaultMaxPatterns
	}

	grouped := map[string][]Observation{}
	for _, observation := range observations {
		if !correlatable[observation.Kind] || observation.Fingerprint == "" {
			// An unrecognised kind is dropped rather than correlated. Silently
			// accepting one would be how an installation starts fingerprinting a
			// field nobody decided was proportionate.
			continue
		}
		if observation.TicketID == "" || observation.CustomerRef == "" {
			continue
		}
		key := observation.Kind + "\x00" + observation.Fingerprint
		grouped[key] = append(grouped[key], observation)
	}

	patterns := make([]Pattern, 0, len(grouped))
	for _, group := range grouped {
		if pattern, found := clusterOf(group, options); found {
			patterns = append(patterns, pattern)
		}
	}

	sort.SliceStable(patterns, func(left, right int) bool {
		if patterns[left].Customers != patterns[right].Customers {
			return patterns[left].Customers > patterns[right].Customers
		}
		// A tighter span is more interesting at the same account count.
		return patterns[left].Span < patterns[right].Span
	})
	if len(patterns) > options.MaxPatterns {
		patterns = patterns[:options.MaxPatterns]
	}
	return patterns
}

// clusterOf finds the densest window within one fingerprint's observations.
func clusterOf(group []Observation, options PatternOptions) (Pattern, bool) {
	sort.SliceStable(group, func(left, right int) bool {
		return group[left].At.Before(group[right].At)
	})

	best := Pattern{}
	found := false
	// A sliding window over a sorted list: for each start, extend while the
	// observations stay inside the window. The alternative — correlating
	// everything ever seen — would report a shared office as a fraud ring.
	for start := range group {
		customers := map[string]bool{}
		tickets := map[string]bool{}
		last := group[start].At
		for index := start; index < len(group); index++ {
			if group[index].At.Sub(group[start].At) > options.Window {
				break
			}
			customers[group[index].CustomerRef] = true
			tickets[group[index].TicketID] = true
			last = group[index].At
		}
		if len(customers) < options.MinCustomers {
			continue
		}
		span := last.Sub(group[start].At)
		if found && (len(customers) < best.Customers ||
			(len(customers) == best.Customers && span >= best.Span)) {
			continue
		}

		best = Pattern{
			Kind: group[start].Kind, Fingerprint: group[start].Fingerprint,
			TicketIDs: sortedKeys(tickets), Customers: len(customers),
			First: group[start].At.UTC(), Last: last.UTC(), Span: span,
		}
		found = true
	}
	if !found {
		return Pattern{}, false
	}

	best.Confidence = confidenceFor(best, options)
	best.Explanation = explain(best)
	return best, true
}

// confidenceFor grades a cluster on how unlikely the coincidence is.
//
// It is arithmetic on the account count and the span, deliberately, so an
// operator can disagree with it specifically rather than with a score.
func confidenceFor(pattern Pattern, options PatternOptions) string {
	tight := pattern.Span <= options.Window/8
	many := pattern.Customers >= options.MinCustomers*2
	// Clearly above the floor is not the same as barely over it. Three accounts
	// is where this becomes worth a look; five in the same minute is not a
	// coincidence anybody has to weigh.
	wellOver := pattern.Customers >= options.MinCustomers+2
	switch {
	case many, tight && wellOver:
		return ConfidenceHigh
	case many || tight:
		return ConfidenceMedium
	default:
		return ConfidenceLow
	}
}

func explain(pattern Pattern) string {
	subject := map[string]string{
		SignalPaymentMethod: "share a payment method",
		SignalDeviceID:      "share a device",
		SignalIPPrefix:      "share a network",
		SignalMessageShape:  "sent a message with the same shape",
		SignalLinkHost:      "linked to the same host",
		SignalReferralCode:  "used the same referral code",
		SignalContactHandle: "gave the same contact handle",
	}[pattern.Kind]
	if subject == "" {
		subject = "share a signal"
	}
	return plural(pattern.Customers, "account") + " across " +
		plural(len(pattern.TicketIDs), "ticket") + " " + subject +
		", first seen " + humanSpan(pattern.Span) + " apart."
}

func plural(count int, noun string) string {
	if count == 1 {
		return "1 " + noun
	}
	return itoa(count) + " " + noun + "s"
}

func itoa(value int) string {
	if value == 0 {
		return "0"
	}
	digits := make([]byte, 0, 8)
	for value > 0 {
		digits = append([]byte{byte('0' + value%10)}, digits...)
		value /= 10
	}
	return string(digits)
}

// humanSpan renders a duration the way an operator would say it, because
// "4m0s" in a sentence reads as a machine talking.
func humanSpan(span time.Duration) string {
	switch {
	case span < time.Minute:
		return itoa(int(span.Seconds())) + " seconds"
	case span < time.Hour:
		return plural(int(span.Minutes()), "minute")
	case span < 24*time.Hour:
		return plural(int(span.Hours()), "hour")
	default:
		return plural(int(span.Hours()/24), "day")
	}
}

func sortedKeys(set map[string]bool) []string {
	keys := make([]string, 0, len(set))
	for key := range set {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}
