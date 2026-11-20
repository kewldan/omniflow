package botapp

import (
	"context"
	"html"
	"strings"

	telegram "github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"
	"github.com/omniflow/omniflow/internal/infopage"
)

// infoPage is one published page as the bot shows it.
type infoPage struct {
	Slug  string
	Title string
	Body  string
}

// pages lists the published, listed information pages — FAQ, rules, offer,
// privacy — in the customer's language, falling back to the other language.
func (store *PostgresStore) pages(ctx context.Context, locale Locale) ([]infoPage, error) {
	rows, err := store.pool.Query(ctx, `SELECT p.slug, COALESCE(l.title, o.title)
		FROM info_pages p
		LEFT JOIN info_page_localizations l ON l.page_slug = p.slug AND l.locale = $1
		LEFT JOIN info_page_localizations o ON o.page_slug = p.slug AND o.locale <> $1
		WHERE p.published_at IS NOT NULL AND p.listed AND COALESCE(l.title, o.title) IS NOT NULL
		ORDER BY p.sort_order, p.slug LIMIT 20`, string(locale))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []infoPage
	for rows.Next() {
		var page infoPage
		if rows.Scan(&page.Slug, &page.Title) == nil {
			out = append(out, page)
		}
	}
	return out, rows.Err()
}

func (store *PostgresStore) page(ctx context.Context, locale Locale, slug string) (infoPage, error) {
	page := infoPage{Slug: slug}
	err := store.pool.QueryRow(ctx, `SELECT l.title, l.body FROM info_pages p
		JOIN info_page_localizations l ON l.page_slug = p.slug
		WHERE p.slug = $1 AND p.published_at IS NOT NULL
		ORDER BY (l.locale = $2) DESC LIMIT 1`, slug, string(locale)).Scan(&page.Title, &page.Body)
	return page, err
}

// pagesView lists the pages as buttons, so a customer reads the rules and the
// offer inside Telegram instead of being sent to a browser.
func pagesView(locale Locale, pages []infoPage) View {
	rows := make([][]models.InlineKeyboardButton, 0, len(pages)+1)
	body := text(locale, "pages.list")
	if len(pages) == 0 {
		body += "\n\n" + text(locale, "pages.empty")
	}
	for _, page := range pages {
		rows = append(rows, row(actionButton(truncateRunes(page.Title, 40), "page:"+page.Slug)))
	}
	rows = append(rows, row(callbackButton(text(locale, "action.back"), routeHome)))
	return View{Text: body, Keyboard: keyboard(rows...)}
}

// pageView renders a page as plain text, within Telegram's message limit.
func pageView(locale Locale, page infoPage) View {
	body := infopage.PlainText(infopage.Parse(page.Body))
	if runes := []rune(body); len(runes) > 3500 {
		body = string(runes[:3500]) + "…"
	}
	return View{
		Text:     "<b>" + html.EscapeString(page.Title) + "</b>\n\n" + html.EscapeString(strings.TrimSpace(body)),
		Keyboard: keyboard(row(actionButton(text(locale, "action.back"), "pages"))),
	}
}

// pagesScreen and pageScreen are the routes behind the menu entry and /faq.
func (app *App) pagesScreen(ctx context.Context, locale Locale) View {
	if app.customers == nil {
		return app.errorView(locale, routeHome)
	}
	pages, err := app.customers.pages(ctx, locale)
	if err != nil {
		app.logger.Warn("information pages could not be read", "error", err)
		return app.errorView(locale, routeHome)
	}
	return pagesView(locale, pages)
}

func (app *App) pageScreen(ctx context.Context, locale Locale, slug string) View {
	page, err := app.customers.page(ctx, locale, slug)
	if err != nil {
		return app.pagesScreen(ctx, locale)
	}
	return pageView(locale, page)
}

// sendPages answers /faq with the in-bot list.
func (app *App) sendPages(ctx context.Context, client *telegram.Bot, chatID int64, locale Locale) {
	_, _ = client.SendMessage(ctx, sendParams(chatID, app.pagesScreen(ctx, locale)))
}
