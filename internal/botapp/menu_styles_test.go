package botapp

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/go-telegram/bot/models"
	"github.com/omniflow/omniflow/internal/installsettings"
)

func TestMenuStylesColourBuiltInAndOwnButtons(t *testing.T) {
	t.Cleanup(func() { currentCustomization.Store(nil) })
	applyCustomization(installsettings.Document{
		"miniAppUrl": "https://app.example.com",
		"buttons": []any{
			map[string]any{"labelEn": "Channel", "url": "https://t.me/example", "kind": "url"},
		},
		"menuStyles": map[string]any{
			routePlans:       map[string]any{"style": "success", "emoji": "5368324170671202286"},
			routeSupport:     map[string]any{"style": "danger"},
			"custom:0":       map[string]any{"style": "primary"},
			MenuEntryCabinet: map[string]any{"style": "success"},
			"refresh":        map[string]any{"style": "primary"},
		},
	})
	keyboard := commerceMainKeyboard(LocaleEnglish, MenuState{CommerceEnabled: true})
	found := map[string]models.InlineKeyboardButton{}
	for _, buttons := range keyboard.InlineKeyboard {
		for _, button := range buttons {
			switch {
			case button.WebApp != nil:
				found[MenuEntryCabinet] = button
			case button.URL != "":
				found["custom:0"] = button
			default:
				found[menuEntryKey(button)] = button
			}
		}
	}
	checks := map[string][2]string{
		routePlans:       {"success", "5368324170671202286"},
		routeSupport:     {"danger", ""},
		"custom:0":       {"primary", ""},
		MenuEntryCabinet: {"success", ""},
		"refresh":        {"primary", ""},
		routeOrders:      {"", ""},
	}
	for key, want := range checks {
		button, ok := found[key]
		if !ok {
			t.Fatalf("%s is not on the menu", key)
		}
		if button.Style != want[0] || button.IconCustomEmojiID != want[1] {
			t.Errorf("%s: style %q emoji %q, want %q %q", key, button.Style, button.IconCustomEmojiID, want[0], want[1])
		}
	}

	// The fields reach Telegram under the Bot API names.
	encoded, err := json.Marshal(found[routePlans])
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(encoded), `"style":"success"`) ||
		!strings.Contains(string(encoded), `"icon_custom_emoji_id":"5368324170671202286"`) {
		t.Fatalf("encoded button = %s", encoded)
	}
}

func TestMenuStylesDropWhatTelegramWouldRefuse(t *testing.T) {
	styles := parseMenuStyles(map[string]any{
		routePlans:     map[string]any{"style": "rainbow", "emoji": "not-a-number"},
		routeSupport:   map[string]any{"style": "danger", "emoji": strings.Repeat("9", 40)},
		"no-such-item": map[string]any{"style": "success"},
		routeNews:      "primary",
	})
	if _, ok := styles[routePlans]; ok {
		t.Errorf("an unknown colour and a non-numeric emoji leave nothing to apply: %+v", styles[routePlans])
	}
	if styles[routeSupport] != (MenuStyle{Style: "danger"}) {
		t.Errorf("an oversized emoji id must be dropped, the colour kept: %+v", styles[routeSupport])
	}
	if _, ok := styles["no-such-item"]; ok {
		t.Error("an unknown entry must be dropped")
	}
	if parseMenuStyles("garbage") != nil {
		t.Error("a malformed document must leave the menu unstyled")
	}
}

func TestMenuStylesInCabinetOnlyMode(t *testing.T) {
	custom := parseCustomization(installsettings.Document{
		"miniAppUrl":  "https://app.example.com",
		"cabinetOnly": true,
		"menuStyles": map[string]any{
			MenuEntryCabinet: map[string]any{"style": "success"},
			routeSupport:     map[string]any{"style": "danger"},
		},
	})
	rows := customizeMenuWith(custom, LocaleEnglish, nil)
	if rows[0][0].Style != "success" || rows[1][0].Style != "danger" {
		t.Fatalf("cabinet-only rows = %+v", rows)
	}
}

func TestMenuPreviewNamesAndStylesOwnButtons(t *testing.T) {
	preview := PreviewMainMenu(installsettings.Document{
		"miniAppUrl": "https://app.example.com",
		"buttons": []any{
			map[string]any{"labelEn": "Channel", "url": "https://t.me/example", "kind": "url"},
			map[string]any{"labelEn": "Shop app", "url": "https://shop.example.com", "kind": "webapp"},
		},
		"menuStyles": map[string]any{"custom:1": map[string]any{"style": "danger"}},
	}, "en")
	keys := map[string]MenuPreviewButton{}
	for _, buttons := range preview {
		for _, button := range buttons {
			keys[button.Key] = button
		}
	}
	for _, key := range []string{MenuEntryCabinet, "custom:0", "custom:1"} {
		if _, ok := keys[key]; !ok {
			t.Errorf("the preview does not name %s; keys = %v", key, keys)
		}
	}
	if keys["custom:1"].Style != "danger" {
		t.Errorf("custom:1 style = %q", keys["custom:1"].Style)
	}
}
