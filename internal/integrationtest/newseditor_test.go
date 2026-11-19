//go:build integration

package integrationtest

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/omniflow/omniflow/internal/panelpg"
)

// An operator writes a post in two languages, cannot publish an empty one,
// cannot reuse a slug, publishes, schedules, and archives — and every change
// is audited.
func TestNewsEditor(t *testing.T) {
	ctx := context.Background()
	harness := newHarness(t)
	service, err := panelpg.New(harness.pool, operationsKey, panelpg.Options{})
	if err != nil {
		t.Fatal(err)
	}
	actor := harness.operator(ctx, t, "news@example.test")

	if _, err = service.SaveNewsPost(ctx, "", panelpg.NewsInput{Slug: "empty-post", Category: "news"}, actor); !errors.Is(err, panelpg.ErrValidaton) {
		t.Fatalf("a post without text: %v", err)
	}
	post, err := service.SaveNewsPost(ctx, "", panelpg.NewsInput{
		Slug: "spring-sale", Category: "announcement",
		Translations: map[string]panelpg.NewsTranslation{
			"ru": {Title: "Весенняя распродажа", Body: "Скидка 20% на все тарифы."},
			"en": {Title: "Spring sale", Body: "20% off every plan."},
		},
	}, actor)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if post.Status != "draft" || post.Translations["ru"].Title != "Весенняя распродажа" {
		t.Fatalf("post = %+v", post)
	}
	if _, err = service.SaveNewsPost(ctx, "", panelpg.NewsInput{
		Slug: "spring-sale", Category: "news",
		Translations: map[string]panelpg.NewsTranslation{"en": {Title: "Again", Body: "Again"}},
	}, actor); !errors.Is(err, panelpg.ErrConflict) {
		t.Fatalf("a reused slug: %v", err)
	}

	// Dropping a language removes its translation.
	post, err = service.SaveNewsPost(ctx, post.ID, panelpg.NewsInput{
		Slug: "spring-sale", Category: "announcement",
		Translations: map[string]panelpg.NewsTranslation{"ru": {Title: "Весенняя распродажа", Body: "Скидка 25%."}},
	}, actor)
	if err != nil || len(post.Translations) != 1 || post.Translations["ru"].Body != "Скидка 25%." {
		t.Fatalf("edit = %+v, %v", post, err)
	}

	if post, err = service.SetNewsState(ctx, post.ID, "published", nil, actor); err != nil || post.Status != "published" || post.PublishedAt == nil {
		t.Fatalf("publish = %+v, %v", post, err)
	}
	past := time.Now().Add(-time.Hour)
	if _, err = service.SetNewsState(ctx, post.ID, "scheduled", &past, actor); !errors.Is(err, panelpg.ErrValidaton) {
		t.Fatalf("a schedule in the past: %v", err)
	}
	future := time.Now().Add(24 * time.Hour)
	if post, err = service.SetNewsState(ctx, post.ID, "scheduled", &future, actor); err != nil || post.Status != "scheduled" {
		t.Fatalf("schedule = %+v, %v", post, err)
	}
	if post, err = service.SetNewsState(ctx, post.ID, "archived", nil, actor); err != nil || post.Status != "archived" {
		t.Fatalf("archive = %+v, %v", post, err)
	}
	posts, err := service.NewsPosts(ctx, "")
	if err != nil || len(posts) != 1 || posts[0].TitleRU != "Весенняя распродажа" {
		t.Fatalf("list = %+v, %v", posts, err)
	}
	var audited int
	if err = harness.pool.QueryRow(ctx, `SELECT count(*) FROM audit_events WHERE target_type = 'news_post'`).Scan(&audited); err != nil || audited != 5 {
		t.Fatalf("audit events = %d, %v", audited, err)
	}
}
