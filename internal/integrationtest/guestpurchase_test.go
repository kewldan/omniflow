//go:build integration

package integrationtest

import (
	"context"
	"errors"
	"log/slog"
	"regexp"
	"testing"

	"github.com/omniflow/omniflow/internal/guestpurchase"
	"github.com/omniflow/omniflow/internal/paymentservice"
)

// A visitor pays for a gift with only an e-mail address; once the order is
// paid the payer and the recipient are mailed the code exactly once, the
// stored code is released, and a signed-in customer redeems it.
func TestGuestGiftPurchaseIsMailedOnceAndRedeemable(t *testing.T) {
	ctx := context.Background()
	harness := newHarness(t)
	store := harness.commerceStore(t, testOptions())
	provider := &hostedStubProvider{name: "yookassa", currency: "RUB"}
	payments := paymentservice.New(harness.pool, store, provider)
	sender := &recordingSender{}
	queue := newEmailQueue(t, harness, sender, false)
	guests, err := guestpurchase.New(harness.pool, store, payments, queue, guestpurchase.Options{
		DataKey: operationsKey, PublicURL: "https://vpn.example", Logger: slog.New(slog.DiscardHandler),
	})
	if err != nil {
		t.Fatal(err)
	}
	plan := harness.catalog(ctx, t, "guest-month", 29900)

	for name, input := range map[string]guestpurchase.Input{
		"disposable payer":       {PlanVersionID: plan, Email: "a@mailinator.com", Provider: "yookassa"},
		"recipient without gift": {PlanVersionID: plan, Email: "a@example.com", RecipientEmail: "b@example.com", Provider: "yookassa"},
		"stars for a guest":      {PlanVersionID: plan, Email: "a@example.com", Provider: "telegram_stars"},
	} {
		if _, err := guests.Start(ctx, "refused-"+name, input); err == nil {
			t.Fatalf("%s was accepted", name)
		}
	}
	input := guestpurchase.Input{PlanVersionID: plan, Email: "Payer@Example.com", Gift: true,
		RecipientEmail: "friend@example.com", Message: "Happy birthday", Provider: "yookassa", Locale: "en"}
	started, err := guests.Start(ctx, "guest-key-0001", input)
	if err != nil || started.Token == "" || started.CheckoutURL == "" || started.AmountMinor != 29900 {
		t.Fatalf("started = %+v (%v)", started, err)
	}
	replay, err := guests.Start(ctx, "guest-key-0001", input)
	if err != nil || replay.ID != started.ID || replay.Token != "" || provider.created != 1 {
		t.Fatalf("replay = %+v (%v), provider calls %d", replay, err, provider.created)
	}
	if _, err = guests.Status(ctx, started.ID, "00"); !errors.Is(err, guestpurchase.ErrNotFound) {
		t.Fatalf("a wrong token: %v", err)
	}

	// Unpaid: nothing is mailed.
	if err = guests.Pass(ctx); err != nil {
		t.Fatal(err)
	}
	if err = queue.Pass(ctx); err != nil || len(sender.messages()) != 0 {
		t.Fatalf("mail before payment: %d (%v)", len(sender.messages()), err)
	}
	var orderID string
	if err = harness.pool.QueryRow(ctx, `SELECT order_id::text FROM guest_purchases WHERE id = $1::uuid`, started.ID).Scan(&orderID); err != nil {
		t.Fatal(err)
	}
	harness.settle(ctx, t, store, orderID, 29900, "guest-paid")
	status, err := guests.Status(ctx, started.ID, started.Token)
	if err != nil || status.Status != "paid" {
		t.Fatalf("status = %+v (%v)", status, err)
	}
	for range 2 {
		if err = guests.Pass(ctx); err != nil {
			t.Fatal(err)
		}
	}
	if err = queue.Pass(ctx); err != nil {
		t.Fatal(err)
	}
	sent := sender.messages()
	if len(sent) != 2 {
		t.Fatalf("mails = %d, want the payer and the recipient once each", len(sent))
	}
	recipients := map[string]string{}
	for _, message := range sent {
		recipients[message.To] = message.Text
	}
	if _, ok := recipients["payer@example.com"]; !ok {
		t.Fatalf("the payer was not mailed: %v", recipients)
	}
	code := regexp.MustCompile(`code=([A-Z0-9-]+)`).FindStringSubmatch(recipients["friend@example.com"])
	if len(code) != 2 {
		t.Fatalf("the recipient's mail has no activation link: %s", recipients["friend@example.com"])
	}
	var released bool
	if err = harness.pool.QueryRow(ctx, `SELECT code_ciphertext IS NULL FROM guest_purchases WHERE id = $1::uuid`, started.ID).Scan(&released); err != nil || !released {
		t.Fatalf("the code is still stored (%v)", err)
	}
	customer := harness.customer(ctx, t)
	if _, err = store.ClaimGift(ctx, code[1], customer); err != nil {
		t.Fatalf("redeem the mailed code: %v", err)
	}
}
