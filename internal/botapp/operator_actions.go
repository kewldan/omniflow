package botapp

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"

	telegram "github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"
	"github.com/omniflow/omniflow/internal/operator"
)

// The operator group's notice buttons.
//
// A notice in the operator group carries buttons that act from the chat: close
// a ticket, block or unblock a customer, retry a failed delivery. They arrive
// here as "opn:<action>:<id>" callbacks. Each one is checked three ways before
// anything changes — it came from the configured operator group, the person
// who pressed it is allowed to act there, and the callback was not already
// handled — and then runs through the same service the panel uses, so a close
// from the chat writes the same audit entry and tells the customer the same
// thing a close from the panel does. A refused tap is audited too.

// OperatorDesk is what the notice buttons act through. The bot process wires
// it to the panel's service; its actor is "telegram:<id>".
type OperatorDesk interface {
	// TicketStatus and CustomerStatus read the current state, so a second
	// tap on an action that already ran changes nothing.
	TicketStatus(ctx context.Context, ticketID string) (string, error)
	CloseTicket(ctx context.Context, ticketID, actor string) error
	CustomerStatus(ctx context.Context, customerID string) (string, error)
	SetCustomerBlocked(ctx context.Context, customerID string, blocked bool, actor string) error
	RetryDelivery(ctx context.Context, operationID, actor string) error
	// RecordDenied audits a tap from somebody not allowed to act.
	RecordDenied(ctx context.Context, actor, action, targetID string) error
}

// operatorDeskConfig is where the operator group is and who may act in it
// when the panel does not say: the environment's group and the operator
// Telegram accounts.
type operatorDeskConfig struct {
	desk     OperatorDesk
	chatID   int64
	allowed  []int64
	isMember func(ctx context.Context, client *telegram.Bot, chatID, userID int64) bool
}

// EnableOperatorDesk turns the notice buttons on. chatID and operatorIDs are
// the environment's APP_OPERATOR_CHAT_ID and APP_OPERATOR_TELEGRAM_IDS; the
// operator group section of the panel overrides both.
func (app *App) EnableOperatorDesk(desk OperatorDesk, chatID int64, operatorIDs []int64) {
	if desk == nil {
		return
	}
	app.desk = &operatorDeskConfig{desk: desk, chatID: chatID, allowed: operatorIDs, isMember: groupAdministrator}
}

// groupAdministrator reports whether a person is an owner or administrator of
// the group.
func groupAdministrator(ctx context.Context, client *telegram.Bot, chatID, userID int64) bool {
	member, err := client.GetChatMember(ctx, &telegram.GetChatMemberParams{ChatID: chatID, UserID: userID})
	if err != nil || member == nil {
		return false
	}
	return member.Type == models.ChatMemberTypeOwner || member.Type == models.ChatMemberTypeAdministrator
}

// operatorGroup is the configured group and its notice language.
func (app *App) operatorGroup() (int64, Locale) {
	chat := app.desk.chatID
	section := app.section("operator_group")
	if raw := section.String("chatId", ""); raw != "" {
		if parsed, err := strconv.ParseInt(strings.TrimSpace(raw), 10, 64); err == nil && parsed != 0 {
			chat = parsed
		}
	}
	locale := LocaleEnglish
	if section.String("locale", "en") == "ru" {
		locale = LocaleRussian
	}
	return chat, locale
}

// operatorActors are the Telegram accounts allowed to press notice buttons:
// the operator group section's list when it has one, the environment's
// operator accounts otherwise. Empty means the group's administrators.
func (app *App) operatorActors() []int64 {
	listed := []int64{}
	for _, raw := range strings.FieldsFunc(app.section("operator_group").String("actionTelegramIds", ""), func(r rune) bool {
		return r == ',' || r == ' ' || r == ';' || r == '\n'
	}) {
		if id, err := strconv.ParseInt(raw, 10, 64); err == nil && id > 0 {
			listed = append(listed, id)
		}
	}
	if len(listed) > 0 {
		return listed
	}
	return app.desk.allowed
}

// mayActInOperatorGroup is the permission check for a notice button.
func (app *App) mayActInOperatorGroup(ctx context.Context, client *telegram.Bot, chatID, userID int64) bool {
	group, _ := app.operatorGroup()
	if group == 0 || chatID != group {
		return false
	}
	if actors := app.operatorActors(); len(actors) > 0 {
		return slices.Contains(actors, userID)
	}
	return app.desk.isMember(ctx, client, chatID, userID)
}

// consequentialNoticeActions change something and are claimed once per tap.
var consequentialNoticeActions = map[string]bool{
	operator.ActionCloseTicket: true, operator.ActionBlockConfirm: true,
	operator.ActionUnblock: true, operator.ActionRetryFulfillment: true,
}

// HandleOperatorNotice answers a notice button in the operator group.
func (app *App) HandleOperatorNotice(ctx context.Context, client *telegram.Bot, update *models.Update) {
	query := update.CallbackQuery
	if query == nil {
		return
	}
	answer := func(message string, alert bool) {
		_, _ = client.AnswerCallbackQuery(ctx, &telegram.AnswerCallbackQueryParams{
			CallbackQueryID: query.ID, Text: message, ShowAlert: alert,
		})
	}
	action, id, ok := operator.ParseAction(query.Data)
	message := query.Message.Message
	if !ok || message == nil || app.desk == nil {
		answer("", false)
		return
	}
	_, locale := app.operatorGroup()
	if action == operator.ActionDone {
		answer("", false)
		return
	}
	if !app.allow(ctx, "bot:opn", query.From.ID, callbackBudget) {
		answer(text(locale, "menu.rateLimit"), true)
		return
	}
	actor := fmt.Sprintf("telegram:%d", query.From.ID)
	if !app.mayActInOperatorGroup(ctx, client, message.Chat.ID, query.From.ID) {
		app.logger.Warn("operator notice action refused", "action", action, "telegram_id", query.From.ID)
		if err := app.desk.desk.RecordDenied(ctx, actor, action, id); err != nil {
			app.logError(ctx, "refused operator action could not be audited", "error", err)
		}
		answer(text(locale, "ops.notice.denied"), true)
		return
	}
	if consequentialNoticeActions[action] && !app.claimCallback(ctx, query, message.Chat.ID, message.ID) {
		answer(text(locale, "menu.replay"), false)
		return
	}
	reply, alert, markup := app.runNoticeAction(ctx, action, id, actor, query.From, message.ReplyMarkup, locale)
	answer(reply, alert)
	if markup != nil {
		if _, err := client.EditMessageReplyMarkup(ctx, &telegram.EditMessageReplyMarkupParams{
			ChatID: message.Chat.ID, MessageID: message.ID, ReplyMarkup: markup,
		}); err != nil {
			app.logger.Debug("operator notice buttons could not be updated", "error", err)
		}
	}
}

// runNoticeAction performs one action and says what the buttons become. A nil
// markup leaves them as they are.
func (app *App) runNoticeAction(
	ctx context.Context, action, id, actor string, from models.User,
	current *models.InlineKeyboardMarkup, locale Locale,
) (string, bool, *models.InlineKeyboardMarkup) {
	desk := app.desk.desk
	who := strings.TrimSpace(from.FirstName)
	if who == "" && from.Username != "" {
		who = "@" + from.Username
	}
	done := func(key string) models.InlineKeyboardButton {
		label := text(locale, key)
		if who != "" {
			label += " · " + who
		}
		return models.InlineKeyboardButton{Text: label, CallbackData: operator.ActionData(operator.ActionDone, "-")}
	}
	failed := func(err error) (string, bool, *models.InlineKeyboardMarkup) {
		app.logError(ctx, "operator notice action failed", "action", action, "error", err)
		return text(locale, "ops.notice.failed"), true, nil
	}
	switch action {
	case operator.ActionCloseTicket:
		status, err := desk.TicketStatus(ctx, id)
		if err != nil {
			return failed(err)
		}
		if status == "closed" || status == "resolved" || status == "merged" {
			return text(locale, "ops.notice.ticketAlreadyClosed"), false,
				swapButton(current, action, id, done("ops.notice.ticketClosed"))
		}
		if err = desk.CloseTicket(ctx, id, actor); err != nil {
			return failed(err)
		}
		return text(locale, "ops.notice.ticketClosedAnswer"), false,
			swapButton(current, action, id, done("ops.notice.ticketClosed"))
	case operator.ActionBlock:
		return "", false, swapRow(current, action, id,
			models.InlineKeyboardButton{Text: text(locale, "ops.notice.blockConfirm"),
				CallbackData: operator.ActionData(operator.ActionBlockConfirm, id)},
			models.InlineKeyboardButton{Text: text(locale, "ops.notice.cancel"),
				CallbackData: operator.ActionData(operator.ActionBlockCancel, id)})
	case operator.ActionBlockCancel:
		return "", false, swapRow(current, operator.ActionBlockConfirm, id, blockButton(locale, id, false))
	case operator.ActionBlockConfirm, operator.ActionUnblock:
		blocking := action == operator.ActionBlockConfirm
		status, err := desk.CustomerStatus(ctx, id)
		if err != nil {
			return failed(err)
		}
		if status != "active" && status != "suspended" {
			return text(locale, "ops.notice.customerGone"), true, nil
		}
		after := swapRow(current, action, id, blockButton(locale, id, blocking))
		if blocking == (status == "suspended") {
			key := "ops.notice.alreadyUnblocked"
			if blocking {
				key = "ops.notice.alreadyBlocked"
			}
			return text(locale, key), false, after
		}
		if err = desk.SetCustomerBlocked(ctx, id, blocking, actor); err != nil {
			return failed(err)
		}
		key := "ops.notice.unblocked"
		if blocking {
			key = "ops.notice.blocked"
		}
		return text(locale, key), false, after
	case operator.ActionRetryFulfillment:
		if err := desk.RetryDelivery(ctx, id, actor); err != nil {
			if errors.Is(err, ErrNothingToRetry) {
				return text(locale, "ops.notice.nothingToRetry"), false,
					swapButton(current, action, id, done("ops.notice.retried"))
			}
			return failed(err)
		}
		return text(locale, "ops.notice.retryQueued"), false,
			swapButton(current, action, id, done("ops.notice.retried"))
	default:
		return "", false, nil
	}
}

// ErrNothingToRetry is what OperatorDesk.RetryDelivery returns for an
// operation that already succeeded or was cancelled.
var ErrNothingToRetry = errors.New("the delivery is not waiting for a retry")

// blockButton is the block button, or the unblock button for a blocked
// customer.
func blockButton(locale Locale, customerID string, blocked bool) models.InlineKeyboardButton {
	if blocked {
		return models.InlineKeyboardButton{Text: text(locale, "ops.notice.unblock"),
			CallbackData: operator.ActionData(operator.ActionUnblock, customerID)}
	}
	return models.InlineKeyboardButton{Text: text(locale, "ops.notice.block"),
		CallbackData: operator.ActionData(operator.ActionBlock, customerID)}
}

// swapButton replaces the button carrying action:id with another.
func swapButton(current *models.InlineKeyboardMarkup, action, id string, with models.InlineKeyboardButton) *models.InlineKeyboardMarkup {
	if current == nil {
		return nil
	}
	data := operator.ActionData(action, id)
	rows := make([][]models.InlineKeyboardButton, 0, len(current.InlineKeyboard))
	for _, row := range current.InlineKeyboard {
		next := make([]models.InlineKeyboardButton, 0, len(row))
		for _, button := range row {
			if button.CallbackData == data {
				button = with
			}
			next = append(next, button)
		}
		rows = append(rows, next)
	}
	return &models.InlineKeyboardMarkup{InlineKeyboard: rows}
}

// swapRow replaces the whole row holding the button carrying action:id with
// the given buttons — the block button becomes "yes / cancel" and back.
func swapRow(current *models.InlineKeyboardMarkup, action, id string, with ...models.InlineKeyboardButton) *models.InlineKeyboardMarkup {
	if current == nil {
		return nil
	}
	data := operator.ActionData(action, id)
	rows := make([][]models.InlineKeyboardButton, 0, len(current.InlineKeyboard))
	for _, row := range current.InlineKeyboard {
		if slices.ContainsFunc(row, func(button models.InlineKeyboardButton) bool { return button.CallbackData == data }) {
			rows = append(rows, with)
			continue
		}
		rows = append(rows, row)
	}
	return &models.InlineKeyboardMarkup{InlineKeyboard: rows}
}
