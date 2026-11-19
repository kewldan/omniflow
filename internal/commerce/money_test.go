package commerce

import (
	"errors"
	"math"
	"testing"
	"time"
)

// Money is minor units and an ISO code; it carries no notion of how many
// decimals a currency has. These tests pin that a zero-decimal currency (JPY)
// and a three-decimal one (KWD) pass through untouched, so nothing upstream can
// silently rescale an amount.
func TestNewMoneyNormalizesTheCurrencyAndRefusesInvalidAmounts(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name     string
		amount   int64
		currency string
		want     Money
		err      error
	}{
		{"rub", 1000, "RUB", Money{1000, "RUB"}, nil},
		{"lower case and padded", 1000, " rub ", Money{1000, "RUB"}, nil},
		{"zero is a valid amount", 0, "USD", Money{0, "USD"}, nil},
		{"zero-decimal currency", 500, "JPY", Money{500, "JPY"}, nil},
		{"three-decimal currency", 1250, "KWD", Money{1250, "KWD"}, nil},
		{"largest amount", math.MaxInt64, "USD", Money{math.MaxInt64, "USD"}, nil},
		{"negative", -1, "RUB", Money{}, ErrInvalidAmount},
		{"two letters", 1, "RU", Money{}, ErrInvalidAmount},
		{"four letters", 1, "RUBL", Money{}, ErrInvalidAmount},
		{"digit", 1, "R1B", Money{}, ErrInvalidAmount},
		{"empty", 1, "", Money{}, ErrInvalidAmount},
		{"non-ASCII", 1, "РУБ", Money{}, ErrInvalidAmount},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			got, err := NewMoney(testCase.amount, testCase.currency)
			if !errors.Is(err, testCase.err) {
				t.Fatalf("NewMoney(%d, %q) error = %v, want %v", testCase.amount, testCase.currency, err, testCase.err)
			}
			if got != testCase.want {
				t.Fatalf("NewMoney(%d, %q) = %+v, want %+v", testCase.amount, testCase.currency, got, testCase.want)
			}
		})
	}
}

func TestMoneyAddRefusesMixedCurrenciesAndOverflow(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		left Money
		add  Money
		want Money
		err  error
	}{
		{"same currency", Money{1000, "RUB"}, Money{250, "RUB"}, Money{1250, "RUB"}, nil},
		{"adding zero", Money{1000, "RUB"}, Money{0, "RUB"}, Money{1000, "RUB"}, nil},
		{"up to the limit", Money{math.MaxInt64 - 1, "RUB"}, Money{1, "RUB"}, Money{math.MaxInt64, "RUB"}, nil},
		{"one past the limit", Money{math.MaxInt64, "RUB"}, Money{1, "RUB"}, Money{}, ErrInvalidAmount},
		{"far past the limit", Money{math.MaxInt64 / 2, "RUB"}, Money{math.MaxInt64, "RUB"}, Money{}, ErrInvalidAmount},
		{"mixed currencies", Money{1000, "RUB"}, Money{1000, "USD"}, Money{}, ErrCurrencyMismatch},
		// Currency codes are compared exactly: a Money built without NewMoney
		// is not quietly normalized into a match.
		{"case differs", Money{1000, "RUB"}, Money{1000, "rub"}, Money{}, ErrCurrencyMismatch},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			got, err := testCase.left.Add(testCase.add)
			if !errors.Is(err, testCase.err) {
				t.Fatalf("Add error = %v, want %v", err, testCase.err)
			}
			if got != testCase.want {
				t.Fatalf("Add = %+v, want %+v", got, testCase.want)
			}
		})
	}
}

func TestPlanVersionPriceIsLookedUpByCurrency(t *testing.T) {
	t.Parallel()
	version := PlanVersion{Prices: map[string]int64{"RUB": 25000, "USD": 299, "JPY": 450, "BAD": -1}}
	cases := []struct {
		currency string
		want     Money
		err      error
	}{
		{"RUB", Money{25000, "RUB"}, nil},
		{"usd", Money{299, "USD"}, nil},
		{"JPY", Money{450, "JPY"}, nil},
		// A currency the plan is not sold in is a refusal, never a zero price.
		{"EUR", Money{}, ErrCurrencyMismatch},
		{"", Money{}, ErrCurrencyMismatch},
		// A corrupt negative price never becomes a payable amount.
		{"BAD", Money{}, ErrInvalidAmount},
	}
	for _, testCase := range cases {
		got, err := version.Price(testCase.currency)
		if !errors.Is(err, testCase.err) {
			t.Fatalf("Price(%q) error = %v, want %v", testCase.currency, err, testCase.err)
		}
		if got != testCase.want {
			t.Fatalf("Price(%q) = %+v, want %+v", testCase.currency, got, testCase.want)
		}
	}
}

func TestNormalizePromoCode(t *testing.T) {
	t.Parallel()
	cases := []struct {
		input string
		want  string
		ok    bool
	}{
		{"spring25", "SPRING25", true},
		{"  black-friday_2026 ", "BLACK-FRIDAY_2026", true},
		{"abc", "ABC", true},
		{"ab", "", false},
		{"", "", false},
		{"spring 25", "", false},
		{"скидка", "", false},
		{"promo!", "", false},
		{string(make([]byte, 65)), "", false},
	}
	long := ""
	for range 64 {
		long += "A"
	}
	cases = append(cases, struct {
		input string
		want  string
		ok    bool
	}{long, long, true}, struct {
		input string
		want  string
		ok    bool
	}{long + "A", "", false})

	for _, testCase := range cases {
		got, err := NormalizePromoCode(testCase.input)
		if (err == nil) != testCase.ok {
			t.Fatalf("NormalizePromoCode(%q) error = %v, want ok=%v", testCase.input, err, testCase.ok)
		}
		if got != testCase.want {
			t.Fatalf("NormalizePromoCode(%q) = %q, want %q", testCase.input, got, testCase.want)
		}
	}
}

var allOrderStates = []OrderState{
	OrderDraft, OrderPending, OrderPaid, OrderFulfilled,
	OrderCancelled, OrderExpired, OrderPartiallyRefunded, OrderRefunded,
}

// TestOrderTransitionMatrix pins every edge of the order state machine. A new
// edge must be added here on purpose; an accidental one fails.
func TestOrderTransitionMatrix(t *testing.T) {
	t.Parallel()
	allowed := map[OrderState][]OrderState{
		OrderDraft:             {OrderPending, OrderPaid, OrderCancelled, OrderExpired},
		OrderPending:           {OrderPaid, OrderCancelled, OrderExpired},
		OrderCancelled:         {OrderPaid},
		OrderExpired:           {OrderPaid},
		OrderPaid:              {OrderFulfilled, OrderPartiallyRefunded, OrderRefunded},
		OrderFulfilled:         {OrderPartiallyRefunded, OrderRefunded},
		OrderPartiallyRefunded: {OrderPartiallyRefunded, OrderRefunded},
		OrderRefunded:          {},
	}
	for _, from := range allOrderStates {
		permitted := make(map[OrderState]bool)
		for _, to := range allowed[from] {
			permitted[to] = true
		}
		for _, to := range append(allOrderStates, OrderState("unknown"), OrderState("")) {
			order := Order{ID: "order", State: from, PaidMinor: 700}
			next, err := order.Transition(to)
			if permitted[to] {
				if err != nil || next.State != to || next.PaidMinor != 700 {
					t.Errorf("%s -> %s: got %+v, %v; want the transition", from, to, next, err)
				}
				continue
			}
			if !errors.Is(err, ErrInvalidTransition) {
				t.Errorf("%s -> %s: error = %v, want ErrInvalidTransition", from, to, err)
			}
			if next.State != "" {
				t.Errorf("%s -> %s: a refused transition returned state %q", from, to, next.State)
			}
		}
	}
	// A paid order can never go back to being unpaid; that would reopen it for
	// a second charge.
	for _, settled := range []OrderState{OrderPaid, OrderFulfilled, OrderPartiallyRefunded, OrderRefunded} {
		for _, unpaid := range []OrderState{OrderDraft, OrderPending, OrderCancelled, OrderExpired} {
			if _, err := (Order{State: settled}).Transition(unpaid); err == nil {
				t.Errorf("%s -> %s was allowed", settled, unpaid)
			}
		}
	}
}

// TestApplyPaymentFromEveryState is the settlement decision for an exact,
// matching payment arriving while the order sits in each state.
func TestApplyPaymentFromEveryState(t *testing.T) {
	t.Parallel()
	price := Money{1000, "RUB"}
	cases := []struct {
		state          OrderState
		late           bool
		classification string
		wantState      OrderState
	}{
		{OrderDraft, false, ClassificationPaid, OrderPaid},
		{OrderPending, false, ClassificationPaid, OrderPaid},
		{OrderPending, true, ClassificationLate, OrderPaid},
		{OrderExpired, false, ClassificationLate, OrderPaid},
		{OrderCancelled, false, ClassificationPaidAfterCancellation, OrderPaid},
		// Cancellation wins over lateness: the operator must see the customer
		// was told nothing would be charged.
		{OrderCancelled, true, ClassificationPaidAfterCancellation, OrderPaid},
		{OrderPaid, false, ClassificationDuplicate, OrderPaid},
		{OrderFulfilled, false, ClassificationDuplicate, OrderFulfilled},
		{OrderPartiallyRefunded, false, ClassificationDuplicate, OrderPartiallyRefunded},
		{OrderRefunded, false, ClassificationDuplicate, OrderRefunded},
	}
	for _, testCase := range cases {
		order, err := NewOrder("order", "customer", price, 0, 0)
		if err != nil {
			t.Fatalf("NewOrder: %v", err)
		}
		order.State = testCase.state
		got, classification, err := order.ApplyPayment(PaymentResult{Amount: price, Late: testCase.late})
		if err != nil {
			t.Fatalf("%s (late=%v): %v", testCase.state, testCase.late, err)
		}
		if classification != testCase.classification || got.State != testCase.wantState {
			t.Fatalf("%s (late=%v): got %q/%s, want %q/%s", testCase.state, testCase.late,
				classification, got.State, testCase.classification, testCase.wantState)
		}
		if classification == ClassificationDuplicate && got.PaidMinor != order.PaidMinor {
			t.Fatalf("%s: a duplicate changed PaidMinor to %d", testCase.state, got.PaidMinor)
		}
		if BlocksSettlement(classification) != (classification == ClassificationDuplicate) {
			t.Fatalf("%s: BlocksSettlement(%q) is wrong for an exact payment", testCase.state, classification)
		}
	}
}

func TestApplyPaymentCountsTheWalletShareOnce(t *testing.T) {
	t.Parallel()
	subtotal := Money{1000, "RUB"}
	// The wallet covers 300 of the 1000; the provider is owed 700.
	order, err := NewOrder("order", "customer", subtotal, 0, 300)
	if err != nil {
		t.Fatalf("NewOrder: %v", err)
	}
	order.State = OrderPending
	paid, classification, err := order.ApplyPayment(PaymentResult{Amount: Money{700, "RUB"}})
	if err != nil || classification != ClassificationPaid {
		t.Fatalf("ApplyPayment = %q, %v", classification, err)
	}
	if paid.PaidMinor != 1000 {
		t.Fatalf("PaidMinor = %d, want the full 1000 (700 external + 300 wallet)", paid.PaidMinor)
	}

	// An order the wallet covers entirely settles on a zero provider amount.
	covered, err := NewOrder("covered", "customer", subtotal, 0, 5000)
	if err != nil {
		t.Fatalf("NewOrder: %v", err)
	}
	if covered.WalletMinor != 1000 || covered.ExternalMinor != 0 {
		t.Fatalf("wallet/external = %d/%d, want 1000/0", covered.WalletMinor, covered.ExternalMinor)
	}
	covered.State = OrderPending
	settled, classification, err := covered.ApplyPayment(PaymentResult{Amount: Money{0, "RUB"}})
	if err != nil || classification != ClassificationPaid || settled.PaidMinor != 1000 {
		t.Fatalf("wallet-only settlement = %+v %q %v", settled, classification, err)
	}
	// Any provider money on top of a wallet-covered order is an overpayment.
	_, classification, _ = covered.ApplyPayment(PaymentResult{Amount: Money{1, "RUB"}})
	if classification != ClassificationOverpayment {
		t.Fatalf("1 minor unit on a wallet-covered order = %q, want overpayment", classification)
	}
}

func TestNewOrderSplitsAndRefusesImpossibleAmounts(t *testing.T) {
	t.Parallel()
	subtotal := Money{1000, "RUB"}
	cases := []struct {
		name             string
		discount, wallet int64
		wantWallet       int64
		wantExternal     int64
		err              error
	}{
		{"nothing applied", 0, 0, 0, 1000, nil},
		{"full discount", 1000, 500, 0, 0, nil},
		{"discount and wallet", 400, 100, 100, 500, nil},
		{"wallet larger than due", 400, 9999, 600, 0, nil},
		{"discount beyond subtotal", 1001, 0, 0, 0, ErrInvalidAmount},
		{"negative discount", -1, 0, 0, 0, ErrInvalidAmount},
		{"negative wallet", 0, -1, 0, 0, ErrInvalidAmount},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			order, err := NewOrder("order", "customer", subtotal, testCase.discount, testCase.wallet)
			if !errors.Is(err, testCase.err) {
				t.Fatalf("error = %v, want %v", err, testCase.err)
			}
			if err != nil {
				return
			}
			if order.State != OrderDraft {
				t.Fatalf("new order state = %s, want draft", order.State)
			}
			if order.WalletMinor != testCase.wantWallet || order.ExternalMinor != testCase.wantExternal {
				t.Fatalf("wallet/external = %d/%d, want %d/%d", order.WalletMinor, order.ExternalMinor,
					testCase.wantWallet, testCase.wantExternal)
			}
			if order.DiscountMinor+order.WalletMinor+order.ExternalMinor != subtotal.Amount {
				t.Fatal("discount + wallet + external does not add up to the subtotal")
			}
		})
	}
}

func TestPromotionDiscountRules(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 11, 19, 12, 0, 0, 0, time.UTC)
	before, after := now.Add(-time.Hour), now.Add(time.Hour)
	limitOne := 1
	rub := Money{1000, "RUB"}
	odd := Money{333, "RUB"}
	cases := []struct {
		name      string
		promotion Promotion
		planID    string
		subtotal  Money
		want      int64
		err       error
	}{
		{"percent floors to the minor unit", Promotion{Kind: "percent", Value: 3333}, "plan", odd, 110, nil},
		{"one basis point of a small price is zero", Promotion{Kind: "percent", Value: 1}, "plan", odd, 0, nil},
		{"hundred percent is the whole price", Promotion{Kind: "percent", Value: 10000}, "plan", rub, 1000, nil},
		{"over a hundred percent refused", Promotion{Kind: "percent", Value: 10001}, "plan", rub, 0, ErrPromotionInvalid},
		{"zero percent refused", Promotion{Kind: "percent", Value: 0}, "plan", rub, 0, ErrPromotionInvalid},
		{"negative percent refused", Promotion{Kind: "percent", Value: -100}, "plan", rub, 0, ErrPromotionInvalid},
		{"fixed below price", Promotion{Kind: "fixed", Value: 300, Currency: "RUB"}, "plan", rub, 300, nil},
		{"fixed is capped at the price", Promotion{Kind: "fixed", Value: 5000, Currency: "RUB"}, "plan", rub, 1000, nil},
		{"fixed currency compared case-insensitively", Promotion{Kind: "fixed", Value: 300, Currency: "rub"}, "plan", rub, 300, nil},
		{"fixed in another currency refused", Promotion{Kind: "fixed", Value: 300, Currency: "USD"}, "plan", rub, 0, ErrCurrencyMismatch},
		{"unknown kind refused", Promotion{Kind: "bogo", Value: 1}, "plan", rub, 0, ErrPromotionInvalid},
		{"starts exactly now", Promotion{Kind: "percent", Value: 1000, StartsAt: &now}, "plan", rub, 100, nil},
		{"not started", Promotion{Kind: "percent", Value: 1000, StartsAt: &after}, "plan", rub, 0, ErrPromotionInvalid},
		{"ends exactly now", Promotion{Kind: "percent", Value: 1000, EndsAt: &now}, "plan", rub, 0, ErrPromotionInvalid},
		{"ended", Promotion{Kind: "percent", Value: 1000, EndsAt: &before}, "plan", rub, 0, ErrPromotionInvalid},
		{"inside the window", Promotion{Kind: "percent", Value: 1000, StartsAt: &before, EndsAt: &after}, "plan", rub, 100, nil},
		{"total limit reached", Promotion{Kind: "percent", Value: 1000, RedemptionLimit: &limitOne, RedemptionCount: 1}, "plan", rub, 0, ErrPromotionInvalid},
		{"total limit not reached", Promotion{Kind: "percent", Value: 1000, RedemptionLimit: &limitOne}, "plan", rub, 100, nil},
		{"customer limit reached", Promotion{Kind: "percent", Value: 1000, CustomerLimit: 2, CustomerRedeemed: 2}, "plan", rub, 0, ErrPromotionInvalid},
		{"no customer limit", Promotion{Kind: "percent", Value: 1000, CustomerRedeemed: 50}, "plan", rub, 100, nil},
		{"ineligible customer", Promotion{Kind: "percent", Value: 1000, Eligibility: func(string) bool { return false }}, "plan", rub, 0, ErrPromotionInvalid},
		{"goods promotion on a plan", Promotion{Kind: "percent", Value: 1000, AppliesTo: PromotionAppliesToGoods}, "plan", rub, 0, ErrPromotionInvalid},
		{"goods promotion through the goods path", Promotion{Kind: "percent", Value: 1000, AppliesTo: PromotionAppliesToGoods}, "", rub, 100, nil},
		{"zero-decimal currency", Promotion{Kind: "percent", Value: 1500}, "plan", Money{999, "JPY"}, 149, nil},
		{"three-decimal currency", Promotion{Kind: "fixed", Value: 500, Currency: "KWD"}, "plan", Money{2750, "KWD"}, 500, nil},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			got, err := testCase.promotion.Discount(now, "customer", testCase.planID, testCase.subtotal)
			if !errors.Is(err, testCase.err) {
				t.Fatalf("Discount error = %v, want %v", err, testCase.err)
			}
			if err != nil {
				return
			}
			if got.Amount != testCase.want || got.Currency != testCase.subtotal.Currency {
				t.Fatalf("Discount = %+v, want %d %s", got, testCase.want, testCase.subtotal.Currency)
			}
			// Whatever the rule, a discount never exceeds what it discounts, so
			// the order it feeds can always be built.
			if _, orderErr := NewOrder("order", "customer", testCase.subtotal, got.Amount, 0); orderErr != nil {
				t.Fatalf("the discount cannot be applied to an order: %v", orderErr)
			}
		})
	}
}

func TestValidateLedgerRefusesMalformedTransactions(t *testing.T) {
	t.Parallel()
	wallet := func(amount int64, currency string) LedgerEntry {
		return LedgerEntry{AccountType: "customer_wallet", CustomerID: "customer", Currency: currency, AmountMinor: amount}
	}
	clearing := func(amount int64, currency string) LedgerEntry {
		return LedgerEntry{AccountType: "provider_clearing", Currency: currency, AmountMinor: amount}
	}
	cases := []struct {
		name    string
		entries []LedgerEntry
		err     error
	}{
		{"balanced pair", []LedgerEntry{wallet(500, "RUB"), clearing(-500, "RUB")}, nil},
		{"balanced in two currencies", []LedgerEntry{
			wallet(500, "RUB"), clearing(-500, "RUB"), wallet(7, "USD"), clearing(-7, "USD"),
		}, nil},
		{"three legs", []LedgerEntry{
			wallet(300, "RUB"), wallet(200, "RUB"), clearing(-500, "RUB"),
		}, nil},
		{"empty", nil, ErrUnbalancedLedger},
		{"one leg", []LedgerEntry{wallet(500, "RUB")}, ErrUnbalancedLedger},
		{"off by one", []LedgerEntry{wallet(500, "RUB"), clearing(-499, "RUB")}, ErrUnbalancedLedger},
		{"balanced only across currencies", []LedgerEntry{wallet(500, "RUB"), clearing(-500, "USD")}, ErrUnbalancedLedger},
		{"zero leg", []LedgerEntry{wallet(0, "RUB"), clearing(0, "RUB")}, ErrInvalidAmount},
		{"bad currency", []LedgerEntry{wallet(500, "rub"), clearing(-500, "rub")}, ErrInvalidAmount},
		{"wallet without a customer", []LedgerEntry{
			{AccountType: "customer_wallet", Currency: "RUB", AmountMinor: 500}, clearing(-500, "RUB"),
		}, ErrUnbalancedLedger},
		{"clearing that names a customer", []LedgerEntry{
			wallet(500, "RUB"),
			{AccountType: "platform_clearing", CustomerID: "customer", Currency: "RUB", AmountMinor: -500},
		}, ErrUnbalancedLedger},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			if err := ValidateLedger(testCase.entries); !errors.Is(err, testCase.err) {
				t.Fatalf("ValidateLedger error = %v, want %v", err, testCase.err)
			}
		})
	}
}

func TestWalletBalanceIgnoresOtherCustomersAndAccounts(t *testing.T) {
	t.Parallel()
	entries := []LedgerEntry{
		{AccountType: "customer_wallet", CustomerID: "customer", Currency: "RUB", AmountMinor: 500},
		{AccountType: "customer_wallet", CustomerID: "other", Currency: "RUB", AmountMinor: -9999},
		{AccountType: "provider_clearing", Currency: "RUB", AmountMinor: -500},
		{AccountType: "customer_wallet", CustomerID: "customer", Currency: "RUB", AmountMinor: -500},
	}
	balance, err := WalletBalance(entries, "customer", "RUB")
	if err != nil || balance != 0 {
		t.Fatalf("WalletBalance = %d, %v; want exactly zero, which is not an overspend", balance, err)
	}
	if balance, err = WalletBalance(nil, "customer", "RUB"); err != nil || balance != 0 {
		t.Fatalf("empty ledger = %d, %v", balance, err)
	}
}

func TestAddonProrationDoesNotOverflowOnLargePricesAndLongPeriods(t *testing.T) {
	t.Parallel()
	start := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	end := start.Add(10 * 365 * 24 * time.Hour)
	now := start.Add(5 * 365 * 24 * time.Hour)
	// unit × seconds-remaining overflows int64 in the naive formula.
	const unit = int64(1) << 50
	charge, err := PriceAddon(unit, 1, 0, ProrationRemainingPeriod, now, start, end)
	if err != nil {
		t.Fatalf("PriceAddon: %v", err)
	}
	if charge.ChargedMinor != unit/2 {
		t.Fatalf("half a period of %d = %d, want %d", unit, charge.ChargedMinor, unit/2)
	}

	if _, err = PriceAddon(math.MaxInt64/2+1, 2, 0, ProrationFullPrice, now, start, end); !errors.Is(err, ErrInvalidAmount) {
		t.Fatalf("an overflowing line total = %v, want ErrInvalidAmount", err)
	}
	if _, err = PriceAddon(-1, 1, 0, ProrationFullPrice, now, start, end); !errors.Is(err, ErrInvalidAmount) {
		t.Fatalf("a negative unit price = %v, want ErrInvalidAmount", err)
	}
	free, err := PriceAddon(0, 3, 0, ProrationDailyRate, now, start, end)
	if err != nil || free.ChargedMinor != 0 {
		t.Fatalf("a free add-on = %+v, %v", free, err)
	}
}

func TestRoundingHelpersRoundUpAndStayBounded(t *testing.T) {
	t.Parallel()
	divisions := []struct{ numerator, denominator, want int64 }{
		{10, 3, 4}, {9, 3, 3}, {1, 1000, 1}, {0, 5, 0}, {-5, 3, 0}, {5, 0, 0}, {5, -1, 0},
	}
	for _, division := range divisions {
		if got := divideRoundingUp(division.numerator, division.denominator); got != division.want {
			t.Errorf("divideRoundingUp(%d, %d) = %d, want %d", division.numerator, division.denominator, got, division.want)
		}
	}
	scales := []struct{ value, numerator, denominator, want int64 }{
		{1000, 1, 3, 334},
		{1000, 2, 3, 667},
		{1, 1, 1_000_000, 1},
		{1000, 3, 3, 1000},
		{1000, 4, 3, 1000},
		{math.MaxInt64, math.MaxInt64 - 1, math.MaxInt64, math.MaxInt64 - 1},
		{0, 1, 2, 0},
		{1000, 0, 2, 0},
		{1000, 1, 0, 0},
	}
	for _, scale := range scales {
		if got := scaleRoundingUp(scale.value, scale.numerator, scale.denominator); got != scale.want {
			t.Errorf("scaleRoundingUp(%d, %d, %d) = %d, want %d",
				scale.value, scale.numerator, scale.denominator, got, scale.want)
		}
	}
}
