package botapp

import (
	"context"

	telegram "github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"
)

// reachabilityRecorder is the part of the store that keeps Telegram delivery
// health honest from the inbound side. The Postgres store implements it; a
// store that does not simply leaves delivery health to the sender.
type reachabilityRecorder interface {
	RecordReachable(ctx context.Context, telegramID int64) error
	RecordBlockedChat(ctx context.Context, telegramID int64) error
}

// inboundMiddleware notes every update a registered handler receives before
// the handler runs. The default handler notes its own updates, because the
// library applies handler middleware only to the handlers registered with it.
func (app *App) inboundMiddleware(next telegram.HandlerFunc) telegram.HandlerFunc {
	return func(ctx context.Context, client *telegram.Bot, update *models.Update) {
		app.noteInbound(ctx, update)
		next(ctx, client, update)
	}
}

// noteInbound clears a final delivery state for whoever sent the update, and
// records the bot being blocked or restarted from a my_chat_member update.
//
// A customer who blocked the bot, or who never started it, is left out of
// every notification pass, so no send will ever succeed and clear the state.
// It used to stay set for good — a customer who unblocked the bot and used it
// every day still received no reminder, receipt, or support reply push. Any
// update from the account proves the chat works, and is what clears it now.
func (app *App) noteInbound(ctx context.Context, update *models.Update) {
	recorder, ok := app.store.(reachabilityRecorder)
	if !ok || update == nil {
		return
	}
	if membership := update.MyChatMember; membership != nil {
		app.noteChatMembership(ctx, recorder, membership)
		return
	}
	telegramID, found := inboundSender(update)
	if !found {
		return
	}
	if err := recorder.RecordReachable(ctx, telegramID); err != nil {
		app.logger.Warn("delivery state could not be cleared", "error", err)
	}
}

// noteChatMembership follows the bot's own membership in a customer's private
// chat. Telegram sends it when the customer blocks the bot and again when they
// unblock or restart it, which is the earliest either fact is known.
func (app *App) noteChatMembership(ctx context.Context, recorder reachabilityRecorder, membership *models.ChatMemberUpdated) {
	if membership.Chat.Type != models.ChatTypePrivate || membership.From.ID != membership.Chat.ID {
		return
	}
	var err error
	switch membership.NewChatMember.Type {
	case models.ChatMemberTypeMember:
		err = recorder.RecordReachable(ctx, membership.From.ID)
	case models.ChatMemberTypeBanned:
		err = recorder.RecordBlockedChat(ctx, membership.From.ID)
	default:
		return
	}
	if err != nil {
		app.logger.Warn("delivery state could not be updated from a membership change", "error", err)
	}
}

// inboundSender names the Telegram account an update came from in a private
// chat with the bot. An update from a group says nothing about whether the
// customer's own chat with the bot works.
func inboundSender(update *models.Update) (int64, bool) {
	switch {
	case update.Message != nil:
		return privateSender(update.Message)
	case update.EditedMessage != nil:
		return privateSender(update.EditedMessage)
	case update.CallbackQuery != nil:
		query := update.CallbackQuery
		if query.Message.Message == nil || query.Message.Message.Chat.Type != models.ChatTypePrivate {
			return 0, false
		}
		return query.From.ID, query.From.ID > 0
	case update.PreCheckoutQuery != nil && update.PreCheckoutQuery.From != nil:
		// An invoice is only ever sent into the customer's private chat.
		return update.PreCheckoutQuery.From.ID, update.PreCheckoutQuery.From.ID > 0
	default:
		return 0, false
	}
}

func privateSender(message *models.Message) (int64, bool) {
	if message.From == nil || message.Chat.Type != models.ChatTypePrivate {
		return 0, false
	}
	return message.From.ID, message.From.ID > 0
}
