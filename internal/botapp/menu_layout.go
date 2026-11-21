package botapp

import (
	"encoding/json"
	"slices"
	"strings"

	"github.com/go-telegram/bot/models"
	"github.com/omniflow/omniflow/internal/installsettings"
)

// Menu layout entry keys beyond the built-in routes.
const (
	// MenuEntryCabinet is the Mini App button the bot section's miniAppUrl adds.
	MenuEntryCabinet = "cabinet"
	// MenuEntryCustomPrefix names a custom button by its position in the bot
	// section's buttons: custom:0, custom:1, ….
	MenuEntryCustomPrefix = "custom:"
	// MenuEntryPages is the info pages entry (offer, rules, privacy).
	MenuEntryPages = "pages"
)

// maxLayoutRows and maxLayoutRowWidth bound a stored layout. Telegram accepts
// up to eight buttons in a row; three is the most that stays legible on a
// phone.
const (
	maxLayoutRows     = 20
	maxLayoutRowWidth = 3
)

// MenuLayoutEntries are the built-in main-menu entries an operator may place,
// in the shipped order. Some appear only when they can be used — top-up, the
// cart, Stars, the shop, offers — and a layout that places them keeps them
// there for when they do. The lapsed-subscription "restore" button always
// stays first and Refresh always stays last.
var MenuLayoutEntries = []string{
	routePlans, routeSubscription, routeConnect, routeDevices, routeOrders, routeWallet,
	routeTopUp, routeCart, routeStars, routeShop, routeGifts, routeOffers,
	routeSupport, routeNews, MenuEntryPages, routeReferral, routeSettings, MenuEntryCabinet,
}

// parseMenuLayout reads the stored layout: an array of rows, each an array of
// entry keys. Unknown shapes, empty rows, duplicates, and anything beyond the
// bounds are dropped rather than refused, so a stale layout never breaks the
// menu.
func parseMenuLayout(raw any) [][]string {
	encoded, err := json.Marshal(raw)
	if err != nil {
		return nil
	}
	var rows [][]string
	if json.Unmarshal(encoded, &rows) != nil {
		return nil
	}
	seen := map[string]bool{}
	layout := make([][]string, 0, len(rows))
	for _, entries := range rows {
		if len(layout) == maxLayoutRows {
			break
		}
		kept := make([]string, 0, maxLayoutRowWidth)
		for _, entry := range entries {
			entry = strings.TrimSpace(entry)
			if !knownMenuEntry(entry) || seen[entry] || len(kept) == maxLayoutRowWidth {
				continue
			}
			seen[entry] = true
			kept = append(kept, entry)
		}
		if len(kept) > 0 {
			layout = append(layout, kept)
		}
	}
	return layout
}

// menuEntryKey names a built-in menu button by the layout key it is placed
// with. The subscription switcher is the subscription entry; the restore and
// refresh buttons answer their fixed positions.
func menuEntryKey(button models.InlineKeyboardButton) string {
	switch data := button.CallbackData; {
	case strings.HasPrefix(data, callbackPrefix):
		route := strings.TrimPrefix(data, callbackPrefix)
		switch route {
		case routeSubscriptions:
			return routeSubscription
		case routeHome:
			return "refresh"
		}
		return route
	case strings.HasPrefix(data, actionPrefix):
		action := strings.TrimPrefix(data, actionPrefix)
		if strings.HasPrefix(action, "sub-renew:") {
			return "recover"
		}
		return action
	}
	return ""
}

// arrangeMenu lays the menu out in the operator's rows. Entries the layout
// does not mention keep their shipped rows after it, so a feature added in a
// later release is never silently missing from a customised menu; entries the
// layout names that are not on the menu right now — hidden, or not usable for
// this customer — are skipped, and a row left empty disappears.
func arrangeMenu(
	layout [][]string, rows [][]models.InlineKeyboardButton,
	extra [][]models.InlineKeyboardButton, extraKeys []string,
) [][]models.InlineKeyboardButton {
	available := map[string]models.InlineKeyboardButton{}
	var recover, refresh []models.InlineKeyboardButton
	for _, buttons := range rows {
		for _, button := range buttons {
			switch key := menuEntryKey(button); key {
			case "recover":
				recover = append(recover, button)
			case "refresh":
				refresh = append(refresh, button)
			case "":
			default:
				available[key] = button
			}
		}
	}
	for index, buttons := range extra {
		if index < len(extraKeys) && len(buttons) > 0 {
			available[extraKeys[index]] = buttons[0]
		}
	}
	placed := map[string]bool{}
	var arranged [][]models.InlineKeyboardButton
	if len(recover) > 0 {
		arranged = append(arranged, recover)
	}
	for _, entries := range layout {
		buttons := make([]models.InlineKeyboardButton, 0, len(entries))
		for _, entry := range entries {
			if button, ok := available[entry]; ok && !placed[entry] {
				buttons = append(buttons, button)
				placed[entry] = true
			}
		}
		if len(buttons) > 0 {
			arranged = append(arranged, buttons)
		}
	}
	for _, buttons := range rows {
		left := make([]models.InlineKeyboardButton, 0, len(buttons))
		for _, button := range buttons {
			key := menuEntryKey(button)
			if key != "" && key != "recover" && key != "refresh" && !placed[key] {
				left = append(left, button)
				placed[key] = true
			}
		}
		if len(left) > 0 {
			arranged = append(arranged, left)
		}
	}
	for index, buttons := range extra {
		if index < len(extraKeys) && !placed[extraKeys[index]] {
			arranged = append(arranged, buttons)
		}
	}
	if len(refresh) > 0 {
		arranged = append(arranged, refresh)
	}
	return arranged
}

// MenuPreviewButton is one main-menu button as the panel's preview draws it.
type MenuPreviewButton struct {
	Key   string `json:"key"`
	Label string `json:"label"`
	// Kind is "screen" for a bot screen, "url" for a link, "webapp" for a Mini
	// App.
	Kind string `json:"kind"`
}

// PreviewMainMenu renders the main menu a bot section document would produce,
// without applying it, for a customer who can see every optional entry. The
// labels are the shipped wording plus the draft's own text overrides and
// custom buttons.
func PreviewMainMenu(section installsettings.Document, language string) [][]MenuPreviewButton {
	locale := localeFrom(language)
	custom := parseCustomization(section)
	rows := commerceMenuRows(locale, MenuState{
		CommerceEnabled: true, ShopEnabled: true, StarsEnabled: true, OfferCount: 1,
		TopUpEnabled: true, HasCart: true,
	})
	rows = customizeMenuWith(custom, locale, rows)
	preview := make([][]MenuPreviewButton, 0, len(rows))
	for _, buttons := range rows {
		line := make([]MenuPreviewButton, 0, len(buttons))
		for _, button := range buttons {
			item := MenuPreviewButton{Key: menuEntryKey(button), Label: button.Text, Kind: "screen"}
			switch {
			case button.WebApp != nil:
				item.Kind = "webapp"
			case button.URL != "":
				item.Kind = "url"
			}
			item.Label = previewLabel(custom, locale, item.Key, item.Label)
			line = append(line, item)
		}
		preview = append(preview, line)
	}
	return preview
}

// previewLabel applies the draft's override of a built-in entry's label.
// The bot reads overrides from the saved section; the preview reads them from
// the draft so the operator sees the wording they typed.
func previewLabel(custom customization, locale Locale, key, label string) string {
	keys := map[string]string{
		routePlans: "menu.plans", routeOrders: "menu.orders", routeWallet: "menu.wallet",
		routeTopUp: "menu.topup", routeCart: "menu.cart", routeStars: "menu.stars",
		routeShop: "menu.shop", routeGifts: "menu.gifts", routeOffers: "menu.offers",
		routeSupport: "menu.support", routeNews: "menu.news", MenuEntryPages: "menu.pages",
		MenuEntryCabinet: "menu.cabinet", "refresh": "action.refresh",
	}
	if catalogKey, ok := keys[key]; ok {
		if override, found := custom.texts[catalogKey]; found {
			return override.get(locale)
		}
	}
	return label
}

// knownMenuEntry reports whether a layout key names something the menu can
// hold.
func knownMenuEntry(key string) bool {
	return slices.Contains(MenuLayoutEntries, key) || strings.HasPrefix(key, MenuEntryCustomPrefix)
}
