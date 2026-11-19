package payments

// Statuses an adapter reports for a payment the provider reversed after it
// had settled. They are not payment-intent statuses — the intent of a reversed
// payment stays `succeeded`, because the money did arrive — but notification
// and poll statuses the payment service turns into a refund record, a ledger
// reversal, and the access or credit the payment bought being taken back.
const (
	// StatusRefunded is a refund the provider made on its own side: from its
	// dashboard, on the payer's request to the bank, or by an acquirer rule.
	StatusRefunded = "refunded"
	// StatusChargeback is a disputed payment the payer's bank pulled back.
	StatusChargeback = "chargeback"
)

// IsReversal reports whether a status reports money that went back to the
// payer after a successful payment.
func IsReversal(status string) bool {
	return status == StatusRefunded || status == StatusChargeback
}

// IntentStatus maps an adapter status onto the closed set a payment intent may
// hold. A reversal reported for a payment Omniflow never saw settle is, for
// that intent, a payment that will not settle.
func IntentStatus(status string) string {
	if IsReversal(status) {
		return "cancelled"
	}
	return status
}
