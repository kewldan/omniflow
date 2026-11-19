package botapp

import (
	"net/url"
	"strings"
	"testing"

	"github.com/go-telegram/bot/models"
	"github.com/omniflow/omniflow/internal/commerce"
	"github.com/omniflow/omniflow/internal/remnawave"
)

func allButtons(view View) []models.InlineKeyboardButton {
	var buttons []models.InlineKeyboardButton
	for _, buttonRow := range view.Keyboard.InlineKeyboard {
		buttons = append(buttons, buttonRow...)
	}
	return buttons
}

func TestImportButtonsGoThroughTheOpenPage(t *testing.T) {
	t.Parallel()
	subscription := remnawave.Subscription{Found: true, SubscriptionURL: "https://sub.example/secret"}
	options := connectOptions{
		PublicURL: "https://panel.example/",
		Settings: commerce.ConnectSettings{
			HappCryptoLinks: true, HideRawLink: true,
			Mirrors: []commerce.LinkMirror{{Label: "Reserve", Origin: "https://mirror.example"}},
		},
	}
	view := connectPlatformView(LocaleEnglish, "ios", subscription, "iOS",
		[]commerce.ClientApp{{Name: "Happ", Scheme: "happ://add/"}}, nil, options)
	for _, button := range allButtons(view) {
		if button.CopyText != nil && strings.Contains(button.CopyText.Text, "sub.example") {
			t.Fatal("a hidden link must not be offered to copy")
		}
		if strings.HasPrefix(button.URL, "https://sub.example") || strings.Contains(button.URL, "mirror.example") {
			t.Fatalf("a hidden link must not be a button: %q", button.URL)
		}
		if strings.HasPrefix(button.URL, "https://panel.example/open#") {
			target, _ := url.QueryUnescape(strings.TrimPrefix(button.URL, "https://panel.example/open#"))
			if !strings.HasPrefix(target, "happ://crypt4/") {
				t.Fatalf("the Happ button must carry the encrypted link, got %q", target)
			}
			return
		}
	}
	t.Fatal("no import button went through /open")
}

func TestConnectModes(t *testing.T) {
	t.Parallel()
	subscription := remnawave.Subscription{Found: true, SubscriptionURL: "https://sub.example/secret"}
	base := connectOptions{PublicURL: "https://panel.example"}

	if _, ok := connectModeView(LocaleEnglish, subscription, base); ok {
		t.Fatal("the default mode is the guide")
	}
	miniApp := base
	miniApp.Settings.ButtonMode = commerce.ConnectModeMiniApp
	view, ok := connectModeView(LocaleEnglish, subscription, miniApp)
	if !ok || allButtons(view)[0].WebApp == nil || allButtons(view)[0].WebApp.URL != "https://panel.example/account/devices" {
		t.Fatalf("mini app mode = %+v", allButtons(view))
	}
	link := base
	link.Settings.ButtonMode = commerce.ConnectModeLink
	link.Settings.Mirrors = []commerce.LinkMirror{{Label: "Reserve", Origin: "https://mirror.example"}}
	view, ok = connectModeView(LocaleEnglish, subscription, link)
	if !ok || !strings.Contains(buttonLabels(view), "Reserve") {
		t.Fatalf("link mode = %s", buttonLabels(view))
	}
	happ := base
	happ.Settings.ButtonMode = commerce.ConnectModeHapp
	view, ok = connectModeView(LocaleEnglish, subscription, happ)
	if !ok || !strings.HasPrefix(allButtons(view)[0].URL, "https://panel.example/open#happ") {
		t.Fatalf("happ mode = %+v", allButtons(view)[0])
	}
	if !strings.Contains(buttonLabels(view), "All apps") {
		t.Fatal("every one-tap mode keeps the guide reachable")
	}
	plainHTTP := happ
	plainHTTP.PublicURL = "http://panel.example"
	view, _ = connectModeView(LocaleEnglish, subscription, plainHTTP)
	if allButtons(view)[0].CopyText == nil {
		t.Fatal("without an https panel the link is offered to copy")
	}
	if _, ok := connectModeView(LocaleEnglish, remnawave.Subscription{}, happ); ok {
		t.Fatal("no link, no one-tap screen")
	}
}
