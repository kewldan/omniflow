package botapp

import (
	"slices"
	"strings"
	"testing"

	"github.com/omniflow/omniflow/internal/analytics"
)

// The bot's reason buttons are the analytics list, in its order: a reason the
// bot offers and analytics refuses would thank the customer for an answer
// nobody keeps.
func TestChurnButtonsOfferEveryReasonAnalyticsKeeps(t *testing.T) {
	reasons := make([]string, 0, len(churnReasonCodes))
	for _, entry := range churnReasonCodes {
		reasons = append(reasons, entry.reason)
	}
	if !slices.Equal(reasons, analytics.ChurnReasons) {
		t.Fatalf("bot reasons %v, analytics reasons %v", reasons, analytics.ChurnReasons)
	}
	subscription := "0b9a3a4e-6d1f-4f43-9c4b-2f6f1d7a9e10"
	view := churnQuestionView(LocaleRussian, "intro", "o", subscription,
		actionButton(text(LocaleRussian, "churn.skip"), "renew-settings:"+subscription))
	var answers []string
	for _, buttons := range view.Keyboard.InlineKeyboard {
		for _, button := range buttons {
			if strings.HasPrefix(button.CallbackData, actionPrefix+"churn:") {
				answers = append(answers, button.CallbackData)
			}
		}
	}
	if len(answers) != len(analytics.ChurnReasons) {
		t.Fatalf("%d answer buttons, want %d", len(answers), len(analytics.ChurnReasons))
	}
	for _, data := range answers {
		parts := strings.Split(strings.TrimPrefix(data, actionPrefix), ":")
		if len(parts) != 4 || churnOccasionCodes[parts[1]] != analytics.OccasionAutoRenewOff || parts[3] != subscription {
			t.Fatalf("answer %q does not name the occasion and the subscription", data)
		}
	}
	if !strings.HasPrefix(view.Text, "intro\n\n") {
		t.Fatalf("the question lost its introduction: %q", view.Text)
	}
}
