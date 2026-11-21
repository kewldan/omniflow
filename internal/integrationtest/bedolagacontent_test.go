//go:build integration

package integrationtest

import (
	"context"
	"testing"

	"github.com/omniflow/omniflow/internal/bedolagaimportpg"
)

// The documents, the support history, and the consents come over with the
// customers: a closed ticket keeps its conversation and its dates, nothing is
// pushed to the customer or announced to the operators, a page an operator
// already wrote stands, and a second run writes nothing.
func TestBedolagaImportCarriesPagesTicketsAndConsents(t *testing.T) {
	fixture := newBedolagaFixture(t)
	ctx := context.Background()
	service := fixture.service(t, nil)
	now := fixture.now

	fixture.exec(ctx, t, `
CREATE TABLE privacy_policies (id serial PRIMARY KEY, language varchar(10) NOT NULL, content text NOT NULL,
  is_enabled boolean NOT NULL DEFAULT true, created_at timestamptz DEFAULT now(), updated_at timestamptz DEFAULT now());
CREATE TABLE public_offers (id serial PRIMARY KEY, language varchar(10) NOT NULL, content text NOT NULL,
  is_enabled boolean NOT NULL DEFAULT true, created_at timestamptz DEFAULT now(), updated_at timestamptz DEFAULT now());
CREATE TABLE service_rules (id serial PRIMARY KEY, "order" integer DEFAULT 0, title varchar(255), content text NOT NULL,
  is_active boolean DEFAULT true, language varchar(5) DEFAULT 'ru', created_at timestamptz DEFAULT now(), updated_at timestamptz DEFAULT now());
CREATE TABLE faq_settings (id serial PRIMARY KEY, language varchar(10) NOT NULL, is_enabled boolean NOT NULL DEFAULT true);
CREATE TABLE faq_pages (id serial PRIMARY KEY, language varchar(10) NOT NULL, title varchar(255) NOT NULL, content text NOT NULL,
  display_order integer NOT NULL DEFAULT 0, is_active boolean NOT NULL DEFAULT true,
  created_at timestamptz DEFAULT now(), updated_at timestamptz DEFAULT now());
CREATE TABLE ticket_messages (id serial PRIMARY KEY, ticket_id integer NOT NULL REFERENCES tickets(id), user_id integer,
  message_text text NOT NULL DEFAULT '', is_from_admin boolean NOT NULL DEFAULT false, has_media boolean NOT NULL DEFAULT false,
  media_type varchar(20), media_file_id varchar(255), media_caption text, created_at timestamptz DEFAULT now());
CREATE TABLE legal_consents (id serial PRIMARY KEY, user_id integer NOT NULL REFERENCES users(id), document varchar(32) NOT NULL,
  accepted_at timestamptz NOT NULL DEFAULT now(), source varchar(32), ip_address varchar(64));
ALTER TABLE tickets ADD COLUMN updated_at timestamptz DEFAULT now(), ADD COLUMN closed_at timestamptz;`)

	fixture.exec(ctx, t, `INSERT INTO privacy_policies (language, content, is_enabled) VALUES
		('ru', E'1. Общие положения\n\nПолитика <b>сервиса</b>.', true), ('en', '', false)`)
	fixture.exec(ctx, t, `INSERT INTO public_offers (language, content, is_enabled) VALUES ('ru', 'Оферта', true)`)
	fixture.exec(ctx, t, `INSERT INTO service_rules ("order", title, content, is_active, language) VALUES
		(1, 'Правила сервиса', 'Старые правила', false, 'ru'), (1, 'Правила сервиса', 'Правила', true, 'ru')`)
	fixture.exec(ctx, t, `INSERT INTO faq_settings (language, is_enabled) VALUES ('ru', true)`)
	fixture.exec(ctx, t, `INSERT INTO faq_pages (language, title, content, display_order) VALUES
		('ru', 'Как подключить', 'Скачайте Happ', 1), ('ru', 'Не работает', 'Обновите ссылку', 2)`)
	// The operator already wrote the offer in Omniflow; theirs stands.
	if _, err := fixture.harness.pool.Exec(ctx, `INSERT INTO info_pages (slug, kind, published_at) VALUES ('offer', 'offer', now());
		INSERT INTO info_page_localizations (page_slug, locale, title, body) VALUES ('offer', 'ru', 'Оферта', 'Своя оферта')`); err != nil {
		t.Fatal(err)
	}

	asked, answered := now.AddDate(0, -2, 0), now.AddDate(0, -2, 1)
	fixture.exec(ctx, t, `INSERT INTO tickets (id, user_id, title, status, priority, created_at, updated_at, closed_at) VALUES
		(101, 1, 'Не подключается', 'closed', 'high', $1, $2, $2),
		(102, 2, 'Вопрос по оплате', 'answered', 'normal', $1, $2, NULL),
		(103, 6, 'Близнец', 'open', 'normal', $1, $1, NULL)`, asked, answered)
	fixture.exec(ctx, t, `INSERT INTO ticket_messages (ticket_id, message_text, is_from_admin, has_media, media_type, media_caption, created_at) VALUES
		(101, 'Не работает VPN', false, false, NULL, NULL, $1),
		(101, '', false, true, 'photo', 'скрин ошибки', $1::timestamptz + interval '1 minute'),
		(101, 'Обновите ссылку', true, false, NULL, NULL, $2),
		(101, 'Спасибо, работает', false, false, NULL, NULL, $2::timestamptz + interval '1 minute'),
		(102, 'Где чек?', false, false, NULL, NULL, $1),
		(102, 'Отправили на почту', true, false, NULL, NULL, $2),
		(103, 'Чужой тикет', false, false, NULL, NULL, $1)`, asked, answered)
	fixture.exec(ctx, t, `INSERT INTO legal_consents (user_id, document, accepted_at, source) VALUES
		(1, 'privacy_policy', $1, 'cabinet_telegram'), (1, 'public_offer', $1, 'cabinet_telegram'),
		(2, 'privacy_policy', $1, 'cabinet_email'), (6, 'privacy_policy', $1, NULL)`, asked)

	source := fixture.read(ctx, t)
	if len(source.Pages) != 7 || len(source.Tickets) < 3 || len(source.Consents) != 4 {
		t.Fatalf("read %d pages, %d tickets, %d consents", len(source.Pages), len(source.Tickets), len(source.Consents))
	}
	result, err := service.Apply(ctx, source, fixture.mapping, bedolagaimportpg.ApplyOptions{})
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	if len(result.Failures) != 0 {
		t.Fatalf("apply failures %+v", result.Failures)
	}
	// privacy, terms, and faq in Russian; the empty English policy and the
	// operator's own offer are not written.
	if result.Written["page"] != 3 || result.Written["ticket"] != 2 || result.Written["consent"] != 3 {
		t.Fatalf("written %+v", result.Written)
	}

	var body string
	var published bool
	if err := fixture.harness.pool.QueryRow(ctx, `SELECT l.body, p.published_at IS NOT NULL FROM info_pages p
		JOIN info_page_localizations l ON l.page_slug = p.slug WHERE p.slug = 'privacy' AND l.locale = 'ru'`).Scan(&body, &published); err != nil {
		t.Fatal(err)
	}
	if body != "## 1. Общие положения\n\nПолитика сервиса." || !published {
		t.Fatalf("privacy page %q published %t", body, published)
	}
	if err := fixture.harness.pool.QueryRow(ctx, `SELECT body FROM info_page_localizations WHERE page_slug = 'offer'`).Scan(&body); err != nil || body != "Своя оферта" {
		t.Fatalf("the operator's offer was replaced: %q %v", body, err)
	}
	if err := fixture.harness.pool.QueryRow(ctx, `SELECT body FROM info_page_localizations WHERE page_slug = 'faq'`).Scan(&body); err != nil ||
		body != "## Как подключить\n\nСкачайте Happ\n\n## Не работает\n\nОбновите ссылку" {
		t.Fatalf("faq %q %v", body, err)
	}

	alice := fixture.customerOf(ctx, t, 1)
	var status, priority string
	var messages, operators, undelivered int
	if err := fixture.harness.pool.QueryRow(ctx, `SELECT t.status, t.priority,
			(SELECT count(*) FROM support_messages m WHERE m.ticket_id = t.id),
			(SELECT count(*) FROM support_messages m WHERE m.ticket_id = t.id AND m.sender = 'operator'),
			(SELECT count(*) FROM support_messages m WHERE m.ticket_id = t.id AND (m.delivered_at IS NULL OR m.read_at IS NULL))
		FROM support_tickets t WHERE t.user_id = $1::uuid`, alice).Scan(&status, &priority, &messages, &operators, &undelivered); err != nil {
		t.Fatal(err)
	}
	if status != "closed" || priority != "high" || messages != 4 || operators != 1 || undelivered != 0 {
		t.Fatalf("alice's ticket: %s %s, %d messages, %d from operators, %d undelivered", status, priority, messages, operators, undelivered)
	}
	if fixture.count(ctx, t, `SELECT count(*) FROM support_messages WHERE body = '📎 фото: скрин ошибки'`) != 1 {
		t.Fatal("a photo is carried as a line saying a file was attached")
	}
	bob := fixture.customerOf(ctx, t, 2)
	if fixture.count(ctx, t, `SELECT count(*) FROM support_tickets WHERE user_id = $1::uuid AND status = 'pending'
		AND first_response_at IS NOT NULL AND resolved_at IS NULL`, bob) != 1 {
		t.Fatal("an answered ticket waits on the customer")
	}
	if fixture.count(ctx, t, `SELECT count(*) FROM operator_notifications WHERE kind = 'support'`) != 0 {
		t.Fatal("carried tickets are not announced to the operators")
	}
	if fixture.count(ctx, t, `SELECT count(*) FROM consent_records WHERE user_id = $1::uuid AND granted
		AND policy_version = 'bedolaga' AND source = 'bedolaga:cabinet_telegram'`, alice) != 2 {
		t.Fatal("alice's two acceptances are carried")
	}

	again, err := service.Apply(ctx, fixture.read(ctx, t), fixture.mapping, bedolagaimportpg.ApplyOptions{})
	if err != nil || len(again.Failures) != 0 {
		t.Fatalf("second apply: %v %+v", err, again.Failures)
	}
	if again.Written["page"] != 0 || again.Written["ticket"] != 0 || again.Written["consent"] != 0 {
		t.Fatalf("a second run writes nothing: %+v", again.Written)
	}
	if fixture.count(ctx, t, `SELECT count(*) FROM support_tickets`) != 2 {
		t.Fatal("tickets are carried once")
	}
}
