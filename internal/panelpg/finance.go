package panelpg

import (
	"context"
	"strings"
	"time"

	"github.com/omniflow/omniflow/internal/database/dbgen"
)

// OrderFilter is what the finance search accepts.
type OrderFilter struct {
	State      string
	Operation  string
	CustomerID string
	Currency   string
	From       *time.Time
	To         *time.Time
	Cursor     string
	PageSize   int32
}

// OrderPage is one page of finance search results.
type OrderPage struct {
	Items      []OrderSummary `json:"items"`
	NextCursor string         `json:"nextCursor,omitempty"`
}

// SearchOrders finds orders across every customer.
func (service *Service) SearchOrders(ctx context.Context, filter OrderFilter) (OrderPage, error) {
	size := pageSize(filter.PageSize)
	cursor := DecodeCursor(filter.Cursor)

	rows, err := service.queries().SearchOrders(ctx, dbgen.SearchOrdersParams{
		CursorCreatedAt: cursor.timestamp(),
		CursorID:        cursor.uuid(),
		State:           optionalText(filter.State),
		Operation:       optionalText(filter.Operation),
		UserID:          optionalUUID(filter.CustomerID),
		Currency:        optionalText(filter.Currency),
		CreatedFrom:     optionalTimestamp(filter.From),
		CreatedTo:       optionalTimestamp(filter.To),
		PageSize:        size + 1,
	})
	if err != nil {
		return OrderPage{}, err
	}

	page := OrderPage{Items: make([]OrderSummary, 0, min(len(rows), int(size)))}
	for index, row := range rows {
		if index == int(size) {
			last := rows[index-1]
			page.NextCursor = EncodeCursor(timeValue(last.CreatedAt), uuidString(last.ID))
			break
		}
		page.Items = append(page.Items, orderSummaryFrom(row))
	}
	return page, nil
}

// PaymentIntentSummary is one payment attempt against an order.
type PaymentIntentSummary struct {
	ID                string         `json:"id"`
	Provider          string         `json:"provider"`
	Status            string         `json:"status"`
	AmountMinor       int64          `json:"amountMinor"`
	Currency          string         `json:"currency"`
	ProviderReference string         `json:"providerReference,omitempty"`
	CreatedAt         time.Time      `json:"createdAt"`
	UpdatedAt         time.Time      `json:"updatedAt"`
	Events            []PaymentEvent `json:"events,omitempty"`
}

// PaymentEvent is one recorded transition or anomaly on an intent.
//
// The event vocabulary is the interesting part of a payment timeline: an
// amount mismatch, a duplicate, a late settlement, an overpayment, and a
// reconciliation each have their own type, so a support operator can read what
// happened rather than infer it from two status values.
type PaymentEvent struct {
	Type           string    `json:"type"`
	PreviousStatus string    `json:"previousStatus,omitempty"`
	Status         string    `json:"status,omitempty"`
	AmountMinor    *int64    `json:"amountMinor,omitempty"`
	Currency       string    `json:"currency,omitempty"`
	OccurredAt     time.Time `json:"occurredAt"`
}

// RefundSummary is one refund against an intent.
type RefundSummary struct {
	ID                string    `json:"id"`
	PaymentIntentID   string    `json:"paymentIntentId"`
	Status            string    `json:"status"`
	AmountMinor       int64     `json:"amountMinor"`
	Currency          string    `json:"currency"`
	Reason            string    `json:"reason"`
	ProviderReference string    `json:"providerReference,omitempty"`
	CreatedAt         time.Time `json:"createdAt"`
	UpdatedAt         time.Time `json:"updatedAt"`
}

// OrderDetail is one order with its full payment and refund timeline.
type OrderDetail struct {
	Order   OrderSummary           `json:"order"`
	Intents []PaymentIntentSummary `json:"intents"`
	Refunds []RefundSummary        `json:"refunds"`
}

// OrderDetail assembles the finance timeline for one order.
//
// The provider payload is deliberately absent. `provider_webhook_events`
// retains the raw body for replay and dispute, and it is reachable through the
// webhook diagnostics with its own permission; putting it in the order view
// would put a provider payload in front of every operator who can read an
// order.
func (service *Service) OrderDetail(ctx context.Context, orderID string) (OrderDetail, error) {
	id, err := parseUUID(orderID)
	if err != nil {
		return OrderDetail{}, err
	}
	queries := service.queries()

	order, err := queries.GetOrder(ctx, id)
	if err != nil {
		return OrderDetail{}, notFound(err)
	}
	intents, err := queries.ListOrderPaymentIntents(ctx, id)
	if err != nil {
		return OrderDetail{}, err
	}
	refunds, err := queries.ListRefundsForOrder(ctx, id)
	if err != nil {
		return OrderDetail{}, err
	}

	detail := OrderDetail{
		Order:   orderSummaryFrom(order),
		Intents: make([]PaymentIntentSummary, 0, len(intents)),
		Refunds: make([]RefundSummary, 0, len(refunds)),
	}
	for _, intent := range intents {
		summary := PaymentIntentSummary{
			ID:                uuidString(intent.ID),
			Provider:          intent.Provider,
			Status:            intent.Status,
			AmountMinor:       intent.AmountMinor,
			Currency:          intent.Currency,
			ProviderReference: textValue(intent.ProviderReference),
			CreatedAt:         timeValue(intent.CreatedAt),
			UpdatedAt:         timeValue(intent.UpdatedAt),
		}
		events, eventsErr := queries.ListPaymentEventsForIntent(ctx, intent.ID)
		if eventsErr != nil {
			return OrderDetail{}, eventsErr
		}
		for _, event := range events {
			summary.Events = append(summary.Events, PaymentEvent{
				Type:           event.Type,
				PreviousStatus: textValue(event.PreviousStatus),
				Status:         textValue(event.Status),
				AmountMinor:    int8Pointer(event.AmountMinor),
				Currency:       textValue(event.Currency),
				OccurredAt:     timeValue(event.OccurredAt),
			})
		}
		detail.Intents = append(detail.Intents, summary)
	}
	for _, refund := range refunds {
		detail.Refunds = append(detail.Refunds, RefundSummary{
			ID:                uuidString(refund.ID),
			PaymentIntentID:   uuidString(refund.PaymentIntentID),
			Status:            refund.Status,
			AmountMinor:       refund.AmountMinor,
			Currency:          refund.Currency,
			Reason:            refund.Reason,
			ProviderReference: textValue(refund.ProviderReference),
			CreatedAt:         timeValue(refund.CreatedAt),
			UpdatedAt:         timeValue(refund.UpdatedAt),
		})
	}
	return detail, nil
}

// StuckPayment is an intent that has been in flight longer than any supported
// provider should take.
type StuckPayment struct {
	ID          string    `json:"id"`
	OrderID     string    `json:"orderId"`
	CustomerID  string    `json:"customerId"`
	Operation   string    `json:"operation"`
	Provider    string    `json:"provider"`
	Status      string    `json:"status"`
	AmountMinor int64     `json:"amountMinor"`
	Currency    string    `json:"currency"`
	UpdatedAt   time.Time `json:"updatedAt"`
	// Age is how long it has been stuck, which is the number that decides
	// whether an operator reconciles now or waits.
	Age time.Duration `json:"-"`
}

// StuckPayments lists the reconciliation queue.
func (service *Service) StuckPayments(ctx context.Context, limit int32) ([]StuckPayment, error) {
	rows, err := service.queries().ListStuckPaymentIntents(ctx, dbgen.ListStuckPaymentIntentsParams{
		StuckAfter: interval(stuckPaymentAfter), PageSize: pageSize(limit),
	})
	if err != nil {
		return nil, err
	}
	now := service.now()
	payments := make([]StuckPayment, 0, len(rows))
	for _, row := range rows {
		updated := timeValue(row.PaymentIntent.UpdatedAt)
		payments = append(payments, StuckPayment{
			ID:          uuidString(row.PaymentIntent.ID),
			OrderID:     uuidString(row.PaymentIntent.OrderID),
			CustomerID:  uuidString(row.UserID),
			Operation:   row.Operation,
			Provider:    row.PaymentIntent.Provider,
			Status:      row.PaymentIntent.Status,
			AmountMinor: row.PaymentIntent.AmountMinor,
			Currency:    row.PaymentIntent.Currency,
			UpdatedAt:   updated,
			Age:         now.Sub(updated),
		})
	}
	return payments, nil
}

// RecordReconciliation appends the audit event for an operator-triggered
// reconciliation.
//
// The reconciliation itself is performed by the payment service, which already
// owns provider polling and is idempotent. This package records who asked for
// it: the panel must never become a second place that decides how a payment
// settles.
func (service *Service) RecordReconciliation(
	ctx context.Context, paymentIntentID, outcome string, actor Actor,
) error {
	return service.inTx(ctx, func(queries *dbgen.Queries) error {
		return appendAudit(ctx, queries, actor.audit(
			"panel.payment.reconciled", "financial", "payment_intent", paymentIntentID,
			map[string]any{"outcome": outcome},
		))
	})
}

// RecordRefund appends the audit event for an operator-initiated refund.
//
// As with reconciliation, the refund is executed by the payment service against
// the provider's own capabilities — full or partial, supported or not. What is
// recorded here is the decision and its reason, which the refund itself does
// not carry.
func (service *Service) RecordRefund(
	ctx context.Context, orderID, refundID string, amountMinor int64, currency string, actor Actor,
) error {
	if strings.TrimSpace(actor.Reason) == "" {
		return ErrValidaton
	}
	return service.inTx(ctx, func(queries *dbgen.Queries) error {
		return appendAudit(ctx, queries, actor.audit(
			"panel.refund.issued", "financial", "order", orderID,
			map[string]any{"refundId": refundID, "amountMinor": amountMinor, "currency": currency},
		))
	})
}

// FinanceExportRow is one line of the CSV export.
//
// The field order here is the column order in the file. The schema is stable: a
// later version appends columns and never inserts one, so an importer built
// against an older export keeps working.
type FinanceExportRow struct {
	OrderID       string
	CreatedAt     time.Time
	UpdatedAt     time.Time
	CustomerID    string
	State         string
	Operation     string
	Currency      string
	SubtotalMinor int64
	DiscountMinor int64
	WalletMinor   int64
	ExternalMinor int64
	PaidMinor     int64
	RefundedMinor int64
	Providers     string
}

// FinanceExportHeader names the columns, in order.
//
// Amounts are labelled `_minor` because they are integers in the currency's
// minor unit, and every timestamp is UTC. Both are stated in the header rather
// than only in the documentation, because a CSV is read by whoever opens it.
var FinanceExportHeader = []string{
	"order_id", "created_at_utc", "updated_at_utc", "customer_id", "state", "operation",
	"currency", "subtotal_minor", "discount_minor", "wallet_minor", "external_minor",
	"paid_minor", "refunded_minor", "providers",
}

// ExportFinance streams one page of the export.
//
// It is paged rather than materialised because an export over a year of orders
// is larger than any request should hold in memory. Transport walks the cursor
// and flushes as it goes.
func (service *Service) ExportFinance(
	ctx context.Context, filter OrderFilter,
) ([]FinanceExportRow, string, error) {
	size := pageSize(filter.PageSize)
	cursor := DecodeCursor(filter.Cursor)

	rows, err := service.queries().ExportFinanceRows(ctx, dbgen.ExportFinanceRowsParams{
		CreatedFrom:     optionalTimestamp(filter.From),
		CreatedTo:       optionalTimestamp(filter.To),
		State:           optionalText(filter.State),
		CursorCreatedAt: cursor.timestamp(),
		CursorID:        cursor.uuid(),
		PageSize:        size + 1,
	})
	if err != nil {
		return nil, "", err
	}

	exported := make([]FinanceExportRow, 0, min(len(rows), int(size)))
	next := ""
	for index, row := range rows {
		if index == int(size) {
			last := rows[index-1]
			next = EncodeCursor(timeValue(last.CreatedAt), uuidString(last.OrderID))
			break
		}
		exported = append(exported, FinanceExportRow{
			OrderID:       uuidString(row.OrderID),
			CreatedAt:     timeValue(row.CreatedAt),
			UpdatedAt:     timeValue(row.UpdatedAt),
			CustomerID:    uuidString(row.UserID),
			State:         row.State,
			Operation:     row.Operation,
			Currency:      row.Currency,
			SubtotalMinor: row.SubtotalMinor,
			DiscountMinor: row.DiscountMinor,
			WalletMinor:   row.WalletMinor,
			ExternalMinor: row.ExternalMinor,
			PaidMinor:     row.PaidMinor,
			RefundedMinor: row.RefundedMinor,
			Providers:     string(row.Providers),
		})
	}
	return exported, next, nil
}

// Fields renders one export row in header order.
func (row FinanceExportRow) Fields() []string {
	return []string{
		row.OrderID,
		row.CreatedAt.UTC().Format(time.RFC3339),
		row.UpdatedAt.UTC().Format(time.RFC3339),
		row.CustomerID,
		row.State,
		row.Operation,
		row.Currency,
		formatInt(row.SubtotalMinor),
		formatInt(row.DiscountMinor),
		formatInt(row.WalletMinor),
		formatInt(row.ExternalMinor),
		formatInt(row.PaidMinor),
		formatInt(row.RefundedMinor),
		row.Providers,
	}
}
