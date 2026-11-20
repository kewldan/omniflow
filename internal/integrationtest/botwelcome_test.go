//go:build integration

package integrationtest

import (
	"context"
	"strings"
	"testing"

	"github.com/go-telegram/bot/models"
	"github.com/omniflow/omniflow/internal/installsettings"
)

// A newcomer gets the operator's welcome once, with their name filled in and
// escaped, and an invite-code button that attributes them to the inviter whose
// code they type; the main menu carries one of the operator's tips.
func TestBotWelcomesNewcomersAndTakesATypedInviteCode(t *testing.T) {
	ctx := context.Background()
	harness := newHarness(t)
	flow := newBotFlow(ctx, t, harness)
	flow.app.UseInstallationSettings(installsettings.Static(map[string]installsettings.Document{
		"marketing": {
			"welcomeTextEn": "Hi {name}, welcome to {service}!", "referralCodePrompt": true,
			"tipsEn": "Invite friends for bonus days",
		},
		"branding": {"serviceName": "FastVPN"},
	}))
	if _, err := harness.pool.Exec(ctx, `INSERT INTO referral_programs (singleton, enabled, currency, inviter_reward_minor)
		VALUES (true, true, 'RUB', 1000)`); err != nil {
		t.Fatal(err)
	}
	inviter := flow.customerOf(ctx, t, 714001)
	if _, err := harness.pool.Exec(ctx, `INSERT INTO referral_codes (user_id, code) VALUES ($1::uuid, 'TYPEDCODE1')`, inviter); err != nil {
		t.Fatal(err)
	}

	start := func() {
		flow.deliver(ctx, &models.Update{Message: &models.Message{
			ID:   int(flow.update + 1),
			From: &models.User{ID: 714002, LanguageCode: "en", FirstName: "Bob <i>"},
			Chat: models.Chat{ID: 714002, Type: models.ChatTypePrivate},
			Text: "/start",
		}})
	}
	start()
	screens := flow.telegram.screens()
	welcome := ""
	for _, screen := range screens {
		if strings.Contains(screen.Fields["text"], "welcome to FastVPN") {
			welcome = screen.Fields["text"]
		}
	}
	if !strings.Contains(welcome, "Hi Bob &lt;i&gt;") {
		t.Fatalf("welcome = %q", welcome)
	}
	if menu := flow.telegram.lastTo(t, 714002); !strings.Contains(menu, "Invite friends for bonus days") {
		t.Fatalf("menu %q carries no tip", menu)
	}
	before := len(flow.telegram.screens())
	start()
	for _, screen := range flow.telegram.screens()[before:] {
		if strings.Contains(screen.Fields["text"], "welcome to FastVPN") {
			t.Fatal("the welcome was sent twice")
		}
	}

	flow.tap(ctx, 714002, 1, "act:ref-code")
	flow.say(ctx, 714002, "typedcode1")
	if reply := flow.telegram.lastTo(t, 714002); !strings.Contains(reply, "Code accepted") {
		t.Fatalf("reply = %q", reply)
	}
	var referrer string
	if err := harness.pool.QueryRow(ctx, `SELECT referrer_user_id::text FROM referral_attributions a
		JOIN identities i ON i.user_id = a.referred_user_id WHERE i.provider_subject = '714002'`).Scan(&referrer); err != nil || referrer != inviter {
		t.Fatalf("attribution = %q (%v)", referrer, err)
	}
}
