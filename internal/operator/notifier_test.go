package operator

import (
	"strings"
	"testing"
)

// A preview is labelled as one. Somebody will eventually see a campaign's copy
// in the operator group and reach for the pause button, and the difference
// between "this went out" and "this is what would go out" has to be in the
// message rather than in the reader's memory of who pressed what.
func TestACampaignPreviewSaysItIsAPreview(t *testing.T) {
	rendered := renderCampaignTest("Spring offer", "en", "Twenty percent off", "Body copy here.")

	for _, expected := range []string{"Spring offer", "en", "preview", "Body copy here."} {
		if !strings.Contains(rendered, expected) {
			t.Fatalf("the preview does not mention %q:\n%s", expected, rendered)
		}
	}
	if !strings.Contains(rendered, "Nobody else received it") {
		t.Fatalf("the preview does not say nobody else got it:\n%s", rendered)
	}
}

// The preview renders variables the way the customer delivery path does, which
// is to nothing at all. Filling in sample values would show copy no customer
// will ever receive, and leaving the placeholders visible would hide the one
// mistake this preview exists to catch: a variable the template declares and the
// send does not fill.
func TestACampaignPreviewRendersVariablesLikeARealSend(t *testing.T) {
	rendered := renderCampaignTest(
		"Renewal nudge", "ru",
		"Привет, {{name}}",
		"Ваша подписка {{ plan_name }} истекает {{expires_at}}.",
	)
	if strings.Contains(rendered, "{{") {
		t.Fatalf("a template placeholder survived into the preview:\n%s", rendered)
	}
	if !strings.Contains(rendered, "Ваша подписка") {
		t.Fatalf("the body did not survive substitution:\n%s", rendered)
	}
}

// Campaign copy is operator-authored and goes into a chat that renders HTML, so
// a template with markup in it must not become markup in the group.
func TestACampaignPreviewEscapesTheCampaignName(t *testing.T) {
	rendered := renderCampaignTest("<b>bold</b>", "en", "", "plain")
	if strings.Contains(rendered, "<b>bold</b>") {
		t.Fatalf("the campaign name was not escaped:\n%s", rendered)
	}
	if !strings.Contains(rendered, "&lt;b&gt;bold&lt;/b&gt;") {
		t.Fatalf("the campaign name was not escaped as expected:\n%s", rendered)
	}
}

// The preview has a topic of its own, and it is not one of the notification
// streams. A campaign preview in the incident topic would be read as an
// incident.
func TestTheCampaignPreviewTopicIsItsOwn(t *testing.T) {
	if topicNames[KindCampaignTest] == "" {
		t.Fatal("the campaign preview topic has no name")
	}
	found := false
	for _, kind := range Kinds {
		if kind == KindCampaignTest {
			found = true
		}
	}
	if !found {
		t.Fatal("the campaign preview topic is never created, so the first preview has nowhere to go")
	}
}
