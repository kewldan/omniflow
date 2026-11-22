package operator

import (
	"errors"
	"strings"
	"testing"
	"time"
	"unicode"

	telegram "github.com/go-telegram/bot"
)

// Topic names are plain words in both languages; the emoji is the icon, and
// every stream has one.
func TestTopicNamesCarryNoEmoji(t *testing.T) {
	for _, kind := range Kinds {
		for _, locale := range []string{"en", "ru"} {
			name := topicTitle(kind, locale)
			if (locale == "en" && topicNames[kind] == "") || (locale == "ru" && topicTitlesRU[kind] == "") {
				t.Errorf("%s/%s has no name", kind, locale)
			}
			for _, letter := range name {
				if letter > 0x2000 && !unicode.IsLetter(letter) {
					t.Errorf("%s/%s name %q carries a symbol %q", kind, locale, name, letter)
				}
			}
		}
		if topicEmoji[kind] == "" {
			t.Errorf("%s has no icon", kind)
		}
	}
	if topicHeading("infrastructure", "en") != "🤖 Infrastructure" {
		t.Fatalf("heading: %q", topicHeading("infrastructure", "en"))
	}
}

func TestEmojiMatchIgnoresVariationSelectors(t *testing.T) {
	if plainEmoji("❗️") != plainEmoji("❗") || plainEmoji("‼️") != "‼" {
		t.Fatal("variation selectors are not ignored")
	}
}

func TestRateLimitIsRead(t *testing.T) {
	if wait, ok := rateLimited(&telegram.TooManyRequestsError{Message: "too many requests", RetryAfter: 38}); !ok || wait != 39*time.Second {
		t.Fatalf("typed: %v %v", wait, ok)
	}
	if wait, ok := rateLimited(errors.New("Too Many Requests: retry after 12")); !ok || wait != 13*time.Second {
		t.Fatalf("text: %v %v", wait, ok)
	}
	if _, ok := rateLimited(errors.New("Bad Request: not enough rights")); ok {
		t.Fatal("a refusal read as a rate limit")
	}
	if !strings.Contains(topicHeading("unknown_kind", "en"), "Unknown kind") {
		t.Fatal("an unknown kind has no heading")
	}
}
