package botapp

import (
	"testing"

	"github.com/omniflow/omniflow/internal/installsettings"
)

func TestMenuLayoutReordersEntriesAndKeepsTheRest(t *testing.T) {
	t.Cleanup(func() { currentCustomization.Store(nil) })
	applyCustomization(installsettings.Document{
		"buttons": []any{
			map[string]any{"labelEn": "Channel", "url": "https://t.me/example", "kind": "url"},
		},
		"hiddenMenu": []any{"gifts"},
		"menuLayout": []any{
			[]any{"custom:0", "support"},
			[]any{"gifts", "connect", "no-such-entry"},
			[]any{"plans", "plans"},
		},
	})
	keyboard := commerceMainKeyboard(LocaleEnglish, MenuState{CommerceEnabled: true})
	rows := keyboard.InlineKeyboard
	if rows[0][0].Text != "Channel" || menuEntryKey(rows[0][1]) != routeSupport {
		t.Fatalf("first row = %+v", rows[0])
	}
	// A hidden entry stays hidden even when the layout names it.
	if len(rows[1]) != 1 || menuEntryKey(rows[1][0]) != routeConnect {
		t.Fatalf("second row = %+v", rows[1])
	}
	if len(rows[2]) != 1 || menuEntryKey(rows[2][0]) != routePlans {
		t.Fatalf("a duplicate must not render twice: %+v", rows[2])
	}
	// Entries the layout leaves out follow, and refresh stays last.
	seen := map[string]int{}
	for _, buttons := range rows {
		for _, button := range buttons {
			seen[menuEntryKey(button)]++
		}
	}
	for _, entry := range []string{routeDevices, routeOrders, routeWallet, routeNews, routeSettings} {
		if seen[entry] != 1 {
			t.Errorf("%s appears %d times", entry, seen[entry])
		}
	}
	if last := rows[len(rows)-1][0]; last.CallbackData != callbackPrefix+routeHome {
		t.Fatalf("refresh must stay last, got %+v", last)
	}
}

func TestMenuPreviewUsesTheDraft(t *testing.T) {
	preview := PreviewMainMenu(installsettings.Document{
		"menuLayout": []any{[]any{"news", "plans"}},
		"texts":      map[string]any{"menu.news": map[string]any{"ru": "Новости!"}},
	}, "ru")
	if preview[0][0].Key != routeNews || preview[0][0].Label != "Новости!" || preview[0][1].Key != routePlans {
		t.Fatalf("preview first row = %+v", preview[0])
	}
	if activeCustomization().layout != nil {
		t.Fatal("a preview must not apply the draft")
	}
}
