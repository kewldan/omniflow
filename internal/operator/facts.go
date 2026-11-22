package operator

import (
	"context"
	"errors"
	"regexp"
	"strconv"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// uuidShape guards the lookups: a payload value that is not a UUID is never
// sent to PostgreSQL as one.
var uuidShape = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

// lookupFacts reads the order, customer, and ticket a notice names. A record
// that cannot be read is left out: the notice still goes, from its payload.
func lookupFacts(ctx context.Context, pool *pgxpool.Pool, payload map[string]any, locale string) noticeFacts {
	facts := noticeFacts{}
	if pool == nil {
		return facts
	}
	text := func(key string) string {
		value, _ := payload[key].(string)
		if uuidShape.MatchString(value) {
			return value
		}
		return ""
	}
	customerID := text("customerId")
	if orderID := text("orderId"); orderID != "" {
		if order, owner, err := lookupOrder(ctx, pool, orderID, locale); err == nil {
			facts.Order = &order
			if customerID == "" {
				customerID = owner
			}
		}
	}
	if customerID == "" {
		if entitlementID := text("entitlementId"); entitlementID != "" {
			_ = pool.QueryRow(ctx, `SELECT user_id::text FROM entitlements WHERE id = $1::uuid`, entitlementID).Scan(&customerID)
		}
	}
	if ticketID := text("ticketId"); ticketID != "" {
		messageID, _ := payload["messageId"].(string)
		if ticket, err := lookupTicket(ctx, pool, ticketID, messageID, locale); err == nil {
			facts.Ticket = &ticket
			if customerID == "" {
				customerID = ticket.CustomerID
			}
		}
	}
	if customerID != "" {
		currency := ""
		if facts.Order != nil {
			currency = facts.Order.Currency
		}
		if value, _ := payload["currency"].(string); value != "" {
			currency = value
		}
		if customer, err := lookupCustomer(ctx, pool, customerID, currency); err == nil {
			facts.Customer = &customer
		}
	}
	return facts
}

func lookupOrder(ctx context.Context, pool *pgxpool.Pool, orderID, locale string) (orderFacts, string, error) {
	var (
		order orderFacts
		owner string
	)
	err := pool.QueryRow(ctx, `SELECT o.id::text, o.user_id::text, o.operation, o.currency, o.subtotal_minor,
			o.discount_minor, o.wallet_minor, o.paid_minor,
			COALESCE(o.period_seconds, v.duration_seconds, 0)::bigint,
			COALESCE(pl.name, en.name, p.code, ''),
			COALESCE(pi.provider, ''), COALESCE(pi.method, ''),
			COALESCE((SELECT c.normalized_code FROM promo_redemptions r JOIN promo_codes c ON c.id = r.promo_code_id
				WHERE r.order_id = o.id ORDER BY r.redeemed_at DESC LIMIT 1), '')
		FROM orders o
		LEFT JOIN LATERAL (SELECT l.plan_id, l.plan_version_id FROM order_lines l
			WHERE l.order_id = o.id ORDER BY l.id LIMIT 1) line ON true
		LEFT JOIN plans p ON p.id = line.plan_id
		LEFT JOIN plan_versions v ON v.id = line.plan_version_id
		LEFT JOIN plan_localizations pl ON pl.plan_id = line.plan_id AND pl.locale = $2
		LEFT JOIN plan_localizations en ON en.plan_id = line.plan_id AND en.locale = 'en'
		LEFT JOIN LATERAL (SELECT i.provider, i.method FROM payment_intents i
			WHERE i.order_id = o.id ORDER BY (i.status = 'succeeded') DESC, i.created_at DESC LIMIT 1) pi ON true
		WHERE o.id = $1::uuid`, orderID, locale).Scan(
		&order.ID, &owner, &order.Operation, &order.Currency, &order.SubtotalMinor,
		&order.DiscountMinor, &order.WalletMinor, &order.PaidMinor, &order.PeriodSeconds,
		&order.PlanName, &order.Provider, &order.Method, &order.PromoCode)
	return order, owner, err
}

// lookupTicket reads a ticket and the customer message a notice is about:
// the one named by messageID (a reply), or the ticket's first customer
// message (a new ticket).
func lookupTicket(ctx context.Context, pool *pgxpool.Pool, ticketID, messageID, locale string) (ticketFacts, error) {
	var ticket ticketFacts
	message, err := strconv.ParseInt(messageID, 10, 64)
	if err != nil {
		message = 0
	}
	err = pool.QueryRow(ctx, `SELECT t.id::text, t.user_id::text, t.priority, t.status,
			CASE WHEN $2 = 'ru' THEN q.name_ru ELSE q.name_en END, t.subject, t.created_at,
			COALESCE((SELECT m.body FROM support_messages m
				WHERE m.ticket_id = t.id AND m.sender = 'customer' AND m.deleted_at IS NULL
				  AND ($3::bigint = 0 OR m.id = $3::bigint)
				ORDER BY m.id LIMIT 1), '')
		FROM support_tickets t JOIN support_queues q ON q.id = t.queue_id
		WHERE t.id = $1::uuid`, ticketID, locale, message).Scan(
		&ticket.ID, &ticket.CustomerID, &ticket.Priority, &ticket.Status, &ticket.QueueName,
		&ticket.Subject, &ticket.CreatedAt, &ticket.Message)
	return ticket, err
}

// lookupCustomer reads the customer's Telegram name and handle, their account
// status, how many payments they have made, and their wallet in the notice's
// currency.
func lookupCustomer(ctx context.Context, pool *pgxpool.Pool, customerID, currency string) (customerFacts, error) {
	customer := customerFacts{ID: customerID}
	var telegram string
	err := pool.QueryRow(ctx, `SELECT COALESCE(i.provider_subject, ''), COALESCE(i.metadata ->> 'username', ''),
			COALESCE(i.metadata ->> 'firstName', ''), COALESCE(i.metadata ->> 'lastName', ''), u.status,
			(SELECT count(*) FROM orders o WHERE o.user_id = u.id AND o.paid_at IS NOT NULL AND o.paid_minor > 0)
			+ (SELECT count(*) FROM legacy_payments lp WHERE lp.user_id = u.id)
		FROM users u
		LEFT JOIN LATERAL (SELECT provider_subject, metadata FROM identities
			WHERE user_id = u.id AND provider = 'telegram' AND status = 'active'
			ORDER BY created_at LIMIT 1) i ON true
		WHERE u.id = $1::uuid`, customerID).Scan(&telegram, &customer.Username,
		&customer.FirstName, &customer.LastName, &customer.Status, &customer.Payments)
	if errors.Is(err, pgx.ErrNoRows) {
		return customerFacts{}, err
	}
	if err != nil {
		return customerFacts{}, err
	}
	if id, parseErr := strconv.ParseInt(telegram, 10, 64); parseErr == nil && id > 0 {
		customer.TelegramID = id
	}
	if currency != "" {
		if err = pool.QueryRow(ctx, `SELECT COALESCE(sum(amount_minor), 0)::bigint FROM ledger_entries
			WHERE account_type = 'customer_wallet' AND user_id = $1::uuid AND currency = $2`,
			customerID, currency).Scan(&customer.WalletMinor); err == nil {
			customer.WalletCurrency = currency
		}
	}
	return customer, nil
}
