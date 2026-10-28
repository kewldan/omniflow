package botapp

import (
	"fmt"
	"html"
	"strings"

	"github.com/go-telegram/bot/models"
)

// supportListView shows every conversation with its unread state.
func supportListView(locale Locale, tickets []Ticket, supportURL string) View {
	rows := make([][]models.InlineKeyboardButton, 0, len(tickets)+3)
	rows = append(rows, row(actionButton(text(locale, "support.new"), "support-new")))
	if len(tickets) == 0 {
		if safeURL(supportURL) {
			rows = append(rows, row(models.InlineKeyboardButton{Text: text(locale, "menu.support"), URL: supportURL}))
		}
		rows = append(rows, row(callbackButton(text(locale, "action.back"), routeHome)))
		return View{Text: text(locale, "support.empty"), Keyboard: keyboard(rows...), RetryRoute: routeSupport}
	}
	lines := []string{text(locale, "support.list"), ""}
	for _, ticket := range tickets {
		unread := ""
		if ticket.UnreadCount > 0 {
			unread = text(locale, "support.unread", ticket.UnreadCount)
		}
		subject := ticket.Subject
		if strings.TrimSpace(subject) == "" {
			subject = shortID(ticket.ID)
		}
		lines = append(lines, fmt.Sprintf("• <b>%s</b> · %s · %s%s",
			html.EscapeString(truncateRunes(subject, 40)), text(locale, "support.status."+ticket.Status),
			formatDate(ticket.LastMessageAt), unread))
		rows = append(rows, row(actionButton(text(locale, "support.row", truncateRunes(subject, 24), text(locale, "support.status."+ticket.Status), unread), "ticket:"+ticket.ID)))
	}
	if safeURL(supportURL) {
		rows = append(rows, row(models.InlineKeyboardButton{Text: text(locale, "menu.support"), URL: supportURL}))
	}
	rows = append(rows, row(callbackButton(text(locale, "action.refresh"), routeSupport), callbackButton(text(locale, "action.back"), routeHome)))
	return View{Text: strings.Join(lines, "\n"), Keyboard: keyboard(rows...), RetryRoute: routeSupport}
}

// supportTicketView renders one conversation with its attachments described but
// never re-uploaded, and the actions the ticket state allows.
func supportTicketView(locale Locale, ticket Ticket, messages []TicketMessage) View {
	subject := ticket.Subject
	if strings.TrimSpace(subject) == "" {
		subject = shortID(ticket.ID)
	}
	lines := []string{text(locale, "support.ticket", html.EscapeString(truncateRunes(subject, 60)), text(locale, "support.status."+ticket.Status), formatDate(ticket.UpdatedAt)), ""}
	blocks := make([]string, len(messages))
	for index, message := range messages {
		blocks[index] = ticketMessageBlock(locale, message)
	}
	shown := newestWithin(blocks, ticketMessageBudget)
	if hidden := len(blocks) - len(shown); hidden > 0 {
		lines = append(lines, text(locale, "support.earlierHidden", hidden), "")
	}
	lines = append(lines, shown...)
	rows := make([][]models.InlineKeyboardButton, 0, 4)
	for _, action := range supportTicketActions(ticket.Status) {
		rows = append(rows, row(actionButton(text(locale, action.label), action.action+":"+ticket.ID)))
	}
	switch ticket.Status {
	case "merged":
		lines = append(lines, text(locale, "support.mergedHint"))
	case "pending":
		lines = append(lines, text(locale, "support.pendingHint"))
	case "resolved":
		lines = append(lines, text(locale, "support.resolvedHint"))
	}
	rows = append(rows, row(callbackButton(text(locale, "action.back"), routeSupport), callbackButton(text(locale, "action.menu"), routeHome)))
	return View{Text: strings.Join(lines, "\n"), Keyboard: keyboard(rows...), Protect: true}
}

// ticketMessageBudget is how much of a ticket screen the conversation may take,
// in UTF-16 code units. The rest of Telegram's 4096 is the header, the status
// hint, and room to spare. Twenty messages of up to 600 characters each — the
// screen's own bounds — came to three times the limit, and Telegram refused the
// whole screen: the customer tapped the request and nothing opened.
const ticketMessageBudget = 3400

// ticketMessageBlock renders one message of a conversation with its
// attachments described, followed by the blank line that separates it from the
// next.
func ticketMessageBlock(locale Locale, message TicketMessage) string {
	sender := text(locale, "support.messageSystem")
	switch message.Sender {
	case "customer":
		sender = text(locale, "support.messageCustomer")
	case "operator":
		sender = text(locale, "support.messageOperator")
	}
	lines := []string{fmt.Sprintf("<b>%s</b> · %s\n%s", sender, formatDate(message.CreatedAt), html.EscapeString(truncateRunes(message.Body, 600)))}
	for _, attachment := range message.Attachments {
		name := attachment.FileName
		if name == "" {
			name = attachment.Kind
		}
		lines = append(lines, text(locale, "support.attachment", html.EscapeString(truncateRunes(name, 40)), formatBytes(attachment.SizeBytes)))
	}
	return strings.Join(lines, "\n") + "\n"
}

// newestWithin keeps the newest blocks that fit the budget together, in their
// original order. Blocks are oldest first; the newest is always kept, because
// the latest reply is what a customer opens a conversation to read.
func newestWithin(blocks []string, budget int) []string {
	used, first := 0, len(blocks)
	for index := len(blocks) - 1; index >= 0; index-- {
		cost := utf16Length(blocks[index]) + 1
		if used+cost > budget && first < len(blocks) {
			break
		}
		used += cost
		first = index
	}
	return blocks[first:]
}

// supportTicketAction is one button under a conversation: the catalogue key of
// its label and the action it dispatches.
type supportTicketAction struct {
	label  string
	action string
}

// supportTicketActions is the state machine a customer sees under a ticket, and
// it mirrors the web panel's rules exactly. Open, pending, and resolved
// conversations can be replied to — writing into a pending one tells the
// operator the customer answered, writing into a resolved one reopens it.
// Only a closed or resolved conversation offers Reopen, and a merged one offers
// nothing, because its conversation continued somewhere else.
func supportTicketActions(status string) []supportTicketAction {
	switch status {
	case "open", "pending":
		return []supportTicketAction{
			{label: "support.reply", action: "ticket-reply"},
			{label: "support.close", action: "ticket-close"},
		}
	case "resolved":
		return []supportTicketAction{
			{label: "support.reply", action: "ticket-reply"},
			{label: "support.reopen", action: "ticket-open"},
			{label: "support.close", action: "ticket-close"},
		}
	case "closed":
		return []supportTicketAction{{label: "support.reopen", action: "ticket-open"}}
	default:
		return nil
	}
}

// supportComposeTicketView prompts for a message, with a cancel path that always
// returns to a usable screen.
func supportComposeTicketView(locale Locale, ticketID string) View {
	back := callbackButton(text(locale, "action.cancel"), routeSupport)
	if ticketID != "" {
		back = actionButton(text(locale, "action.cancel"), "ticket:"+ticketID)
	}
	return View{Text: text(locale, "support.compose"), Keyboard: keyboard(row(back))}
}

// supportReplyView is the push notification that carries an operator reply, or
// a system notice about the conversation when the operator closed, resolved, or
// merged it. The two are headed differently so a customer can tell an answer
// from an announcement about the thread.
func supportReplyView(locale Locale, reply PendingOperatorReply) View {
	subject := reply.Subject
	if strings.TrimSpace(subject) == "" {
		subject = shortID(reply.TicketID)
	}
	key := "support.replyAlert"
	if reply.Sender == "system" {
		key = "support.systemAlert"
	}
	return View{
		Text: text(locale, key, html.EscapeString(truncateRunes(subject, 60)), html.EscapeString(truncateRunes(reply.Body, 2000))),
		Keyboard: keyboard(
			row(actionButton(text(locale, "support.open"), "ticket:"+reply.TicketID)),
			row(callbackButton(text(locale, "action.menu"), routeHome)),
		),
		Protect: true,
	}
}
