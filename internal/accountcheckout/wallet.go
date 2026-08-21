package accountcheckout

import (
	"context"
	"time"

	"github.com/omniflow/omniflow/internal/commerce"
	"github.com/omniflow/omniflow/internal/commercepg"
)

// WalletBalance is the customer's credit in one currency.
//
// The three figures are carried separately because they answer different
// questions and a single "balance" would answer neither honestly. Total is what
// the ledger holds; Reserved is what unpaid orders have already claimed; and
// Available is what a new order may actually spend. A customer who sees only the
// total and then watches a smaller amount apply at checkout has been told the
// wrong number twice.
type WalletBalance struct {
	Currency       string
	TotalMinor     int64
	ReservedMinor  int64
	AvailableMinor int64
}

// WalletEntry is one customer-visible ledger movement.
type WalletEntry struct {
	ID          string
	Type        string
	AmountMinor int64
	Currency    string
	OccurredAt  time.Time
	// Reason is the operator's own note on a correction. It is shown because a
	// balance that changed without explanation is worse than one whose
	// explanation was written for staff, and because the bot already shows it.
	Reason string
}

// WalletView is the wallet screen: what the customer holds and what they may do
// about it.
type WalletView struct {
	Balances []WalletBalance
	// Currency is the settlement currency a top-up defaults to.
	Currency string
	// TopUpEnabled, MinimumMinor, MaximumMinor, and Presets come from the
	// operator's configured limits, so a panel never offers an amount the store
	// would refuse.
	TopUpEnabled bool
	MinimumMinor int64
	MaximumMinor int64
	Presets      []int64
	// RemainingWindowMinor is what the customer may still credit inside the
	// rolling window, which is the limit most top-up refusals are about.
	RemainingWindowMinor int64
	Providers            []PaymentChoice
}

// TopUp is a started wallet funding attempt.
type TopUp struct {
	OrderID     string
	Currency    string
	AmountMinor int64
	State       string
	Payment     PaymentHandle
}

// WalletBalances reads every currency the customer holds credit in.
//
// The reservation is subtracted with the same arithmetic the order path uses:
// credit claimed by a draft or pending order is not available to a second one,
// or two checkouts opened side by side would each believe they could be paid for
// entirely from the wallet.
func (store *Store) WalletBalances(ctx context.Context, customerID string) ([]WalletBalance, error) {
	rows, err := store.pool.Query(ctx, `SELECT e.currency,
		COALESCE(sum(e.amount_minor), 0)::bigint AS total_minor,
		COALESCE((SELECT sum(o.wallet_minor) FROM orders o
			WHERE o.user_id = $1::uuid AND o.currency = e.currency
			  AND o.state IN ('draft','pending')), 0)::bigint AS reserved_minor
		FROM ledger_entries e
		WHERE e.account_type = 'customer_wallet' AND e.user_id = $1::uuid
		GROUP BY e.currency ORDER BY e.currency`, customerID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	balances := make([]WalletBalance, 0, 4)
	for rows.Next() {
		var balance WalletBalance
		if err = rows.Scan(&balance.Currency, &balance.TotalMinor, &balance.ReservedMinor); err != nil {
			return nil, err
		}
		balance.AvailableMinor = balance.TotalMinor - balance.ReservedMinor
		if balance.AvailableMinor < 0 {
			balance.AvailableMinor = 0
		}
		balances = append(balances, balance)
	}
	return balances, rows.Err()
}

// WalletHistory lists the customer's own ledger entries, newest first.
func (store *Store) WalletHistory(
	ctx context.Context, customerID, currency string, cursor Cursor, limit int,
) ([]WalletEntry, error) {
	limit = boundLimit(limit)
	rows, err := store.pool.Query(ctx, `SELECT e.id::text, t.type, e.amount_minor, e.currency,
		e.created_at, COALESCE(t.reason, '')
		FROM ledger_entries e JOIN ledger_transactions t ON t.id = e.transaction_id
		WHERE e.account_type = 'customer_wallet' AND e.user_id = $1::uuid
		  AND ($2 = '' OR e.currency = $2)
		  AND ($4::timestamptz IS NULL
		       OR e.created_at < $4::timestamptz
		       OR (e.created_at = $4::timestamptz AND e.id < NULLIF($5, '')::uuid))
		ORDER BY e.created_at DESC, e.id DESC LIMIT $3`,
		customerID, currency, limit, optionalTime(cursor.At), cursor.ID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	entries := make([]WalletEntry, 0, limit)
	for rows.Next() {
		var entry WalletEntry
		if err = rows.Scan(&entry.ID, &entry.Type, &entry.AmountMinor, &entry.Currency,
			&entry.OccurredAt, &entry.Reason); err != nil {
			return nil, err
		}
		entry.OccurredAt = entry.OccurredAt.UTC()
		entries = append(entries, entry)
	}
	return entries, rows.Err()
}

// Wallet reads the balances together with the top-up policy that governs them.
func (service *Service) Wallet(ctx context.Context, customerID string) (WalletView, error) {
	balances, err := service.store.WalletBalances(ctx, customerID)
	if err != nil {
		return WalletView{}, err
	}
	limits := service.orders.TopUpLimits()
	credited, err := service.orders.TopUpAllowance(ctx, customerID, service.settings.Currency)
	if err != nil {
		return WalletView{}, err
	}
	// A customer who has never held credit still needs a row to render: a wallet
	// with no line at all reads as "unavailable" rather than as "empty", and the
	// two lead somewhere different.
	if !holdsCurrency(balances, service.settings.Currency) {
		balances = append(balances, WalletBalance{Currency: service.settings.Currency})
	}
	view := WalletView{
		Balances: balances, Currency: service.settings.Currency,
		TopUpEnabled: limits.Enabled, MinimumMinor: limits.Minimum(),
		MaximumMinor: limits.MaximumMinor,
		// The presets are filtered by the domain against what the customer has
		// already credited, so an amount that is offered is one that would be
		// accepted.
		Presets: limits.OfferedPresets(credited),
	}
	if view.Providers, err = service.forCustomer(
		ctx, customerID, service.externalChoices(service.settings.Currency),
	); err != nil {
		return WalletView{}, err
	}
	if view.Presets == nil {
		view.Presets = []int64{}
	}
	if limits.WindowLimitMinor > 0 {
		remaining := limits.WindowLimitMinor - credited
		if remaining < 0 {
			remaining = 0
		}
		view.RemainingWindowMinor = remaining
	}
	return view, nil
}

// StartTopUp creates the top-up order and its provider payment.
//
// The idempotency key comes from the caller's Idempotency-Key header, so a
// retried request credits one top-up rather than two. Limits, validation, and
// the ledger movement all stay in commercepg; nothing about how much a customer
// may credit is decided here.
func (service *Service) StartTopUp(
	ctx context.Context, customerID, currency string, amountMinor int64,
	provider, idempotencyKey string,
) (TopUp, error) {
	if currency == "" {
		currency = service.settings.Currency
	}
	if service.payments == nil || !service.payments.Enabled(provider) {
		return TopUp{}, ErrProviderUnavailable
	}
	order, err := service.orders.CreateTopUpOrder(ctx, commercepg.TopUpInput{
		CustomerID: customerID, Currency: currency,
		AmountMinor: amountMinor, IdempotencyKey: idempotencyKey,
	})
	if err != nil {
		return TopUp{}, err
	}
	result := TopUp{
		OrderID: UUIDText(order.ID), Currency: order.Currency,
		AmountMinor: order.SubtotalMinor, State: order.State,
	}
	handle, err := service.StartPayment(ctx, PaymentRequest{
		OrderID: result.OrderID, Provider: provider,
		Description: "Wallet top-up", Channel: "customer_web",
	})
	if err != nil {
		// The order exists and is the customer's; a provider that refused the
		// intent is a retryable state, not a reason to hide what was created.
		return result, err
	}
	result.Payment = handle
	return result, nil
}

func holdsCurrency(balances []WalletBalance, currency string) bool {
	for _, balance := range balances {
		if balance.Currency == currency {
			return true
		}
	}
	return false
}

// externalChoices lists the methods that can settle an arbitrary amount in one
// currency. A top-up is not tied to a plan, so it has no price list to filter by.
func (service *Service) externalChoices(currency string) []PaymentChoice {
	choices := make([]PaymentChoice, 0, 4)
	if service.payments == nil {
		return choices
	}
	for _, option := range service.payments.Options() {
		if !option.Enabled || !option.Supports(currency) {
			continue
		}
		choices = append(choices, PaymentChoice{
			Provider: option.Provider, Currency: currency, Recurring: option.Recurring,
		})
	}
	return choices
}

// TopUpRejection recovers the stable machine reason from a wrapped top-up
// rejection so the panel can show localized copy for it.
func TopUpRejection(err error) string {
	if err == nil {
		return ""
	}
	message := err.Error()
	for _, reason := range []string{
		commerce.TopUpDisabled, commerce.TopUpBelowMinimum, commerce.TopUpAboveMaximum,
		commerce.TopUpWindowExceeded, commerce.TopUpInvalidAmount,
	} {
		if len(message) >= len(reason) && message[len(message)-len(reason):] == reason {
			return reason
		}
	}
	return ""
}
