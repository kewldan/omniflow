//go:build integration

package integrationtest

import (
	"context"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/omniflow/omniflow/internal/customerauthpg"
	"github.com/omniflow/omniflow/internal/customerpg"
	"github.com/omniflow/omniflow/internal/mailer"
)

type capturingMailer struct{ sent []mailer.Message }

func (capture *capturingMailer) Send(_ context.Context, message mailer.Message) error {
	capture.sent = append(capture.sent, message)
	return nil
}

func (capture *capturingMailer) lastToken(t *testing.T) string {
	t.Helper()
	if len(capture.sent) == 0 {
		t.Fatal("no e-mail was sent")
	}
	text := capture.sent[len(capture.sent)-1].Text
	start := strings.Index(text, "https://")
	end := strings.IndexAny(text[start:], "\n ")
	link, err := url.Parse(text[start : start+end])
	if err != nil {
		t.Fatalf("parse link: %v", err)
	}
	return link.Query().Get("token")
}

// E-mail sign-in: an address that belongs to a customer signs that customer
// in; an unknown address creates exactly one customer, and only when the link
// is redeemed; a link works once.
func TestEmailSignInFindsOrCreatesTheCustomer(t *testing.T) {
	ctx := context.Background()
	harness := newHarness(t)
	contacts, err := customerpg.New(harness.pool, adminTestKey, 30*24*time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	mail := &capturingMailer{}
	service, err := customerauthpg.New(harness.pool, adminTestKey, customerauthpg.Options{
		PublicURL:   "https://vpn.test",
		EmailSignIn: &customerauthpg.EmailSignInOptions{Contacts: contacts, Mailer: mail},
	})
	if err != nil {
		t.Fatal(err)
	}
	request := customerauthpg.RequestContext{AcceptLanguage: "ru-RU"}

	// A customer imported from Bedolaga with a verified address.
	existing := harness.customer(ctx, t)
	verifiedAt := time.Now()
	if _, err := contacts.SetContact(ctx, existing, "email", "Client@Example.com", &verifiedAt, true, false); err != nil {
		t.Fatal(err)
	}
	if err := service.RequestEmailSignIn(ctx, "client@example.com", "", request); err != nil {
		t.Fatalf("request for a known address: %v", err)
	}
	if !strings.Contains(mail.sent[0].Subject, "Вход") {
		t.Fatalf("subject = %q, want the Russian e-mail", mail.sent[0].Subject)
	}
	signedIn, err := service.CompleteMagicLink(ctx, mail.lastToken(t), request)
	if err != nil || signedIn.Customer.ID != existing {
		t.Fatalf("known address signed in %q (%v), want %q", signedIn.Customer.ID, err, existing)
	}

	var before int
	_ = harness.pool.QueryRow(ctx, `SELECT count(*) FROM users`).Scan(&before)
	if err := service.RequestEmailSignIn(ctx, "new@example.com", "en", request); err != nil {
		t.Fatalf("request for a new address: %v", err)
	}
	var afterRequest int
	_ = harness.pool.QueryRow(ctx, `SELECT count(*) FROM users`).Scan(&afterRequest)
	if afterRequest != before {
		t.Fatal("requesting a link created a customer before the link was used")
	}
	token := mail.lastToken(t)
	created, err := service.CompleteMagicLink(ctx, token, request)
	if err != nil || created.Customer.ID == "" || created.Customer.ID == existing {
		t.Fatalf("new address: %+v %v", created.Customer, err)
	}
	if _, err := service.CompleteMagicLink(ctx, token, request); err == nil {
		t.Fatal("a link was redeemed twice")
	}
	// The created customer now holds the address, so the next link signs them in.
	if err := service.RequestEmailSignIn(ctx, "NEW@example.com", "en", request); err != nil {
		t.Fatal(err)
	}
	again, err := service.CompleteMagicLink(ctx, mail.lastToken(t), request)
	if err != nil || again.Customer.ID != created.Customer.ID {
		t.Fatalf("second sign-in landed on %q (%v), want %q", again.Customer.ID, err, created.Customer.ID)
	}

	if err := service.RequestEmailSignIn(ctx, "not an address", "en", request); err != customerauthpg.ErrEmailInvalid {
		t.Fatalf("invalid address: %v", err)
	}
}
