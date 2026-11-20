package botapp

import (
	"testing"

	"github.com/omniflow/omniflow/internal/installsettings"
)

func TestCustomizationAppliesToTextAndMenu(t *testing.T) {
	t.Cleanup(func() { currentCustomization.Store(nil) })
	applyCustomization(installsettings.Document{
		"texts":      map[string]any{"menu.support": map[string]any{"ru": "🆘 Помощь"}, "no.such.key": map[string]any{"en": "x"}},
		"hiddenMenu": []any{"gifts"},
		"buttons": []any{
			map[string]any{"labelRu": "Канал", "labelEn": "Channel", "url": "https://t.me/example", "kind": "url"},
			map[string]any{"labelEn": "Insecure", "url": "http://example.com", "kind": "url"},
		},
		"miniAppUrl": "https://panel.example/account",
	})
	if got := text(LocaleRussian, "menu.support"); got != "🆘 Помощь" {
		t.Fatalf("override = %q", got)
	}
	if got := text(LocaleEnglish, "menu.support"); got == "🆘 Помощь" || got == "" {
		t.Fatalf("an override in one language keeps the other: %q", got)
	}
	keyboard := commerceMainKeyboard(LocaleEnglish, MenuState{CommerceEnabled: true})
	var labels []string
	for _, buttons := range keyboard.InlineKeyboard {
		for _, button := range buttons {
			labels = append(labels, button.Text)
			if button.CallbackData == callbackPrefix+routeGifts {
				t.Fatal("a hidden entry must not render")
			}
			if button.URL == "http://example.com" {
				t.Fatal("a non-https button must be dropped")
			}
		}
	}
	last := keyboard.InlineKeyboard[len(keyboard.InlineKeyboard)-1][0]
	if last.CallbackData != callbackPrefix+routeHome {
		t.Fatalf("refresh must stay last, got %+v", last)
	}
	found := false
	for _, label := range labels {
		found = found || label == "Channel"
	}
	if !found {
		t.Fatalf("custom button missing: %v", labels)
	}

	applyCustomization(installsettings.Document{"cabinetOnly": true, "miniAppUrl": "https://panel.example/account"})
	cabinet := commerceMainKeyboard(LocaleEnglish, MenuState{CommerceEnabled: true})
	if len(cabinet.InlineKeyboard) != 2 || cabinet.InlineKeyboard[0][0].WebApp == nil {
		t.Fatalf("cabinet-only menu = %+v", cabinet.InlineKeyboard)
	}
}
