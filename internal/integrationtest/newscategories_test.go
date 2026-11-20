//go:build integration

package integrationtest

import (
	"context"
	"errors"
	"testing"

	"github.com/omniflow/omniflow/internal/panelpg"
)

// An operator adds a coloured category whose posts are announced as
// maintenance, files a featured post with tags and a cover under it, and the
// customer inbox shows the category's name and colour with the post pinned
// apart from the paged list; opening counts views, and a category with posts
// cannot be deleted. Covers that are not https and malformed tags are refused.
func TestNewsCategoriesTagsCoverFeatured(t *testing.T) {
	ctx := context.Background()
	harness := newHarness(t)
	service, err := panelpg.New(harness.pool, operationsKey, panelpg.Options{})
	if err != nil {
		t.Fatal(err)
	}
	actor := harness.operator(ctx, t, "news-categories@example.test")

	categories, err := service.NewsCategories(ctx)
	if err != nil || len(categories) != 4 || categories[0].Code != "news" {
		t.Fatalf("seeded categories = %+v (%v)", categories, err)
	}
	if _, err = service.SaveNewsCategory(ctx, panelpg.NewsCategory{Code: "Promo", NameRU: "Акции", NameEN: "Deals",
		Color: "red", Kind: "news"}, actor); !errors.Is(err, panelpg.ErrValidaton) {
		t.Fatalf("a colour that is not #rrggbb: %v", err)
	}
	if _, err = service.SaveNewsCategory(ctx, panelpg.NewsCategory{Code: "outages", NameRU: "Перебои", NameEN: "Outages",
		Color: "#FF0000", Kind: "maintenance", SortOrder: 5}, actor); err != nil {
		t.Fatalf("save category: %v", err)
	}

	base := panelpg.NewsInput{
		Slug: "night-works", Category: "outages",
		Translations: map[string]panelpg.NewsTranslation{"ru": {Title: "Ночные работы", Body: "С 2 до 4."}},
	}
	for name, input := range map[string]panelpg.NewsInput{
		"http cover":   {Slug: base.Slug, Category: base.Category, Translations: base.Translations, CoverURL: "http://cdn.example.com/a.png"},
		"script cover": {Slug: base.Slug, Category: base.Category, Translations: base.Translations, CoverURL: "javascript:alert(1)"},
		"bad tag":      {Slug: base.Slug, Category: base.Category, Translations: base.Translations, Tags: []string{"two words"}},
		"unknown":      {Slug: base.Slug, Category: "nope", Translations: base.Translations},
		"too many tags": {Slug: base.Slug, Category: base.Category, Translations: base.Translations,
			Tags: []string{"a1", "a2", "a3", "a4", "a5", "a6", "a7", "a8", "a9", "a10", "a11"}},
	} {
		if _, err = service.SaveNewsPost(ctx, "", input, actor); !errors.Is(err, panelpg.ErrValidaton) {
			t.Fatalf("%s: %v", name, err)
		}
	}
	input := base
	input.Tags = []string{"#Works", "сеть", "works"}
	input.CoverURL = "https://cdn.example.com/night.png"
	input.Featured = true
	post, err := service.SaveNewsPost(ctx, "", input, actor)
	if err != nil {
		t.Fatalf("save post: %v", err)
	}
	if post.Category != "outages" || post.Kind != "maintenance" || !post.Featured ||
		len(post.Tags) != 2 || post.Tags[0] != "works" || post.Tags[1] != "сеть" || post.CoverURL != input.CoverURL {
		t.Fatalf("post = %+v", post)
	}
	if _, err = service.SetNewsState(ctx, post.ID, "published", nil, actor); err != nil {
		t.Fatal(err)
	}
	plain, err := service.SaveNewsPost(ctx, "", panelpg.NewsInput{Slug: "plain-news", Category: "news",
		Translations: map[string]panelpg.NewsTranslation{"ru": {Title: "Обычная", Body: "Текст."}}}, actor)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = service.SetNewsState(ctx, plain.ID, "published", nil, actor); err != nil {
		t.Fatal(err)
	}

	customer := harness.customer(ctx, t)
	support := newAccountSupport(t, harness)
	page, err := support.News(ctx, customer, "ru", "", 20)
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Featured) != 1 || page.Featured[0].ID != post.ID || page.Featured[0].CategoryName != "Перебои" ||
		page.Featured[0].CategoryColor != "#ff0000" || page.Featured[0].CoverURL != input.CoverURL {
		t.Fatalf("featured = %+v", page.Featured)
	}
	if len(page.Items) != 1 || page.Items[0].ID != plain.ID || page.Unread != 2 {
		t.Fatalf("items = %+v, unread %d", page.Items, page.Unread)
	}
	for range 2 {
		if err = support.MarkNewsRead(ctx, customer, post.ID); err != nil {
			t.Fatal(err)
		}
	}
	read, err := service.NewsPost(ctx, post.ID)
	if err != nil || read.Views != 2 || read.Reads != 1 {
		t.Fatalf("views = %d, reads = %d (%v)", read.Views, read.Reads, err)
	}

	if err = service.DeleteNewsCategory(ctx, "outages", actor); !errors.Is(err, panelpg.ErrConflict) {
		t.Fatalf("deleting a category with posts: %v", err)
	}
	// Changing the category's kind moves its posts' announcements with it.
	if _, err = service.SaveNewsCategory(ctx, panelpg.NewsCategory{Code: "outages", NameRU: "Перебои", NameEN: "Outages",
		Color: "#ff0000", Kind: "incident"}, actor); err != nil {
		t.Fatal(err)
	}
	var kind string
	if err = harness.pool.QueryRow(ctx, `SELECT category FROM news_posts WHERE id = $1::uuid`, post.ID).Scan(&kind); err != nil || kind != "incident" {
		t.Fatalf("kind after the category changed = %q (%v)", kind, err)
	}
	if _, err = service.SaveNewsCategory(ctx, panelpg.NewsCategory{Code: "empty", NameRU: "Пусто", NameEN: "Empty",
		Color: "#000000"}, actor); err != nil {
		t.Fatal(err)
	}
	if err = service.DeleteNewsCategory(ctx, "empty", actor); err != nil {
		t.Fatalf("deleting an unused category: %v", err)
	}
}
