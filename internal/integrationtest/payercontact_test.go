//go:build integration

package integrationtest

import (
	"context"
	"testing"
	"time"

	"github.com/omniflow/omniflow/internal/commercepg"
	"github.com/omniflow/omniflow/internal/customerpg"
	"github.com/omniflow/omniflow/internal/payments"
	"github.com/omniflow/omniflow/internal/paymentservice"
)

// contactProvider records the payer metadata it was given.
type contactProvider struct {
	hostedStubProvider
	wantsContact bool
	seen         []map[string]string
}

func (provider *contactProvider) UsesCustomerContact() bool { return provider.wantsContact }

func (provider *contactProvider) Create(ctx context.Context, request payments.CreateRequest) (payments.Intent, error) {
	provider.seen = append(provider.seen, request.Metadata)
	return provider.hostedStubProvider.Create(ctx, request)
}

// The verified e-mail reaches only an adapter that declares it needs one, an
// unverified address never leaves the database, and an installation without
// the contact key sends none.
func TestPayerEmailReachesOnlyAdaptersThatNeedIt(t *testing.T) {
	ctx := context.Background()
	harness := newHarness(t)
	store := harness.commerceStore(t, testOptions())
	key := make([]byte, 32)
	for index := range key {
		key[index] = byte(index + 1)
	}
	contacts, err := customerpg.New(harness.pool, key, 0)
	if err != nil {
		t.Fatalf("contacts: %v", err)
	}
	needs := &contactProvider{hostedStubProvider: hostedStubProvider{name: "antilopay", currency: "RUB"}, wantsContact: true}
	plain := &contactProvider{hostedStubProvider: hostedStubProvider{name: "freekassa", currency: "RUB"}}
	service := paymentservice.New(harness.pool, store, needs, plain).WithContacts(contacts)

	verified := harness.customer(ctx, t)
	now := time.Now()
	if _, err = contacts.SetContact(ctx, verified, "email", "payer@example.com", &now, true, false); err != nil {
		t.Fatalf("set contact: %v", err)
	}
	unverified := harness.customer(ctx, t)
	if _, err = contacts.SetContact(ctx, unverified, "email", "stranger@example.com", nil, true, false); err != nil {
		t.Fatalf("set contact: %v", err)
	}

	pay := func(customerID, provider, key string) {
		t.Helper()
		order, err := store.CreateTopUpOrder(ctx, commercepg.TopUpInput{CustomerID: customerID, Currency: "RUB", AmountMinor: 10000, IdempotencyKey: key})
		if err != nil {
			t.Fatalf("order: %v", err)
		}
		if _, err = service.CreateIntent(ctx, paymentservice.CreateIntentInput{OrderID: uuidText(order.ID), Provider: provider, IdempotencyKey: key + "-intent"}); err != nil {
			t.Fatalf("intent: %v", err)
		}
	}
	pay(verified, "antilopay", "contact-verified")
	pay(verified, "freekassa", "contact-plain")
	pay(unverified, "antilopay", "contact-unverified")

	if got := needs.seen[0]["customer_email"]; got != "payer@example.com" {
		t.Fatalf("an adapter that needs the e-mail got %q", got)
	}
	if _, leaked := plain.seen[0]["customer_email"]; leaked {
		t.Fatal("the e-mail reached an adapter that has no use for it")
	}
	if _, leaked := needs.seen[1]["customer_email"]; leaked {
		t.Fatal("an unverified address was handed to a provider")
	}

	bare := paymentservice.New(harness.pool, store, needs)
	order, err := store.CreateTopUpOrder(ctx, commercepg.TopUpInput{CustomerID: verified, Currency: "RUB", AmountMinor: 10000, IdempotencyKey: "contact-nokey"})
	if err != nil {
		t.Fatalf("order: %v", err)
	}
	if _, err = bare.CreateIntent(ctx, paymentservice.CreateIntentInput{OrderID: uuidText(order.ID), Provider: "antilopay", IdempotencyKey: "contact-nokey-intent"}); err != nil {
		t.Fatalf("intent: %v", err)
	}
	if _, leaked := needs.seen[2]["customer_email"]; leaked {
		t.Fatal("an installation without the contact key opened an address")
	}
}
