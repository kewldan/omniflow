package operator

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/go-telegram/bot/models"
)

const testTicket = "0d9b1f2c-6a44-4f0e-9d7a-11c2f3e4a5b6"

func buttonTexts(markup *models.InlineKeyboardMarkup) [][]string {
	if markup == nil {
		return nil
	}
	rows := [][]string{}
	for _, row := range markup.InlineKeyboard {
		texts := []string{}
		for _, button := range row {
			texts = append(texts, button.Text)
		}
		rows = append(rows, texts)
	}
	return rows
}

// A new ticket reads like the previous bot's: the header in capitals, one
// bold-labelled line per fact, and the four buttons — DM and profile on one
// row, then close and block on rows of their own.
func TestANewTicketLooksLikeThePreviousBots(t *testing.T) {
	created := time.Date(2026, 11, 20, 18, 31, 0, 0, time.UTC)
	text, markup := renderNotice(renderInput{
		Kind:    "support",
		Payload: payloadOf(t, `{"ticketId":"`+testTicket+`","queueId":"q"}`),
		Facts: noticeFacts{
			Ticket: &ticketFacts{ID: testTicket, QueueName: "Общие", Status: "open", CustomerID: testCustomer,
				Subject: "Процент за друга", Message: "Я пришёл по рефералке <друга>", CreatedAt: created},
			Customer: &customerFacts{ID: testCustomer, TelegramID: 8792980158, Username: "ranalda16", FirstName: "G"},
		},
		At: created.Add(time.Minute), Locale: "ru", Location: moscow, PanelURL: "https://lk.example.test", Actions: true,
	})
	want := strings.Join([]string{
		"<b>🎫 НОВЫЙ ТИКЕТ</b>",
		"",
		"🆔 <b>ID:</b> <code>0d9b1f2c</code>",
		"👤 <b>Пользователь:</b> G",
		"🆔 <b>Telegram ID:</b> <code>8792980158</code>",
		"📱 <b>Username:</b> @ranalda16",
		"📂 <b>Очередь:</b> Общие",
		"📝 <b>Заголовок:</b> Процент за друга",
		"📩 <b>Сообщение:</b>",
		"Я пришёл по рефералке &lt;друга&gt;",
		"📅 <b>Создан:</b> 20 ноя, 21:31 MSK",
	}, "\n")
	if text != want {
		t.Fatalf("ticket notice:\n%s\nwant:\n%s", text, want)
	}
	rows := buttonTexts(markup)
	wantRows := [][]string{{"✉️ ЛС", "👤 Профиль"}, {"💬 Открыть тикет"}, {"🔒 Закрыть тикет"}, {"🚫 Заблокировать"}}
	if len(rows) != len(wantRows) {
		t.Fatalf("buttons: %v, want %v", rows, wantRows)
	}
	for index := range wantRows {
		if strings.Join(rows[index], "|") != strings.Join(wantRows[index], "|") {
			t.Fatalf("buttons: %v, want %v", rows, wantRows)
		}
	}
	keyboard := markup.InlineKeyboard
	if keyboard[0][0].URL != "https://t.me/ranalda16" {
		t.Fatalf("DM button: %q", keyboard[0][0].URL)
	}
	if keyboard[0][1].URL != "https://lk.example.test/admin/customers/"+testCustomer {
		t.Fatalf("profile button: %q", keyboard[0][1].URL)
	}
	if keyboard[2][0].CallbackData != "opn:tclose:"+testTicket || keyboard[3][0].CallbackData != "opn:block:"+testCustomer {
		t.Fatalf("action callbacks: %q %q", keyboard[2][0].CallbackData, keyboard[3][0].CallbackData)
	}
	for _, row := range keyboard {
		for _, button := range row {
			if len(button.CallbackData) > 64 {
				t.Fatalf("callback data over Telegram's 64 bytes: %q", button.CallbackData)
			}
		}
	}
}

// The panel's preview and any process that cannot answer a button get no
// action buttons; a closed ticket gets no close button; a blocked customer is
// offered an unblock instead.
func TestActionButtonsFollowState(t *testing.T) {
	input := renderInput{
		Kind:    "support",
		Payload: payloadOf(t, `{"ticketId":"`+testTicket+`"}`),
		Facts: noticeFacts{
			Ticket:   &ticketFacts{ID: testTicket, Status: "open", CustomerID: testCustomer},
			Customer: &customerFacts{ID: testCustomer, TelegramID: 1, Username: "x"},
		},
		At: time.Unix(0, 0), Locale: "en", PanelURL: "https://lk.example.test",
	}
	_, markup := renderNotice(input)
	for _, row := range markup.InlineKeyboard {
		for _, button := range row {
			if button.CallbackData != "" {
				t.Fatalf("an action button without Actions: %+v", button)
			}
		}
	}
	// "x" is not a valid public username, so no DM link is made from it.
	if strings.Contains(markup.InlineKeyboard[0][0].URL, "t.me") {
		t.Fatalf("a DM link for an invalid username: %+v", markup.InlineKeyboard)
	}

	input.Actions = true
	input.Facts.Ticket.Status = "closed"
	input.Facts.Customer.Status = "suspended"
	_, markup = renderNotice(input)
	data := []string{}
	for _, row := range markup.InlineKeyboard {
		for _, button := range row {
			if button.CallbackData != "" {
				data = append(data, button.CallbackData)
			}
		}
	}
	if strings.Join(data, ",") != "opn:unblock:"+testCustomer {
		t.Fatalf("actions for a closed ticket and a blocked customer: %v", data)
	}
}

// The support section can keep what the customer wrote out of the group.
func TestTicketTextCanBeHidden(t *testing.T) {
	text, _ := renderNotice(renderInput{
		Kind:    "support",
		Payload: payloadOf(t, `{"ticketId":"`+testTicket+`","event":"customer_reply"}`),
		Facts:   noticeFacts{Ticket: &ticketFacts{ID: testTicket, Subject: "card charged twice", Message: "my secret"}},
		At:      time.Unix(0, 0), Locale: "en", HideTicketText: true,
	})
	if strings.Contains(text, "card charged twice") || strings.Contains(text, "my secret") {
		t.Fatalf("hidden ticket text leaked:\n%s", text)
	}
	if !strings.Contains(text, "CUSTOMER REPLIED") {
		t.Fatalf("reply title missing:\n%s", text)
	}
}

func TestLongTicketTextIsClipped(t *testing.T) {
	long := strings.Repeat("я", ticketTextLimit+50)
	text, _ := renderNotice(renderInput{
		Kind: "support", Payload: payloadOf(t, `{"ticketId":"`+testTicket+`"}`),
		Facts: noticeFacts{Ticket: &ticketFacts{ID: testTicket, Message: long}}, At: time.Unix(0, 0),
	})
	if strings.Contains(text, long) || !strings.Contains(text, strings.Repeat("я", ticketTextLimit)+"…") {
		t.Fatalf("message was not clipped at %d characters", ticketTextLimit)
	}
}

// A failed delivery can be retried from the chat; the operation comes from
// the notice's dedupe key.
func TestAFailedDeliveryOffersARetry(t *testing.T) {
	_, markup := renderNotice(renderInput{
		Kind:    "fulfillment_failure",
		Payload: payloadOf(t, `{"operation":"create","status":"retrying","operationId":"`+testOrder+`"}`),
		At:      time.Unix(0, 0), Locale: "ru", Actions: true,
	})
	if markup == nil || markup.InlineKeyboard[len(markup.InlineKeyboard)-1][0].CallbackData != "opn:fretry:"+testOrder {
		t.Fatalf("no retry action: %+v", markup)
	}
}

func TestStartReport(t *testing.T) {
	report := StartReport{
		Name: "Omniflow", Version: "1.4.1", UsersTotal: 2894, UsersNew: 1,
		PaidSubscriptions: 240, TrialSubscriptions: 5,
		TopupsDay: map[string]int64{"RUB": 151800}, Balances: map[string]int64{"RUB": 431300},
		AwaitingReply: 1, OpenTickets: 1, RemnawaveChecked: true, RemnawaveReachable: true,
		RemnawaveLatency: 341 * time.Millisecond, SalesMode: "open",
	}
	// Through JSON, as the queue stores it.
	payload := payloadOf(t, string(mustJSON(t, report.payload())))
	text, markup := renderNotice(renderInput{
		Kind: "infrastructure", Payload: payload, At: time.Unix(0, 0), Locale: "ru",
		PanelURL: "https://lk.example.test",
	})
	for _, want := range []string{
		"<b>🚀 Omniflow · v1.4.1</b>\nЗапущен и готов к работе",
		"⚠️ <b>Требует внимания</b>\n• Тикетов ждут ответа: <b>1</b>",
		"👥 <b>Пользователи</b>\nВсего: <b>2 894</b>\nНовых за сутки: <b>+1</b>",
		"Платных: <b>240</b>\nПробных: <b>5</b>",
		"Пополнения за сутки: <b>1 518 ₽</b>\nНа балансах: <b>4 313 ₽</b>",
		"Remnawave: 🟢 на связи · 341 мс",
		"Тикетов открыто: <b>1</b>",
		"Продажи: открыты для всех",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("start report lacks %q:\n%s", want, text)
		}
	}
	if markup == nil || len(markup.InlineKeyboard[0]) != 2 {
		t.Fatalf("start report buttons: %+v", markup)
	}

	report.AwaitingReply, report.OpenTickets, report.RemnawaveReachable = 0, 0, false
	text, _ = renderNotice(renderInput{
		Kind: "infrastructure", Payload: payloadOf(t, string(mustJSON(t, report.payload()))),
		At: time.Unix(0, 0), Locale: "en",
	})
	for _, want := range []string{"The Remnawave panel does not answer", "Remnawave: 🔴 no answer"} {
		if !strings.Contains(text, want) {
			t.Fatalf("start report lacks %q:\n%s", want, text)
		}
	}
}

func TestUpperTextKeepsEntities(t *testing.T) {
	if got := upperText("tom &amp; jerry &lt;3"); got != "TOM &amp; JERRY &lt;3" {
		t.Fatalf("upperText: %q", got)
	}
}

func TestActionDataRoundTrip(t *testing.T) {
	action, id, ok := ParseAction(ActionData(ActionCloseTicket, testTicket))
	if !ok || action != ActionCloseTicket || id != testTicket {
		t.Fatalf("round trip: %q %q %v", action, id, ok)
	}
	for _, bad := range []string{"nav:home", "opn:", "opn:tclose", "act:x:y"} {
		if _, _, ok := ParseAction(bad); ok {
			t.Fatalf("%q parsed", bad)
		}
	}
}

func mustJSON(t *testing.T, value any) []byte {
	t.Helper()
	encoded, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return encoded
}
