//go:build integration

package integrationtest

import (
	"context"
	"strings"
	"testing"

	"github.com/omniflow/omniflow/internal/termsgate"
)

// With the terms required, the bot shows the gate instead of the menu until
// the customer accepts, the acceptance is a terms consent of the current
// version, and raising the version asks again.
func TestTermsGateHoldsTheBotUntilAccepted(t *testing.T) {
	ctx := context.Background()
	harness := newHarness(t)
	flow := newBotFlow(ctx, t, harness)
	gate := termsgate.New(harness.pool)
	if _, err := harness.pool.Exec(ctx, `UPDATE installation_settings SET document = '{"requireTerms": true, "termsVersion": "2026-11"}' WHERE section = 'marketing'`); err != nil {
		t.Fatal(err)
	}
	flow.say(ctx, 713001, "/start")
	gateScreen := flow.telegram.lastTo(t, 713001)
	if !strings.Contains(gateScreen, "Terms of service") {
		t.Fatalf("start showed %q, want the terms gate", gateScreen)
	}
	customer := flow.customerOf(ctx, t, 713001)
	if blocked, _, err := gate.Blocked(ctx, customer); err != nil || !blocked {
		t.Fatalf("blocked = %v (%v), want true before accepting", blocked, err)
	}

	flow.tap(ctx, 713001, 1, "act:terms-accept")
	if blocked, _, err := gate.Blocked(ctx, customer); err != nil || blocked {
		t.Fatalf("blocked = %v (%v), want false after accepting", blocked, err)
	}
	var source, version string
	if err := harness.pool.QueryRow(ctx, `SELECT source, policy_version FROM consent_records
		WHERE user_id = $1::uuid AND purpose = 'terms' AND granted`, customer).Scan(&source, &version); err != nil {
		t.Fatalf("consent: %v", err)
	}
	if source != "telegram_bot" || version != "2026-11" {
		t.Fatalf("consent = %s/%s", source, version)
	}

	if _, err := harness.pool.Exec(ctx, `UPDATE installation_settings SET document = '{"requireTerms": true, "termsVersion": "2027-01"}' WHERE section = 'marketing'`); err != nil {
		t.Fatal(err)
	}
	if blocked, _, _ := gate.Blocked(ctx, customer); !blocked {
		t.Fatal("a new version did not ask again")
	}
}

// The gate shows the published terms, offer, and privacy policy; accepting
// with a privacy policy published records a privacy consent beside the terms
// one, and publishing a policy after somebody accepted asks them again.
func TestTermsGateRecordsPrivacyConsent(t *testing.T) {
	ctx := context.Background()
	harness := newHarness(t)
	flow := newBotFlow(ctx, t, harness)
	gate := termsgate.New(harness.pool)
	publish := func(slug, kind, title string) {
		t.Helper()
		if _, err := harness.pool.Exec(ctx, `INSERT INTO info_pages (slug, kind, published_at, sort_order)
			VALUES ($1, $2, now(), 10)`, slug, kind); err != nil {
			t.Fatal(err)
		}
		if _, err := harness.pool.Exec(ctx, `INSERT INTO info_page_localizations (page_slug, locale, title, body)
			VALUES ($1, 'ru', $2, 'Текст')`, slug, title); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := harness.pool.Exec(ctx, `UPDATE installation_settings SET document = '{"requireTerms": true, "termsVersion": "3"}' WHERE section = 'marketing'`); err != nil {
		t.Fatal(err)
	}
	publish("rules", "terms", "Правила сервиса")
	publish("help", "faq", "Вопросы")

	flow.say(ctx, 713002, "/start")
	customer := flow.customerOf(ctx, t, 713002)
	flow.tap(ctx, 713002, 1, "act:terms-accept")
	if blocked, _, err := gate.Blocked(ctx, customer); err != nil || blocked {
		t.Fatalf("blocked = %v (%v), want false after accepting the terms", blocked, err)
	}

	publish("privacy", "privacy", "Политика конфиденциальности")
	documents, err := gate.Documents(ctx, "en")
	if err != nil || len(documents) != 2 || documents[0].Slug != "rules" || documents[1].Slug != "privacy" ||
		documents[1].Title != "Политика конфиденциальности" {
		t.Fatalf("documents = %+v (%v), want the terms then the privacy policy", documents, err)
	}
	if blocked, _, _ := gate.Blocked(ctx, customer); !blocked {
		t.Fatal("publishing a privacy policy did not ask for consent to it")
	}
	if err := gate.Accept(ctx, customer, "3", "web_panel", "request-1"); err != nil {
		t.Fatal(err)
	}
	if blocked, _, err := gate.Blocked(ctx, customer); err != nil || blocked {
		t.Fatalf("blocked = %v (%v), want false after accepting both", blocked, err)
	}
	var purposes string
	if err := harness.pool.QueryRow(ctx, `SELECT string_agg(purpose, ',' ORDER BY purpose) FROM consent_records
		WHERE user_id = $1::uuid AND granted AND policy_version = '3' AND request_id = 'request-1'`, customer).Scan(&purposes); err != nil {
		t.Fatal(err)
	}
	if purposes != "privacy,terms" {
		t.Fatalf("one acceptance recorded %q, want privacy and terms", purposes)
	}
}
