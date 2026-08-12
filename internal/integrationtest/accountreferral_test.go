//go:build integration

package integrationtest

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"strings"
	"testing"

	"github.com/omniflow/omniflow/internal/accountreferral"
)

// referralService builds the customer referral, loyalty, and privacy adapter
// against the migrated database.
//
// The encryption key is supplied so the contact routes are exercised rather
// than skipped: a contact value is the one thing this surface seals, and a suite
// that ran without a key would prove nothing about it.
func referralService(t *testing.T, harness *harness) *accountreferral.Service {
	t.Helper()
	key := make([]byte, 32)
	for index := range key {
		key[index] = byte(index + 1)
	}
	service, err := accountreferral.New(harness.pool, accountreferral.Options{
		PublicURL: "https://vpn.test", EncryptionKey: key,
		Logger: slog.New(slog.DiscardHandler),
	})
	if err != nil {
		t.Fatalf("build referral service: %v", err)
	}
	return service
}

// referralRecords is one customer's worth of history, seeded through raw SQL so
// the test exercises the same rows the reward and evaluation workers write
// rather than a shape invented for the test.
type referralRecords struct {
	customerID string
	orderID    string
	ticketBody string
	reference  string
	checkout   string
}

func (harness *harness) seedReferralAccount(ctx context.Context, t *testing.T, tag string) referralRecords {
	t.Helper()
	records := referralRecords{
		customerID: harness.customer(ctx, t),
		ticketBody: "support conversation belonging to " + tag,
		reference:  "provider-reference-" + tag,
		checkout:   "https://provider.test/pay/bearer-" + tag,
	}

	if err := harness.pool.QueryRow(ctx, `INSERT INTO orders
		(user_id, state, operation, currency, subtotal_minor, external_minor, paid_minor, idempotency_key)
		VALUES ($1::uuid, 'paid', 'purchase', 'RUB', 50000, 50000, 50000, $2)
		RETURNING id::text`, records.customerID, "order-"+tag).Scan(&records.orderID); err != nil {
		t.Fatalf("seed order for %s: %v", tag, err)
	}
	if _, err := harness.pool.Exec(ctx, `INSERT INTO payment_intents
		(order_id, provider, status, amount_minor, currency, provider_reference, checkout_url, idempotency_key)
		VALUES ($1::uuid, 'manual', 'succeeded', 50000, 'RUB', $2, $3, $4)`,
		records.orderID, records.reference, records.checkout, "intent-"+tag); err != nil {
		t.Fatalf("seed payment for %s: %v", tag, err)
	}

	var transactionID string
	if err := harness.pool.QueryRow(ctx, `INSERT INTO ledger_transactions
		(type, reference_type, reference_id, idempotency_key, reason)
		VALUES ('credit', 'order', $1, $2, $3) RETURNING id::text`,
		records.orderID, "ledger-"+tag, "operator note about "+tag).Scan(&transactionID); err != nil {
		t.Fatalf("seed ledger transaction for %s: %v", tag, err)
	}
	if _, err := harness.pool.Exec(ctx, `INSERT INTO ledger_entries
		(transaction_id, account_type, user_id, currency, amount_minor)
		VALUES ($1::uuid, 'customer_wallet', $2::uuid, 'RUB', 50000)`,
		transactionID, records.customerID); err != nil {
		t.Fatalf("seed ledger entry for %s: %v", tag, err)
	}

	var ticketID string
	if err := harness.pool.QueryRow(ctx,
		`INSERT INTO support_tickets (user_id, queue_id)
		 SELECT $1::uuid, id FROM support_queues WHERE is_default AND archived_at IS NULL
		 RETURNING id::text`,
		records.customerID).Scan(&ticketID); err != nil {
		t.Fatalf("seed ticket for %s: %v", tag, err)
	}
	if _, err := harness.pool.Exec(ctx,
		`INSERT INTO support_messages (ticket_id, sender, body) VALUES ($1::uuid, 'customer', $2)`,
		ticketID, records.ticketBody); err != nil {
		t.Fatalf("seed message for %s: %v", tag, err)
	}

	if _, err := harness.pool.Exec(ctx,
		`INSERT INTO referral_codes (user_id, code) VALUES ($1::uuid, $2)`,
		records.customerID, referralCodeFor(tag)); err != nil {
		t.Fatalf("seed referral code for %s: %v", tag, err)
	}
	return records
}

// referralCodeFor produces a deterministic ten-character code matching the
// schema's `^[A-Z0-9]{10}$` check, so a failure names the customer it came from.
func referralCodeFor(tag string) string {
	code := strings.ToUpper(tag) + "AAAAAAAAAA"
	return code[:10]
}

// TestExportCarriesOnlyTheCallersOwnRecords is the disclosure gate. A personal
// data export is the single response in this API that concentrates everything an
// installation holds about somebody, so the failure that matters is one row of
// it belonging to a different person.
func TestExportCarriesOnlyTheCallersOwnRecords(t *testing.T) {
	harness := newHarness(t)
	ctx := context.Background()
	service := referralService(t, harness)

	alpha := harness.seedReferralAccount(ctx, t, "alpha")
	bravo := harness.seedReferralAccount(ctx, t, "bravo")

	if _, err := service.AddContact(ctx, alpha.customerID, accountreferral.ContactInput{
		Kind: "email", Value: "alpha@example.com", Transactional: true,
	}, accountreferral.RequestContext{RequestID: "seed-alpha"}); err != nil {
		t.Fatalf("add alpha contact: %v", err)
	}
	if _, err := service.AddContact(ctx, bravo.customerID, accountreferral.ContactInput{
		Kind: "email", Value: "bravo@example.com", Transactional: true,
	}, accountreferral.RequestContext{RequestID: "seed-bravo"}); err != nil {
		t.Fatalf("add bravo contact: %v", err)
	}

	document, err := service.Export(ctx, alpha.customerID, accountreferral.RequestContext{RequestID: "export-1"})
	if err != nil {
		t.Fatalf("export: %v", err)
	}
	encoded, err := json.Marshal(document)
	if err != nil {
		t.Fatalf("encode export: %v", err)
	}
	rendered := string(encoded)

	// The customer's own records are present.
	for _, expected := range []string{
		alpha.customerID, alpha.orderID, alpha.ticketBody, "alpha@example.com",
	} {
		if !strings.Contains(rendered, expected) {
			t.Fatalf("the export is missing the customer's own record %q", expected)
		}
	}
	// Nothing belonging to anybody else, and no credential belonging to anybody.
	for _, forbidden := range []string{
		bravo.customerID, bravo.orderID, bravo.ticketBody, "bravo@example.com",
		alpha.reference, alpha.checkout, bravo.reference, bravo.checkout,
		"operator note about alpha", "operator note about bravo",
	} {
		if strings.Contains(rendered, forbidden) {
			t.Fatalf("the export leaked %q", forbidden)
		}
	}

	if document.Profile.ID != alpha.customerID {
		t.Fatalf("the export names %q, want %q", document.Profile.ID, alpha.customerID)
	}
	if len(document.Orders) != 1 || len(document.Payments) != 1 || len(document.Wallet) != 1 {
		t.Fatalf("expected exactly the customer's own commerce records, got %d orders, %d payments, %d entries",
			len(document.Orders), len(document.Payments), len(document.Wallet))
	}
	if len(document.Support) != 1 || len(document.Support[0].Messages) != 1 {
		t.Fatalf("expected exactly one conversation with one message, got %+v", document.Support)
	}
	if len(document.Redactions) == 0 {
		t.Fatal("the export must declare what it withholds")
	}

	// The disclosure leaves a trail an operator can be held to, and the trail is
	// not a second copy of what was disclosed.
	var audits int
	if err := harness.pool.QueryRow(ctx, `SELECT count(*) FROM audit_events
		WHERE actor_type = 'customer' AND action = 'account.data.exported' AND target_id = $1`,
		alpha.customerID).Scan(&audits); err != nil {
		t.Fatalf("count audit events: %v", err)
	}
	if audits != 1 {
		t.Fatalf("expected one recorded export, got %d", audits)
	}
	var metadata string
	if err := harness.pool.QueryRow(ctx, `SELECT metadata::text FROM audit_events
		WHERE action = 'account.data.exported' AND target_id = $1`, alpha.customerID).
		Scan(&metadata); err != nil {
		t.Fatalf("read audit metadata: %v", err)
	}
	for _, forbidden := range []string{alpha.ticketBody, "alpha@example.com", alpha.reference} {
		if strings.Contains(metadata, forbidden) {
			t.Fatalf("the audit record duplicated the export payload: %s", metadata)
		}
	}
}

// TestExportNamesNoInviter covers the other direction of the same rule: an
// invitee's export says they arrived through an invite without saying whose,
// because a referral code is a stable pseudonym for the person who owns it.
func TestExportNamesNoInviter(t *testing.T) {
	harness := newHarness(t)
	ctx := context.Background()
	service := referralService(t, harness)

	inviter := harness.seedReferralAccount(ctx, t, "alpha")
	invitee := harness.seedReferralAccount(ctx, t, "bravo")
	if _, err := harness.pool.Exec(ctx, `INSERT INTO referral_attributions
		(referred_user_id, referrer_user_id, code) VALUES ($1::uuid, $2::uuid, $3)`,
		invitee.customerID, inviter.customerID, referralCodeFor("alpha")); err != nil {
		t.Fatalf("seed attribution: %v", err)
	}

	document, err := service.Export(ctx, invitee.customerID, accountreferral.RequestContext{})
	if err != nil {
		t.Fatalf("export: %v", err)
	}
	if document.Referral.InvitedBy == nil {
		t.Fatal("the export must record that the customer arrived through an invite")
	}
	encoded, err := json.Marshal(document.Referral)
	if err != nil {
		t.Fatalf("encode referral section: %v", err)
	}
	for _, forbidden := range []string{inviter.customerID, referralCodeFor("alpha")} {
		if strings.Contains(string(encoded), forbidden) {
			t.Fatalf("the export identifies the inviter: %s", encoded)
		}
	}
}

// TestDeletionIsRecordedAndNeverExecuted is the irreversibility gate. A customer
// panel may ask for an account to be deleted; nothing it does may delete one.
func TestDeletionIsRecordedAndNeverExecuted(t *testing.T) {
	harness := newHarness(t)
	ctx := context.Background()
	service := referralService(t, harness)
	customerID := harness.customer(ctx, t)

	deletion, err := service.RequestDeletion(ctx, customerID, "I no longer need the service",
		accountreferral.RequestContext{RequestID: "delete-1"})
	if err != nil {
		t.Fatalf("request deletion: %v", err)
	}
	if !deletion.Pending || deletion.RequestedAt == nil {
		t.Fatalf("the request was not recorded as pending: %+v", deletion)
	}
	if deletion.ExecutedBy == "" {
		t.Fatal("the response must name what would carry the deletion out")
	}

	// The lifecycle event exists, with the customer as its actor.
	var events int
	if err := harness.pool.QueryRow(ctx, `SELECT count(*) FROM customer_lifecycle_events
		WHERE user_id = $1::uuid AND action = 'deletion_requested' AND actor_type = 'customer'`,
		customerID).Scan(&events); err != nil {
		t.Fatalf("count lifecycle events: %v", err)
	}
	if events != 1 {
		t.Fatalf("expected one recorded request, got %d", events)
	}

	// And absolutely nothing was deleted.
	var status string
	var deleted, retention, anonymized *string
	if err := harness.pool.QueryRow(ctx,
		`SELECT status, deleted_at::text, retention_until::text, anonymized_at::text
		 FROM users WHERE id = $1::uuid`, customerID).
		Scan(&status, &deleted, &retention, &anonymized); err != nil {
		t.Fatalf("read customer: %v", err)
	}
	if status != "active" || deleted != nil || retention != nil || anonymized != nil {
		t.Fatalf("a request executed the deletion: status=%q deleted=%v retention=%v anonymized=%v",
			status, deleted, retention, anonymized)
	}

	// Repeating the request is refused rather than piling up records an operator
	// has to reconcile.
	if _, err := service.RequestDeletion(ctx, customerID, "again",
		accountreferral.RequestContext{}); !errors.Is(err, accountreferral.ErrDeletionPending) {
		t.Fatalf("expected a pending-request refusal, got %v", err)
	}

	privacy, err := service.Privacy(ctx, customerID)
	if err != nil {
		t.Fatalf("read privacy: %v", err)
	}
	if !privacy.Deletion.Pending {
		t.Fatal("the privacy screen does not show the pending request")
	}
	if privacy.Retention.Status != "active" {
		t.Fatalf("retention status = %q, want the account untouched", privacy.Retention.Status)
	}
}

// TestCancellingADeletionClearsThePendingStateWithoutErasingTheRequest proves
// the withdrawal is additive: the request stays on the record, and only the
// state derived from it changes.
func TestCancellingADeletionClearsThePendingStateWithoutErasingTheRequest(t *testing.T) {
	harness := newHarness(t)
	ctx := context.Background()
	service := referralService(t, harness)
	customerID := harness.customer(ctx, t)

	if _, err := service.RequestDeletion(ctx, customerID, "changed my mind shortly",
		accountreferral.RequestContext{RequestID: "delete-1"}); err != nil {
		t.Fatalf("request deletion: %v", err)
	}
	cancelled, err := service.CancelDeletion(ctx, customerID, accountreferral.RequestContext{RequestID: "cancel-1"})
	if err != nil {
		t.Fatalf("cancel deletion: %v", err)
	}
	if cancelled.Pending {
		t.Fatalf("the request is still pending after cancellation: %+v", cancelled)
	}
	if cancelled.CancelledAt == nil {
		t.Fatal("the cancellation must be dated")
	}

	// The lifecycle record is append-only: the request was not removed.
	var events int
	if err := harness.pool.QueryRow(ctx, `SELECT count(*) FROM customer_lifecycle_events
		WHERE user_id = $1::uuid AND action = 'deletion_requested'`, customerID).Scan(&events); err != nil {
		t.Fatalf("count lifecycle events: %v", err)
	}
	if events != 1 {
		t.Fatalf("the withdrawal changed the append-only record: %d events", events)
	}

	privacy, err := service.Privacy(ctx, customerID)
	if err != nil {
		t.Fatalf("read privacy: %v", err)
	}
	if privacy.Deletion.Pending {
		t.Fatal("the privacy screen still reports a pending request")
	}

	// Cancelling twice is refused, and asking again after a withdrawal reopens.
	if _, err := service.CancelDeletion(ctx, customerID,
		accountreferral.RequestContext{}); !errors.Is(err, accountreferral.ErrNoDeletionPending) {
		t.Fatalf("expected nothing to cancel, got %v", err)
	}
	reopened, err := service.RequestDeletion(ctx, customerID, "decided after all",
		accountreferral.RequestContext{})
	if err != nil {
		t.Fatalf("re-request deletion: %v", err)
	}
	if !reopened.Pending {
		t.Fatal("a request made after a withdrawal must be pending")
	}
}

// TestReferralCountsMatchTheAttributionRows checks the summary against the rows
// it claims to describe, because the counts are the only part of the referral
// screen a customer can argue with.
func TestReferralCountsMatchTheAttributionRows(t *testing.T) {
	harness := newHarness(t)
	ctx := context.Background()
	service := referralService(t, harness)

	inviter := harness.customer(ctx, t)
	summary, err := service.Referrals(ctx, inviter, "", 0)
	if err != nil {
		t.Fatalf("read referrals: %v", err)
	}
	// The code is minted on first read, which is what makes the link shareable
	// without a separate "join the programme" step.
	if len(summary.Code) != 10 {
		t.Fatalf("code = %q, want a ten-character code", summary.Code)
	}
	if summary.Link == "" {
		t.Fatalf("a configured public URL must produce a link, reason %q", summary.LinkReason)
	}
	if !strings.Contains(summary.Link, summary.Code) {
		t.Fatalf("the link does not carry the code: %s", summary.Link)
	}

	// Reading twice must not mint a second code.
	again, err := service.Referrals(ctx, inviter, "", 0)
	if err != nil {
		t.Fatalf("re-read referrals: %v", err)
	}
	if again.Code != summary.Code {
		t.Fatalf("a second read changed the code from %q to %q", summary.Code, again.Code)
	}

	// Three invitees: one qualified, one still pending, one rejected on review.
	for index, state := range []string{"qualified", "pending", "rejected"} {
		invitee := harness.customer(ctx, t)
		if _, err := harness.pool.Exec(ctx, `INSERT INTO referral_attributions
			(referred_user_id, referrer_user_id, code) VALUES ($1::uuid, $2::uuid, $3)`,
			invitee, inviter, summary.Code); err != nil {
			t.Fatalf("seed attribution %d: %v", index, err)
		}
		switch state {
		case "qualified":
			if _, err := harness.pool.Exec(ctx,
				`UPDATE referral_attributions SET qualified_at = now() WHERE referred_user_id = $1::uuid`,
				invitee); err != nil {
				t.Fatalf("qualify attribution: %v", err)
			}
		case "rejected":
			if _, err := harness.pool.Exec(ctx,
				`UPDATE referral_attributions SET review_state = 'rejected' WHERE referred_user_id = $1::uuid`,
				invitee); err != nil {
				t.Fatalf("reject attribution: %v", err)
			}
		}
	}

	counted, err := service.Referrals(ctx, inviter, "", 0)
	if err != nil {
		t.Fatalf("read referrals after seeding: %v", err)
	}
	if counted.Invited != 3 {
		t.Fatalf("invited = %d, want 3", counted.Invited)
	}
	if counted.Qualified != 1 {
		t.Fatalf("qualified = %d, want 1", counted.Qualified)
	}
	if counted.Rejected != 1 {
		t.Fatalf("rejected = %d, want 1", counted.Rejected)
	}
	if counted.Pending != 1 {
		t.Fatalf("pending = %d, want 1", counted.Pending)
	}

	var attributed int64
	if err := harness.pool.QueryRow(ctx,
		`SELECT count(*) FROM referral_attributions WHERE referrer_user_id = $1::uuid`, inviter).
		Scan(&attributed); err != nil {
		t.Fatalf("count attributions: %v", err)
	}
	if attributed != counted.Invited {
		t.Fatalf("the summary claims %d invites against %d rows", counted.Invited, attributed)
	}
}

// TestReferralRewardsAreScopedToTheirBeneficiary is the ownership gate for the
// referral surface: one customer's earnings must be invisible to another, and a
// reversal must not still be counted as money kept.
func TestReferralRewardsAreScopedToTheirBeneficiary(t *testing.T) {
	harness := newHarness(t)
	ctx := context.Background()
	service := referralService(t, harness)

	earner := harness.seedReferralAccount(ctx, t, "alpha")
	stranger := harness.seedReferralAccount(ctx, t, "bravo")
	invitee := harness.customer(ctx, t)

	if _, err := harness.pool.Exec(ctx, `INSERT INTO referral_attributions
		(referred_user_id, referrer_user_id, code, qualified_at)
		VALUES ($1::uuid, $2::uuid, $3, now())`,
		invitee, earner.customerID, referralCodeFor("alpha")); err != nil {
		t.Fatalf("seed attribution: %v", err)
	}

	var transactionID string
	if err := harness.pool.QueryRow(ctx, `INSERT INTO ledger_transactions
		(type, reference_type, reference_id, idempotency_key)
		VALUES ('referral_reward', 'referral', $1, $2) RETURNING id::text`,
		invitee, "reward-alpha").Scan(&transactionID); err != nil {
		t.Fatalf("seed reward transaction: %v", err)
	}
	if _, err := harness.pool.Exec(ctx, `INSERT INTO referral_rewards
		(referred_user_id, beneficiary_user_id, role, order_id, amount_minor, currency, ledger_transaction_id)
		VALUES ($1::uuid, $2::uuid, 'inviter', $3::uuid, 20000, 'RUB', $4::uuid)`,
		invitee, earner.customerID, earner.orderID, transactionID); err != nil {
		t.Fatalf("seed reward: %v", err)
	}

	earned, err := service.Referrals(ctx, earner.customerID, "", 0)
	if err != nil {
		t.Fatalf("read the earner's referrals: %v", err)
	}
	if earned.RewardedMinor != 20000 || len(earned.Rewards.Items) != 1 {
		t.Fatalf("the earner cannot see their own reward: %+v", earned)
	}
	if earned.Rewards.Items[0].State != accountreferral.RewardQualified {
		t.Fatalf("state = %q, want a granted reward to read as qualified", earned.Rewards.Items[0].State)
	}

	seen, err := service.Referrals(ctx, stranger.customerID, "", 0)
	if err != nil {
		t.Fatalf("read the stranger's referrals: %v", err)
	}
	if seen.RewardedMinor != 0 || len(seen.Rewards.Items) != 0 {
		t.Fatalf("a stranger can see somebody else's rewards: %+v", seen)
	}
	page, err := service.Rewards(ctx, stranger.customerID, "", 0)
	if err != nil {
		t.Fatalf("read the stranger's reward history: %v", err)
	}
	if len(page.Items) != 0 {
		t.Fatalf("the reward history is not scoped to its owner: %+v", page.Items)
	}

	// A reversal stops being money the customer kept, and says so.
	if _, err := harness.pool.Exec(ctx, `UPDATE referral_rewards
		SET reversed_at = now(), reversal_ledger_transaction_id = $1::uuid, reversal_reason = 'abuse review'
		WHERE beneficiary_user_id = $2::uuid`, transactionID, earner.customerID); err != nil {
		t.Fatalf("reverse reward: %v", err)
	}
	reversed, err := service.Referrals(ctx, earner.customerID, "", 0)
	if err != nil {
		t.Fatalf("re-read the earner's referrals: %v", err)
	}
	if reversed.RewardedMinor != 0 {
		t.Fatalf("a reversed reward is still counted as earned: %d", reversed.RewardedMinor)
	}
	if reversed.ReversedMinor != 20000 {
		t.Fatalf("reversedMinor = %d, want the withdrawn amount to be visible", reversed.ReversedMinor)
	}
	if len(reversed.Rewards.Items) != 1 ||
		reversed.Rewards.Items[0].State != accountreferral.RewardRejected {
		t.Fatalf("the history does not report the reversal: %+v", reversed.Rewards.Items)
	}
}

// TestContactChannelsAreOwnedAndConflictsHandOffToSupport covers the identity
// half of the surface. The conflict answer is deliberately the same whoever owns
// the address, so the route cannot be used to ask whether somebody has an
// account here.
func TestContactChannelsAreOwnedAndConflictsHandOffToSupport(t *testing.T) {
	harness := newHarness(t)
	ctx := context.Background()
	service := referralService(t, harness)

	owner := harness.customer(ctx, t)
	stranger := harness.customer(ctx, t)

	contact, err := service.AddContact(ctx, owner, accountreferral.ContactInput{
		Kind: "email", Value: "Shared@Example.com", Transactional: true, Marketing: true,
	}, accountreferral.RequestContext{RequestID: "add-1"})
	if err != nil {
		t.Fatalf("add contact: %v", err)
	}
	if contact.Value != "shared@example.com" {
		t.Fatalf("the stored value was not normalized: %q", contact.Value)
	}

	// Opting in to marketing is a dated decision, not only a flag.
	privacy, err := service.Privacy(ctx, owner)
	if err != nil {
		t.Fatalf("read privacy: %v", err)
	}
	if granted, recorded := privacy.Consents.Current["marketing"]; !recorded || !granted {
		t.Fatalf("marketing consent was not recorded: %+v", privacy.Consents)
	}

	// The same address from a different account is refused with no hint that it
	// exists elsewhere.
	if _, err := service.AddContact(ctx, stranger, accountreferral.ContactInput{
		Kind: "email", Value: "shared@example.com", Transactional: true,
	}, accountreferral.RequestContext{}); !errors.Is(err, accountreferral.ErrContactConflict) {
		t.Fatalf("expected a conflict handoff, got %v", err)
	}
	strangerContacts, err := service.Contacts(ctx, stranger)
	if err != nil {
		t.Fatalf("list the stranger's contacts: %v", err)
	}
	if len(strangerContacts) != 0 {
		t.Fatalf("a refused add still created a channel: %+v", strangerContacts)
	}

	// Somebody else's channel is not found rather than forbidden, so an
	// identifier cannot be probed for existence.
	if err := service.RemoveContact(ctx, stranger, contact.ID,
		accountreferral.RequestContext{}); !errors.Is(err, accountreferral.ErrNotFound) {
		t.Fatalf("expected a not-found answer, got %v", err)
	}
	owned, err := service.Contacts(ctx, owner)
	if err != nil {
		t.Fatalf("list the owner's contacts: %v", err)
	}
	if len(owned) != 1 {
		t.Fatalf("the owner's channel was affected by somebody else's request: %+v", owned)
	}

	if err := service.RemoveContact(ctx, owner, contact.ID,
		accountreferral.RequestContext{RequestID: "remove-1"}); err != nil {
		t.Fatalf("remove contact: %v", err)
	}
	// Withdrawing the last marketing channel withdraws the consent with it.
	withdrawn, err := service.Privacy(ctx, owner)
	if err != nil {
		t.Fatalf("re-read privacy: %v", err)
	}
	if granted := withdrawn.Consents.Current["marketing"]; granted {
		t.Fatalf("marketing consent survived the last channel: %+v", withdrawn.Consents)
	}
	// The row is revoked rather than deleted, so the address cannot be silently
	// moved to another account.
	var rows int
	if err := harness.pool.QueryRow(ctx,
		`SELECT count(*) FROM contact_channels WHERE user_id = $1::uuid AND revoked_at IS NOT NULL`,
		owner).Scan(&rows); err != nil {
		t.Fatalf("count revoked channels: %v", err)
	}
	if rows != 1 {
		t.Fatalf("expected the channel to be revoked and kept, got %d revoked rows", rows)
	}
}

// TestLoyaltyReportsNotEnabledUntilAProgrammeIsPublished, and reads a published
// one without ever promoting anybody: placement belongs to the evaluation
// worker, and a page load must not move a customer between tiers.
func TestLoyaltyReportsWhatTheWorkerDecidedAndNothingMore(t *testing.T) {
	harness := newHarness(t)
	ctx := context.Background()
	service := referralService(t, harness)
	customerID := harness.customer(ctx, t)

	standing, err := service.Loyalty(ctx, customerID)
	if err != nil {
		t.Fatalf("read loyalty: %v", err)
	}
	if standing.Enabled {
		t.Fatal("loyalty must report itself off until a programme is published")
	}

	var programID string
	if err := harness.pool.QueryRow(ctx, `INSERT INTO loyalty_programs
		(version, enabled, metric, currency, window_days, grace_days, published_at)
		VALUES (1, true, 'spend', 'RUB', 365, 30, now()) RETURNING id::text`).
		Scan(&programID); err != nil {
		t.Fatalf("publish programme: %v", err)
	}
	tiers := map[string]int64{"bronze": 0, "silver": 10000, "gold": 50000}
	tierIDs := make(map[string]string, len(tiers))
	for code, threshold := range tiers {
		var tierID string
		if err := harness.pool.QueryRow(ctx, `INSERT INTO loyalty_tiers
			(program_id, code, name_en, name_ru, threshold, discount_bps)
			VALUES ($1::uuid, $2, $2, $2, $3, 0) RETURNING id::text`,
			programID, code, threshold).Scan(&tierID); err != nil {
			t.Fatalf("create tier %s: %v", code, err)
		}
		tierIDs[code] = tierID
	}

	// No standing yet: the ladder renders, nobody is placed.
	unplaced, err := service.Loyalty(ctx, customerID)
	if err != nil {
		t.Fatalf("read loyalty: %v", err)
	}
	if !unplaced.Enabled || unplaced.Evaluated {
		t.Fatalf("an unevaluated customer must not be placed: %+v", unplaced)
	}
	if len(unplaced.Tiers) != 3 {
		t.Fatalf("the ladder is not rendered: %+v", unplaced.Tiers)
	}

	// The worker's decision, held through grace at a metric that no longer earns
	// the tier. Reading it must not demote the customer.
	if _, err := harness.pool.Exec(ctx, `INSERT INTO loyalty_standings
		(user_id, program_id, tier_id, evaluated_metric, grace_until)
		VALUES ($1::uuid, $2::uuid, $3::uuid, 2000, now() + interval '10 days')`,
		customerID, programID, tierIDs["silver"]); err != nil {
		t.Fatalf("seed standing: %v", err)
	}
	placed, err := service.Loyalty(ctx, customerID)
	if err != nil {
		t.Fatalf("read loyalty: %v", err)
	}
	if !placed.Evaluated || placed.Tier.Code != "silver" {
		t.Fatalf("the stored standing was not reported: %+v", placed)
	}
	if placed.Next == nil || placed.Next.Code != "gold" {
		t.Fatalf("the next rung was not reported: %+v", placed.Next)
	}
	if placed.GraceUntil == nil {
		t.Fatal("a tier held on grace must say so")
	}
	if placed.Metric != 2000 {
		t.Fatalf("metric = %d, want the number the decision was made on", placed.Metric)
	}

	var standings int
	if err := harness.pool.QueryRow(ctx,
		`SELECT count(*) FROM loyalty_standing_history WHERE user_id = $1::uuid`, customerID).
		Scan(&standings); err != nil {
		t.Fatalf("count history: %v", err)
	}
	if standings != 0 {
		t.Fatalf("reading the screen wrote %d history rows", standings)
	}
}
