package panelpg

import (
	"context"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/omniflow/omniflow/internal/database/dbgen"
	"github.com/omniflow/omniflow/internal/infopage"
)

// Information pages: the FAQ, the terms, the offer, the privacy policy, and
// whatever else an operator needs at an address of their own.
//
// They are not news posts, and the difference decides the whole shape. A news
// post is dated, expires, is read once, and counts towards an unread badge. One
// of these is a permanent address whose content changes in place, readable by
// somebody who has never signed in — a payment provider's reviewer, or a
// customer deciding whether to become one.

// InfoPageLocale is one language of a page.
type InfoPageLocale struct {
	Locale string `json:"locale"`
	Title  string `json:"title"`
	// Body is the operator's own source text, returned to the panel so it can
	// be edited. The public surfaces get the parsed structure instead, and
	// never this.
	Body string `json:"body"`
}

// InfoPage is one page as an operator sees it.
type InfoPage struct {
	Slug string `json:"slug"`
	Kind string `json:"kind"`
	// PublishedAt is zero for a draft, which answers 404 publicly.
	PublishedAt time.Time `json:"publishedAt,omitzero"`
	// Listed decides whether the page appears in the customer panel's menu. A
	// page can be published and unlisted: a document that exists to satisfy a
	// provider's review needs a stable address rather than a menu entry.
	Listed    bool             `json:"listed"`
	SortOrder int32            `json:"sortOrder"`
	Locales   []InfoPageLocale `json:"locales,omitempty"`
	// AvailableLocales is the summary the list view shows, so a page missing a
	// translation is visible without loading every body.
	AvailableLocales []string  `json:"availableLocales,omitempty"`
	UpdatedAt        time.Time `json:"updatedAt"`
	UpdatedBy        string    `json:"updatedBy,omitempty"`
}

// PublicInfoPage is what a reader gets: a title and a parsed document, never
// the source text and never HTML.
type PublicInfoPage struct {
	Slug     string            `json:"slug"`
	Kind     string            `json:"kind"`
	Locale   string            `json:"locale"`
	Title    string            `json:"title"`
	Document infopage.Document `json:"document"`
	// UpdatedAt is published because a terms document that changed is a thing
	// people are entitled to notice.
	UpdatedAt time.Time `json:"updatedAt"`
}

// PublicInfoPageSummary is one entry in the public list.
type PublicInfoPageSummary struct {
	Slug  string `json:"slug"`
	Kind  string `json:"kind"`
	Title string `json:"title"`
}

// InfoPageKinds are the documents this build knows how to be asked for. `custom`
// is the escape hatch, and the four named ones exist so the customer panel can
// link to "the privacy policy" without an operator naming the same slug in two
// places.
var InfoPageKinds = []string{"faq", "terms", "offer", "privacy", "custom"}

var infoPageSlugPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{1,48}$`)

// InfoPages lists every page, drafts included.
func (service *Service) InfoPages(ctx context.Context) ([]InfoPage, error) {
	rows, err := service.queries().ListInfoPages(ctx)
	if err != nil {
		return nil, err
	}
	pages := make([]InfoPage, 0, len(rows))
	for _, row := range rows {
		pages = append(pages, InfoPage{
			Slug: row.Slug, Kind: row.Kind, PublishedAt: row.PublishedAt.Time,
			Listed: row.Listed, SortOrder: row.SortOrder,
			AvailableLocales: row.Locales,
			UpdatedAt:        row.UpdatedAt.Time, UpdatedBy: uuidString(row.UpdatedBy),
		})
	}
	return pages, nil
}

// InfoPage reads one page with every translation, for the editor.
func (service *Service) InfoPage(ctx context.Context, slug string) (InfoPage, error) {
	row, err := service.queries().GetInfoPage(ctx, slug)
	if err != nil {
		return InfoPage{}, notFound(err)
	}
	localizations, err := service.queries().GetInfoPageLocalizations(ctx, slug)
	if err != nil {
		return InfoPage{}, err
	}
	page := InfoPage{
		Slug: row.Slug, Kind: row.Kind, PublishedAt: row.PublishedAt.Time,
		Listed: row.Listed, SortOrder: row.SortOrder,
		UpdatedAt: row.UpdatedAt.Time, UpdatedBy: uuidString(row.UpdatedBy),
		Locales: make([]InfoPageLocale, 0, len(localizations)),
	}
	for _, localization := range localizations {
		page.Locales = append(page.Locales, InfoPageLocale{
			Locale: localization.Locale, Title: localization.Title, Body: localization.Body,
		})
	}
	return page, nil
}

// SaveInfoPage creates or updates a page and its translations.
//
// A translation with an empty title and body is deleted rather than stored
// empty, so "this page has no Russian version" is one state rather than two.
func (service *Service) SaveInfoPage(
	ctx context.Context, page InfoPage, actor Actor,
) (InfoPage, error) {
	page.Slug = strings.ToLower(strings.TrimSpace(page.Slug))
	if !infoPageSlugPattern.MatchString(page.Slug) {
		return InfoPage{}, wrapValidation(fmt.Errorf(
			"%q is not an address; use lowercase letters, digits, and dashes", page.Slug,
		))
	}
	if !allowed(InfoPageKinds, page.Kind) {
		return InfoPage{}, wrapValidation(fmt.Errorf("%q is not a page kind", page.Kind))
	}

	written := 0
	for index, locale := range page.Locales {
		locale.Locale = strings.ToLower(strings.TrimSpace(locale.Locale))
		if locale.Locale != "en" && locale.Locale != "ru" {
			return InfoPage{}, wrapValidation(fmt.Errorf("%q is not a supported language", locale.Locale))
		}
		locale.Title = strings.TrimSpace(locale.Title)
		locale.Body = strings.TrimSpace(locale.Body)
		if locale.Title != "" && locale.Body != "" {
			written++
		} else if locale.Title != "" || locale.Body != "" {
			// Half a translation is a page that renders a heading over nothing,
			// or nothing over a body. Both are worse than an absent language.
			return InfoPage{}, wrapValidation(fmt.Errorf(
				"the %s version needs both a title and a body, or neither", locale.Locale,
			))
		}
		if len(locale.Body) > 40000 {
			return InfoPage{}, wrapValidation(fmt.Errorf(
				"the %s body is longer than 40000 characters", locale.Locale,
			))
		}
		page.Locales[index] = locale
	}
	// A page with no language at all has no content at any address, which is a
	// 404 an operator would spend an afternoon on.
	if written == 0 {
		return InfoPage{}, wrapValidation(fmt.Errorf("a page needs at least one language"))
	}

	err := service.inTx(ctx, func(queries *dbgen.Queries) error {
		if _, err := queries.UpsertInfoPage(ctx, dbgen.UpsertInfoPageParams{
			Slug: page.Slug, Kind: page.Kind, Listed: page.Listed,
			SortOrder: page.SortOrder, UpdatedBy: optionalUUID(actor.AdminID),
		}); err != nil {
			return err
		}
		for _, locale := range page.Locales {
			if locale.Title == "" {
				if err := queries.DeleteInfoPageLocalization(
					ctx, dbgen.DeleteInfoPageLocalizationParams{
						PageSlug: page.Slug, Locale: locale.Locale,
					},
				); err != nil {
					return err
				}
				continue
			}
			if err := queries.UpsertInfoPageLocalization(
				ctx, dbgen.UpsertInfoPageLocalizationParams{
					PageSlug: page.Slug, Locale: locale.Locale,
					Title: locale.Title, Body: locale.Body,
				},
			); err != nil {
				return err
			}
		}
		return appendAudit(ctx, queries, actor.audit(
			"content.page.saved", "marketing", "info_page", page.Slug,
			map[string]any{"kind": page.Kind, "listed": page.Listed, "languages": written},
		))
	})
	if err != nil {
		return InfoPage{}, err
	}
	return service.InfoPage(ctx, page.Slug)
}

// SetInfoPagePublication publishes or withdraws a page.
//
// Withdrawing is reversible and takes the address out of the world without
// destroying it. Deleting is the irreversible one, and it takes the address a
// payment provider approved with it.
func (service *Service) SetInfoPagePublication(
	ctx context.Context, slug string, published bool, actor Actor,
) (InfoPage, error) {
	var saved InfoPage
	err := service.inTx(ctx, func(queries *dbgen.Queries) error {
		row, err := queries.SetInfoPagePublication(ctx, dbgen.SetInfoPagePublicationParams{
			Published: published, Slug: slug, UpdatedBy: optionalUUID(actor.AdminID),
		})
		if err != nil {
			return notFound(err)
		}
		saved = InfoPage{
			Slug: row.Slug, Kind: row.Kind, PublishedAt: row.PublishedAt.Time,
			Listed: row.Listed, SortOrder: row.SortOrder,
			UpdatedAt: row.UpdatedAt.Time, UpdatedBy: uuidString(row.UpdatedBy),
		}
		action := "content.page.withdrawn"
		if published {
			action = "content.page.published"
		}
		return appendAudit(ctx, queries, actor.audit(
			action, "marketing", "info_page", slug, nil,
		))
	})
	if err != nil {
		return InfoPage{}, err
	}
	return saved, nil
}

// DeleteInfoPage removes a page and its translations.
func (service *Service) DeleteInfoPage(ctx context.Context, slug string, actor Actor) error {
	return service.inTx(ctx, func(queries *dbgen.Queries) error {
		removed, err := queries.DeleteInfoPage(ctx, slug)
		if err != nil {
			return err
		}
		if removed == 0 {
			return ErrNotFound
		}
		return appendAudit(ctx, queries, actor.audit(
			"content.page.deleted", "marketing", "info_page", slug, nil,
		))
	})
}

// PublicInfoPages lists what a reader may see, in their language.
func (service *Service) PublicInfoPages(
	ctx context.Context, locale string,
) ([]PublicInfoPageSummary, error) {
	rows, err := service.queries().ListPublishedInfoPages(ctx, normaliseLocale(locale))
	if err != nil {
		return nil, err
	}
	pages := make([]PublicInfoPageSummary, 0, len(rows))
	for _, row := range rows {
		pages = append(pages, PublicInfoPageSummary{
			Slug: row.Slug, Kind: row.Kind, Title: row.Title,
		})
	}
	return pages, nil
}

// PublicInfoPage reads one published page, parsed.
//
// The language falls back rather than 404ing: a page that exists in English
// only must still answer at its address for a Russian reader, because the
// address is what a provider approved and what somebody bookmarked.
func (service *Service) PublicInfoPage(
	ctx context.Context, slug, locale string,
) (PublicInfoPage, error) {
	row, err := service.queries().GetPublishedInfoPage(ctx, dbgen.GetPublishedInfoPageParams{
		Slug: slug, Locale: normaliseLocale(locale),
	})
	if err != nil {
		return PublicInfoPage{}, notFound(err)
	}
	return PublicInfoPage{
		Slug: row.Slug, Kind: row.Kind, Locale: row.Locale, Title: row.Title,
		Document: infopage.Parse(row.Body), UpdatedAt: row.UpdatedAt.Time,
	}, nil
}

// normaliseLocale maps anything unfamiliar to English, which is the default
// language of the documentation tree and of this application.
func normaliseLocale(locale string) string {
	if strings.HasPrefix(strings.ToLower(strings.TrimSpace(locale)), "ru") {
		return "ru"
	}
	return "en"
}
