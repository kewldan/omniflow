package botapp

import (
	"fmt"
	"strings"
	"testing"
	"time"
)

func TestFitTextLeavesAShortTextAlone(t *testing.T) {
	t.Parallel()
	short := "<b>Hello</b> &amp; welcome"
	if got := fitText(short); got != short {
		t.Fatalf("fitText changed a text within the limit: %q", got)
	}
}

// A text over the limit is cut between tags and entities, never through one,
// and every tag the cut left open is closed — Telegram refuses unbalanced
// markup as surely as it refuses a long text.
func TestFitTextCutsSafelyAndClosesTags(t *testing.T) {
	t.Parallel()
	long := "<b>Title</b>\n<i>" + strings.Repeat("a &amp; ", 1000) + "</i><code>tail</code>"
	got := fitText(long)
	if length := utf16Length(got); length > telegramTextLimit {
		t.Fatalf("fitText left %d code units, the limit is %d", length, telegramTextLimit)
	}
	if !strings.HasSuffix(got, ellipsis+"</i>") {
		t.Fatalf("the open <i> was not closed after the ellipsis: …%q", got[len(got)-20:])
	}
	if err := checkBalanced(got); err != nil {
		t.Fatalf("fitText produced unbalanced markup: %v", err)
	}
	// The cut never lands inside an entity.
	body := strings.TrimSuffix(strings.TrimSuffix(got, "</i>"), ellipsis)
	if index := strings.LastIndexByte(body, '&'); index >= 0 && !strings.Contains(body[index:], ";") {
		t.Fatalf("the text was cut inside an entity: %q", body[index:])
	}
}

// Characters outside the Basic Multilingual Plane count twice, the way
// Telegram counts them.
func TestFitTextCountsAsTelegramDoes(t *testing.T) {
	t.Parallel()
	got := fitText(strings.Repeat("🌊", 3000))
	if length := utf16Length(got); length > telegramTextLimit {
		t.Fatalf("an emoji text came to %d code units", length)
	}
	if !strings.HasSuffix(got, ellipsis) {
		t.Fatal("a shortened text does not say so")
	}
}

// Every message the bot sends or edits goes through the guard.
func TestSendAndEditParamsApplyTheLengthGuard(t *testing.T) {
	t.Parallel()
	view := View{Text: strings.Repeat("x", telegramTextLimit+10)}
	if length := utf16Length(sendParams(1, view).Text); length > telegramTextLimit {
		t.Fatalf("sendParams passed %d code units through", length)
	}
	if length := utf16Length(editParams(1, 2, view).Text); length > telegramTextLimit {
		t.Fatalf("editParams passed %d code units through", length)
	}
}

// checkBalanced verifies that every tag closes in order.
func checkBalanced(text string) error {
	var open []string
	for index := 0; index < len(text); {
		unit, width := nextUnit(text[index:])
		name, opens, closes := tagOf(unit)
		switch {
		case opens:
			open = append(open, name)
		case closes:
			if len(open) == 0 || open[len(open)-1] != name {
				return fmt.Errorf("</%s> closes nothing", name)
			}
			open = open[:len(open)-1]
		}
		index += width
	}
	if len(open) > 0 {
		return fmt.Errorf("<%s> never closed", open[0])
	}
	return nil
}

// Twenty messages of up to 600 characters each — the screen's own bounds —
// came to three times Telegram's limit, and the request would not open. The
// screen now keeps the newest messages that fit, says how many it left out,
// and always shows the latest reply.
func TestTicketScreenFitsAndKeepsTheNewestMessages(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 10, 28, 12, 0, 0, 0, time.UTC)
	messages := make([]TicketMessage, 0, 20)
	for index := range 20 {
		messages = append(messages, TicketMessage{
			Sender: "customer", CreatedAt: now.Add(time.Duration(index) * time.Minute),
			Body: fmt.Sprintf("message %02d ", index) + strings.Repeat("<&>", 300),
		})
	}
	view := supportTicketView(LocaleEnglish, Ticket{ID: "t1", Subject: "Help", Status: "open"}, messages)
	if length := utf16Length(view.Text); length > telegramTextLimit {
		t.Fatalf("the ticket screen is %d code units, the limit is %d", length, telegramTextLimit)
	}
	if !strings.Contains(view.Text, "message 19") {
		t.Fatal("the newest message was not shown")
	}
	if strings.Contains(view.Text, "message 00") {
		t.Fatal("the oldest message was shown instead of a newer one")
	}
	if !strings.Contains(view.Text, "earlier messages did not fit") {
		t.Fatal("the screen does not say it left messages out")
	}
	if strings.Index(view.Text, "message 18") > strings.Index(view.Text, "message 19") {
		t.Fatal("the shown messages are not in conversation order")
	}
}
