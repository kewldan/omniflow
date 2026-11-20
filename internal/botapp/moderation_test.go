package botapp

import (
	"errors"
	"testing"

	"github.com/go-telegram/bot/models"
)

func TestDangerousNames(t *testing.T) {
	cases := []struct {
		user models.User
		want bool
	}{
		{models.User{FirstName: "Поддержка", LastName: "VPN"}, true},
		{models.User{FirstName: "Ivan", Username: "vpn_admin_bot"}, true},
		{models.User{FirstName: "Join t.me/scam"}, true},
		{models.User{FirstName: "Мария", LastName: "Иванова"}, false},
	}
	for _, testCase := range cases {
		if got := dangerousName(&testCase.user, defaultNamePatterns); got != testCase.want {
			t.Errorf("%+v = %v", testCase.user, got)
		}
	}
	if dangerousName(&models.User{FirstName: "Support"}, []string{"casino"}) {
		t.Fatal("an operator's own pattern list replaces the defaults")
	}
}

func TestActivityActionKeepsOnlyTheRoute(t *testing.T) {
	cases := map[string]*models.Update{
		"plans":     {CallbackQuery: &models.CallbackQuery{Data: "plans"}},
		"pay":       {CallbackQuery: &models.CallbackQuery{Data: "pay:018f0000-0000:1500"}},
		"cmd:start": {Message: &models.Message{Text: "/start ref_ABC"}},
		"":          {Message: &models.Message{Text: "my private message"}},
	}
	for want, update := range cases {
		if got := activityAction(update); got != want {
			t.Errorf("activityAction = %q, want %q", got, want)
		}
	}
	if !chatClosed(errors.New("Forbidden: bot was blocked by the user")) || chatClosed(errors.New("Too Many Requests")) {
		t.Fatal("chatClosed misclassifies")
	}
}
