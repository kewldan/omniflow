package commercepg

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/omniflow/omniflow/internal/commerce"
	"github.com/omniflow/omniflow/internal/database/dbgen"
)

// grantReferralCommission pays the inviter their share of a referee's payment,
// once per order, in the transaction that settles it. It is a referral reward
// of role `commission`, credited to the wallet through a balanced ledger
// transaction, so it is reviewed, reversed, withdrawn, and announced like any
// other reward.
func (store *Store) grantReferralCommission(ctx context.Context, tx pgx.Tx, queries *dbgen.Queries, order dbgen.Order) error {
	trigger := ""
	switch {
	case commerce.SubscriptionOperation(order.Operation):
		trigger = "subscription"
	case order.Operation == "topup":
		trigger = "topup"
	default:
		return nil
	}
	var (
		enabled  bool
		currency string
		terms    commerce.CommissionTerms
		first    pgtype.Int4
		cap      pgtype.Int4
		tiers    []byte
	)
	err := tx.QueryRow(ctx, `SELECT enabled, currency, commission_bps, first_payment_commission_bps,
		commission_payment_cap, commission_min_payment_minor, commission_triggers, commission_tiers
		FROM referral_programs WHERE singleton`).Scan(&enabled, &currency, &terms.BPS, &first, &cap,
		&terms.MinimumPaymentMinor, &terms.Triggers, &tiers)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	if first.Valid {
		value := int(first.Int32)
		terms.FirstPaymentBPS = &value
	}
	if cap.Valid {
		value := int(cap.Int32)
		terms.PaymentCap = &value
	}
	_ = json.Unmarshal(tiers, &terms.Tiers)
	// Commission is paid in the programme's currency, from payments in it.
	if !enabled || !terms.Configured() || order.Currency != currency {
		return nil
	}
	var (
		inviter     pgtype.UUID
		reviewState string
	)
	err = tx.QueryRow(ctx, `SELECT referrer_user_id, review_state FROM referral_attributions
		WHERE referred_user_id = $1 FOR UPDATE`, order.UserID).Scan(&inviter, &reviewState)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	if reviewState == "rejected" {
		return nil
	}
	var (
		already, earlier, qualified int
		personal                    commerce.InviterTerms
		personalBPS, personalFirst  pgtype.Int4
	)
	if err = tx.QueryRow(ctx, `SELECT
		(SELECT count(*) FROM referral_rewards WHERE role = 'commission' AND order_id = $2),
		(SELECT count(*) FROM referral_rewards WHERE role = 'commission' AND referred_user_id = $1 AND order_id <> $2),
		(SELECT count(*) FROM referral_attributions WHERE referrer_user_id = $3
			AND qualified_at IS NOT NULL AND review_state <> 'rejected')`,
		order.UserID, order.ID, inviter).Scan(&already, &earlier, &qualified); err != nil {
		return err
	}
	if already > 0 {
		return nil
	}
	err = tx.QueryRow(ctx, `SELECT commission_bps, first_payment_commission_bps FROM referral_inviter_terms
		WHERE user_id = $1`, inviter).Scan(&personalBPS, &personalFirst)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return err
	}
	if personalBPS.Valid {
		value := int(personalBPS.Int32)
		personal.BPS = &value
	}
	if personalFirst.Valid {
		value := int(personalFirst.Int32)
		personal.FirstPaymentBPS = &value
	}
	amount, bps := commerce.Commission(terms, personal, commerce.CommissionPayment{
		Trigger: trigger, PaidMinor: max(order.PaidMinor-order.WalletMinor, 0),
		Index: earlier + 1, QualifiedReferrals: qualified,
	})
	if amount <= 0 {
		return nil
	}
	key := "referral-commission:" + uuidString(order.ID)
	transaction, err := queries.CreateLedgerTransaction(ctx, dbgen.CreateLedgerTransactionParams{
		Type: "referral_reward", ReferenceType: "referral", ReferenceID: uuidString(order.ID), IdempotencyKey: key,
		Reason: pgtype.Text{String: "referral commission on order " + uuidString(order.ID), Valid: true},
	})
	if err != nil {
		return err
	}
	if _, err = queries.InsertLedgerEntry(ctx, dbgen.InsertLedgerEntryParams{TransactionID: transaction.ID, AccountType: "customer_wallet", UserID: inviter, Currency: currency, AmountMinor: amount}); err != nil {
		return err
	}
	if _, err = queries.InsertLedgerEntry(ctx, dbgen.InsertLedgerEntryParams{TransactionID: transaction.ID, AccountType: "platform_clearing", Currency: currency, AmountMinor: -amount}); err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, `INSERT INTO referral_rewards (
			referred_user_id, beneficiary_user_id, role, order_id, amount_minor, currency, ledger_transaction_id)
		VALUES ($1, $2, 'commission', $3, $4, $5, $6)`,
		order.UserID, inviter, order.ID, amount, currency, transaction.ID); err != nil {
		return err
	}
	metadata, _ := json.Marshal(map[string]any{"role": "commission", "amountMinor": amount, "currency": currency, "bps": bps, "orderId": uuidString(order.ID)})
	_, err = queries.InsertAuditEvent(ctx, dbgen.InsertAuditEventParams{
		ActorType: "system", Action: "referral.commission_granted", TargetType: "customer",
		Category: "financial", Outcome: "success", TargetID: uuidString(inviter),
		Reason: pgtype.Text{String: "referral commission", Valid: true}, Metadata: metadata,
	})
	return err
}
