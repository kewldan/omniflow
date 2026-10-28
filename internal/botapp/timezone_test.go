package botapp

import (
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/omniflow/omniflow/internal/commerce"
)

var timezoneTestNow = time.Date(2026, 10, 28, 12, 0, 0, 0, time.UTC)

// The bot asks about the zone a Telegram language suggests, and only while the
// customer is still on the UTC default. It never sets one on its own.
func TestSuggestedTimezoneOnlyAsksWhileOnUTC(t *testing.T) {
	t.Parallel()
	if got := suggestedTimezone("UTC", "ru"); got != "Europe/Moscow" {
		t.Fatalf("a Russian-language client on UTC is asked about %q", got)
	}
	if got := suggestedTimezone("", "uk-UA"); got != "Europe/Kyiv" {
		t.Fatalf("a regional language code is asked about %q", got)
	}
	if got := suggestedTimezone("Asia/Almaty", "ru"); got != "" {
		t.Fatalf("a customer who chose a zone is still asked about %q", got)
	}
	if got := suggestedTimezone("UTC", "en"); got != "" {
		t.Fatalf("an English client is asked about %q", got)
	}
	for _, zone := range languageZones {
		if _, offered := offeredTimezone(zone); !offered {
			t.Fatalf("the suggestion %q is not in the list a customer can choose from", zone)
		}
	}
}

func TestTimezoneLabelNamesTheCityAndOffset(t *testing.T) {
	t.Parallel()
	if got := timezoneLabel(LocaleRussian, "Europe/Moscow", timezoneTestNow); got != "Москва · UTC+03:00" {
		t.Fatalf("timezoneLabel(Moscow) = %q", got)
	}
	if got := timezoneLabel(LocaleEnglish, "America/Los_Angeles", timezoneTestNow); got != "Los Angeles · UTC-07:00" {
		t.Fatalf("timezoneLabel(Los Angeles) = %q", got)
	}
	if got := timezoneLabel(LocaleEnglish, "", timezoneTestNow); got != "UTC" {
		t.Fatalf("timezoneLabel(empty) = %q", got)
	}
	// A zone set on the web that the bot does not list is still named.
	if got := timezoneLabel(LocaleEnglish, "Asia/Kathmandu", timezoneTestNow); got != "Asia/Kathmandu · UTC+05:45" {
		t.Fatalf("timezoneLabel(Kathmandu) = %q", got)
	}
}

func TestTimezoneViewAsksBeforeUsingTheSuggestion(t *testing.T) {
	t.Parallel()
	view := timezoneView(LocaleRussian, "UTC", "Europe/Moscow", timezoneTestNow)
	data := callbackData(view)
	if data[0] != actionPrefix+"tz:Europe/Moscow" || !strings.Contains(view.Text, "Это так?") {
		t.Fatalf("the suggestion is not asked about first: %q %v", view.Text, data)
	}
	if !slices.Contains(data, actionPrefix+"tz:Asia/Vladivostok") || !slices.Contains(data, actionPrefix+"quiet-menu") {
		t.Fatalf("the full list and the way back are missing: %v", data)
	}
	chosen := timezoneView(LocaleEnglish, "Asia/Omsk", "", timezoneTestNow)
	if strings.Contains(chosen.Text, "Is that right?") || !strings.Contains(buttonLabels(chosen), "● Omsk") {
		t.Fatalf("a chosen zone is not marked, or is asked about: %q / %s", chosen.Text, buttonLabels(chosen))
	}
}

// Quiet hours were shown as "22:00–08:00" with no zone and counted in UTC for
// everybody. The window now names its zone, and a customer on the default is
// told that it is UTC and offered the picker.
func TestQuietHoursNameTheirZone(t *testing.T) {
	t.Parallel()
	preferences := CustomerPreferences{QuietHours: commerce.QuietHours{Configured: true, StartHour: 22, EndHour: 8}, Timezone: "UTC"}
	view := quietHoursView(LocaleEnglish, preferences, timezoneTestNow)
	if !strings.Contains(view.Text, "No time zone is set, so UTC is used") ||
		!slices.Contains(callbackData(view), actionPrefix+"tz-menu") {
		t.Fatalf("a customer on UTC is not told so: %q", view.Text)
	}
	preferences.Timezone = "Asia/Vladivostok"
	settings := customerSettingsView(LocaleEnglish, preferences, 3, timezoneTestNow)
	if !strings.Contains(buttonLabels(settings), "22:00–08:00 · Vladivostok · UTC+10:00") {
		t.Fatalf("the settings screen does not name the zone beside the window: %s", buttonLabels(settings))
	}
}
