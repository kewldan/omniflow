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
	for _, message := range messages {
		sender := text(locale, "support.messageSystem")
		switch message.Sender {
		case "customer":
			sender = text(locale, "support.messageCustomer")
		case "operator":
			sender = text(locale, "support.messageOperator")
		}
		lines = append(lines, fmt.Sprintf("<b>%s</b> · %s\n%s", sender, formatDate(message.CreatedAt), html.EscapeString(truncateRunes(message.Body, 600))))
		for _, attachment := range message.Attachments {
			name := attachment.FileName
			if name == "" {
				name = attachment.Kind
			}
			lines = append(lines, text(locale, "support.attachment", html.EscapeString(truncateRunes(name, 40)), formatBytes(attachment.SizeBytes)))
		}
		lines = append(lines, "")
	}
	rows := make([][]models.InlineKeyboardButton, 0, 3)
	if ticket.Status == "open" {
		rows = append(rows, row(actionButton(text(locale, "support.reply"), "ticket-reply:"+ticket.ID)))
		rows = append(rows, row(actionButton(text(locale, "support.close"), "ticket-close:"+ticket.ID)))
	} else {
		rows = append(rows, row(actionButton(text(locale, "support.reopen"), "ticket-open:"+ticket.ID)))
	}
	rows = append(rows, row(callbackButton(text(locale, "action.back"), routeSupport), callbackButton(text(locale, "action.menu"), routeHome)))
	return View{Text: strings.Join(lines, "\n"), Keyboard: keyboard(rows...), Protect: true}
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
