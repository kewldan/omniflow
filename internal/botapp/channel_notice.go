package botapp

import (
	"context"
	"errors"
	"html"
	"strings"

	"github.com/go-telegram/bot/models"
	"github.com/omniflow/omniflow/internal/channelworker"
)

// NotifyChannelEvent tells a customer what the channel worker decided about
// them: a warning with the channels to rejoin and the deadline, a suspension,
// or a restoration.
//
// It is delivered through the same sender as every other push, so a customer
// who blocked the bot is recorded the same way, and it is one message per
// change rather than one per pass — the worker only raises an event when the
// state moves.
func (app *App) NotifyChannelEvent(ctx context.Context, event channelworker.ChannelEvent) error {
	if app.sender == nil || event.TelegramID == 0 {
		return errors.New("channel notice has no delivery path")
	}
	locale := app.locale(ctx, event.TelegramID, "")
	return app.sender.Send(ctx, event.CustomerID, event.TelegramID, channelNoticeView(locale, event))
}

// channelNoticeView renders one channel event. A warning and a suspension
// list the channels with a button each, because a message that says "join a
// channel" without saying which one cannot be acted on; a restoration only
// needs the way back to the menu.
func channelNoticeView(locale Locale, event channelworker.ChannelEvent) View {
	var lines []string
	switch event.Kind {
	case channelworker.EventWarned:
		deadline := "—"
		if event.GraceUntil != nil {
			deadline = formatDate(*event.GraceUntil)
		}
		lines = append(lines, text(locale, "channels.warned", deadline))
	case channelworker.EventSuspended:
		lines = append(lines, text(locale, "channels.suspended"))
	default:
		lines = append(lines, text(locale, "channels.restored"))
	}
	rows := make([][]models.InlineKeyboardButton, 0, len(event.Missing)+1)
	if event.Kind != channelworker.EventRestored {
		for _, channel := range event.Missing {
			lines = append(lines, "• "+html.EscapeString(channel.Title))
			if safeURL(channel.InviteURL) {
				rows = append(rows, row(models.InlineKeyboardButton{
					Text: text(locale, "channels.open", channel.Title), URL: channel.InviteURL,
				}))
			}
		}
	}
	rows = append(rows, row(callbackButton(text(locale, "action.menu"), routeHome)))
	return View{Text: strings.Join(lines, "\n"), Keyboard: keyboard(rows...)}
}
