package panelpg

import (
	"context"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/omniflow/omniflow/internal/database/dbgen"
	"github.com/omniflow/omniflow/internal/goodsdelivery"
	"github.com/omniflow/omniflow/internal/tonrate"
)

// GoodsSales is the Stars shop's performance over a window, in the shop
// currency.
//
// Cost and margin are pointers because they can be unknown, and unknown is a
// different answer from zero: a delivery whose provider publishes no cost, or
// a TON cost with no rate to convert it at, leaves the margin unknowable, and
// reporting it as the full revenue would overstate what the shop earned.
type GoodsSales struct {
	Days              int    `json:"days"`
	Currency          string `json:"currency"`
	OrdersTotal       int32  `json:"ordersTotal"`
	OrdersDelivered   int32  `json:"ordersDelivered"`
	OrdersRefunded    int32  `json:"ordersRefunded"`
	OrdersNeedsReview int32  `json:"ordersNeedsReview"`
	OrdersInFlight    int32  `json:"ordersInFlight"`
	StarsSold         int64  `json:"starsSold"`
	RevenueMinor      int64  `json:"revenueMinor"`
	RefundedMinor     int64  `json:"refundedMinor"`
	CostNano          int64  `json:"costNano"`
	CostMinor         *int64 `json:"costMinor,omitempty"`
	MarginMinor       *int64 `json:"marginMinor,omitempty"`
	RateMinor         *int64 `json:"rateMinor,omitempty"`
	RateSource        string `json:"rateSource,omitempty"`
	MarginBasisPoints *int64 `json:"marginBps,omitempty"`
}

// GoodsSalesStats reports Stars sales since `days` ago.
//
// Each delivered order contributes the cost fixed at its own delivery. A
// delivery that recorded a TON cost but no conversion — one an operator
// closed by hand, or one delivered while no rate was available — is
// converted at `rate`, the rate of the moment, which is exactly what the
// fork's statistics do for the same rows.
func (service *Service) GoodsSalesStats(
	ctx context.Context, days int, currency string, rate *tonrate.Rate,
) (GoodsSales, error) {
	if days <= 0 || days > 3650 {
		return GoodsSales{}, ErrValidaton
	}
	since := time.Now().UTC().Add(-time.Duration(days) * 24 * time.Hour)
	row, err := service.queries().GoodsSalesStats(ctx, dbgen.GoodsSalesStatsParams{
		Since: pgtype.Timestamptz{Time: since, Valid: true}, Currency: currency,
	})
	if err != nil {
		return GoodsSales{}, err
	}
	sales := GoodsSales{
		Days: days, Currency: currency,
		OrdersTotal: row.OrdersTotal, OrdersDelivered: row.OrdersDelivered,
		OrdersRefunded: row.OrdersRefunded, OrdersNeedsReview: row.OrdersNeedsReview,
		OrdersInFlight: row.OrdersInFlight, StarsSold: row.StarsSold,
		RevenueMinor: row.RevenueMinor, RefundedMinor: row.RefundedMinor, CostNano: row.CostNano,
	}
	if rate != nil {
		value := rate.MinorPerTON
		sales.RateMinor, sales.RateSource = &value, rate.Source
	}
	if row.CostUnknownOrders > 0 {
		return sales, nil
	}
	cost := row.CostMinor
	if row.UnpricedNano > 0 {
		if rate == nil {
			return sales, nil
		}
		cost += rate.MinorFromNano(row.UnpricedNano)
	}
	margin := row.RevenueMinor - cost
	sales.CostMinor, sales.MarginMinor = &cost, &margin
	if row.RevenueMinor > 0 {
		bps := margin * 10_000 / row.RevenueMinor
		sales.MarginBasisPoints = &bps
	}
	return sales, nil
}

// RetryGoodsDelivery puts a parked delivery back in the queue.
//
// It is the operator saying "I checked the wallet: the payment did not leave".
// The attempt count and the payment marker are cleared, so the worker treats
// it as a fresh delivery. A reason is required because a wrong call here pays
// for the same Stars twice, and the evidence lives outside Omniflow.
func (service *Service) RetryGoodsDelivery(ctx context.Context, orderID string, actor Actor) error {
	if strings.TrimSpace(actor.Reason) == "" {
		return ErrValidaton
	}
	id, err := parseUUID(orderID)
	if err != nil {
		return err
	}
	return service.inTx(ctx, func(queries *dbgen.Queries) error {
		if _, txErr := queries.RequeueGoodsDelivery(ctx, id); txErr != nil {
			return rejected(txErr)
		}
		if _, txErr := queries.SetGoodsOrderStatus(ctx, dbgen.SetGoodsOrderStatusParams{
			OrderID: id, Status: "paid",
		}); txErr != nil {
			return txErr
		}
		return appendAudit(ctx, queries, actor.audit(
			"panel.goods.delivery_retried", "financial", "order", orderID, nil,
		))
	})
}

// RefundGoodsOrder returns the customer's money for a delivery that will not
// happen: one parked for review, or one still waiting whose payment has not
// started. The credit is the same ledger refund the worker issues, under the
// same idempotency key, so an operator and the worker refunding at the same
// moment credit once.
//
// A delivery whose payment marker is set and which has not been parked is
// mid-payment, and is refused: refunding it could give money back for Stars
// the recipient is about to receive.
func (service *Service) RefundGoodsOrder(ctx context.Context, orderID string, actor Actor) error {
	if strings.TrimSpace(actor.Reason) == "" {
		return ErrValidaton
	}
	id, err := parseUUID(orderID)
	if err != nil {
		return err
	}
	return service.inTx(ctx, func(queries *dbgen.Queries) error {
		if _, txErr := queries.LockGoodsDelivery(ctx, id); txErr != nil {
			return notFound(txErr)
		}
		delivery, txErr := queries.AbandonGoodsDelivery(ctx, dbgen.AbandonGoodsDeliveryParams{
			OrderID: id, LastErrorCode: optionalText("operator_refund"),
		})
		if txErr != nil {
			return rejected(txErr)
		}
		order, txErr := queries.GetGoodsOrder(ctx, id)
		if txErr != nil {
			return txErr
		}
		if txErr = goodsdelivery.Refund(ctx, queries, order, "refunded by an operator"); txErr != nil {
			return txErr
		}
		return appendAudit(ctx, queries, actor.audit(
			"panel.goods.order_refunded", "financial", "order", orderID,
			map[string]any{
				"amountMinor": order.QuotedPriceMinor - order.DiscountMinor, "currency": order.Currency,
				"fromStatus": delivery.Status,
			},
		))
	})
}

// CompleteGoodsDelivery closes a parked delivery as delivered: the operator
// confirmed the Stars arrived. The transaction hash they found, if any, is
// kept so the order links to the payment that settled it.
func (service *Service) CompleteGoodsDelivery(
	ctx context.Context, orderID, txHash string, actor Actor,
) error {
	if strings.TrimSpace(actor.Reason) == "" {
		return ErrValidaton
	}
	txHash = strings.TrimSpace(txHash)
	if len(txHash) > 128 {
		return ErrValidaton
	}
	id, err := parseUUID(orderID)
	if err != nil {
		return err
	}
	return service.inTx(ctx, func(queries *dbgen.Queries) error {
		if _, txErr := queries.CompleteReviewedGoodsDelivery(ctx, dbgen.CompleteReviewedGoodsDeliveryParams{
			OrderID: id, SettlementTxHash: optionalText(txHash),
		}); txErr != nil {
			return rejected(txErr)
		}
		if _, txErr := queries.SetGoodsOrderStatus(ctx, dbgen.SetGoodsOrderStatusParams{
			OrderID: id, Status: "delivered",
		}); txErr != nil {
			return txErr
		}
		return appendAudit(ctx, queries, actor.audit(
			"panel.goods.delivery_completed", "financial", "order", orderID,
			map[string]any{"txHashGiven": txHash != ""},
		))
	})
}
