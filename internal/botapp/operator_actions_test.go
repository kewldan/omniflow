package botapp

import (
	"context"
	"io"
	"log/slog"
	"slices"
	"strings"
	"testing"

	telegram "github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"
	"github.com/omniflow/omniflow/internal/operator"
)

const (
	deskGroup    = int64(-1001234567890)
	deskOperator = int64(42)
	deskTicket   = "0d9b1f2c-6a44-4f0e-9d7a-11c2f3e4a5b6"
	deskCustomer = "6f1d0b6e-4b64-4c1e-9e3f-2b1f0f1a8d21"
)

type fakeDesk struct {
	ticketStatus   string
	customerStatus string
	calls          []string
	denied         []string
}

func (desk *fakeDesk) TicketStatus(context.Context, string) (string, error) {
	return desk.ticketStatus, nil
}

func (desk *fakeDesk) CloseTicket(_ context.Context, id, actor string) error {
	desk.calls = append(desk.calls, "close:"+id+":"+actor)
	desk.ticketStatus = "closed"
	return nil
}

func (desk *fakeDesk) CustomerStatus(context.Context, string) (string, error) {
	return desk.customerStatus, nil
}

func (desk *fakeDesk) SetCustomerBlocked(_ context.Context, id string, blocked bool, actor string) error {
	if blocked {
		desk.calls = append(desk.calls, "block:"+id+":"+actor)
		desk.customerStatus = "suspended"
	} else {
		desk.calls = append(desk.calls, "unblock:"+id+":"+actor)
		desk.customerStatus = "active"
	}
	return nil
}

func (desk *fakeDesk) RetryDelivery(_ context.Context, id, actor string) error {
	desk.calls = append(desk.calls, "retry:"+id+":"+actor)
	return nil
}

func (desk *fakeDesk) RecordDenied(_ context.Context, actor, action, target string) error {
	desk.denied = append(desk.denied, actor+":"+action+":"+target)
	return nil
}

func deskApp(desk *fakeDesk) *App {
	app := New(slog.New(slog.NewTextHandler(io.Discard, nil)), &fakeIdentityStore{}, &fakeRemnawave{}, "")
	app.EnableOperatorDesk(desk, deskGroup, []int64{deskOperator})
	return app
}

// noticeTap is a tap on a notice button in a chat, with the notice's
// keyboard as Telegram would send it back.
func noticeTap(chat, from int64, data string, keyboard [][]models.InlineKeyboardButton) *models.Update {
	return &models.Update{ID: 9, CallbackQuery: &models.CallbackQuery{
		ID: "cb-" + data, From: models.User{ID: from, FirstName: "Anna"}, Data: data,
		Message: models.MaybeInaccessibleMessage{Message: &models.Message{
			ID: 77, Chat: models.Chat{ID: chat, Type: models.ChatTypeSupergroup},
			ReplyMarkup: &models.InlineKeyboardMarkup{InlineKeyboard: keyboard},
		}},
	}}
}

func ticketKeyboard() [][]models.InlineKeyboardButton {
	return [][]models.InlineKeyboardButton{
		{{Text: "👤 Profile", URL: "https://lk.example.test/admin/customers/" + deskCustomer}},
		{{Text: "🔒 Close", CallbackData: operator.ActionData(operator.ActionCloseTicket, deskTicket)}},
		{{Text: "🚫 Block", CallbackData: operator.ActionData(operator.ActionBlock, deskCustomer)}},
	}
}

func TestAnOperatorClosesATicketFromTheGroup(t *testing.T) {
	t.Parallel()
	client, calls := telegramStub(t)
	desk := &fakeDesk{ticketStatus: "open", customerStatus: "active"}
	app := deskApp(desk)

	app.HandleOperatorNotice(context.Background(), client,
		noticeTap(deskGroup, deskOperator, operator.ActionData(operator.ActionCloseTicket, deskTicket), ticketKeyboard()))
	if !slices.Equal(desk.calls, []string{"close:" + deskTicket + ":telegram:42"}) {
		t.Fatalf("close calls: %v", desk.calls)
	}
	if !slices.ContainsFunc(*calls, func(call string) bool { return strings.HasSuffix(call, "/editMessageReplyMarkup") }) {
		t.Fatalf("the buttons were not updated: %v", *calls)
	}

	// A second tap on a closed ticket changes nothing.
	app.HandleOperatorNotice(context.Background(), client,
		noticeTap(deskGroup, deskOperator, operator.ActionData(operator.ActionCloseTicket, deskTicket), ticketKeyboard()))
	if len(desk.calls) != 1 {
		t.Fatalf("a closed ticket was closed again: %v", desk.calls)
	}
}

// Somebody else in the group, or anybody in another chat, is refused and
// the refusal is audited.
func TestOnlyOperatorsInTheOperatorGroupMayAct(t *testing.T) {
	t.Parallel()
	client, _ := telegramStub(t)
	desk := &fakeDesk{ticketStatus: "open", customerStatus: "active"}
	app := deskApp(desk)

	data := operator.ActionData(operator.ActionCloseTicket, deskTicket)
	app.HandleOperatorNotice(context.Background(), client, noticeTap(deskGroup, 7, data, ticketKeyboard()))
	app.HandleOperatorNotice(context.Background(), client, noticeTap(-100999, deskOperator, data, ticketKeyboard()))
	if len(desk.calls) != 0 {
		t.Fatalf("a refused tap acted: %v", desk.calls)
	}
	if len(desk.denied) != 2 || desk.denied[0] != "telegram:7:tclose:"+deskTicket {
		t.Fatalf("refusals were not audited: %v", desk.denied)
	}
}

// Blocking asks first: the first tap only swaps the button for "yes /
// cancel", the second blocks, and the button becomes "unblock".
func TestBlockingAsksFirst(t *testing.T) {
	t.Parallel()
	desk := &fakeDesk{ticketStatus: "open", customerStatus: "active"}
	app := deskApp(desk)

	reply, _, markup := app.runNoticeAction(context.Background(), operator.ActionBlock, deskCustomer, "telegram:42",
		models.User{FirstName: "Anna"}, &models.InlineKeyboardMarkup{InlineKeyboard: ticketKeyboard()}, LocaleRussian)
	if len(desk.calls) != 0 || reply != "" {
		t.Fatalf("the first tap acted: %v %q", desk.calls, reply)
	}
	confirm := markup.InlineKeyboard[2]
	if len(confirm) != 2 || confirm[0].CallbackData != operator.ActionData(operator.ActionBlockConfirm, deskCustomer) ||
		confirm[1].CallbackData != operator.ActionData(operator.ActionBlockCancel, deskCustomer) {
		t.Fatalf("confirmation row: %+v", confirm)
	}

	// Cancel puts the block button back.
	_, _, cancelled := app.runNoticeAction(context.Background(), operator.ActionBlockCancel, deskCustomer, "telegram:42",
		models.User{}, markup, LocaleRussian)
	if cancelled.InlineKeyboard[2][0].CallbackData != operator.ActionData(operator.ActionBlock, deskCustomer) {
		t.Fatalf("cancel did not restore the block button: %+v", cancelled.InlineKeyboard[2])
	}

	reply, _, blocked := app.runNoticeAction(context.Background(), operator.ActionBlockConfirm, deskCustomer, "telegram:42",
		models.User{}, markup, LocaleRussian)
	if !slices.Equal(desk.calls, []string{"block:" + deskCustomer + ":telegram:42"}) || reply != "Клиент заблокирован." {
		t.Fatalf("block: %v %q", desk.calls, reply)
	}
	if row := blocked.InlineKeyboard[2]; len(row) != 1 || row[0].CallbackData != operator.ActionData(operator.ActionUnblock, deskCustomer) {
		t.Fatalf("after blocking: %+v", row)
	}
	// Blocking a blocked customer is a no-op.
	reply, _, _ = app.runNoticeAction(context.Background(), operator.ActionBlockConfirm, deskCustomer, "telegram:42",
		models.User{}, markup, LocaleRussian)
	if len(desk.calls) != 1 || reply != "Клиент уже заблокирован." {
		t.Fatalf("a second block: %v %q", desk.calls, reply)
	}
}

// With no operator list anywhere, the group's administrators may act.
func TestGroupAdministratorsActWithoutAList(t *testing.T) {
	t.Parallel()
	client, _ := telegramStub(t)
	desk := &fakeDesk{ticketStatus: "open"}
	app := New(slog.New(slog.NewTextHandler(io.Discard, nil)), &fakeIdentityStore{}, &fakeRemnawave{}, "")
	app.EnableOperatorDesk(desk, deskGroup, nil)
	asked := 0
	app.desk.isMember = func(_ context.Context, _ *telegram.Bot, chat, user int64) bool {
		asked++
		return chat == deskGroup && user == 5
	}
	if !app.mayActInOperatorGroup(context.Background(), client, deskGroup, 5) ||
		app.mayActInOperatorGroup(context.Background(), client, deskGroup, 6) || asked != 2 {
		t.Fatalf("administrator check: asked %d", asked)
	}
}
