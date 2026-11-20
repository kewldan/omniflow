//go:build integration

package integrationtest

import (
	"context"
	"errors"
	"regexp"
	"testing"
	"time"

	"github.com/omniflow/omniflow/internal/commercepg"
	"github.com/omniflow/omniflow/internal/customerpg"
	"github.com/omniflow/omniflow/internal/database/dbgen"
	"github.com/omniflow/omniflow/internal/gifts"
)

func (harness *harness) namedTelegramCustomer(ctx context.Context, t *testing.T, store *commercepg.Store, telegramID, username string) string {
	t.Helper()
	customer := harness.customer(ctx, t)
	if _, err := harness.pool.Exec(ctx, `INSERT INTO identities (user_id, provider, provider_subject, verified_at, status)
		VALUES ($1::uuid, 'telegram', $2, now(), 'active')`, customer, telegramID); err != nil {
		t.Fatalf("link Telegram: %v", err)
	}
	if username != "" {
		if err := store.RecordTelegramUsername(ctx, customer, username); err != nil {
			t.Fatalf("record username: %v", err)
		}
	}
	return customer
}

func (harness *harness) giftDelivery(ctx context.Context, t *testing.T, giftID string) (string, string) {
	t.Helper()
	var status, via string
	if err := harness.pool.QueryRow(ctx, `SELECT status, COALESCE(delivered_via, '') FROM gift_recipients
		WHERE gift_id = $1::uuid`, giftID).Scan(&status, &via); err != nil {
		t.Fatalf("read gift delivery: %v", err)
	}
	return status, via
}

// A gift addressed to a @username is claimed for the customer who writes to
// the bot under it, once, and the sender's own code is then spent; one
// addressed to the sender or to a malformed address is refused before any
// money moves; a username moves with its owner.
func TestAGiftAddressedToAUsernameIsDeliveredOnce(t *testing.T) {
	ctx := context.Background()
	harness := newHarness(t)
	store := harness.commerceStore(t, testOptions())
	plan := harness.catalog(ctx, t, "addressed-gift-plan", 20000)
	sender := harness.namedTelegramCustomer(ctx, t, store, "7100001", "Gift_Sender")
	friend := harness.namedTelegramCustomer(ctx, t, store, "7100002", "@Gift_Friend")
	harness.seedWallet(ctx, t, sender, "addressed-gift-wallet", 100000)

	for name, recipient := range map[string]commercepg.GiftRecipient{
		"self":      {Username: "gift_sender"},
		"malformed": {Username: "x"},
	} {
		if _, err := store.CreateGiftOrder(ctx, commercepg.GiftOrderInput{
			SenderID: sender, Kind: gifts.KindSubscription, PlanVersionID: plan, Currency: "RUB",
			IdempotencyKey: "refused-" + name, Recipient: recipient,
		}); err == nil {
			t.Fatalf("a gift to the %s recipient was accepted", name)
		}
	}
	username, email, err := commercepg.ParseGiftRecipient("  @Gift_Friend ")
	if err != nil || username != "gift_friend" || email != "" {
		t.Fatalf("parse = %q %q %v", username, email, err)
	}

	purchase, err := store.CreateGiftOrder(ctx, commercepg.GiftOrderInput{
		SenderID: sender, Kind: gifts.KindSubscription, PlanVersionID: plan, Currency: "RUB",
		IdempotencyKey: "addressed-gift", Recipient: commercepg.GiftRecipient{Username: username, Locale: "en"},
	})
	if err != nil || purchase.Order.State != "paid" {
		t.Fatalf("buy the gift: %+v %v", purchase.Order.State, err)
	}
	giftID := uuidText(purchase.Gift.ID)
	waiting, err := store.WaitingAddressedGifts(ctx, 10)
	if err != nil || len(waiting) != 1 || waiting[0].GiftID != giftID || waiting[0].PlanName == "" {
		t.Fatalf("waiting = %+v (%v)", waiting, err)
	}
	found, ok, err := store.GiftRecipientCustomer(ctx, waiting[0])
	if err != nil || !ok || found != friend {
		t.Fatalf("recipient = %q %v %v, want the friend", found, ok, err)
	}
	if _, err = store.DeliverAddressedGift(ctx, giftID, found); err != nil {
		t.Fatalf("deliver: %v", err)
	}
	if _, err = store.DeliverAddressedGift(ctx, giftID, found); !errors.Is(err, commercepg.ErrGiftNotFound) {
		t.Fatalf("a second delivery: %v", err)
	}
	if status, via := harness.giftDelivery(ctx, t, giftID); status != "delivered" || via != "telegram" {
		t.Fatalf("delivery = %s/%s", status, via)
	}
	var entitlements int
	if err = harness.pool.QueryRow(ctx, `SELECT count(*) FROM entitlements WHERE user_id = $1::uuid`, friend).Scan(&entitlements); err != nil || entitlements != 1 {
		t.Fatalf("the friend holds %d entitlements (%v), want one", entitlements, err)
	}
	if _, err = store.ClaimGift(ctx, purchase.Code, friend); !errors.Is(err, commercepg.ErrGiftNotFound) {
		t.Fatalf("the code still claimed a delivered gift: %v", err)
	}
	if waiting, err = store.WaitingAddressedGifts(ctx, 10); err != nil || len(waiting) != 0 {
		t.Fatalf("still waiting: %+v (%v)", waiting, err)
	}

	// The username moves to whoever writes under it now.
	newcomer := harness.namedTelegramCustomer(ctx, t, store, "7100003", "gift_friend")
	var holder string
	if err = harness.pool.QueryRow(ctx, `SELECT user_id::text FROM telegram_usernames WHERE username = 'gift_friend'`).Scan(&holder); err != nil || holder != newcomer {
		t.Fatalf("username holder = %q (%v), want the newcomer", holder, err)
	}
}

// A gift redeemed with its code before it reaches its recipient is closed,
// not delivered a second time.
func TestAnAddressedGiftRedeemedByCodeIsNotDeliveredAgain(t *testing.T) {
	ctx := context.Background()
	harness := newHarness(t)
	store := harness.commerceStore(t, testOptions())
	plan := harness.catalog(ctx, t, "code-first-plan", 20000)
	sender := harness.namedTelegramCustomer(ctx, t, store, "7200001", "code_sender")
	friend := harness.namedTelegramCustomer(ctx, t, store, "7200002", "code_friend")
	stranger := harness.customer(ctx, t)
	harness.seedWallet(ctx, t, sender, "code-first-wallet", 100000)
	purchase, err := store.CreateGiftOrder(ctx, commercepg.GiftOrderInput{
		SenderID: sender, Kind: gifts.KindSubscription, PlanVersionID: plan, Currency: "RUB",
		IdempotencyKey: "code-first-gift", Recipient: commercepg.GiftRecipient{Username: "code_friend"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = store.ClaimGift(ctx, purchase.Code, stranger); err != nil {
		t.Fatalf("claim by code: %v", err)
	}
	if status, _ := harness.giftDelivery(ctx, t, uuidText(purchase.Gift.ID)); status != "closed" {
		t.Fatalf("delivery after a code claim = %s, want closed", status)
	}
	if _, err = store.DeliverAddressedGift(ctx, uuidText(purchase.Gift.ID), friend); !errors.Is(err, commercepg.ErrGiftNotFound) {
		t.Fatalf("a claimed gift was delivered: %v", err)
	}
}

// A gift addressed to an e-mail address nobody here holds is mailed a claim
// code of its own, once, after it is paid; the address is erased once the
// mail is queued, and the mailed code claims the gift.
func TestAGiftAddressedToAnEmailIsMailedACodeOnce(t *testing.T) {
	ctx := context.Background()
	harness := newHarness(t)
	store := harness.commerceStore(t, testOptions())
	plan := harness.catalog(ctx, t, "mailed-gift-plan", 20000)
	sender := harness.namedTelegramCustomer(ctx, t, store, "7300001", "mail_sender")
	harness.seedWallet(ctx, t, sender, "mailed-gift-wallet", 100000)
	contacts, err := customerpg.New(harness.pool, operationsKey, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	sealed, fingerprint, err := contacts.SealContact("email", "friend@example.com")
	if err != nil {
		t.Fatal(err)
	}
	purchase, err := store.CreateGiftOrder(ctx, commercepg.GiftOrderInput{
		SenderID: sender, Kind: gifts.KindSubscription, PlanVersionID: plan, Currency: "RUB",
		IdempotencyKey: "mailed-gift", SenderMessage: "Enjoy",
		Recipient: commercepg.GiftRecipient{EmailCiphertext: sealed, EmailFingerprint: fingerprint, Locale: "en"},
	})
	if err != nil {
		t.Fatalf("buy the gift: %v", err)
	}
	giftID := uuidText(purchase.Gift.ID)
	waiting, err := store.WaitingAddressedGifts(ctx, 10)
	if err != nil || len(waiting) != 1 {
		t.Fatalf("waiting = %+v (%v)", waiting, err)
	}
	if _, ok, lookupErr := store.GiftRecipientCustomer(ctx, waiting[0]); lookupErr != nil || ok {
		t.Fatalf("an unknown address resolved to a customer (%v)", lookupErr)
	}

	mail := &recordingSender{}
	queue := newEmailQueue(t, harness, mail, false)
	for range 2 {
		if err = queue.Pass(ctx); err != nil {
			t.Fatal(err)
		}
	}
	sent := mail.messages()
	if len(sent) != 1 || sent[0].To != "friend@example.com" {
		t.Fatalf("mails = %+v, want the recipient once", sent)
	}
	code := regexp.MustCompile(`code=([A-Z0-9-]+)`).FindStringSubmatch(sent[0].Text)
	if len(code) != 2 {
		t.Fatalf("the mail has no activation link: %s", sent[0].Text)
	}
	if status, via := harness.giftDelivery(ctx, t, giftID); status != "delivered" || via != "email" {
		t.Fatalf("delivery = %s/%s", status, via)
	}
	var erased bool
	if err = harness.pool.QueryRow(ctx, `SELECT email_ciphertext IS NULL AND email_fingerprint IS NULL
		FROM gift_recipients WHERE gift_id = $1::uuid`, giftID).Scan(&erased); err != nil || !erased {
		t.Fatalf("the address is still stored (%v)", err)
	}
	recipient := harness.customer(ctx, t)
	claimed, err := store.ClaimGift(ctx, code[1], recipient)
	if err != nil || uuidText(claimed.ID) != giftID {
		t.Fatalf("claim with the mailed code: %v", err)
	}
}

// Anonymising a customer forgets the username the bot remembered for them and
// the addresses of their gifts still waiting to be delivered.
func TestAnonymisingASenderForgetsGiftAddresses(t *testing.T) {
	ctx := context.Background()
	harness := newHarness(t)
	store := harness.commerceStore(t, testOptions())
	plan := harness.catalog(ctx, t, "anonymised-gift-plan", 20000)
	sender := harness.namedTelegramCustomer(ctx, t, store, "7400001", "anon_sender")
	harness.namedTelegramCustomer(ctx, t, store, "7400002", "anon_friend")
	purchase, err := store.CreateGiftOrder(ctx, commercepg.GiftOrderInput{
		SenderID: sender, Kind: gifts.KindSubscription, PlanVersionID: plan, Currency: "RUB",
		IdempotencyKey: "anonymised-gift", Recipient: commercepg.GiftRecipient{Username: "anon_friend"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err = dbgen.New(harness.pool).AnonymizeCustomerData(ctx, mustUUID(t, sender)); err != nil {
		t.Fatalf("anonymise: %v", err)
	}
	var usernames, addresses int
	if err = harness.pool.QueryRow(ctx, `SELECT
		(SELECT count(*) FROM telegram_usernames WHERE user_id = $1::uuid),
		(SELECT count(*) FROM gift_recipients WHERE gift_id = $2)`, sender, purchase.Gift.ID).Scan(&usernames, &addresses); err != nil {
		t.Fatal(err)
	}
	if usernames != 0 || addresses != 0 {
		t.Fatalf("after anonymisation: %d usernames, %d gift addresses", usernames, addresses)
	}
}
