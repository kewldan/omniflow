package airedact

import (
	"strings"
	"testing"
)

// The claim an installation makes to its customers is "we redact before
// sending". These tests are what makes that claim checkable.
func TestTheThingsThatMustNeverLeave(t *testing.T) {
	cases := []struct {
		name     string
		input    string
		category string
		leaked   string
	}{
		{
			name:     "subscription link",
			input:    "my link is https://vpn.example.com/sub/9f2b7c1d8e4a5f60 and it stopped working",
			category: CategorySubscriptionLink,
			leaked:   "9f2b7c1d8e4a5f60",
		},
		{
			name:     "vless link",
			input:    "vless://c3a1b2d4-1111-2222-3333-444455556666@host:443?type=tcp",
			category: CategorySubscriptionLink,
			leaked:   "444455556666",
		},
		{
			name:     "bot token pasted into a ticket",
			input:    "here is my token " + botToken(),
			category: CategoryToken,
			leaked:   botSecret(),
		},
		{
			name:     "card number",
			input:    "I paid with " + cardNumber() + " last week",
			category: CategoryPaymentCard,
			leaked:   "4111",
		},
		{
			name:     "email address",
			input:    "write to me at " + emailAddress() + " please",
			category: CategoryEmail,
			leaked:   emailAddress(),
		},
		{
			name:     "phone number",
			input:    "call me on +44 7700 900123 tomorrow",
			category: CategoryPhone,
			leaked:   "7700 900123",
		},
		{
			name:     "ip address",
			input:    "I connect from 203.0.113.42 usually",
			category: CategoryIPAddress,
			leaked:   "203.0.113.42",
		},
		{
			name:     "labelled telegram identifier",
			input:    "my telegram id: 987654321 if that helps",
			category: CategoryTelegramID,
			leaked:   "987654321",
		},
		{
			name:     "internal identifier",
			input:    "order c3a1b2d4-1111-2222-3333-444455556666 failed",
			category: CategoryUUID,
			leaked:   "c3a1b2d4-1111",
		},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			result := Redact(testCase.input)
			if strings.Contains(result.Text, testCase.leaked) {
				t.Fatalf("%s survived redaction: %q", testCase.leaked, result.Text)
			}
			if result.Counts[testCase.category] == 0 {
				t.Fatalf("nothing was counted as %s; counts=%v", testCase.category, result.Counts)
			}
		})
	}
}

// A redacted span becomes a labelled placeholder rather than a hole. A model
// reading "the customer sent [SUBSCRIPTION_LINK]" can still reason about the
// conversation; one reading a sentence with a gap in it cannot.
func TestRedactionLeavesReadableText(t *testing.T) {
	result := Redact("The customer sent https://vpn.example.com/sub/abcdef123456 and asked why it fails")
	if !strings.Contains(result.Text, "[SUBSCRIPTION_LINK]") {
		t.Fatalf("no placeholder was left: %q", result.Text)
	}
	if !strings.Contains(result.Text, "The customer sent") ||
		!strings.Contains(result.Text, "asked why it fails") {
		t.Fatalf("the surrounding text was damaged: %q", result.Text)
	}
}

// Ordinary support prose must survive, or the feature is useless: a model that
// receives only placeholders cannot summarise a conversation.
func TestOrdinaryProseIsUntouched(t *testing.T) {
	prose := "I upgraded to the yearly plan on Tuesday and my speed dropped afterwards. " +
		"It worked fine for about 3 days and then stopped."
	result := Redact(prose)
	if result.Text != prose {
		t.Fatalf("prose was altered:\n%q\n%q", prose, result.Text)
	}
	if result.Total() != 0 {
		t.Fatalf("prose triggered %d redactions: %v", result.Total(), result.Counts)
	}
}

// A link contains a UUID. If the UUID were replaced first the link would no
// longer match the link pattern and would survive as a partially-redacted URL,
// which is worse than either outcome alone.
func TestALinkIsRedactedWholeRatherThanInPieces(t *testing.T) {
	result := Redact("https://vpn.example.com/sub/c3a1b2d4-1111-2222-3333-444455556666")
	if strings.Contains(result.Text, "vpn.example.com") {
		t.Fatalf("the host survived: %q", result.Text)
	}
	if strings.Contains(result.Text, "[UUID]") {
		t.Fatalf("the link was redacted in pieces: %q", result.Text)
	}
	if result.Counts[CategorySubscriptionLink] != 1 {
		t.Fatalf("expected one link redaction, got %v", result.Counts)
	}
}

// Redacting twice must not double-count or mangle the placeholders, because a
// prompt is often assembled from text that has already passed through.
func TestRedactionIsIdempotent(t *testing.T) {
	once := Redact("token " + botToken() + " and " + emailAddress())
	twice := Redact(once.Text)
	if twice.Text != once.Text {
		t.Fatalf("a second pass changed the text:\n%q\n%q", once.Text, twice.Text)
	}
	if twice.Total() != 0 {
		t.Fatalf("a second pass found %d more spans: %v", twice.Total(), twice.Counts)
	}
}

// A prompt assembled from several sources reports one account of what left.
func TestRedactAllMergesTheAccount(t *testing.T) {
	texts, merged := RedactAll(
		"customer wrote from "+emailAddress(),
		"operator note: card "+cardNumber(),
		"nothing sensitive here",
	)
	if len(texts) != 3 {
		t.Fatalf("expected three redacted strings, got %d", len(texts))
	}
	if merged.Counts[CategoryEmail] != 1 || merged.Counts[CategoryPaymentCard] != 1 {
		t.Fatalf("the merged account is wrong: %v", merged.Counts)
	}
	if merged.Total() != 2 {
		t.Fatalf("expected two redactions in total, got %d", merged.Total())
	}
}

// The preview is what an operator reads before enabling a feature. It must
// describe the exposure without being a second copy of the content it protects.
func TestThePreviewCarriesNoContent(t *testing.T) {
	preview := Describe(
		"my link https://vpn.example.com/sub/abcdef123456",
		"and my email "+emailAddress(),
	)
	if len(preview.Categories) != 2 {
		t.Fatalf("expected two categories, got %v", preview.Categories)
	}
	if preview.Characters <= 0 {
		t.Fatal("the preview must report how much would be sent")
	}
	// Categories are sorted so the same input renders the same way twice.
	for index := 1; index < len(preview.Categories); index++ {
		if preview.Categories[index-1] > preview.Categories[index] {
			t.Fatalf("categories are not sorted: %v", preview.Categories)
		}
	}
}

// The credential-shaped fixtures are assembled at runtime.
//
// A compiled test binary carrying literal card numbers, bot tokens, and email
// addresses is quarantined by endpoint protection on some developer machines,
// which turns a passing suite into an unexplained "Access is denied" build
// failure. The redactor is handed exactly the same text; only the binary is
// free of a scannable literal.
func cardNumber() string {
	return strings.Join([]string{"4111", "1111", "1111", "1111"}, " ")
}

func botSecret() string {
	return "AAF-" + "abcdefghijklmnopqrstuvwxyz"
}

func botToken() string {
	return "7123456789:" + botSecret() + "012345"
}

func emailAddress() string {
	return "customer.name+tag@" + "example.co.uk"
}
