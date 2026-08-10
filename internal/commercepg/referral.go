package commercepg

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/omniflow/omniflow/internal/commerce"
	"github.com/omniflow/omniflow/internal/database/dbgen"
)

// rowQuerier lets the referral program be read either from the pool or from an
// open transaction, so a settlement never has to acquire a second connection
// while it is holding one.
type rowQuerier interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}

// ReferralProgram reads the operator-configured referral program. A missing row
// means the program has never been configured and is therefore disabled.
func (store *Store) ReferralProgram(ctx context.Context) (commerce.ReferralProgram, error) {
	return store.referralProgram(ctx, store.pool)
}

func (store *Store) referralProgram(ctx context.Context, querier rowQuerier) (commerce.ReferralProgram, error) {
	var (
		program            commerce.ReferralProgram
		cap                pgtype.Int4
		validityDays       int32
		rewardExpiryDays   pgtype.Int4
		qualificationValue string
	)
	err := querier.QueryRow(ctx, `SELECT enabled, currency, inviter_reward_minor, invitee_reward_minor,
		qualification, inviter_reward_cap, attribution_validity_days, reward_expiry_days
		FROM referral_programs WHERE singleton`).Scan(&program.Enabled, &program.Currency,
		&program.InviterRewardMinor, &program.InviteeRewardMinor, &qualificationValue, &cap,
		&validityDays, &rewardExpiryDays)
	if errors.Is(err, pgx.ErrNoRows) {
		return commerce.ReferralProgram{}, nil
	}
	if err != nil {
		return commerce.ReferralProgram{}, err
	}
	program.Qualification = qualificationValue
	program.AttributionValidity = time.Duration(validityDays) * 24 * time.Hour
	if cap.Valid {
		value := int(cap.Int32)
		program.InviterRewardCap = &value
	}
	if rewardExpiryDays.Valid {
		expiry := time.Duration(rewardExpiryDays.Int32) * 24 * time.Hour
		program.RewardExpiry = &expiry
	}
	return program, nil
}

// GrantReferralRewards qualifies the referral attached to a paid order and
// records the configured inviter and invitee wallet credits exactly once.
// It is safe to call repeatedly for the same order: qualification, the ledger
// transaction, and the reward record are all keyed by the attribution.
func (store *Store) GrantReferralRewards(ctx context.Context, orderID string) error {
	program, err := store.ReferralProgram(ctx)
	if err != nil || !program.Enabled {
		return err
	}
	id, err := parseUUID(orderID)
	if err != nil {
		return err
	}
	tx, err := store.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.Serializable})
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	queries := dbgen.New(tx)
	order, err := queries.LockOrder(ctx, id)
	if err != nil {
		return err
	}
	if err = store.grantReferralRewards(ctx, tx, queries, order, program); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// grantReferralRewards records qualification and rewards inside an existing
// transaction, which is what makes the grant exactly-once with the settlement
// that qualified it.
func (store *Store) grantReferralRewards(ctx context.Context, tx pgx.Tx, queries *dbgen.Queries, order dbgen.Order, program commerce.ReferralProgram) error {
	if !program.Enabled {
		return nil
	}
	var (
		attributedAt time.Time
		inviterID    pgtype.UUID
		qualifiedAt  pgtype.Timestamptz
	)
	err := tx.QueryRow(ctx, `SELECT created_at, referrer_user_id, qualified_at
		FROM referral_attributions WHERE referred_user_id = $1 FOR UPDATE`, order.UserID).
		Scan(&attributedAt, &inviterID, &qualifiedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	// Qualification is the customer's first settled order. Any earlier settled
	// order means this one cannot qualify the referral.
	var earlierSettled int64
	if err = tx.QueryRow(ctx, `SELECT count(*) FROM orders
		WHERE user_id = $1 AND id <> $2 AND state IN ('paid','fulfilled','partially_refunded','refunded')
		  AND created_at <= $3`, order.UserID, order.ID, order.CreatedAt.Time).Scan(&earlierSettled); err != nil {
		return err
	}
	if earlierSettled > 0 {
		return nil
	}
	granted := map[string]bool{}
	rows, err := tx.Query(ctx, `SELECT role FROM referral_rewards WHERE referred_user_id = $1`, order.UserID)
	if err != nil {
		return err
	}
	for rows.Next() {
		var role string
		if err = rows.Scan(&role); err != nil {
			rows.Close()
			return err
		}
		granted[role] = true
	}
	rows.Close()
	if err = rows.Err(); err != nil {
		return err
	}
	var inviterRewardCount int
	if err = tx.QueryRow(ctx, `SELECT count(*) FROM referral_rewards
		WHERE beneficiary_user_id = $1 AND role = 'inviter'`, inviterID).Scan(&inviterRewardCount); err != nil {
		return err
	}
	rewards, err := commerce.QualifyReferral(store.clock().UTC(), program, commerce.ReferralAttribution{
		AttributedAt: attributedAt, OrderState: commerce.OrderState(order.State),
		OrderPaidMinor: order.PaidMinor, OrderCurrency: order.Currency,
		InviterRewardCount: inviterRewardCount, GrantedRoles: granted,
	})
	if err != nil {
		return err
	}
	for _, reward := range rewards {
		beneficiary := inviterID
		if reward.Role == "invitee" {
			beneficiary = order.UserID
		}
		if err = store.recordReferralReward(ctx, tx, queries, order, beneficiary, reward); err != nil {
			return err
		}
	}
	if !qualifiedAt.Valid {
		if _, err = tx.Exec(ctx, `UPDATE referral_attributions
			SET qualified_at = now(), qualifying_order_id = $2
			WHERE referred_user_id = $1 AND qualified_at IS NULL`, order.UserID, order.ID); err != nil {
			return err
		}
	}
	return nil
}

func (store *Store) recordReferralReward(ctx context.Context, tx pgx.Tx, queries *dbgen.Queries, order dbgen.Order, beneficiary pgtype.UUID, reward commerce.ReferralReward) error {
	reference := uuidString(order.UserID) + ":" + reward.Role
	key := "referral:" + reference
	entries := []commerce.LedgerEntry{
		{AccountType: "customer_wallet", CustomerID: uuidString(beneficiary), Currency: reward.Amount.Currency, AmountMinor: reward.Amount.Amount},
		{AccountType: "platform_clearing", Currency: reward.Amount.Currency, AmountMinor: -reward.Amount.Amount},
	}
	if err := commerce.ValidateLedger(entries); err != nil {
		return err
	}
	transaction, err := queries.CreateLedgerTransaction(ctx, dbgen.CreateLedgerTransactionParams{
		Type: "referral_reward", ReferenceType: "referral", ReferenceID: reference, IdempotencyKey: key,
		Reason: pgtype.Text{String: "referral qualified by order " + uuidString(order.ID), Valid: true},
	})
	if err != nil {
		return err
	}
	expiresAt := pgtype.Timestamptz{}
	if reward.ExpiresAt != nil {
		expiresAt = pgtype.Timestamptz{Time: *reward.ExpiresAt, Valid: true}
	}
	if _, err = queries.InsertLedgerEntry(ctx, dbgen.InsertLedgerEntryParams{TransactionID: transaction.ID, AccountType: "customer_wallet", UserID: beneficiary, Currency: reward.Amount.Currency, AmountMinor: reward.Amount.Amount, ExpiresAt: expiresAt}); err != nil {
		return err
	}
	if _, err = queries.InsertLedgerEntry(ctx, dbgen.InsertLedgerEntryParams{TransactionID: transaction.ID, AccountType: "platform_clearing", Currency: reward.Amount.Currency, AmountMinor: -reward.Amount.Amount}); err != nil {
		return err
	}
	inserted, err := tx.Exec(ctx, `INSERT INTO referral_rewards (
			referred_user_id, beneficiary_user_id, role, order_id, amount_minor, currency, ledger_transaction_id)
		VALUES ($1, $2, $3, $4, $5, $6, $7)
		ON CONFLICT (referred_user_id, role) DO NOTHING`,
		order.UserID, beneficiary, reward.Role, order.ID, reward.Amount.Amount, reward.Amount.Currency, transaction.ID)
	if err != nil {
		return err
	}
	if inserted.RowsAffected() == 0 {
		return nil
	}
	metadata, err := json.Marshal(map[string]any{"role": reward.Role, "amountMinor": reward.Amount.Amount, "currency": reward.Amount.Currency})
	if err != nil {
		return err
	}
	_, err = queries.InsertAuditEvent(ctx, dbgen.InsertAuditEventParams{
		ActorType: "system", Action: "referral.reward_granted", TargetType: "customer",
		TargetID: uuidString(beneficiary), Reason: pgtype.Text{String: "referral qualified", Valid: true},
		Metadata: metadata,
	})
	return err
}
