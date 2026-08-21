package commerce

import (
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"
)

var currencyPattern = regexp.MustCompile(`^[A-Z]{3}$`)

var (
	ErrCurrencyMismatch  = errors.New("currency mismatch")
	ErrInvalidAmount     = errors.New("invalid monetary amount")
	ErrInvalidTransition = errors.New("invalid order transition")
	ErrPromotionInvalid  = errors.New("promotion is not eligible")
	ErrUnbalancedLedger  = errors.New("ledger transaction is not balanced")
)

type Money struct {
	Amount   int64  `json:"amountMinor"`
	Currency string `json:"currency"`
}

func NewMoney(amount int64, currency string) (Money, error) {
	currency = strings.ToUpper(strings.TrimSpace(currency))
	if amount < 0 {
		return Money{}, ErrInvalidAmount
	}
	if !currencyPattern.MatchString(currency) {
		return Money{}, fmt.Errorf("%w: currency", ErrInvalidAmount)
	}
	return Money{Amount: amount, Currency: currency}, nil
}

func (money Money) Add(other Money) (Money, error) {
	if money.Currency != other.Currency {
		return Money{}, ErrCurrencyMismatch
	}
	if other.Amount > 0 && money.Amount > (1<<63-1)-other.Amount {
		return Money{}, ErrInvalidAmount
	}
	return Money{Amount: money.Amount + other.Amount, Currency: money.Currency}, nil
}

type PlanVersion struct {
	ID                    string
	PlanID                string
	Version               int
	BillingPeriod         string
	Duration              time.Duration
	TrafficAllowanceBytes *int64
	DeviceLimit           *int
	SquadIDs              []string
	RecurringCapable      bool
	Prices                map[string]int64
	UpgradePolicy         string
	DowngradePolicy       string
	CancellationPolicy    string
}

type EntitlementSchedule struct {
	StartsAt    time.Time
	EndsAt      time.Time
	EffectiveAt time.Time
}

func ScheduleEntitlement(now time.Time, duration time.Duration, operation, upgradePolicy, downgradePolicy string, currentEndsAt *time.Time) (EntitlementSchedule, error) {
	if duration <= 0 {
		return EntitlementSchedule{}, ErrInvalidAmount
	}
	base := now
	if currentEndsAt != nil && currentEndsAt.After(now) {
		base = *currentEndsAt
	}
	schedule := EntitlementSchedule{StartsAt: now, EndsAt: now.Add(duration), EffectiveAt: now}
	switch operation {
	case "purchase":
		return schedule, nil
	case "extension", "renewal":
		// A renewal extends the same way a manual extension does. They are
		// separate operations because the *reason* differs — one the customer
		// asked for, one an automatic charge produced — and support has to be
		// able to tell them apart. The entitlement arithmetic is identical.
		schedule.EndsAt = base.Add(duration)
		return schedule, nil
	case "upgrade":
		switch upgradePolicy {
		case "replace":
			return schedule, nil
		case "extend":
			schedule.EndsAt = base.Add(duration)
			return schedule, nil
		default:
			return EntitlementSchedule{}, ErrInvalidTransition
		}
	case "downgrade":
		switch downgradePolicy {
		case "immediate":
			return schedule, nil
		case "at_expiry":
			schedule.StartsAt = base
			schedule.EffectiveAt = base
			schedule.EndsAt = base.Add(duration)
			return schedule, nil
		default:
			return EntitlementSchedule{}, ErrInvalidTransition
		}
	default:
		return EntitlementSchedule{}, ErrInvalidTransition
	}
}

// ResetsTraffic reports whether settling the operation starts the customer's
// traffic period afresh in Remnawave. An extension, a renewal, and an upgrade
// buy a new period, so the counter restarts and a LIMITED user comes back as
// ACTIVE. A purchase provisions a new user with nothing to reset, and a
// downgrade keeps whatever the policy says about the current period.
func ResetsTraffic(operation string) bool {
	switch operation {
	case "extension", "renewal", "upgrade":
		return true
	default:
		return false
	}
}

func (version PlanVersion) Price(currency string) (Money, error) {
	amount, ok := version.Prices[strings.ToUpper(currency)]
	if !ok {
		return Money{}, ErrCurrencyMismatch
	}
	return NewMoney(amount, currency)
}

type Promotion struct {
	ID       string
	Kind     string
	Value    int64
	Currency string
	// AppliesTo names the catalogue this promotion discounts: plans or goods,
	// never both. An empty value reads as plans, which is what every promotion
	// written before the shop existed meant.
	AppliesTo string
	StartsAt  *time.Time
	EndsAt    *time.Time
	// PlanIDs scopes a plan promotion. Empty means every plan.
	PlanIDs map[string]struct{}
	// ProductIDs scopes a goods promotion. Empty means every visible product.
	ProductIDs       map[string]struct{}
	Eligibility      func(customerID string) bool
	RedemptionLimit  *int
	CustomerLimit    int
	RedemptionCount  int
	CustomerRedeemed int
}

// AppliesToGoods reports whether this promotion is for the shop.
func (promotion Promotion) AppliesToGoods() bool {
	return promotion.AppliesTo == PromotionAppliesToGoods
}

func (promotion Promotion) Discount(now time.Time, customerID, planID string, subtotal Money) (Money, error) {
	// A promotion written for the shop does not discount a plan. The check is
	// here rather than only at the call site so the two catalogues cannot drift:
	// whichever path a promotion reaches, it is refused unless it names that one.
	//
	// `planID` is empty when DiscountForGoods delegates the shared rules here,
	// which is the one case where a goods promotion legitimately arrives.
	if promotion.AppliesToGoods() && planID != "" {
		return Money{}, ErrPromotionInvalid
	}
	if promotion.StartsAt != nil && now.Before(*promotion.StartsAt) || promotion.EndsAt != nil && !now.Before(*promotion.EndsAt) {
		return Money{}, ErrPromotionInvalid
	}
	if len(promotion.PlanIDs) > 0 {
		if _, ok := promotion.PlanIDs[planID]; !ok {
			return Money{}, ErrPromotionInvalid
		}
	}
	if promotion.Eligibility != nil && !promotion.Eligibility(customerID) {
		return Money{}, ErrPromotionInvalid
	}
	if promotion.RedemptionLimit != nil && promotion.RedemptionCount >= *promotion.RedemptionLimit {
		return Money{}, ErrPromotionInvalid
	}
	if promotion.CustomerLimit > 0 && promotion.CustomerRedeemed >= promotion.CustomerLimit {
		return Money{}, ErrPromotionInvalid
	}
	var amount int64
	switch promotion.Kind {
	case "percent":
		if promotion.Value <= 0 || promotion.Value > 10000 {
			return Money{}, ErrPromotionInvalid
		}
		amount = subtotal.Amount * promotion.Value / 10000
	case "fixed":
		if strings.ToUpper(promotion.Currency) != subtotal.Currency {
			return Money{}, ErrCurrencyMismatch
		}
		amount = min(promotion.Value, subtotal.Amount)
	default:
		return Money{}, ErrPromotionInvalid
	}
	return Money{Amount: amount, Currency: subtotal.Currency}, nil
}

func NormalizePromoCode(code string) (string, error) {
	normalized := strings.ToUpper(strings.TrimSpace(code))
	if matched, _ := regexp.MatchString(`^[A-Z0-9_-]{3,64}$`, normalized); !matched {
		return "", errors.New("invalid promo code")
	}
	return normalized, nil
}

type OrderState string

const (
	OrderDraft             OrderState = "draft"
	OrderPending           OrderState = "pending"
	OrderPaid              OrderState = "paid"
	OrderFulfilled         OrderState = "fulfilled"
	OrderCancelled         OrderState = "cancelled"
	OrderExpired           OrderState = "expired"
	OrderPartiallyRefunded OrderState = "partially_refunded"
	OrderRefunded          OrderState = "refunded"
)

type Order struct {
	ID            string
	CustomerID    string
	State         OrderState
	Subtotal      Money
	DiscountMinor int64
	WalletMinor   int64
	ExternalMinor int64
	PaidMinor     int64
	RefundedMinor int64
}

func NewOrder(id, customerID string, subtotal Money, discountMinor, walletBalance int64) (Order, error) {
	if discountMinor < 0 || discountMinor > subtotal.Amount || walletBalance < 0 {
		return Order{}, ErrInvalidAmount
	}
	due := subtotal.Amount - discountMinor
	wallet := min(walletBalance, due)
	return Order{
		ID: id, CustomerID: customerID, State: OrderDraft, Subtotal: subtotal,
		DiscountMinor: discountMinor, WalletMinor: wallet, ExternalMinor: due - wallet,
	}, nil
}

func (order Order) Transition(next OrderState) (Order, error) {
	allowed := map[OrderState]map[OrderState]bool{
		OrderDraft:   {OrderPending: true, OrderPaid: true, OrderCancelled: true, OrderExpired: true},
		OrderPending: {OrderPaid: true, OrderCancelled: true, OrderExpired: true},
		// A closed order can still be paid: the provider's checkout outlives
		// the order, and money that arrives is money the customer sent. The
		// settlement is classified as late so nobody mistakes it for a normal
		// sale, and the product is delivered because it was paid for.
		OrderCancelled:         {OrderPaid: true},
		OrderExpired:           {OrderPaid: true},
		OrderPaid:              {OrderFulfilled: true, OrderPartiallyRefunded: true, OrderRefunded: true},
		OrderFulfilled:         {OrderPartiallyRefunded: true, OrderRefunded: true},
		OrderPartiallyRefunded: {OrderPartiallyRefunded: true, OrderRefunded: true},
	}
	if !allowed[order.State][next] {
		return Order{}, ErrInvalidTransition
	}
	order.State = next
	return order, nil
}

type PaymentResult struct {
	Amount Money
	Late   bool
}

// Payment classifications. Every settled provider payment is classified
// against its order, and the classification is what a support operator reads
// instead of inferring what happened from two status values.
const (
	ClassificationPaid         = "paid"
	ClassificationLate         = "late"
	ClassificationDuplicate    = "duplicate"
	ClassificationUnderpayment = "underpayment"
	ClassificationOverpayment  = "overpayment"
	ClassificationCurrency     = "currency_mismatch"
	ClassificationWallet       = "wallet_unavailable"
	// ClassificationPaidAfterCancellation is a matching payment that landed
	// on an order the customer or an operator had already cancelled. It
	// settles — the customer paid and gets what they paid for — but it is
	// named apart from an ordinary late payment because an operator should
	// look at it: the customer was told nothing would be charged.
	ClassificationPaidAfterCancellation = "paid_after_cancellation"
)

// NeedsOperator reports whether a classification is one an operator has to
// act on or at least know about: money arrived and the order did not simply
// settle as a sale.
func NeedsOperator(classification string) bool {
	switch classification {
	case ClassificationDuplicate, ClassificationUnderpayment, ClassificationOverpayment,
		ClassificationCurrency, ClassificationWallet, ClassificationPaidAfterCancellation:
		return true
	default:
		return false
	}
}

func (order Order) ApplyPayment(payment PaymentResult) (Order, string, error) {
	if payment.Amount.Currency != order.Subtotal.Currency {
		return order, ClassificationCurrency, ErrCurrencyMismatch
	}
	if order.State == OrderPaid || order.State == OrderFulfilled || order.State == OrderPartiallyRefunded || order.State == OrderRefunded {
		return order, ClassificationDuplicate, nil
	}
	if payment.Amount.Amount < order.ExternalMinor {
		return order, ClassificationUnderpayment, nil
	}
	order.PaidMinor = payment.Amount.Amount + order.WalletMinor
	if payment.Amount.Amount > order.ExternalMinor {
		return order, ClassificationOverpayment, nil
	}
	previous := order.State
	order.State = OrderPaid
	switch {
	case previous == OrderCancelled:
		return order, ClassificationPaidAfterCancellation, nil
	case previous == OrderExpired || payment.Late:
		// An expired order is late by definition, whatever the caller knew
		// about the payment window.
		return order, ClassificationLate, nil
	default:
		return order, ClassificationPaid, nil
	}
}

type LedgerEntry struct {
	AccountType string
	CustomerID  string
	Currency    string
	AmountMinor int64
}

func ValidateLedger(entries []LedgerEntry) error {
	if len(entries) < 2 {
		return ErrUnbalancedLedger
	}
	totals := make(map[string]int64)
	for _, entry := range entries {
		if entry.AmountMinor == 0 || !currencyPattern.MatchString(entry.Currency) {
			return ErrInvalidAmount
		}
		if entry.AccountType == "customer_wallet" && entry.CustomerID == "" || (entry.AccountType == "platform_clearing" || entry.AccountType == "provider_clearing") && entry.CustomerID != "" {
			return ErrUnbalancedLedger
		}
		totals[entry.Currency] += entry.AmountMinor
	}
	for _, total := range totals {
		if total != 0 {
			return ErrUnbalancedLedger
		}
	}
	return nil
}

func WalletBalance(entries []LedgerEntry, customerID, currency string) (int64, error) {
	var balance int64
	for _, entry := range entries {
		if entry.AccountType != "customer_wallet" || entry.CustomerID != customerID {
			continue
		}
		if entry.Currency != currency {
			continue
		}
		balance += entry.AmountMinor
	}
	if balance < 0 {
		return 0, errors.New("wallet invariant violated: negative balance")
	}
	return balance, nil
}
