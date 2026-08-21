//go:build integration

package integrationtest

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/omniflow/omniflow/internal/accountreferral"
	"github.com/omniflow/omniflow/internal/customerauth"
	"github.com/omniflow/omniflow/internal/customerauthpg"
)

// An identity unlinked from the security screen left a revoked row behind, and
// the upsert every later sign-in ran only updated active rows: the Telegram
// account was unreachable for good and every attempt was a 500. Signing in
// again must land on the same customer with the identity back.
func TestTelegramSignInReactivatesAnUnlinkedIdentity(t *testing.T) {
	ctx := context.Background()
	harness := newHarness(t)
	clock := time.Now().UTC()
	service := newCustomerService(t, harness, func() time.Time { return clock })

	first, err := service.SignInWithTelegram(ctx, signedWidget(700300, clock), customerauthpg.RequestContext{})
	if err != nil {
		t.Fatalf("first sign-in: %v", err)
	}
	// A second method so the unlink is allowed: the last method cannot be
	// removed. The OIDC row is written directly; the link flow needs a provider.
	if _, err = harness.pool.Exec(ctx, `INSERT INTO identities (user_id, provider, provider_subject, verified_at, status)
		VALUES ($1::uuid, 'oidc:acme', 'sub-700300', now(), 'active')`, first.Customer.ID); err != nil {
		t.Fatalf("seed second method: %v", err)
	}
	methods, err := service.ListSignInMethods(ctx, first.Customer.ID)
	if err != nil {
		t.Fatalf("list methods: %v", err)
	}
	telegramIdentity := ""
	for _, method := range methods {
		if method.Provider == customerauth.ProviderTelegram {
			telegramIdentity = method.ID
		}
	}
	if telegramIdentity == "" {
		t.Fatal("the Telegram method is not listed")
	}
	if err = service.UnlinkIdentity(ctx, first.Customer.ID, telegramIdentity, customerauthpg.RequestContext{}); err != nil {
		t.Fatalf("unlink: %v", err)
	}

	again, err := service.SignInWithTelegram(ctx, signedWidget(700300, clock), customerauthpg.RequestContext{})
	if err != nil {
		t.Fatalf("sign-in after unlink: %v", err)
	}
	if again.Customer.ID != first.Customer.ID {
		t.Fatalf("sign-in after unlink produced customer %s, want %s", again.Customer.ID, first.Customer.ID)
	}
	var status string
	var rows int
	if err = harness.pool.QueryRow(ctx,
		`SELECT status, (SELECT count(*) FROM identities WHERE provider = 'telegram' AND provider_subject = '700300')
		 FROM identities WHERE id = $1::uuid`, telegramIdentity,
	).Scan(&status, &rows); err != nil {
		t.Fatalf("read identity: %v", err)
	}
	if status != "active" || rows != 1 {
		t.Fatalf("identity status = %q with %d rows, want active and 1", status, rows)
	}
}

// A customer imported from Remnawave carries a Telegram ID on the mapping row
// and no identity row until they press /start. The widget must adopt that
// customer, as the bot does, rather than provision an empty second account.
func TestTelegramSignInAdoptsAnImportedCustomerByMapping(t *testing.T) {
	ctx := context.Background()
	harness := newHarness(t)
	clock := time.Now().UTC()
	service := newCustomerService(t, harness, func() time.Time { return clock })

	imported := harness.customer(ctx, t)
	if _, err := harness.pool.Exec(ctx,
		`INSERT INTO remnawave_users (user_id, remnawave_id, telegram_id) VALUES ($1::uuid, 7310, 700310)`,
		imported); err != nil {
		t.Fatalf("seed mapping: %v", err)
	}

	result, err := service.SignInWithTelegram(ctx, signedWidget(700310, clock), customerauthpg.RequestContext{})
	if err != nil {
		t.Fatalf("sign-in: %v", err)
	}
	if result.Customer.ID != imported {
		t.Fatalf("the widget provisioned %s instead of adopting %s", result.Customer.ID, imported)
	}
	var customers int
	if err = harness.pool.QueryRow(ctx, `SELECT count(*) FROM users`).Scan(&customers); err != nil {
		t.Fatalf("count customers: %v", err)
	}
	if customers != 1 {
		t.Fatalf("%d customers exist after adopting one, want 1", customers)
	}
	var linked int
	if err = harness.pool.QueryRow(ctx,
		`SELECT count(*) FROM identities WHERE user_id = $1::uuid AND provider = 'telegram' AND status = 'active'`,
		imported).Scan(&linked); err != nil {
		t.Fatalf("count identities: %v", err)
	}
	if linked != 1 {
		t.Fatalf("the adopted customer holds %d Telegram identities, want 1", linked)
	}
}

// When the mapping names one customer and a revoked identity names another,
// neither may be chosen silently: the sign-in is refused as identity_taken,
// never as a 500 and never by moving the identity.
func TestTelegramSignInRefusesAnIdentityClaimedByTwoRecords(t *testing.T) {
	ctx := context.Background()
	harness := newHarness(t)
	clock := time.Now().UTC()
	service := newCustomerService(t, harness, func() time.Time { return clock })

	revokedOwner := harness.customer(ctx, t)
	mappedOwner := harness.customer(ctx, t)
	if _, err := harness.pool.Exec(ctx, `INSERT INTO identities (user_id, provider, provider_subject, verified_at, status, revoked_at)
		VALUES ($1::uuid, 'telegram', '700320', now(), 'revoked', now())`, revokedOwner); err != nil {
		t.Fatalf("seed revoked identity: %v", err)
	}
	if _, err := harness.pool.Exec(ctx,
		`INSERT INTO remnawave_users (user_id, remnawave_id, telegram_id) VALUES ($1::uuid, 7320, 700320)`,
		mappedOwner); err != nil {
		t.Fatalf("seed mapping: %v", err)
	}

	_, err := service.SignInWithTelegram(ctx, signedWidget(700320, clock), customerauthpg.RequestContext{})
	if !errors.Is(err, customerauth.ErrIdentityTaken) {
		t.Fatalf("err = %v, want ErrIdentityTaken", err)
	}
	var status, owner string
	if err = harness.pool.QueryRow(ctx,
		`SELECT status, user_id::text FROM identities WHERE provider = 'telegram' AND provider_subject = '700320'`,
	).Scan(&status, &owner); err != nil {
		t.Fatalf("read identity: %v", err)
	}
	if status != "revoked" || owner != revokedOwner {
		t.Fatalf("the refused sign-in changed the identity: status %q owner %s", status, owner)
	}
}

// A customer the widget creates gets the preferences row the bot would have
// written and the language the browser asked for, in both places the two
// surfaces read it from.
func TestWidgetCreatedCustomerCarriesLocaleAndPreferences(t *testing.T) {
	ctx := context.Background()
	harness := newHarness(t)
	clock := time.Now().UTC()
	service := newCustomerService(t, harness, func() time.Time { return clock })

	result, err := service.SignInWithTelegram(ctx, signedWidget(700330, clock), customerauthpg.RequestContext{
		AcceptLanguage: "ru-RU,ru;q=0.9,en-US;q=0.8",
	})
	if err != nil {
		t.Fatalf("sign-in: %v", err)
	}
	if result.Customer.Locale != "ru" {
		t.Fatalf("users.locale = %q, want ru from Accept-Language", result.Customer.Locale)
	}
	var botLocale string
	if err = harness.pool.QueryRow(ctx,
		`SELECT locale FROM bot_preferences WHERE user_id = $1::uuid`, result.Customer.ID,
	).Scan(&botLocale); err != nil {
		t.Fatalf("a widget-created customer has no bot_preferences row: %v", err)
	}
	if botLocale != "ru" {
		t.Fatalf("bot_preferences.locale = %q, want ru", botLocale)
	}
}

// A web sign-up through an invite link is attributed the way /start ref_ is:
// once, never to oneself, never for a customer who has already paid.
func TestReferralAttributionFromTheWebFollowsTheBotsRule(t *testing.T) {
	ctx := context.Background()
	harness := newHarness(t)
	referrals := referralService(t, harness)
	if _, err := harness.pool.Exec(ctx, `INSERT INTO referral_programs
		(singleton, enabled, currency, inviter_reward_minor, invitee_reward_minor, terms_url)
		VALUES (true, true, 'RUB', 20000, 10000, 'https://example.test/referrals')`); err != nil {
		t.Fatalf("configure the referral programme: %v", err)
	}
	inviter := harness.seedReferralAccount(ctx, t, "web")
	code := referralCodeFor("web")
	invitee := harness.customer(ctx, t)

	result, err := referrals.Attribute(ctx, invitee, code)
	if err != nil {
		t.Fatalf("attribute: %v", err)
	}
	if !result.Attributed || result.Reason != accountreferral.AttributionRecorded {
		t.Fatalf("first attribution = %+v, want recorded", result)
	}

	// Idempotent: the same link followed twice, or another code afterwards, does
	// not rewrite the pair.
	result, err = referrals.Attribute(ctx, invitee, code)
	if err != nil || result.Attributed || result.Reason != accountreferral.AttributionAlreadyRecorded {
		t.Fatalf("second attribution = %+v, %v; want already_attributed", result, err)
	}
	var referrer string
	if err = harness.pool.QueryRow(ctx,
		`SELECT referrer_user_id::text FROM referral_attributions WHERE referred_user_id = $1::uuid`, invitee,
	).Scan(&referrer); err != nil {
		t.Fatalf("read attribution: %v", err)
	}
	if referrer != inviter.customerID {
		t.Fatalf("attributed to %s, want %s", referrer, inviter.customerID)
	}

	// Self-referral is impossible by construction.
	result, err = referrals.Attribute(ctx, inviter.customerID, code)
	if err != nil || result.Attributed || result.Reason != accountreferral.AttributionSelf {
		t.Fatalf("self attribution = %+v, %v; want self_referral", result, err)
	}

	// A customer who has already paid cannot be "invited" afterwards.
	paid := harness.seedReferralAccount(ctx, t, "paid")
	result, err = referrals.Attribute(ctx, paid.customerID, code)
	if err != nil || result.Attributed || result.Reason != accountreferral.AttributionNotNew {
		t.Fatalf("attribution of a paying customer = %+v, %v; want not_new", result, err)
	}

	// An unknown code is reported as such, and a malformed one the same way.
	for _, bad := range []string{"ZZZZZZZZZZ", "short", ""} {
		result, err = referrals.Attribute(ctx, harness.customer(ctx, t), bad)
		if err != nil || result.Attributed || result.Reason != accountreferral.AttributionUnknownCode {
			t.Fatalf("attribution with %q = %+v, %v; want unknown_code", bad, result, err)
		}
	}

	// With the programme off nothing is written.
	if _, err = harness.pool.Exec(ctx, `UPDATE referral_programs SET enabled = false`); err != nil {
		t.Fatalf("disable the programme: %v", err)
	}
	result, err = referrals.Attribute(ctx, harness.customer(ctx, t), code)
	if err != nil || result.Attributed || result.Reason != accountreferral.AttributionProgramOff {
		t.Fatalf("attribution with the programme off = %+v, %v; want program_disabled", result, err)
	}
}
