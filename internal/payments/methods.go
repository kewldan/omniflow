package payments

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// A provider may offer several ways to pay — SBP, a card, SberPay, crypto —
// that an installation wants shown as separate buttons. A method is named by
// a short lowercase key the adapter understands ("sbp", "card"); the empty key
// is the adapter's own default, which is what an adapter with no methods
// offers.
//
// The customer-facing identity of a button is the option key: the provider
// slug alone, or the slug and the method joined by a dot ("freekassa.sbp").
// It travels through callback data and the web panel, and is split again at
// the boundary that opens the payment.

// MethodLister is implemented by adapters configured with more than one
// method. The order is the operator's order from the environment.
type MethodLister interface {
	Methods() []string
}

// ProviderMethods lists the methods an adapter offers; an adapter without
// methods offers its default only.
func ProviderMethods(provider Provider) []string {
	if lister, ok := provider.(MethodLister); ok {
		if methods := lister.Methods(); len(methods) > 0 {
			return methods
		}
	}
	return []string{""}
}

// OptionKey names one customer-facing payment button.
func OptionKey(provider, method string) string {
	if method == "" {
		return provider
	}
	return provider + "." + method
}

// SplitOptionKey separates an option key into its provider and method.
func SplitOptionKey(key string) (provider, method string) {
	provider, method, _ = strings.Cut(strings.TrimSpace(key), ".")
	return provider, method
}

var methodKeyPattern = regexp.MustCompile(`^[a-z0-9_]{1,10}$`)

// ValidMethodKey reports whether a method key is well formed: short enough to
// fit a Telegram callback beside a provider slug, and safe to store.
func ValidMethodKey(method string) bool {
	return method == "" || methodKeyPattern.MatchString(method)
}

// parseMethodCodes reads a list of methods from the environment.
//
// Each entry is `key:code`, or a bare key whose code comes from aliases, or —
// when aliases are numeric IDs — a bare known alias. The returned order is the
// operator's. An empty value is no methods at all, which leaves the adapter on
// its single default.
func parseMethodCodes(name, value string, aliases map[string]string) ([]string, map[string]string, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return nil, nil, nil
	}
	order := make([]string, 0, 4)
	codes := make(map[string]string, 4)
	for _, entry := range strings.Split(value, ",") {
		entry = strings.TrimSpace(entry)
		if entry == "" {
			continue
		}
		key, code, explicit := strings.Cut(entry, ":")
		key = strings.ToLower(strings.TrimSpace(key))
		code = strings.TrimSpace(code)
		if !explicit {
			known, ok := aliases[key]
			if !ok {
				return nil, nil, fmt.Errorf("%s: unknown method %q; write it as key:code", name, key)
			}
			code = known
		}
		if !methodKeyPattern.MatchString(key) || code == "" {
			return nil, nil, fmt.Errorf("%s: %q is not a valid method", name, entry)
		}
		if _, duplicate := codes[key]; duplicate {
			return nil, nil, fmt.Errorf("%s: method %q is listed twice", name, key)
		}
		codes[key] = code
		order = append(order, key)
	}
	return order, codes, nil
}

// parseMethodIDs is parseMethodCodes for providers whose method codes are
// positive integers.
func parseMethodIDs(name, value string, aliases map[string]int) ([]string, map[string]int, error) {
	text := make(map[string]string, len(aliases))
	for key, id := range aliases {
		text[key] = strconv.Itoa(id)
	}
	order, codes, err := parseMethodCodes(name, value, text)
	if err != nil {
		return nil, nil, err
	}
	ids := make(map[string]int, len(codes))
	for key, code := range codes {
		id, convErr := strconv.Atoi(code)
		if convErr != nil || id <= 0 {
			return nil, nil, fmt.Errorf("%s: method %q must name a positive ID", name, key)
		}
		ids[key] = id
	}
	return order, ids, nil
}
