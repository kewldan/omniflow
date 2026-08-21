package customerauth

import (
	"sort"
	"strconv"
	"strings"
)

// supportedLocales are the languages both customer surfaces render. The order
// is the tie-break when a request expresses no preference between them.
var supportedLocales = []string{"en", "ru"}

// ResolveSignUpLocale picks the language a customer provisioned by the web
// panel starts with.
//
// The Telegram client's own language wins when the payload carried one — a
// Mini App does, the login widget does not — because it is the language the
// bot will greet them in. Otherwise the browser's Accept-Language is the best
// signal available, and only when neither names a supported language does the
// installation default apply. The value is written to both users.locale and
// bot_preferences.locale, so a customer who signed up in Russian in a browser
// is not greeted in English by the bot.
func ResolveSignUpLocale(telegramLanguage, acceptLanguage string) string {
	if locale := normaliseLocale(telegramLanguage); locale != "" {
		return locale
	}
	if locale := PreferredLocale(acceptLanguage); locale != "" {
		return locale
	}
	return "en"
}

// PreferredLocale reads an Accept-Language header and returns the supported
// language it prefers most, or "" when it names none.
//
// It honours q-values and treats a regional tag as its base language, so
// "ru-RU,ru;q=0.9,en-US;q=0.8" is Russian and "de-DE,en;q=0.5" is English.
// A wildcard counts for nothing: it says "anything", and "anything" is the
// default the caller already has.
func PreferredLocale(acceptLanguage string) string {
	type candidate struct {
		locale  string
		quality float64
		order   int
	}
	var candidates []candidate
	for index, part := range strings.Split(acceptLanguage, ",") {
		tag, parameters, _ := strings.Cut(strings.TrimSpace(part), ";")
		locale := normaliseLocale(tag)
		if locale == "" {
			continue
		}
		quality := 1.0
		for _, parameter := range strings.Split(parameters, ";") {
			key, value, found := strings.Cut(strings.TrimSpace(parameter), "=")
			if !found || strings.TrimSpace(key) != "q" {
				continue
			}
			if parsed, err := strconv.ParseFloat(strings.TrimSpace(value), 64); err == nil {
				quality = parsed
			}
		}
		if quality <= 0 {
			continue
		}
		candidates = append(candidates, candidate{locale: locale, quality: quality, order: index})
	}
	if len(candidates) == 0 {
		return ""
	}
	sort.SliceStable(candidates, func(left, right int) bool {
		if candidates[left].quality != candidates[right].quality {
			return candidates[left].quality > candidates[right].quality
		}
		return candidates[left].order < candidates[right].order
	})
	return candidates[0].locale
}

// normaliseLocale maps a language tag onto a supported locale, or "".
func normaliseLocale(tag string) string {
	base, _, _ := strings.Cut(strings.ToLower(strings.TrimSpace(tag)), "-")
	base, _, _ = strings.Cut(base, "_")
	for _, supported := range supportedLocales {
		if base == supported {
			return supported
		}
	}
	return ""
}
