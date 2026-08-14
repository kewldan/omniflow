//go:build integration

package integrationtest

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/omniflow/omniflow/internal/panelpg"
)

// Information pages against a real database.
//
// Two properties need a container. Publication is a nullable column and the
// public reads filter on it, so "a draft is invisible" is a statement about a
// query rather than about Go. And a language falling back is an ORDER BY, which
// is exactly the kind of thing a mock agrees with whatever the code believed.

func TestADraftIsInvisibleUntilItIsPublished(t *testing.T) {
	ctx := context.Background()
	harness := newHarness(t)
	service := newOperations(t, harness)
	actor := harness.operator(ctx, t, "pages@example.test")

	if _, err := service.SaveInfoPage(ctx, panelpg.InfoPage{
		Slug: "privacy", Kind: "privacy", Listed: true,
		Locales: []panelpg.InfoPageLocale{
			{Locale: "en", Title: "Privacy policy", Body: "## What we keep\n\nAs little as possible."},
		},
	}, actor); err != nil {
		t.Fatalf("save: %v", err)
	}

	// A draft answers nothing publicly, and it is a 404 rather than a distinct
	// state: telling an anonymous visitor that an address exists as a draft is
	// telling them something they have no business knowing.
	if _, err := service.PublicInfoPage(ctx, "privacy", "en"); !errors.Is(err, panelpg.ErrNotFound) {
		t.Fatalf("an unpublished page was readable: %v", err)
	}
	listed, err := service.PublicInfoPages(ctx, "en")
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(listed) != 0 {
		t.Fatalf("a draft appeared in the public list: %+v", listed)
	}
	// The operator sees it, because editing it is what they came for.
	pages, err := service.InfoPages(ctx)
	if err != nil {
		t.Fatalf("panel list: %v", err)
	}
	if len(pages) != 1 || !pages[0].PublishedAt.IsZero() {
		t.Fatalf("the operator view is %+v", pages)
	}

	if _, err := service.SetInfoPagePublication(ctx, "privacy", true, actor); err != nil {
		t.Fatalf("publish: %v", err)
	}

	page, err := service.PublicInfoPage(ctx, "privacy", "en")
	if err != nil {
		t.Fatalf("read after publishing: %v", err)
	}
	// The body arrives parsed, never as source text and never as HTML.
	if len(page.Document.Blocks) != 2 || page.Document.Blocks[0].Kind != "heading" {
		t.Fatalf("the document did not parse: %+v", page.Document)
	}

	// Withdrawing is reversible and takes the address out of the world again.
	if _, err := service.SetInfoPagePublication(ctx, "privacy", false, actor); err != nil {
		t.Fatalf("withdraw: %v", err)
	}
	if _, err := service.PublicInfoPage(ctx, "privacy", "en"); !errors.Is(err, panelpg.ErrNotFound) {
		t.Fatalf("a withdrawn page is still readable: %v", err)
	}
}

// The address is what a payment provider approved and what somebody bookmarked,
// so it must answer even for a reader whose language the page does not have.
func TestALanguageFallsBackRatherThanFailing(t *testing.T) {
	ctx := context.Background()
	harness := newHarness(t)
	service := newOperations(t, harness)
	actor := harness.operator(ctx, t, "pages-locale@example.test")

	if _, err := service.SaveInfoPage(ctx, panelpg.InfoPage{
		Slug: "offer", Kind: "offer", Listed: true,
		Locales: []panelpg.InfoPageLocale{
			{Locale: "en", Title: "Offer", Body: "English only."},
		},
	}, actor); err != nil {
		t.Fatalf("save: %v", err)
	}
	if _, err := service.SetInfoPagePublication(ctx, "offer", true, actor); err != nil {
		t.Fatalf("publish: %v", err)
	}

	page, err := service.PublicInfoPage(ctx, "offer", "ru")
	if err != nil {
		t.Fatalf("a Russian reader got %v instead of the English page", err)
	}
	if page.Locale != "en" {
		t.Fatalf("the fallback returned %q", page.Locale)
	}

	// Once the Russian version exists, it is preferred.
	if _, err := service.SaveInfoPage(ctx, panelpg.InfoPage{
		Slug: "offer", Kind: "offer", Listed: true,
		Locales: []panelpg.InfoPageLocale{
			{Locale: "en", Title: "Offer", Body: "English only."},
			{Locale: "ru", Title: "Оферта", Body: "Русская версия."},
		},
	}, actor); err != nil {
		t.Fatalf("add russian: %v", err)
	}
	page, err = service.PublicInfoPage(ctx, "offer", "ru")
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if page.Locale != "ru" || page.Title != "Оферта" {
		t.Fatalf("a Russian reader got %+v", page)
	}
}

func TestHalfATranslationIsRefused(t *testing.T) {
	ctx := context.Background()
	harness := newHarness(t)
	service := newOperations(t, harness)
	actor := harness.operator(ctx, t, "pages-partial@example.test")

	for name, locales := range map[string][]panelpg.InfoPageLocale{
		"title with no body": {{Locale: "en", Title: "Terms"}},
		"body with no title": {{Locale: "en", Body: "Some terms."}},
		"no language at all": {},
	} {
		if _, err := service.SaveInfoPage(ctx, panelpg.InfoPage{
			Slug: "terms", Kind: "terms", Locales: locales,
		}, actor); !errors.Is(err, panelpg.ErrValidaton) {
			t.Errorf("%s was accepted: %v", name, err)
		}
	}

	// And an address that is not an address.
	if _, err := service.SaveInfoPage(ctx, panelpg.InfoPage{
		Slug: "Not An Address", Kind: "terms",
		Locales: []panelpg.InfoPageLocale{{Locale: "en", Title: "T", Body: "B"}},
	}, actor); !errors.Is(err, panelpg.ErrValidaton) {
		t.Errorf("a malformed slug was accepted: %v", err)
	}
}

// An unlisted page keeps its address and leaves the menu, which is what a
// document that exists to satisfy a provider's review needs.
func TestAnUnlistedPageKeepsItsAddress(t *testing.T) {
	ctx := context.Background()
	harness := newHarness(t)
	service := newOperations(t, harness)
	actor := harness.operator(ctx, t, "pages-unlisted@example.test")

	if _, err := service.SaveInfoPage(ctx, panelpg.InfoPage{
		Slug: "offer-2026", Kind: "offer", Listed: false,
		Locales: []panelpg.InfoPageLocale{
			{Locale: "en", Title: "Offer", Body: "Terms of the offer."},
		},
	}, actor); err != nil {
		t.Fatalf("save: %v", err)
	}
	if _, err := service.SetInfoPagePublication(ctx, "offer-2026", true, actor); err != nil {
		t.Fatalf("publish: %v", err)
	}

	listed, err := service.PublicInfoPages(ctx, "en")
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(listed) != 0 {
		t.Fatalf("an unlisted page appeared in the menu: %+v", listed)
	}
	page, err := service.PublicInfoPage(ctx, "offer-2026", "en")
	if err != nil {
		t.Fatalf("an unlisted page lost its address: %v", err)
	}
	if !strings.Contains(page.Title, "Offer") {
		t.Fatalf("read back %+v", page)
	}
}
