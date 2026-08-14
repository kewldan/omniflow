package botapp

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

// A message without a keyboard must not carry a `reply_markup` field at all.
//
// This is asserted on the encoded request rather than on the struct because the
// defect only exists once it is encoded. `ReplyMarkup` is an `any`; a nil
// `*InlineKeyboardMarkup` assigned to it is a non-nil interface holding a nil
// pointer, and it marshals to `"reply_markup": null`. Telegram answers
// `Bad Request: object expected as reply markup` and drops the message.
//
// It cost every keyboard-less message the bot sends: the web sign-in link and
// its four notices, the rate-limit and promo replies, the support hints, and the
// operator's test notification. None of them were ever delivered, and nothing
// said so, because those sends discarded their error.

func TestAMessageWithNoKeyboardSendsNoReplyMarkup(t *testing.T) {
	encoded, err := json.Marshal(sendParams(42, View{Text: "no keyboard here"}))
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if strings.Contains(string(encoded), "reply_markup") {
		t.Fatalf("a keyboardless message carries reply_markup, which Telegram refuses: %s", encoded)
	}

	edited, err := json.Marshal(editParams(42, 7, View{Text: "no keyboard here either"}))
	if err != nil {
		t.Fatalf("marshal edit: %v", err)
	}
	if strings.Contains(string(edited), "reply_markup") {
		t.Fatalf("a keyboardless edit carries reply_markup: %s", edited)
	}
}

// And a message that does have one must still send it, or the fix would have
// traded one silent failure for another.
func TestAMessageWithAKeyboardStillSendsIt(t *testing.T) {
	view := View{Text: "with a keyboard", Keyboard: keyboard(
		row(callbackButton("Menu", routeHome)),
	)}

	encoded, err := json.Marshal(sendParams(42, view))
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if !strings.Contains(string(encoded), "reply_markup") {
		t.Fatalf("the keyboard was dropped: %s", encoded)
	}
	if !strings.Contains(string(encoded), "Menu") {
		t.Fatalf("the button is missing: %s", encoded)
	}
}

// The messages this actually broke, asserted by name so that a view which
// stops carrying a keyboard cannot quietly become undeliverable again.
func TestTheKeyboardlessViewsAreDeliverable(t *testing.T) {
	var none notices
	for name, view := range map[string]View{
		"web sign-in link":   {Text: text(LocaleEnglish, "weblogin.link", "https://example.test/x")},
		"sign-in notice":     {Text: text(LocaleEnglish, "weblogin.unavailable")},
		"test notification":  testNotificationView(LocaleEnglish, time.Unix(0, 0).UTC()),
		"dunning (has keys)": dunningAlertView(none, LocaleEnglish, false),
	} {
		encoded, err := json.Marshal(sendParams(42, view))
		if err != nil {
			t.Fatalf("%s: marshal: %v", name, err)
		}
		// Either a real keyboard object or no field: never a null.
		if strings.Contains(string(encoded), `"reply_markup":null`) {
			t.Fatalf("%s would be refused by Telegram: %s", name, encoded)
		}
	}
}
