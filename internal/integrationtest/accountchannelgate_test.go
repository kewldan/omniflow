//go:build integration

package integrationtest

import (
	"context"
	"errors"
	"testing"

	"github.com/omniflow/omniflow/internal/accountcheckout"
	"github.com/omniflow/omniflow/internal/commercepg"
)

// The web checkout applies the same required-channel gate the bot applies
// before "Pay", over the memberships the worker records: a customer known to
// be absent is refused with the channel list, an unknown or stale answer never
// blocks, an exempt customer passes, and a customer the bot cannot reach is
// never asked.
func TestWebCheckoutAppliesTheRequiredChannelGate(t *testing.T) {
	harness := newHarness(t)
	ctx := context.Background()
	store := commercepg.New(harness.pool, nil, testOptions())
	service := newAccountCheckout(t, harness, store)
	planVersionID := harness.catalog(ctx, t, "gated-plan", 34900)

	var channelID string
	if err := harness.pool.QueryRow(ctx, `INSERT INTO required_channels
		(telegram_chat_id, username, title, enabled, require_for_purchase, require_for_activation)
		VALUES (-1001234567890, 'omniflow_news', 'Omniflow news', true, true, false)
		RETURNING id::text`).Scan(&channelID); err != nil {
		t.Fatalf("seed channel: %v", err)
	}

	telegramCustomer := func(subject string) string {
		t.Helper()
		customerID := harness.customer(ctx, t)
		if _, err := harness.pool.Exec(ctx, `INSERT INTO identities (user_id, provider, provider_subject, verified_at, status)
			VALUES ($1::uuid, 'telegram', $2, now(), 'active')`, customerID, subject); err != nil {
			t.Fatalf("link telegram: %v", err)
		}
		return customerID
	}
	confirm := func(customerID string) error {
		t.Helper()
		openSession(ctx, t, service, customerID, planVersionID, "purchase")
		_, err := service.ConfirmCheckout(ctx, customerID, "en")
		return err
	}

	// Known to be absent: refused, and the refusal names the channel with a link.
	absent := telegramCustomer("800100")
	if _, err := harness.pool.Exec(ctx, `INSERT INTO channel_memberships (user_id, channel_id, state, checked_at)
		VALUES ($1::uuid, $2::uuid, 'absent', now())`, absent, channelID); err != nil {
		t.Fatalf("record absence: %v", err)
	}
	err := confirm(absent)
	if !errors.Is(err, accountcheckout.ErrChannelRequired) {
		t.Fatalf("an absent customer was not refused: %v", err)
	}
	var refusal accountcheckout.ChannelRefusal
	if !errors.As(err, &refusal) || len(refusal.Missing) != 1 ||
		refusal.Missing[0].Title != "Omniflow news" || refusal.Missing[0].InviteURL != "https://t.me/omniflow_news" {
		t.Fatalf("refusal = %+v, want the channel with its invite link", refusal)
	}
	var orders int
	if err = harness.pool.QueryRow(ctx, `SELECT count(*) FROM orders WHERE user_id = $1::uuid`, absent).Scan(&orders); err != nil {
		t.Fatalf("count orders: %v", err)
	}
	if orders != 0 {
		t.Fatalf("%d orders were created for a refused confirmation", orders)
	}

	// A stale absence is unknown, and unknown never blocks: the API has no live
	// check, and an outage must not gate a purchase.
	stale := telegramCustomer("800101")
	if _, err = harness.pool.Exec(ctx, `INSERT INTO channel_memberships (user_id, channel_id, state, checked_at)
		VALUES ($1::uuid, $2::uuid, 'absent', now() - interval '2 days')`, stale, channelID); err != nil {
		t.Fatalf("record stale absence: %v", err)
	}
	if err = confirm(stale); err != nil {
		t.Fatalf("a stale absence blocked a purchase: %v", err)
	}

	// Never checked: unknown, allowed.
	unchecked := telegramCustomer("800102")
	if err = confirm(unchecked); err != nil {
		t.Fatalf("an unchecked customer was blocked: %v", err)
	}

	// A member passes.
	member := telegramCustomer("800103")
	if _, err = harness.pool.Exec(ctx, `INSERT INTO channel_memberships (user_id, channel_id, state, checked_at)
		VALUES ($1::uuid, $2::uuid, 'member', now())`, member, channelID); err != nil {
		t.Fatalf("record membership: %v", err)
	}
	if err = confirm(member); err != nil {
		t.Fatalf("a member was blocked: %v", err)
	}

	// An exempt customer passes even when absent.
	exempt := telegramCustomer("800104")
	if _, err = harness.pool.Exec(ctx, `INSERT INTO channel_memberships (user_id, channel_id, state, checked_at)
		VALUES ($1::uuid, $2::uuid, 'absent', now())`, exempt, channelID); err != nil {
		t.Fatalf("record absence: %v", err)
	}
	if _, err = harness.pool.Exec(ctx, `INSERT INTO channel_exemptions (user_id, reason)
		VALUES ($1::uuid, 'integration test')`, exempt); err != nil {
		t.Fatalf("record exemption: %v", err)
	}
	if err = confirm(exempt); err != nil {
		t.Fatalf("an exempt customer was blocked: %v", err)
	}

	// A customer with no Telegram identity cannot be in a Telegram channel and
	// is not asked to be.
	web := harness.customer(ctx, t)
	if err = confirm(web); err != nil {
		t.Fatalf("a customer without Telegram was blocked: %v", err)
	}
}
