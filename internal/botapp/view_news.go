package botapp

import (
	"fmt"
	"html"
	"strings"

	"github.com/go-telegram/bot/models"
)

// newsListView shows the announcement inbox with unread markers.
func newsListView(locale Locale, items []NewsItem) View {
	if len(items) == 0 {
		return View{Text: text(locale, "news.empty"), Keyboard: keyboard(row(callbackButton(text(locale, "action.back"), routeHome))), RetryRoute: routeNews}
	}
	lines := []string{text(locale, "news.title"), ""}
	rows := make([][]models.InlineKeyboardButton, 0, len(items)+1)
	for _, item := range items {
		marker := ""
		if !item.Read {
			marker = text(locale, "news.unread")
		}
		lines = append(lines, fmt.Sprintf("%s<b>%s</b>\n%s · %s", marker, html.EscapeString(item.Title),
			newsCategoryLabel(locale, item.Category), formatDate(item.PublishedAt)))
		rows = append(rows, row(actionButton(text(locale, "news.row", marker, truncateRunes(item.Title, 28), formatDate(item.PublishedAt)), "news:"+item.ID)))
	}
	rows = append(rows, row(callbackButton(text(locale, "action.refresh"), routeNews), callbackButton(text(locale, "action.back"), routeHome)))
	return View{Text: strings.Join(lines, "\n"), Keyboard: keyboard(rows...), RetryRoute: routeNews}
}

// newsItemView renders one post.
func newsItemView(locale Locale, item NewsItem) View {
	return View{
		Text: text(locale, "news.item", newsCategoryLabel(locale, item.Category), html.EscapeString(item.Title), html.EscapeString(item.Body)),
		Keyboard: keyboard(
			row(callbackButton(text(locale, "action.back"), routeNews), callbackButton(text(locale, "action.menu"), routeHome)),
		),
	}
}

// newsAlertView is the push notification announcing a newly published post.
func newsAlertView(locale Locale, announcement PendingNewsAnnouncement) View {
	return View{
		Text: text(locale, "news.alert", newsCategoryLabel(locale, announcement.Category), html.EscapeString(announcement.Title)),
		Keyboard: keyboard(
			row(actionButton(text(locale, "news.open"), "news:"+announcement.PostID)),
			row(callbackButton(text(locale, "action.menu"), routeHome)),
		),
	}
}

func newsCategoryLabel(locale Locale, category string) string {
	label := text(locale, "news.category."+category)
	if label == "news.category."+category {
		return category
	}
	return label
}
