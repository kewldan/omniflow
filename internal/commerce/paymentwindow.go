package commerce

import "time"

// DefaultPaymentWindow is how long a pending order waits for a provider
// payment before the expiry sweep closes it. An hour is right for a customer
// sitting at a checkout: a hosted page or an invoice link is either paid in
// minutes or abandoned, and an order left open longer only holds a wallet
// reservation against a purchase that is not coming.
const DefaultPaymentWindow = time.Hour

// ManualPaymentWindow is the payment window for a manual transfer.
//
// A manual payment is a bank or card transfer the customer makes on their
// own and an operator confirms by hand. Neither happens in an hour: the
// transfer lands when the bank processes it, and the operator approves it when
// they next look. Three days covers a weekend, which is the gap that most often
// separates the transfer from the approval. An operator approval after even
// this window still settles the order, as a late payment.
const ManualPaymentWindow = 72 * time.Hour

// ManualProvider is the name of the operator-confirmed transfer adapter.
const ManualProvider = "manual"

// PaymentWindow is how long an order paid through the named provider stays
// payable. An empty or unknown provider gets the default; only a manual
// transfer is given longer.
func PaymentWindow(provider string) time.Duration {
	if provider == ManualProvider {
		return ManualPaymentWindow
	}
	return DefaultPaymentWindow
}
