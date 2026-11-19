package panelpg

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/omniflow/omniflow/internal/database/dbgen"
)

// DirectMessage is one operator message queued for a customer.
type DirectMessage struct {
	ID     string `json:"id"`
	Status string `json:"status"`
}

// MessageCustomer queues a message the bot delivers to the customer's
// Telegram. The text is the operator's own and is sent as plain text.
func (service *Service) MessageCustomer(ctx context.Context, customerID, body string, actor Actor) (DirectMessage, error) {
	id, err := parseUUID(customerID)
	if err != nil {
		return DirectMessage{}, err
	}
	body = strings.TrimSpace(body)
	if body == "" || utf8.RuneCountInString(body) > 3500 {
		return DirectMessage{}, ErrValidaton
	}
	var message DirectMessage
	err = service.inTx(ctx, func(queries *dbgen.Queries) error {
		if err := service.queueDirectMessage(ctx, queries, id, body, actor, &message); err != nil {
			return err
		}
		return appendAudit(ctx, queries, actor.audit(
			"customer.message.sent", "support", "customer", customerID,
			map[string]any{"messageId": message.ID, "length": utf8.RuneCountInString(body)},
		))
	})
	return message, err
}

func (service *Service) queueDirectMessage(
	ctx context.Context, queries *dbgen.Queries, userID pgtype.UUID, body string, actor Actor, message *DirectMessage,
) error {
	createdBy := pgtype.UUID{}
	if actor.AdminID != "" {
		createdBy, _ = parseUUID(actor.AdminID)
	}
	id, err := queries.InsertCustomerDirectMessage(ctx, dbgen.InsertCustomerDirectMessageParams{
		UserID: userID, Body: body, CreatedBy: createdBy,
	})
	if err != nil {
		return notFound(err)
	}
	*message = DirectMessage{ID: uuidString(id), Status: "pending"}
	return nil
}

// WalletAdjustment is an operator credit or debit of a customer's wallet.
type WalletAdjustment struct {
	AmountMinor int64  `json:"amountMinor"`
	Currency    string `json:"currency"`
	Reason      string `json:"reason"`
	// Notify sends the customer a message saying what changed.
	Notify bool `json:"notify"`
	// IdempotencyKey makes a retried click the same adjustment.
	IdempotencyKey string `json:"idempotencyKey"`
}

// ErrNegativeBalance refuses a debit larger than the balance.
var ErrNegativeBalance = errors.New("the adjustment would make the balance negative")

// AdjustCustomerWallet credits or debits a wallet with a balanced ledger
// transaction and an audit event, optionally telling the customer.
func (service *Service) AdjustCustomerWallet(
	ctx context.Context, customerID string, adjustment WalletAdjustment, actor Actor,
) error {
	userID, err := parseUUID(customerID)
	if err != nil {
		return err
	}
	currency := strings.ToUpper(strings.TrimSpace(adjustment.Currency))
	reason := strings.TrimSpace(adjustment.Reason)
	key := strings.TrimSpace(adjustment.IdempotencyKey)
	if adjustment.AmountMinor == 0 || len(currency) != 3 || reason == "" || len(key) < 8 || len(key) > 128 {
		return ErrValidaton
	}
	kind := "credit"
	if adjustment.AmountMinor < 0 {
		kind = "debit"
	}
	return service.inTx(ctx, func(queries *dbgen.Queries) error {
		if _, existing := queries.GetLedgerTransactionByIdempotency(ctx, "panel-wallet:"+key); existing == nil {
			return nil
		}
		if adjustment.AmountMinor < 0 {
			balance, balanceErr := queries.GetWalletBalance(ctx, dbgen.GetWalletBalanceParams{UserID: userID, Currency: currency})
			if balanceErr != nil {
				return balanceErr
			}
			if balance+adjustment.AmountMinor < 0 {
				return ErrNegativeBalance
			}
		}
		transaction, err := queries.CreateLedgerTransaction(ctx, dbgen.CreateLedgerTransactionParams{
			Type: kind, ReferenceType: "operator_adjustment", ReferenceID: customerID + ":" + key,
			IdempotencyKey: "panel-wallet:" + key,
			Reason:         pgtype.Text{String: reason, Valid: true},
			ActorID:        pgtype.Text{String: actor.AdminID, Valid: actor.AdminID != ""},
		})
		if err != nil {
			return err
		}
		if _, err = queries.InsertLedgerEntry(ctx, dbgen.InsertLedgerEntryParams{TransactionID: transaction.ID, AccountType: "customer_wallet", UserID: userID, Currency: currency, AmountMinor: adjustment.AmountMinor}); err != nil {
			return notFound(err)
		}
		if _, err = queries.InsertLedgerEntry(ctx, dbgen.InsertLedgerEntryParams{TransactionID: transaction.ID, AccountType: "platform_clearing", Currency: currency, AmountMinor: -adjustment.AmountMinor}); err != nil {
			return err
		}
		if adjustment.Notify {
			var message DirectMessage
			if err = service.queueDirectMessage(ctx, queries, userID, walletNotice(adjustment.AmountMinor, currency, reason), actor, &message); err != nil {
				return err
			}
		}
		return appendAudit(ctx, queries, actor.audit(
			"wallet.adjusted", "financial", "customer", customerID,
			map[string]any{"amountMinor": adjustment.AmountMinor, "currency": currency, "notified": adjustment.Notify},
		))
	})
}

// walletNotice is the customer-facing text for an adjustment. It is in
// Russian and English together because the panel does not choose the
// customer's language for an operator's note.
func walletNotice(amount int64, currency, reason string) string {
	sign := "+"
	if amount < 0 {
		sign, amount = "−", -amount
	}
	value := fmt.Sprintf("%s%d.%02d %s", sign, amount/100, amount%100, currency)
	return fmt.Sprintf("💰 Баланс изменён: %s\nПричина: %s\n\n💰 Balance changed: %s\nReason: %s", value, reason, value, reason)
}
