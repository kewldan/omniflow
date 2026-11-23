// Package smartsub is the smart subscription: Omniflow serves the link a VPN
// client imports, reads the user's subscription from Remnawave behind it, and
// rewrites what goes back to the client.
//
// Remnawave stays authoritative for users, hosts, nodes, and the access link
// itself; this package never writes to it. What it adds is the client-facing
// shape: servers grouped by country with a balancer per group and an automatic
// "fastest" group, a failover onto the hosts the operator marked as the
// whitelist bypass, and routing rules a customer chooses for their own
// subscription — which countries they see, which ones the automatic group may
// pick, and which domains go direct, through the tunnel, or nowhere.
//
// The package is pure: it takes the panel's response and the settings as
// values and returns bytes. Fetching, storage, and HTTP live elsewhere.
package smartsub

import (
	"strings"
	"unicode/utf8"
)

// compiledCountry is one country's patterns, lowercased once.
type compiledCountry struct {
	code     string
	patterns []string
}

var compiledCountries = func() []compiledCountry {
	compiled := make([]compiledCountry, 0, len(countryTable))
	for _, row := range countryTable {
		patterns := make([]string, 0, len(row.patterns))
		for _, pattern := range row.patterns {
			if pattern = strings.ToLower(strings.TrimSpace(pattern)); pattern != "" {
				patterns = append(patterns, pattern)
			}
		}
		compiled = append(compiled, compiledCountry{code: row.code, patterns: patterns})
	}
	return compiled
}()

// knownCountries is every code the table names, for validating an operator's
// override.
var knownCountries = func() map[string]bool {
	known := make(map[string]bool, len(countryTable))
	for _, row := range countryTable {
		known[row.code] = true
	}
	return known
}()

// ValidCountry reports whether code is an ISO country code the package knows.
func ValidCountry(code string) bool { return knownCountries[code] }

// DetectCountry names the country a host remark points at, or "" when it
// points at none.
//
// A flag wins outright: "🇫🇮 Helsinki" is Finland whatever else the remark
// says. Without one, the remark is matched against the name table and the
// longest matching pattern decides. A pattern of two letters or fewer — an ISO
// code — only matches as a whole word, so "DE 1" is Germany and "Modern" is
// not Germany.
func DetectCountry(remark string) string {
	if code := flagCountry(remark); code != "" {
		return code
	}
	lowered := strings.ToLower(remark)
	best, bestLength := "", 0
	for _, country := range compiledCountries {
		for _, pattern := range country.patterns {
			length := utf8.RuneCountInString(pattern)
			if length <= bestLength {
				continue
			}
			if length <= 2 {
				if !containsWord(lowered, pattern) {
					continue
				}
			} else if !strings.Contains(lowered, pattern) {
				continue
			}
			best, bestLength = country.code, length
		}
	}
	return best
}

// flagCountry reads the first flag emoji in text: two regional indicator
// symbols spell the ISO code.
func flagCountry(text string) string {
	var previous rune
	for _, current := range text {
		if current >= 0x1F1E6 && current <= 0x1F1FF {
			if previous != 0 {
				code := string([]rune{'A' + previous - 0x1F1E6, 'A' + current - 0x1F1E6})
				if knownCountries[code] {
					return code
				}
				previous = 0
				continue
			}
			previous = current
			continue
		}
		previous = 0
	}
	return ""
}

// containsWord reports whether word occurs in text bounded on both sides by
// the text's edge or a character that is neither a Latin letter nor a dot:
// "lk" in "lk.example.com" is part of a domain, not Sri Lanka.
func containsWord(text, word string) bool {
	for start := 0; ; {
		index := strings.Index(text[start:], word)
		if index < 0 {
			return false
		}
		at := start + index
		end := at + len(word)
		if (at == 0 || !isWordByte(text[at-1])) && (end == len(text) || !isWordByte(text[end])) {
			return true
		}
		start = at + 1
	}
}

func isWordByte(b byte) bool { return b >= 'a' && b <= 'z' || b == '.' }
