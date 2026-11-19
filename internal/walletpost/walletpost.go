// Package walletpost posts a balanced two-entry movement between a customer's
// wallet and the platform clearing account, inside the caller's transaction.
//
// It is what withdrawals, engagement prizes, and spin charges share, so each
// of them writes the ledger exactly as an operator adjustment does: one
// ledger_transactions row keyed by an idempotency key, and two entries that
// sum to zero. A replay with the same key changes nothing and returns the
// original transaction.
package walletpost

import (
	"context"
	"errors"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/omniflow/omniflow/internal/database/dbgen"
)

// ErrInsufficient reports a debit larger than the available balance.
var ErrInsufficient = errors.New("the wallet does not hold that much")

// Posting is one movement. A positive amount credits the customer, a negative
// one debits them.
type Posting struct {
	Type           string
	ReferenceType  string
	ReferenceID    string
	IdempotencyKey string
	Reason         string
	ActorID        string
	CustomerID     string
	Currency       string
	AmountMinor    int64
	// CheckBalance refuses a debit the available balance cannot cover.
	CheckBalance bool
}

// Post writes the movement and returns the ledger transaction id.
func Post(ctx context.Context, tx pgx.Tx, posting Posting) (string, error) {
	if posting.AmountMinor == 0 || posting.CustomerID == "" || posting.IdempotencyKey == "" {
		return "", errors.New("a wallet posting needs a customer, an amount, and an idempotency key")
	}
	posting.Currency = strings.ToUpper(posting.Currency)
	var existing string
	err := tx.QueryRow(ctx, `SELECT id::text FROM ledger_transactions WHERE idempotency_key = $1`,
		posting.IdempotencyKey).Scan(&existing)
	if err == nil {
		return existing, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return "", err
	}
	if posting.AmountMinor < 0 && posting.CheckBalance {
		available, err := Available(ctx, tx, posting.CustomerID, posting.Currency)
		if err != nil {
			return "", err
		}
		if available+posting.AmountMinor < 0 {
			return "", ErrInsufficient
		}
	}
	var transactionID string
	if err = tx.QueryRow(ctx, `INSERT INTO ledger_transactions
			(type, reference_type, reference_id, idempotency_key, reason, actor_id)
		VALUES ($1, $2, $3, $4, NULLIF($5, ''), NULLIF($6, '')) RETURNING id::text`,
		posting.Type, posting.ReferenceType, posting.ReferenceID, posting.IdempotencyKey,
		posting.Reason, posting.ActorID).Scan(&transactionID); err != nil {
		return "", err
	}
	if _, err = tx.Exec(ctx, `INSERT INTO ledger_entries (transaction_id, account_type, user_id, currency, amount_minor)
		VALUES ($1::uuid, 'customer_wallet', $2::uuid, $3, $4), ($1::uuid, 'platform_clearing', NULL, $3, -$4::bigint)`,
		transactionID, posting.CustomerID, posting.Currency, posting.AmountMinor); err != nil {
		return "", err
	}
	return transactionID, nil
}

// Available is what a new order may spend from the wallet, read inside the
// caller's transaction through the one shared definition of "available".
func Available(ctx context.Context, tx pgx.Tx, customerID, currency string) (int64, error) {
	var userID = dbgen.GetWalletBalancesParams{}
	if err := userID.UserID.Scan(customerID); err != nil {
		return 0, err
	}
	userID.Currency.String, userID.Currency.Valid = currency, true
	rows, err := dbgen.New(tx).GetWalletBalances(ctx, userID)
	if err != nil {
		return 0, err
	}
	for _, row := range rows {
		if row.Currency == currency {
			return row.AvailableMinor, nil
		}
	}
	return 0, nil
}
