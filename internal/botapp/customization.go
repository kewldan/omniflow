package botapp

import (
	"encoding/json"
	"net/url"
	"slices"
	"strings"
	"sync/atomic"

	"github.com/go-telegram/bot/models"
	"github.com/omniflow/omniflow/internal/installsettings"
)

// CustomButton is one operator-defined main-menu button.
type CustomButton struct {
	LabelRU string `json:"labelRu"`
	LabelEN string `json:"labelEn"`
	URL     string `json:"url"`
	// Kind is "url" (opens in the browser) or "webapp" (opens as a Mini App
	// inside Telegram).
	Kind string `json:"kind"`
}

// customization is how an operator shaped the bot in the panel: text
// overrides, built-in menu entries hidden, custom buttons, the Mini App, and
// cabinet-only mode. It is process-wide for the same reason the language
// policy is: text is rendered in many places with no App at hand.
type customization struct {
	texts       map[string]phrase
	hidden      []string
	buttons     []CustomButton
	miniApp     string
	cabinetOnly bool
}

var currentCustomization atomic.Pointer[customization]

func activeCustomization() customization {
	if value := currentCustomization.Load(); value != nil {
		return *value
	}
	return customization{}
}

// MenuRoutes are the built-in main-menu entries an operator may hide.
var MenuRoutes = []string{
	routePlans, routeSubscription, routeConnect, routeDevices, routeOrders, routeWallet,
	routeGifts, routeSupport, routeNews, routeReferral, routeSettings,
}

// applyCustomization reads the bot section into the process-wide state.
func applyCustomization(section installsettings.Document) {
	next := customization{texts: map[string]phrase{}}
	var overrides map[string]struct {
		RU string `json:"ru"`
		EN string `json:"en"`
	}
	if raw, ok := section["texts"].(string); ok && strings.TrimSpace(raw) != "" {
		_ = json.Unmarshal([]byte(raw), &overrides)
	} else if encoded, err := json.Marshal(section["texts"]); err == nil {
		_ = json.Unmarshal(encoded, &overrides)
	}
	for key, value := range overrides {
		if base, known := catalog[key]; known {
			if value.RU != "" {
				base.ru = value.RU
			}
			if value.EN != "" {
				base.en = value.EN
			}
			next.texts[key] = base
		}
	}
	next.hidden = section.Strings("hiddenMenu", nil)
	if encoded, err := json.Marshal(section["buttons"]); err == nil {
		var buttons []CustomButton
		if json.Unmarshal(encoded, &buttons) == nil {
			for _, button := range buttons {
				if validButtonURL(button.URL) && (button.LabelRU != "" || button.LabelEN != "") {
					next.buttons = append(next.buttons, button)
				}
			}
		}
	}
	if link := section.String("miniAppUrl", ""); validButtonURL(link) {
		next.miniApp = link
	}
	next.cabinetOnly = section.Bool("cabinetOnly", false) && next.miniApp != ""
	currentCustomization.Store(&next)
}

// validButtonURL accepts only https links, which is also what Telegram
// requires of a Mini App.
func validButtonURL(raw string) bool {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	return err == nil && parsed.Scheme == "https" && parsed.Host != ""
}

// overrideText returns the operator's wording for a key, if any.
func overrideText(key string) (phrase, bool) {
	value, ok := activeCustomization().texts[key]
	return value, ok
}

// customizeMenu applies hidden entries, custom buttons, the Mini App button,
// and cabinet-only mode to the main menu.
func customizeMenu(locale Locale, rows [][]models.InlineKeyboardButton) [][]models.InlineKeyboardButton {
	custom := activeCustomization()
	if custom.cabinetOnly {
		return [][]models.InlineKeyboardButton{
			row(miniAppButton(locale, custom.miniApp)),
			row(callbackButton(text(locale, "menu.support"), routeSupport), callbackButton(text(locale, "action.refresh"), routeHome)),
		}
	}
	if len(custom.hidden) > 0 {
		filtered := make([][]models.InlineKeyboardButton, 0, len(rows))
		for _, buttons := range rows {
			kept := make([]models.InlineKeyboardButton, 0, len(buttons))
			for _, button := range buttons {
				route := strings.TrimPrefix(strings.TrimPrefix(button.CallbackData, callbackPrefix), actionPrefix)
				if button.CallbackData == "" || !slices.Contains(custom.hidden, route) {
					kept = append(kept, button)
				}
			}
			if len(kept) > 0 {
				filtered = append(filtered, kept)
			}
		}
		rows = filtered
	}
	extra := make([][]models.InlineKeyboardButton, 0, len(custom.buttons)+1)
	if custom.miniApp != "" {
		extra = append(extra, row(miniAppButton(locale, custom.miniApp)))
	}
	for _, button := range custom.buttons {
		label := button.LabelEN
		if locale == LocaleRussian && button.LabelRU != "" || label == "" {
			label = button.LabelRU
		}
		if button.Kind == "webapp" {
			extra = append(extra, row(models.InlineKeyboardButton{Text: label, WebApp: &models.WebAppInfo{URL: button.URL}}))
		} else {
			extra = append(extra, row(models.InlineKeyboardButton{Text: label, URL: button.URL}))
		}
	}
	if len(extra) == 0 {
		return rows
	}
	// Custom buttons sit above the refresh row, which stays last.
	if len(rows) > 0 {
		last := rows[len(rows)-1]
		return append(append(rows[:len(rows)-1:len(rows)-1], extra...), last)
	}
	return extra
}

func miniAppButton(locale Locale, link string) models.InlineKeyboardButton {
	return models.InlineKeyboardButton{Text: text(locale, "menu.cabinet"), WebApp: &models.WebAppInfo{URL: link}}
}
