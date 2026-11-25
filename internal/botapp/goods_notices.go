package botapp

import (
	"context"
	"html"

	"github.com/go-telegram/bot/models"
	"github.com/omniflow/omniflow/internal/goods"
)

// Shop notices tell a buyer what became of a purchase without them having to
// look: the Stars arrived (with a VPN offer for a buyer who has none), the
// order was refunded, a saved purchase bought itself after a top-up, or it did
// not because the price rose.
//
// Every one of them is derived from durable state — the order, the delivery,
// the cart — rather than queued by whoever changed it, so the worker, the
// sweep, and an operator in the panel need no Telegram client, and a notice
// can never be lost between a commit and a send. The notification ledger's
// dedupe key is what makes each one go out once.

// goodsNoticeWindow bounds how far back a pass looks. It keeps a pass from
// announcing every order ever delivered, and is long enough to ride out a bot
// that was down for a few hours.
const goodsNoticeWindow = "6 hours"

// GoodsNotice is one shop outcome waiting to be told to its buyer.
type GoodsNotice = goodsNotice

type goodsNotice struct {
	UserID    string
	OrderID   string
	Kind      string
	Name      string
	Status    string
	Failure   string
	Recipient string
	Stars     int
	PaidMinor int64
	Currency  string
	Dedupe    string
	Auto      bool
	PriceRose bool
}

// deliverGoodsNotices sends one batch of shop notices.
func (notifier *Notifier) deliverGoodsNotices(ctx context.Context) {
	notices, err := notifier.store.PendingGoodsNotices(ctx, notificationBatch)
	if err != nil {
		notifier.logger.Error("shop notice lookup failed", "error", err)
		return
	}
	lookup := notifier.candidateLookup()
	for _, notice := range notices {
		if ctx.Err() != nil {
			return
		}
		candidate, found, err := lookup(ctx, notice.UserID)
		if err != nil {
			notifier.logger.Error("shop notice recipient lookup failed", "error", err)
			continue
		}
		if !found {
			// A web-only customer has no chat to send to. The order screen in
			// the panel says the same thing.
			if suppressErr := notifier.store.suppressUnreachable(ctx, notice.UserID, "", "fulfillment",
				notice.Dedupe, "no_telegram"); suppressErr != nil {
				notifier.logger.Warn("shop notice suppression failed", "error", suppressErr)
			}
			continue
		}
		locale := localeFrom(candidate.Locale)
		view := goodsNoticeView(locale, notice)
		if notice.Status == "delivered" && !notice.Auto && !notice.PriceRose && notifier.settings.GoodsVPNOffer {
			if offerText, button, ok := notifier.vpnOffer(ctx, locale, notice.UserID); ok {
				view.Text += "\n\n" + offerText
				view.Keyboard = keyboard(row(button))
			}
		}
		notifier.deliver(ctx, candidate, "", "fulfillment", notice.Dedupe, view)
	}
}

func goodsNoticeView(locale Locale, notice goodsNotice) View {
	if notice.Kind == goods.KindSteamTopUp {
		return steamNoticeView(locale, notice)
	}
	recipient := html.EscapeString(notice.Recipient)
	paid := formatMoney(notice.PaidMinor, notice.Currency)
	view := View{Keyboard: keyboard(row(callbackButton(text(locale, "stars.myOrders"), routeShopOrders)))}
	switch {
	case notice.PriceRose:
		view.Text = text(locale, "stars.autoPriceChanged", notice.Stars, recipient)
		view.Keyboard = keyboard(row(callbackButton(text(locale, "menu.stars"), routeStars)))
	case notice.Auto:
		view.Text = text(locale, "stars.autoPurchased", notice.Stars, recipient)
	case notice.Status == "delivered" && notice.Kind == "telegram_stars":
		view.Text = text(locale, "stars.delivered", notice.Stars, recipient)
		view.Keyboard = nil
	case notice.Status == "delivered":
		view.Text = text(locale, "stars.goodsDelivered", html.EscapeString(notice.Name), recipient)
		view.Keyboard = nil
	case notice.Failure == "recipient_invalid":
		view.Text = text(locale, "stars.refundedRecipient", recipient, paid)
	default:
		view.Text = text(locale, "stars.refunded", recipient, paid)
	}
	return view
}

// steamNoticeView tells a Steam buyer what became of a top-up. The login is
// shown as code rather than as a Telegram handle.
func steamNoticeView(locale Locale, notice goodsNotice) View {
	login := html.EscapeString(notice.Recipient)
	paid := formatMoney(notice.PaidMinor, notice.Currency)
	view := View{Keyboard: keyboard(row(callbackButton(text(locale, "stars.myOrders"), routeShopOrders)))}
	switch {
	case notice.PriceRose:
		view.Text = text(locale, "steam.autoPriceChanged", notice.Stars, login)
		view.Keyboard = keyboard(row(callbackButton(text(locale, "menu.steam"), routeSteam)))
	case notice.Auto:
		view.Text = text(locale, "steam.autoPurchased", notice.Stars, login)
	case notice.Status == "delivered":
		view.Text = text(locale, "steam.delivered", notice.Stars, login)
		view.Keyboard = nil
	case notice.Failure == "recipient_invalid":
		view.Text = text(locale, "steam.refundedRecipient", login, paid)
	default:
		view.Text = text(locale, "steam.refunded", login, paid)
	}
	return view
}

// vpnOffer is the fork's offer after a delivery: nothing for a buyer with an
// active paid subscription or a running trial; a trial for one who never had
// one, when a trial plan is on sale; a purchase otherwise.
func (notifier *Notifier) vpnOffer(ctx context.Context, locale Locale, customerID string) (string, models.InlineKeyboardButton, bool) {
	var paid, trialing, trialled bool
	var trialVersion *string
	err := notifier.store.pool.QueryRow(ctx, `SELECT
		EXISTS (SELECT 1 FROM entitlements e
			JOIN plan_versions v ON v.id = e.plan_version_id JOIN plans p ON p.id = v.plan_id
			WHERE e.user_id = $1::uuid AND e.status IN ('pending', 'active', 'limited')
			  AND e.ends_at > now() AND p.kind <> 'trial'),
		EXISTS (SELECT 1 FROM entitlements e
			JOIN plan_versions v ON v.id = e.plan_version_id JOIN plans p ON p.id = v.plan_id
			WHERE e.user_id = $1::uuid AND e.status IN ('pending', 'active', 'limited')
			  AND e.ends_at > now() AND p.kind = 'trial'),
		EXISTS (SELECT 1 FROM trial_claims c WHERE c.user_id = $1::uuid),
		(SELECT v.id::text FROM plans p JOIN plan_versions v ON v.plan_id = p.id
			WHERE p.kind = 'trial' AND p.visible AND p.archived_at IS NULL AND v.retired_at IS NULL
			ORDER BY p.sort_order, v.version DESC LIMIT 1)`, customerID).
		Scan(&paid, &trialing, &trialled, &trialVersion)
	if err != nil {
		notifier.logger.Warn("vpn offer lookup failed", "error", err)
		return "", models.InlineKeyboardButton{}, false
	}
	switch {
	case paid, trialing:
		return "", models.InlineKeyboardButton{}, false
	case !trialled && trialVersion != nil:
		return text(locale, "stars.vpnTrial"),
			actionButton(text(locale, "stars.vpnTrialButton"), "plan:"+*trialVersion), true
	default:
		return text(locale, "stars.vpnBuy"), callbackButton(text(locale, "stars.vpnBuyButton"), routePlans), true
	}
}

// PendingGoodsNotices finds shop outcomes a buyer has not been told about. It
// is exported so the query can be checked against a real database.
func (store *PostgresStore) PendingGoodsNotices(ctx context.Context, limit int) ([]GoodsNotice, error) {
	rows, err := store.pool.Query(ctx, `
		WITH outcomes AS (
			SELECT g.user_id, g.order_id, p.kind, COALESCE(l.name, p.code) AS name, g.status,
			       COALESCE(d.failure_class, '') AS failure, g.recipient_username,
			       COALESCE(g.star_quantity, p.star_quantity * g.quantity, 0) AS stars,
			       g.quoted_price_minor - g.discount_minor AS paid, g.currency,
			       'goods:' || g.order_id::text || ':' || g.status AS dedupe,
			       false AS auto, false AS price_rose
			FROM goods_orders g
			JOIN goods_products p ON p.id = g.product_id
			LEFT JOIN goods_deliveries d ON d.order_id = g.order_id
			LEFT JOIN users u ON u.id = g.user_id
			LEFT JOIN goods_product_localizations l ON l.product_id = p.id AND l.locale = COALESCE(u.locale, 'en')
			WHERE g.status IN ('delivered', 'refunded')
			  AND g.updated_at > now() - interval '`+goodsNoticeWindow+`'
			UNION ALL
			SELECT g.user_id, g.order_id, p.kind, p.code, 'paid', '', g.recipient_username,
			       COALESCE(g.star_quantity, 0), g.quoted_price_minor - g.discount_minor, g.currency,
			       'goods-auto:' || g.order_id::text, true, false
			FROM goods_orders g
			JOIN goods_products p ON p.id = g.product_id
			WHERE g.source = 'auto_cart' AND g.created_at > now() - interval '`+goodsNoticeWindow+`'
			UNION ALL
			SELECT c.user_id, NULL, p.kind, p.code, 'open', '', cg.recipient_username,
			       COALESCE(cg.star_quantity, p.star_quantity * cg.quantity, 0), cg.saved_price_minor, cg.currency,
			       'goods-price:' || c.id::text || ':' || c.idempotency_key, false, true
			FROM carts c
			JOIN cart_goods cg ON cg.cart_id = c.id
			JOIN goods_products p ON p.id = cg.product_id
			WHERE c.kind = 'goods' AND c.status = 'open' AND c.last_failure = 'cart_price_changed'
			  AND c.updated_at > now() - interval '`+goodsNoticeWindow+`'
		)
		SELECT o.user_id::text, COALESCE(o.order_id::text, ''), o.kind, o.name, o.status, o.failure,
		       o.recipient_username, o.stars, o.paid, o.currency, o.dedupe, o.auto, o.price_rose
		FROM outcomes o
		WHERE NOT EXISTS (
			SELECT 1 FROM notification_deliveries n
			WHERE n.user_id = o.user_id AND n.kind = 'fulfillment' AND n.subscription_id IS NULL
			  AND n.dedupe_key = o.dedupe AND NOT `+reclaimableDeliveryAs("n")+`
		)
		LIMIT $1`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	notices := make([]GoodsNotice, 0, 16)
	for rows.Next() {
		var notice GoodsNotice
		if err := rows.Scan(&notice.UserID, &notice.OrderID, &notice.Kind, &notice.Name, &notice.Status,
			&notice.Failure, &notice.Recipient, &notice.Stars, &notice.PaidMinor, &notice.Currency,
			&notice.Dedupe, &notice.Auto, &notice.PriceRose); err != nil {
			return nil, err
		}
		notices = append(notices, notice)
	}
	return notices, rows.Err()
}
