package botapp

import (
	"encoding/json"
	"slices"
	"strings"

	"github.com/go-telegram/bot/models"
)

// MenuStyle is how an operator dressed one main-menu button: a Bot API
// colour and, optionally, a custom emoji shown before the label.
type MenuStyle struct {
	// Style is one of MenuButtonStyles. Empty leaves Telegram's default.
	Style string `json:"style,omitempty"`
	// Emoji is a custom emoji identifier. Telegram shows it only when the
	// bot's owner has Telegram Premium or the bot has bought an additional
	// username on Fragment; otherwise it refuses the whole message, which is
	// why the panel says so beside the field.
	Emoji string `json:"emoji,omitempty"`
}

// MenuButtonStyles are the colours the Bot API accepts for an inline button.
var MenuButtonStyles = []string{"primary", "success", "danger"}

// maxEmojiIDLength bounds a custom emoji identifier, which Telegram issues as
// a decimal number well inside it.
const maxEmojiIDLength = 32

// parseMenuStyles reads the stored styles: an object from menu entry key to
// {style, emoji}. Unknown entries, unknown colours, and identifiers that are
// not decimal are dropped rather than refused, so a stale or hand-edited
// document never breaks the menu.
func parseMenuStyles(raw any) map[string]MenuStyle {
	encoded, err := json.Marshal(raw)
	if err != nil {
		return nil
	}
	// Each entry is read on its own, so one malformed value costs only its
	// own button its colour.
	var stored map[string]json.RawMessage
	if json.Unmarshal(encoded, &stored) != nil {
		return nil
	}
	styles := make(map[string]MenuStyle, len(stored))
	for key, raw := range stored {
		key = strings.TrimSpace(key)
		var value MenuStyle
		if (!knownMenuEntry(key) && key != "refresh") || json.Unmarshal(raw, &value) != nil {
			continue
		}
		style := MenuStyle{}
		if slices.Contains(MenuButtonStyles, value.Style) {
			style.Style = value.Style
		}
		if emoji := strings.TrimSpace(value.Emoji); validEmojiID(emoji) {
			style.Emoji = emoji
		}
		if style != (MenuStyle{}) {
			styles[key] = style
		}
	}
	if len(styles) == 0 {
		return nil
	}
	return styles
}

func validEmojiID(value string) bool {
	if value == "" || len(value) > maxEmojiIDLength {
		return false
	}
	for _, digit := range value {
		if digit < '0' || digit > '9' {
			return false
		}
	}
	return true
}

// styleButton applies the style stored for key to a button.
func styleButton(styles map[string]MenuStyle, key string, button models.InlineKeyboardButton) models.InlineKeyboardButton {
	if style, ok := styles[key]; ok {
		button.Style = style.Style
		button.IconCustomEmojiID = style.Emoji
	}
	return button
}

// styleMenu applies styles to the built-in buttons of a finished menu, which
// it recognises by their routes. Buttons without a route — the Mini App and
// the operator's own links — are styled where they are built, because only
// there is their key known.
func styleMenu(styles map[string]MenuStyle, rows [][]models.InlineKeyboardButton) [][]models.InlineKeyboardButton {
	if len(styles) == 0 {
		return rows
	}
	styled := make([][]models.InlineKeyboardButton, len(rows))
	for index, buttons := range rows {
		line := make([]models.InlineKeyboardButton, len(buttons))
		for position, button := range buttons {
			if key := menuEntryKey(button); key != "" {
				button = styleButton(styles, key, button)
			}
			line[position] = button
		}
		styled[index] = line
	}
	return styled
}
