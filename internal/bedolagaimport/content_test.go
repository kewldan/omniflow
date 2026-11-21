package bedolagaimport

import (
	"strings"
	"testing"
	"time"
)

// Bedolaga's documents are Telegram HTML or plain text. Links survive as page
// links, other markup goes, and the numbered section titles the policy and
// the offer use become headings.
func TestPageTextConvertsBedolagaDocuments(t *testing.T) {
	source := "1. Общие положения\r\n\r\nНастоящая <b>Политика</b> описывает &laquo;сервис&raquo;.<br>Связь: " +
		"<a href=\"https://t.me/omniflow_bot\">бот</a>, <a href=\"http://insecure.example\">тут</a>.\n\n\n\n" +
		"1. Сервис предоставляется лицам старше 18 лет.\n2. Ключ именной.\n\n2. Права сторон\n\nТекст."
	got := PageText(source)
	want := "## 1. Общие положения\n\nНастоящая Политика описывает «сервис».\nСвязь: " +
		"[бот](https://t.me/omniflow_bot), тут.\n\n" +
		"1. Сервис предоставляется лицам старше 18 лет.\n2. Ключ именной.\n\n## 2. Права сторон\n\nТекст."
	if got != want {
		t.Fatalf("PageText:\n%q\nwant\n%q", got, want)
	}
}

func pageSource() Source {
	updated := planNow.AddDate(0, -1, 0)
	return Source{Pages: []Page{
		{Kind: PagePrivacy, SourceID: 1, Language: "ru", Body: "Политика", Enabled: true, UpdatedAt: updated},
		{Kind: PagePrivacy, SourceID: 2, Language: "en", Body: "", Enabled: false, UpdatedAt: updated},
		{Kind: PageOffer, SourceID: 1, Language: "ru", Body: "Оферта старая", Enabled: false, UpdatedAt: updated},
		{Kind: PageRules, SourceID: 1, Language: "ru", Title: "Правила", Body: "Старые", Enabled: false, UpdatedAt: updated},
		{Kind: PageRules, SourceID: 3, Language: "ru", Title: "Правила сервиса", Body: "Новые", Enabled: true, UpdatedAt: updated},
		{Kind: PageFAQ, SourceID: 7, Language: "ru", Title: "Второй", Body: "Ответ 2", Enabled: true, Order: 2, UpdatedAt: updated},
		{Kind: PageFAQ, SourceID: 6, Language: "ru", Title: "Первый", Body: "Ответ 1", Enabled: true, Order: 1, UpdatedAt: updated},
		{Kind: PageFAQ, SourceID: 8, Language: "ru", Title: "Скрытый", Body: "Нет", Enabled: false, Order: 0, UpdatedAt: updated},
		{Kind: PageFAQ, SourceID: 9, Language: "de", Title: "Frage", Body: "Antwort", Enabled: true, UpdatedAt: updated},
	}}
}

func pageDecision(t *testing.T, plan Plan, slug, locale string) PageDecision {
	t.Helper()
	for _, decision := range plan.Pages {
		if decision.Slug == slug && decision.Locale == locale {
			return decision
		}
	}
	t.Fatalf("no page decision for %s/%s", slug, locale)
	return PageDecision{}
}

func TestPagesAreCarriedOncePerLanguage(t *testing.T) {
	target := fixtureTarget()
	target.InfoPageLocales = map[string]bool{"terms:ru": true}
	plan := Plan{}
	decidePages(&plan, pageSource(), target)

	privacy := pageDecision(t, plan, "privacy", "ru")
	if privacy.Action != ActionCreate || !privacy.Published || privacy.Body != "Политика" || privacy.Title != "Политика конфиденциальности" {
		t.Fatalf("privacy %+v", privacy)
	}
	if empty := pageDecision(t, plan, "privacy", "en"); empty.Action != ActionSkip || empty.Codes[0] != CodeEmptyPage {
		t.Fatalf("empty English policy %+v", empty)
	}
	// An offer Bedolaga had switched off is carried as a draft, so its text
	// is not lost.
	if offer := pageDecision(t, plan, "offer", "ru"); offer.Action != ActionCreate || offer.Published {
		t.Fatalf("disabled offer %+v", offer)
	}
	// A page the operator already has in Omniflow stands.
	if rules := pageDecision(t, plan, "terms", "ru"); rules.Action != ActionUnchanged || rules.Body != "Новые" || rules.Title != "Правила сервиса" {
		t.Fatalf("rules %+v", rules)
	}
	faq := pageDecision(t, plan, "faq", "ru")
	if faq.Action != ActionCreate || faq.Body != "## Первый\n\nОтвет 1\n\n## Второй\n\nОтвет 2" {
		t.Fatalf("faq %+v", faq)
	}
	if german := pageDecision(t, plan, "faq", "de"); german.Action != ActionSkip || german.Codes[0] != CodePageLocale {
		t.Fatalf("German FAQ %+v", german)
	}
}

func TestTicketsAndConsentsFollowTheirCustomer(t *testing.T) {
	source, remote := fixtureSource()
	at := planNow.AddDate(0, -2, 0)
	closed := at.Add(time.Hour)
	source.Tickets = []Ticket{
		{ID: 1, UserID: 1, Title: strings.Repeat("в", 200), Status: "closed", Priority: "high", CreatedAt: at, UpdatedAt: at, ClosedAt: &closed,
			Messages: []TicketMessage{
				{ID: 1, Text: "Не работает", CreatedAt: at},
				{ID: 2, FromAdmin: true, Text: "", HasMedia: true, MediaType: "photo", Caption: "Скрин", CreatedAt: at.Add(time.Minute)},
			}},
		{ID: 2, UserID: 1, Title: "Вопрос", Status: "answered", CreatedAt: at, UpdatedAt: at,
			Messages: []TicketMessage{{ID: 3, Text: "?", CreatedAt: at}}},
		{ID: 3, UserID: 6, Title: "Чужой", Status: "open", CreatedAt: at, UpdatedAt: at,
			Messages: []TicketMessage{{ID: 4, Text: "x", CreatedAt: at}}},
		{ID: 4, UserID: 1, Title: "Пустой", Status: "open", CreatedAt: at, UpdatedAt: at},
	}
	source.Consents = []Consent{
		{ID: 1, UserID: 1, Document: "privacy_policy", Source: "cabinet_telegram", AcceptedAt: at},
		{ID: 2, UserID: 1, Document: "public_offer", AcceptedAt: at},
		{ID: 3, UserID: 1, Document: "marketing_newsletter", AcceptedAt: at},
		{ID: 4, UserID: 6, Document: "privacy_policy", AcceptedAt: at},
	}
	target := fixtureTarget()
	target.LegacyTickets = map[int64]bool{2: true}
	target.LegacyConsents = map[int64]bool{2: true}
	plan := BuildPlan(source, remote, target, fixtureMapping(), planNow)

	byID := map[int64]TicketDecision{}
	for _, decision := range plan.Tickets {
		byID[decision.Source.ID] = decision
	}
	first := byID[1]
	if first.Action != ActionCreate || first.Status != "closed" || first.Priority != "high" || len([]rune(first.Subject)) != maxSubject {
		t.Fatalf("closed ticket %+v", first)
	}
	if byID[2].Action != ActionUnchanged || byID[2].Status != "pending" {
		t.Fatalf("answered ticket carried before %+v", byID[2])
	}
	if byID[3].Action != ActionSkip || byID[3].Codes[0] != CodeUserNotImported {
		t.Fatalf("ticket of a user who does not import %+v", byID[3])
	}
	if byID[4].Action != ActionSkip || byID[4].Codes[0] != CodeEmptyTicket {
		t.Fatalf("ticket without messages %+v", byID[4])
	}
	if body := TicketMessageBody(source.Tickets[0].Messages[1]); body != "📎 фото: Скрин" {
		t.Fatalf("media message body %q", body)
	}

	consents := map[int64]ConsentDecision{}
	for _, decision := range plan.Consents {
		consents[decision.Source.ID] = decision
	}
	if consents[1].Action != ActionCreate || consents[1].Purpose != "privacy" {
		t.Fatalf("privacy consent %+v", consents[1])
	}
	if consents[2].Action != ActionUnchanged || consents[2].Purpose != "terms" {
		t.Fatalf("offer consent carried before %+v", consents[2])
	}
	if consents[3].Action != ActionSkip || consents[3].Codes[0] != CodeConsentDocument {
		t.Fatalf("unknown document %+v", consents[3])
	}
	if consents[4].Action != ActionSkip {
		t.Fatalf("consent of a user who does not import %+v", consents[4])
	}
	if plan.Report.Tickets.Messages != 3 || plan.Report.Consents.Actions[ActionCreate] != 1 {
		t.Fatalf("report tickets %+v consents %+v", plan.Report.Tickets, plan.Report.Consents)
	}
}
