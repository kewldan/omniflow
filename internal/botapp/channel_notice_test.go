package botapp

import (
	"strings"
	"testing"
	"time"

	"github.com/omniflow/omniflow/internal/channelworker"
)

func TestChannelNoticeWarningNamesChannelsAndDeadline(t *testing.T) {
	t.Parallel()
	deadline := time.Date(2026, 9, 3, 12, 0, 0, 0, time.UTC)
	view := channelNoticeView(LocaleEnglish, channelworker.ChannelEvent{
		Kind: channelworker.EventWarned, GraceUntil: &deadline,
		Missing: []channelworker.MissingChannel{
			{Title: "News & <offers>", InviteURL: "https://t.me/omniflow_news"},
			{Title: "Private", InviteURL: "tg://resolve?domain=private"},
		},
	})
	if !strings.Contains(view.Text, formatDate(deadline)) {
		t.Fatalf("warning does not name the deadline: %s", view.Text)
	}
	if !strings.Contains(view.Text, "News &amp; &lt;offers&gt;") {
		t.Fatalf("channel title is not escaped: %s", view.Text)
	}
	urls := 0
	for _, row := range view.Keyboard.InlineKeyboard {
		for _, button := range row {
			if button.URL != "" {
				urls++
				if button.URL != "https://t.me/omniflow_news" {
					t.Fatalf("a non-https invite was rendered as a button: %s", button.URL)
				}
			}
		}
	}
	if urls != 1 {
		t.Fatalf("expected one join button, got %d", urls)
	}
}

func TestChannelNoticeRestorationCarriesNoChannels(t *testing.T) {
	t.Parallel()
	view := channelNoticeView(LocaleRussian, channelworker.ChannelEvent{
		Kind:    channelworker.EventRestored,
		Missing: []channelworker.MissingChannel{{Title: "Stale", InviteURL: "https://t.me/stale"}},
	})
	if strings.Contains(view.Text, "Stale") {
		t.Fatalf("a restoration must not list channels: %s", view.Text)
	}
	if len(view.Keyboard.InlineKeyboard) != 1 {
		t.Fatalf("expected only the menu button, got %d rows", len(view.Keyboard.InlineKeyboard))
	}
}

func TestChannelNoticeSuspensionSaysHowToRecover(t *testing.T) {
	t.Parallel()
	view := channelNoticeView(LocaleEnglish, channelworker.ChannelEvent{
		Kind:    channelworker.EventSuspended,
		Missing: []channelworker.MissingChannel{{Title: "Main", InviteURL: "https://t.me/main"}},
	})
	if !strings.Contains(view.Text, "suspended") || !strings.Contains(view.Text, "Main") {
		t.Fatalf("suspension notice is incomplete: %s", view.Text)
	}
}
