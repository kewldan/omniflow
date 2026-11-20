package botapp

import (
	"context"
	"slices"
	"strings"
	"sync/atomic"

	telegram "github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"
)

// languagePolicy is which languages the bot speaks and which one it falls back
// to. It is process-wide because a locale is chosen in many places that have
// no App at hand — the notifier, the campaign sender, the customer store — and
// they must all agree with the screen the customer is looking at.
type languagePolicy struct {
	// fallback answers a Telegram language the bot does not speak.
	fallback Locale
	// only is set when the operator offers a single language: every message is
	// in it, whatever the customer chose before.
	only Locale
	// offered is every language a customer may use. Russian and English when
	// the operator lists none; Ukrainian, Chinese, and Persian only when listed.
	offered []Locale
}

// defaultOffered is what an installation that configured nothing speaks.
var defaultOffered = []Locale{LocaleEnglish, LocaleRussian}

// supportedLocales maps every code the bot speaks to its locale.
var supportedLocales = map[string]Locale{
	"ru": LocaleRussian, "en": LocaleEnglish, "uk": LocaleUkrainian, "zh": LocaleChinese, "fa": LocalePersian,
}

// offeredLanguages lists the languages the operator offers.
func offeredLanguages() []Locale {
	if offered := activeLanguages().offered; len(offered) > 0 {
		return offered
	}
	return defaultOffered
}

// languageOffered reports whether a customer may use the locale.
func languageOffered(locale Locale) bool {
	return slices.Contains(offeredLanguages(), locale)
}

var currentLanguages atomic.Pointer[languagePolicy]

func activeLanguages() languagePolicy {
	if policy := currentLanguages.Load(); policy != nil {
		return *policy
	}
	return languagePolicy{fallback: LocaleEnglish, offered: defaultOffered}
}

// SetLanguagePolicy applies the branding section's default language and the
// bot section's available languages. Unknown codes are ignored; offering none
// means both.
func SetLanguagePolicy(defaultLocale string, available []string) {
	policy := languagePolicy{fallback: LocaleEnglish}
	if strings.HasPrefix(strings.ToLower(defaultLocale), "ru") {
		policy.fallback = LocaleRussian
	}
	offered := []Locale{}
	for _, code := range available {
		if locale, ok := supportedLocales[strings.ToLower(strings.TrimSpace(code))]; ok {
			offered = append(offered, locale)
		}
	}
	slices.Sort(offered)
	offered = slices.Compact(offered)
	if len(offered) == 0 {
		offered = defaultOffered
	}
	policy.offered = offered
	if !slices.Contains(offered, policy.fallback) {
		policy.fallback = offered[0]
	}
	if len(offered) == 1 {
		policy.only, policy.fallback = offered[0], offered[0]
	}
	currentLanguages.Store(&policy)
}

// languageChoiceOffered reports whether the settings screen shows the
// language switch: not when the operator offers one language only.
func languageChoiceOffered() bool { return activeLanguages().only == "" }

// applyLanguagePolicy re-reads the policy from the installation settings.
func (app *App) applyLanguagePolicy() {
	if app.installation == nil {
		return
	}
	bot := app.section("bot")
	SetLanguagePolicy(app.section("branding").String("defaultLocale", ""), bot.Strings("languages", nil))
	applyCustomization(bot)
}

// TextCatalog lists every bot text key with its shipped Russian and English
// wording, for the panel's text override editor.
func TextCatalog() map[string][2]string {
	out := make(map[string][2]string, len(catalog))
	for key, value := range catalog {
		out[key] = [2]string{value.ru, value.en}
	}
	return out
}

// SettingsMiddleware refreshes the settings-driven policies before an update
// is handled, so a language saved in the panel applies to the next message.
// The reads are cached, so this costs nothing per update.
func (app *App) SettingsMiddleware() telegram.Middleware {
	return func(next telegram.HandlerFunc) telegram.HandlerFunc {
		return func(ctx context.Context, client *telegram.Bot, update *models.Update) {
			app.applyLanguagePolicy()
			next(ctx, client, update)
		}
	}
}
