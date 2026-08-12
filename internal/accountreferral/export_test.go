package accountreferral

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

// The redaction rules are the reason this package can answer an export request
// synchronously at all. Each projector is tested against a row that carries the
// thing which must not come out, because a redaction expressed only as an
// omission in a SELECT list is one that a later `SELECT *` silently undoes.

func TestPaymentProjectionDropsTheProvidersHandles(t *testing.T) {
	row := paymentRow{
		OrderID:  "11111111-0000-4000-8000-000000000000",
		Provider: "yookassa", Status: "succeeded",
		AmountMinor: 50000, Currency: "RUB",
		ProviderReference: "2e7b1d0a-live-payment-reference",
		CheckoutURL:       "https://provider.example/pay/secret-bearer-token",
		CreatedAt:         time.Now(), UpdatedAt: time.Now(),
	}
	payment := projectPayment(row)

	encoded, err := json.Marshal(payment)
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	for _, secret := range []string{row.ProviderReference, row.CheckoutURL, "secret-bearer-token"} {
		if strings.Contains(string(encoded), secret) {
			t.Fatalf("the export carries a payment credential: %s", encoded)
		}
	}
	// What a customer actually needs from a payment record must survive.
	if payment.Provider != "yookassa" || payment.AmountMinor != 50000 || payment.Currency != "RUB" {
		t.Fatalf("the readable part of the payment was lost: %+v", payment)
	}
	if payment.OrderID != row.OrderID {
		t.Fatal("a payment must stay attached to the order it paid for")
	}
}

func TestWalletProjectionDropsOperatorProse(t *testing.T) {
	expires := time.Now().Add(24 * time.Hour)
	row := walletRow{
		Type: "correction", ReferenceType: "referral_reward",
		ReferenceID: "99999999-0000-4000-8000-000000000000",
		Reason:      "referral reward reversed: shares a device with customer 4c2f",
		Currency:    "RUB", AmountMinor: -50000,
		ExpiresAt: &expires, CreatedAt: time.Now(),
	}
	entry := projectWalletEntry(row)

	encoded, err := json.Marshal(entry)
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	for _, leak := range []string{row.Reason, row.ReferenceID, "4c2f"} {
		if strings.Contains(string(encoded), leak) {
			t.Fatalf("the export carries an operator's note: %s", encoded)
		}
	}
	// The movement itself is exactly what a customer checking a balance needs.
	if entry.Type != "correction" || entry.AmountMinor != -50000 || entry.Currency != "RUB" {
		t.Fatalf("the movement was lost: %+v", entry)
	}
	if entry.ExpiresAt == nil {
		t.Fatal("an expiring credit must say when it expires")
	}
}

func TestInvitedByProjectionNamesNoInviter(t *testing.T) {
	qualified := time.Now()
	row := attributionRow{
		ReferrerID:   "aaaaaaaa-0000-4000-8000-000000000000",
		Code:         "ZXCVBNMASD",
		AttributedAt: time.Now().Add(-72 * time.Hour),
		QualifiedAt:  &qualified,
	}
	invited := projectInvitedBy(row)

	encoded, err := json.Marshal(invited)
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	for _, leak := range []string{row.ReferrerID, row.Code} {
		if strings.Contains(string(encoded), leak) {
			t.Fatalf("the export identifies the inviter: %s", encoded)
		}
	}
	if !invited.Qualified || invited.QualifiedAt == nil {
		t.Fatal("the fact of the invite and its outcome must survive")
	}
	if invited.AttributedAt.IsZero() {
		t.Fatal("the invite must keep its date")
	}
}

// The document announces its own exclusions. A customer cannot ask about an
// absence they cannot see, so every section named in the export has to be
// accompanied by the standing list of what was left out of it.
func TestExportDeclaresItsSectionsAndItsExclusions(t *testing.T) {
	sections := ExportSections()
	if len(sections) == 0 {
		t.Fatal("an export with no sections is not an export")
	}
	seen := make(map[string]bool, len(sections))
	for _, section := range sections {
		if seen[section] {
			t.Fatalf("section %q is listed twice", section)
		}
		seen[section] = true
	}
	for _, required := range []string{
		"profile", "identities", "subscriptions", "orders", "payments",
		"wallet", "support", "referral", "loyalty", "consents",
	} {
		if !seen[required] {
			t.Fatalf("the export does not describe its %q section", required)
		}
	}

	redactions := ExportRedactions()
	declared := make(map[string]bool, len(redactions))
	for _, redaction := range redactions {
		declared[redaction] = true
	}
	for _, required := range []string{
		"other_customer_identifiers", "payment_credentials",
		"provider_secrets", "subscription_links", "device_and_network_identifiers",
	} {
		if !declared[required] {
			t.Fatalf("the export does not declare that it withholds %q", required)
		}
	}
}
