package botapp

import (
	"strings"
	"testing"

	"github.com/omniflow/omniflow/internal/termsgate"
)

var gateDocuments = []termsgate.Document{
	{Kind: "terms", Slug: "terms", Title: "Правила сервиса"},
	{Kind: "offer", Slug: "offer", Title: "Публичная оферта"},
	{Kind: "privacy", Slug: "privacy", Title: "Политика конфиденциальности"},
}

// The gate links every document on the public site, says that accepting
// consents to the processing of personal data, and ends with the accept
// button.
func TestTermsGateLinksEveryDocument(t *testing.T) {
	view := termsGateView(LocaleRussian, gateDocuments, "https://lk.example.com/", "https://lk.example.com/pages/terms")
	rows := view.Keyboard.InlineKeyboard
	if len(rows) != 4 {
		t.Fatalf("rows = %d, want three documents and the accept button", len(rows))
	}
	for index, document := range gateDocuments {
		button := rows[index][0]
		if button.URL != "https://lk.example.com/pages/"+document.Slug || !strings.Contains(button.Text, document.Title) {
			t.Fatalf("row %d = %q → %q", index, button.Text, button.URL)
		}
	}
	if rows[3][0].CallbackData != actionPrefix+termsAcceptAction {
		t.Fatalf("last row = %+v, want the accept button", rows[3][0])
	}
	if !strings.Contains(view.Text, "персональных данных") {
		t.Fatalf("text %q does not mention the privacy consent", view.Text)
	}
}

// Without a public address the documents open inside Telegram, and the
// gate lets those buttons through.
func TestTermsGateOpensDocumentsInTelegramWithoutAPublicAddress(t *testing.T) {
	view := termsGateView(LocaleEnglish, gateDocuments[:2], "", "")
	button := view.Keyboard.InlineKeyboard[0][0]
	if button.URL != "" || button.CallbackData != actionPrefix+"page:terms" {
		t.Fatalf("button = %+v, want an in-bot page", button)
	}
	if !termsPassThrough(button.CallbackData) || termsPassThrough(actionPrefix+"pages") {
		t.Fatal("only a document passes the gate")
	}
	if strings.Contains(view.Text, "personal data") {
		t.Fatalf("text %q mentions a privacy policy that is not published", view.Text)
	}
}

// With nothing published, the configured terms address stands in.
func TestTermsGateFallsBackToTheConfiguredAddress(t *testing.T) {
	view := termsGateView(LocaleEnglish, nil, "https://lk.example.com", "https://lk.example.com/pages/terms")
	rows := view.Keyboard.InlineKeyboard
	if len(rows) != 2 || rows[0][0].URL != "https://lk.example.com/pages/terms" {
		t.Fatalf("rows = %+v", rows)
	}
}
