package botapp

import (
	"context"
	"fmt"
	"time"

	"github.com/go-telegram/bot/models"
)

// timezoneChoice is one zone the bot offers, with the city a customer would
// recognise it by in both languages.
type timezoneChoice struct {
	Zone string
	City phrase
}

// timezoneChoices is the closed list the bot offers. A closed list keeps the
// callback data closed and the screen one tap deep; a customer whose zone is
// not here can set any IANA zone in the web panel, and the bot then shows it.
// The list leans on the zones Russian-speaking customers live in, because
// those are the customers least served by the UTC default.
var timezoneChoices = []timezoneChoice{
	{"UTC", phrase{ru: "UTC", en: "UTC"}},
	{"Europe/London", phrase{ru: "Лондон", en: "London"}},
	{"Europe/Berlin", phrase{ru: "Берлин", en: "Berlin"}},
	{"Europe/Kaliningrad", phrase{ru: "Калининград", en: "Kaliningrad"}},
	{"Europe/Kyiv", phrase{ru: "Киев", en: "Kyiv"}},
	{"Europe/Minsk", phrase{ru: "Минск", en: "Minsk"}},
	{"Europe/Moscow", phrase{ru: "Москва", en: "Moscow"}},
	{"Europe/Istanbul", phrase{ru: "Стамбул", en: "Istanbul"}},
	{"Europe/Samara", phrase{ru: "Самара", en: "Samara"}},
	{"Asia/Tbilisi", phrase{ru: "Тбилиси", en: "Tbilisi"}},
	{"Asia/Yerevan", phrase{ru: "Ереван", en: "Yerevan"}},
	{"Asia/Baku", phrase{ru: "Баку", en: "Baku"}},
	{"Asia/Dubai", phrase{ru: "Дубай", en: "Dubai"}},
	{"Asia/Yekaterinburg", phrase{ru: "Екатеринбург", en: "Yekaterinburg"}},
	{"Asia/Tashkent", phrase{ru: "Ташкент", en: "Tashkent"}},
	{"Asia/Almaty", phrase{ru: "Алматы", en: "Almaty"}},
	{"Asia/Omsk", phrase{ru: "Омск", en: "Omsk"}},
	{"Asia/Novosibirsk", phrase{ru: "Новосибирск", en: "Novosibirsk"}},
	{"Asia/Krasnoyarsk", phrase{ru: "Красноярск", en: "Krasnoyarsk"}},
	{"Asia/Irkutsk", phrase{ru: "Иркутск", en: "Irkutsk"}},
	{"Asia/Yakutsk", phrase{ru: "Якутск", en: "Yakutsk"}},
	{"Asia/Vladivostok", phrase{ru: "Владивосток", en: "Vladivostok"}},
	{"Asia/Magadan", phrase{ru: "Магадан", en: "Magadan"}},
	{"Asia/Kamchatka", phrase{ru: "Камчатка", en: "Kamchatka"}},
	{"Asia/Shanghai", phrase{ru: "Пекин", en: "Beijing"}},
	{"Asia/Tokyo", phrase{ru: "Токио", en: "Tokyo"}},
	{"America/New_York", phrase{ru: "Нью-Йорк", en: "New York"}},
	{"America/Chicago", phrase{ru: "Чикаго", en: "Chicago"}},
	{"America/Denver", phrase{ru: "Денвер", en: "Denver"}},
	{"America/Los_Angeles", phrase{ru: "Лос-Анджелес", en: "Los Angeles"}},
}

// languageZones is the zone a Telegram interface language suggests. It is a
// suggestion the customer confirms with a tap, never a setting the bot makes
// on its own: a Russian-language client is as likely to be in Almaty or Berlin
// as in Moscow.
var languageZones = map[string]string{
	"ru": "Europe/Moscow", "uk": "Europe/Kyiv", "be": "Europe/Minsk", "kk": "Asia/Almaty",
	"uz": "Asia/Tashkent", "hy": "Asia/Yerevan", "ka": "Asia/Tbilisi", "az": "Asia/Baku",
}

// offeredTimezone reports whether a zone is one the bot offers.
func offeredTimezone(zone string) (timezoneChoice, bool) {
	for _, choice := range timezoneChoices {
		if choice.Zone == zone {
			return choice, true
		}
	}
	return timezoneChoice{}, false
}

// suggestedTimezone is the zone to ask about, or "" when there is nothing to
// ask: the customer already chose a zone other than UTC, or their Telegram
// language suggests none.
func suggestedTimezone(current, telegramLanguage string) string {
	if current != "" && current != "UTC" {
		return ""
	}
	if len(telegramLanguage) > 2 {
		telegramLanguage = telegramLanguage[:2]
	}
	return languageZones[telegramLanguage]
}

// utcOffset renders a zone's current offset as `UTC+03:00`.
func utcOffset(zone string, now time.Time) string {
	_, seconds := now.In(loadLocation(zone)).Zone()
	sign := "+"
	if seconds < 0 {
		sign, seconds = "-", -seconds
	}
	return fmt.Sprintf("UTC%s%02d:%02d", sign, seconds/3600, seconds%3600/60)
}

// timezoneLabel names a zone the way a customer reads it: the city when the
// bot knows one, the IANA name otherwise, with the offset in force today.
func timezoneLabel(locale Locale, zone string, now time.Time) string {
	if zone == "" {
		zone = "UTC"
	}
	name := zone
	if choice, known := offeredTimezone(zone); known {
		name = choice.City.get(locale)
	}
	if zone == "UTC" {
		return "UTC"
	}
	return name + " · " + utcOffset(zone, now)
}

// timezoneView lists the zones, the current one marked, and — when the
// customer is still on UTC and their Telegram language suggests a zone — asks
// about that zone first rather than assuming it.
func timezoneView(locale Locale, current, suggestion string, now time.Time) View {
	if current == "" {
		current = "UTC"
	}
	body := text(locale, "tz.title", timezoneLabel(locale, current, now))
	rows := make([][]models.InlineKeyboardButton, 0, len(timezoneChoices)/2+3)
	if suggestion != "" {
		body += "\n\n" + text(locale, "tz.suggest", timezoneLabel(locale, suggestion, now))
		rows = append(rows, row(actionButton(text(locale, "tz.useSuggested", timezoneLabel(locale, suggestion, now)), "tz:"+suggestion)))
	}
	pair := make([]models.InlineKeyboardButton, 0, 2)
	for _, choice := range timezoneChoices {
		label := choice.City.get(locale)
		if choice.Zone != "UTC" {
			label += " " + utcOffset(choice.Zone, now)
		}
		if choice.Zone == current {
			label = "● " + label
		}
		pair = append(pair, actionButton(label, "tz:"+choice.Zone))
		if len(pair) == 2 {
			rows = append(rows, pair)
			pair = make([]models.InlineKeyboardButton, 0, 2)
		}
	}
	if len(pair) > 0 {
		rows = append(rows, pair)
	}
	rows = append(rows, row(actionButton(text(locale, "action.back"), "quiet-menu")))
	return View{Text: body, Keyboard: keyboard(rows...)}
}

// timezoneScreen opens the zone picker.
func (app *App) timezoneScreen(ctx context.Context, session commerceContext) View {
	preferences, err := app.customers.CustomerPreferences(ctx, session.Customer.ID)
	if err != nil {
		app.logger.Error("preferences lookup failed", "error", err)
		return app.errorView(session.Locale, routeSettings)
	}
	return timezoneView(session.Locale, preferences.Timezone,
		suggestedTimezone(preferences.Timezone, session.TelegramLanguage), time.Now())
}

// setTimezone stores a zone from the closed list and returns to quiet hours,
// which is what the zone is for.
func (app *App) setTimezone(ctx context.Context, session commerceContext, zone string) View {
	if _, offered := offeredTimezone(zone); !offered {
		// Callback data is the customer's to write; only the list is accepted.
		return app.timezoneScreen(ctx, session)
	}
	if err := app.customers.SetTimezone(ctx, session.Customer.ID, zone); err != nil {
		app.logger.Error("timezone update failed", "error", err)
		return app.errorView(session.Locale, routeSettings)
	}
	return app.quietHoursScreen(ctx, session)
}

// quietHoursScreen renders the quiet-hour presets with the zone they are
// counted in.
func (app *App) quietHoursScreen(ctx context.Context, session commerceContext) View {
	preferences, err := app.customers.CustomerPreferences(ctx, session.Customer.ID)
	if err != nil {
		app.logger.Error("preferences lookup failed", "error", err)
		return app.errorView(session.Locale, routeSettings)
	}
	return quietHoursView(session.Locale, preferences, time.Now())
}
