package commercepg

import (
	"context"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/omniflow/omniflow/internal/database/dbgen"
)

// WalletBalance is the customer's credit in one currency.
//
// The three figures are carried separately because they answer different
// questions and a single "balance" would answer neither honestly. Total is what
// the ledger holds; Reserved is what unpaid orders have already claimed; and
// Available is what a new order may actually spend. A customer who sees only
// the total and then watches a smaller amount apply at checkout has been told
// the wrong number twice.
type WalletBalance struct {
	Currency       string
	TotalMinor     int64
	ReservedMinor  int64
	AvailableMinor int64
}

// WalletBalances reads every currency the customer holds credit in. It is the
// single implementation of the available-balance rule: the reservation is
// subtracted with exactly the arithmetic order creation uses, so two checkouts
// opened side by side can never each believe they may spend the same credit.
func (store *Store) WalletBalances(ctx context.Context, customerID string) ([]WalletBalance, error) {
	userID, err := parseUUID(customerID)
	if err != nil {
		return nil, err
	}
	rows, err := dbgen.New(store.pool).GetWalletBalances(ctx, dbgen.GetWalletBalancesParams{UserID: userID})
	if err != nil {
		return nil, err
	}
	balances := make([]WalletBalance, 0, len(rows))
	for _, row := range rows {
		balances = append(balances, WalletBalance{
			Currency: row.Currency, TotalMinor: row.TotalMinor,
			ReservedMinor: row.ReservedMinor, AvailableMinor: row.AvailableMinor,
		})
	}
	return balances, nil
}

// AvailableWalletBalance is what a new order in this currency may spend from
// the wallet.
func (store *Store) AvailableWalletBalance(ctx context.Context, customerID, currency string) (int64, error) {
	userID, err := parseUUID(customerID)
	if err != nil {
		return 0, err
	}
	return availableWallet(ctx, dbgen.New(store.pool), userID, currency)
}

// availableWallet reads the available balance inside the caller's
// transaction, which is how order creation and settlement see it.
func availableWallet(ctx context.Context, queries *dbgen.Queries, userID pgtype.UUID, currency string) (int64, error) {
	rows, err := queries.GetWalletBalances(ctx, dbgen.GetWalletBalancesParams{UserID: userID, Currency: pgtype.Text{String: currency, Valid: true}})
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
