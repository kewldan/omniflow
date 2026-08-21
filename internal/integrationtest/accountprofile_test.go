//go:build integration

package integrationtest

import (
	"context"
	"log/slog"
	"testing"

	"github.com/omniflow/omniflow/internal/accountpg"
)

// The profile screen's language used to write users.locale alone, while the
// bot reads bot_preferences.locale first: the web claimed to set the bot's
// language and, for any customer whose preferences row said anything, did not.
// Both rows must carry the choice afterwards.
func TestProfileLanguageIsWrittenWhereBothSurfacesReadIt(t *testing.T) {
	ctx := context.Background()
	harness := newHarness(t)
	account, err := accountpg.New(harness.pool, nil, accountpg.Options{Logger: slog.New(slog.DiscardHandler)})
	if err != nil {
		t.Fatalf("build account service: %v", err)
	}

	// A customer the bot already wrote a preferences row for, pinned to English.
	withRow := harness.customer(ctx, t)
	if _, err = harness.pool.Exec(ctx,
		`INSERT INTO bot_preferences (user_id, locale) VALUES ($1::uuid, 'en')`, withRow); err != nil {
		t.Fatalf("seed preferences: %v", err)
	}
	// And one with no row at all, as an earlier web release created them.
	withoutRow := harness.customer(ctx, t)

	for _, customerID := range []string{withRow, withoutRow} {
		updated, updateErr := account.UpdateProfile(ctx, customerID, "ru", "Europe/Moscow")
		if updateErr != nil {
			t.Fatalf("update profile for %s: %v", customerID, updateErr)
		}
		if updated.Locale != "ru" || updated.Timezone != "Europe/Moscow" {
			t.Fatalf("profile = %+v, want ru / Europe/Moscow", updated)
		}
		var userLocale, botLocale string
		if err = harness.pool.QueryRow(ctx,
			`SELECT u.locale, p.locale FROM users u JOIN bot_preferences p ON p.user_id = u.id WHERE u.id = $1::uuid`,
			customerID).Scan(&userLocale, &botLocale); err != nil {
			t.Fatalf("read both locales for %s: %v", customerID, err)
		}
		if userLocale != "ru" || botLocale != "ru" {
			t.Fatalf("users.locale = %q, bot_preferences.locale = %q; want ru on both", userLocale, botLocale)
		}
	}

	// A refused value leaves both untouched: the validation runs before the
	// transaction, and nothing is half-written.
	if _, err = account.UpdateProfile(ctx, withRow, "de", "Europe/Moscow"); err == nil {
		t.Fatal("an unsupported locale was accepted")
	}
	var botLocale string
	if err = harness.pool.QueryRow(ctx,
		`SELECT locale FROM bot_preferences WHERE user_id = $1::uuid`, withRow).Scan(&botLocale); err != nil {
		t.Fatalf("read preferences: %v", err)
	}
	if botLocale != "ru" {
		t.Fatalf("a refused update changed bot_preferences.locale to %q", botLocale)
	}
}
