//go:build integration

package integrationtest

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/omniflow/omniflow/internal/commercepg"
	"github.com/omniflow/omniflow/internal/sweeper"
)

// paidWalletGift creates a wallet-credit gift from sender and settles it, so
// the code it returns is deliverable.
func (harness *harness) paidWalletGift(
	ctx context.Context, t *testing.T, store *commercepg.Store, senderID, key string, amount int64,
) (code, giftID string) {
	t.Helper()
	purchase, err := store.CreateGiftOrder(ctx, commercepg.GiftOrderInput{
		SenderID: senderID, Kind: "wallet_credit", CreditMinor: amount, Currency: "RUB",
		IdempotencyKey: key, SkipWallet: true,
	})
	if err != nil {
		t.Fatalf("create gift order %s: %v", key, err)
	}
	harness.settle(ctx, t, store, uuidText(purchase.Order.ID), amount, "event-"+key)
	if err = harness.pool.QueryRow(ctx, `SELECT id::text FROM gifts WHERE order_id = $1`,
		purchase.Order.ID).Scan(&giftID); err != nil {
		t.Fatalf("read gift %s: %v", key, err)
	}
	if status := harness.giftStatus(ctx, t, giftID); status != "deliverable" {
		t.Fatalf("a paid gift is %q, want deliverable", status)
	}
	return purchase.Code, giftID
}

func serializationAbort(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && (pgErr.Code == "40001" || pgErr.Code == "40P01")
}

func (harness *harness) giftStatus(ctx context.Context, t *testing.T, giftID string) string {
	t.Helper()
	var status string
	if err := harness.pool.QueryRow(ctx, `SELECT status FROM gifts WHERE id = $1::uuid`, giftID).Scan(&status); err != nil {
		t.Fatalf("read gift status: %v", err)
	}
	return status
}

// TestConcurrentGiftClaimsCreditOnce races many recipients — and one recipient
// on several devices — for the same code. Exactly one claim may win, and the
// money credited across everybody is exactly the gift's value.
func TestConcurrentGiftClaimsCreditOnce(t *testing.T) {
	raceGiftClaims(t, false)
}

// TestConcurrentGiftClaimLosersAreRefusedCleanly is the stricter half of the
// race: every losing claimant should hear ErrGiftNotFound, as ClaimGift's doc
// comment promises, rather than a raw serialization failure.
func TestConcurrentGiftClaimLosersAreRefusedCleanly(t *testing.T) {
	raceGiftClaims(t, true)
}

func raceGiftClaims(t *testing.T, strict bool) {
	ctx := context.Background()
	harness := newHarness(t)
	store := harness.commerceStore(t, testOptions())
	senderID := harness.customer(ctx, t)
	const amount = 40_000
	code, giftID := harness.paidWalletGift(ctx, t, store, senderID, "gift-race", amount)

	const contenders = 8
	recipients := make([]string, contenders)
	for index := range recipients {
		// Half the claims come from one customer pressing claim on several
		// devices at once; the rest are different customers holding the link.
		if index%2 == 0 && index > 0 {
			recipients[index] = recipients[0]
			continue
		}
		recipients[index] = harness.customer(ctx, t)
	}

	var (
		waitGroup sync.WaitGroup
		start     = make(chan struct{})
		mutex     sync.Mutex
		winners   []string
		failures  []error
		aborted   int
	)
	for _, recipientID := range recipients {
		waitGroup.Add(1)
		go func() {
			defer waitGroup.Done()
			<-start
			_, err := store.ClaimGift(ctx, code, recipientID)
			mutex.Lock()
			defer mutex.Unlock()
			switch {
			case err == nil:
				winners = append(winners, recipientID)
			case errors.Is(err, commercepg.ErrGiftNotFound):
			case !strict && serializationAbort(err):
				// A loser that exhausted its retries. It changed nothing,
				// which is what this test is about; the strict variant
				// covers the error it should have received instead.
				aborted++
			default:
				failures = append(failures, err)
			}
		}()
	}
	close(start)
	waitGroup.Wait()

	if len(failures) > 0 {
		t.Fatalf("claims failed with unexpected errors: %v", failures)
	}
	if aborted > 0 {
		t.Logf("%d losing claims ended in a serialization failure rather than a refusal", aborted)
	}
	if len(winners) != 1 {
		t.Fatalf("%d claims succeeded, want exactly 1", len(winners))
	}
	var credited int64
	seen := map[string]bool{}
	for _, recipientID := range recipients {
		if seen[recipientID] {
			continue
		}
		seen[recipientID] = true
		credited += harness.walletBalance(ctx, t, recipientID)
	}
	if credited != amount {
		t.Fatalf("recipients were credited %d in total, want exactly %d", credited, amount)
	}
	if balance := harness.walletBalance(ctx, t, winners[0]); balance != amount {
		t.Fatalf("the winner holds %d, want %d", balance, amount)
	}
	if status := harness.giftStatus(ctx, t, giftID); status != "claimed" {
		t.Fatalf("gift is %q after the race, want claimed", status)
	}
}

// TestExpiredGiftIsRefusedAndSweptIdempotently proves the claim predicate, not
// the sweep, is what refuses an expired gift, and that the sweep only makes the
// state visible: it touches deliverable gifts past expiry and nothing else, and
// a second pass changes nothing.
func TestExpiredGiftIsRefusedAndSweptIdempotently(t *testing.T) {
	ctx := context.Background()
	harness := newHarness(t)
	store := harness.commerceStore(t, testOptions())
	senderID := harness.customer(ctx, t)
	recipientID := harness.customer(ctx, t)

	expiredCode, expiredID := harness.paidWalletGift(ctx, t, store, senderID, "gift-expired", 10_000)
	liveCode, liveID := harness.paidWalletGift(ctx, t, store, senderID, "gift-live", 20_000)
	claimedCode, claimedID := harness.paidWalletGift(ctx, t, store, senderID, "gift-claimed", 30_000)

	if _, err := store.ClaimGift(ctx, claimedCode, recipientID); err != nil {
		t.Fatalf("claim the third gift: %v", err)
	}
	// Both the expired and the already-claimed gift now sit past expiry.
	if _, err := harness.pool.Exec(ctx, `UPDATE gifts SET expires_at = now() - interval '1 minute'
		WHERE id = ANY($1::uuid[])`, []string{expiredID, claimedID}); err != nil {
		t.Fatalf("move expiry: %v", err)
	}

	// Before any sweep has run, the expired code is already refused and the
	// refusal credits nothing.
	if _, err := store.ClaimGift(ctx, expiredCode, recipientID); !errors.Is(err, commercepg.ErrGiftNotFound) {
		t.Fatalf("claim of an expired gift = %v, want ErrGiftNotFound", err)
	}
	if balance := harness.walletBalance(ctx, t, recipientID); balance != 30_000 {
		t.Fatalf("recipient holds %d after the refused claim, want only the claimed 30000", balance)
	}

	lifecycle := sweeper.New(harness.pool, nil, slog.New(slog.DiscardHandler), sweeper.Config{})
	lifecycle.SweepLifecycle(ctx)

	want := map[string]string{expiredID: "expired", liveID: "deliverable", claimedID: "claimed"}
	for id, status := range want {
		if got := harness.giftStatus(ctx, t, id); got != status {
			t.Fatalf("gift %s is %q after the sweep, want %q", id, got, status)
		}
	}
	var firstPass time.Time
	if err := harness.pool.QueryRow(ctx, `SELECT updated_at FROM gifts WHERE id = $1::uuid`,
		expiredID).Scan(&firstPass); err != nil {
		t.Fatalf("read updated_at: %v", err)
	}

	lifecycle.SweepLifecycle(ctx)
	for id, status := range want {
		if got := harness.giftStatus(ctx, t, id); got != status {
			t.Fatalf("gift %s is %q after a second sweep, want %q", id, got, status)
		}
	}
	var secondPass time.Time
	if err := harness.pool.QueryRow(ctx, `SELECT updated_at FROM gifts WHERE id = $1::uuid`,
		expiredID).Scan(&secondPass); err != nil {
		t.Fatalf("read updated_at: %v", err)
	}
	if !secondPass.Equal(firstPass) {
		t.Fatal("a second sweep rewrote an already-expired gift")
	}

	// The live gift is unaffected and still redeems normally.
	if _, err := store.ClaimGift(ctx, liveCode, recipientID); err != nil {
		t.Fatalf("claim the live gift after the sweep: %v", err)
	}
	if balance := harness.walletBalance(ctx, t, recipientID); balance != 50_000 {
		t.Fatalf("recipient holds %d, want 50000", balance)
	}
	// And an expired gift stays unclaimable after the sweep too.
	if _, err := store.ClaimGift(ctx, expiredCode, recipientID); !errors.Is(err, commercepg.ErrGiftNotFound) {
		t.Fatalf("claim of a swept gift = %v, want ErrGiftNotFound", err)
	}
}

// TestPersonalOfferExpirySweepIsIdempotent closes active offers whose window
// has passed and leaves every other offer alone, however often it runs.
func TestPersonalOfferExpirySweepIsIdempotent(t *testing.T) {
	ctx := context.Background()
	harness := newHarness(t)
	customerID := harness.customer(ctx, t)

	var promotionID string
	if err := harness.pool.QueryRow(ctx, `INSERT INTO promotions (code, kind, value, per_customer_limit)
		VALUES ('offer-sweep', 'percent', 1000, 1) RETURNING id::text`).Scan(&promotionID); err != nil {
		t.Fatalf("create promotion: %v", err)
	}
	offer := func(status string, startsAgo, expiresIn time.Duration) string {
		t.Helper()
		var id string
		if err := harness.pool.QueryRow(ctx, `INSERT INTO personal_offers
			(user_id, promotion_id, title_ru, title_en, status, starts_at, expires_at)
			VALUES ($1::uuid, $2::uuid, 'Предложение', 'Offer', $3,
			        now() - $4::interval, now() + $5::interval)
			RETURNING id::text`,
			customerID, promotionID, status,
			fmt.Sprintf("%d seconds", int(startsAgo.Seconds())),
			fmt.Sprintf("%d seconds", int(expiresIn.Seconds()))).Scan(&id); err != nil {
			t.Fatalf("create %s offer: %v", status, err)
		}
		return id
	}
	// Only one active offer per customer and promotion may exist, so the
	// lapsed active one is the only active row.
	lapsed := offer("active", 48*time.Hour, -time.Hour)
	dismissed := offer("dismissed", 48*time.Hour, -time.Hour)
	revoked := offer("revoked", 48*time.Hour, -time.Hour)

	var otherPromotionID string
	if err := harness.pool.QueryRow(ctx, `INSERT INTO promotions (code, kind, value, per_customer_limit)
		VALUES ('offer-live', 'percent', 1000, 1) RETURNING id::text`).Scan(&otherPromotionID); err != nil {
		t.Fatalf("create promotion: %v", err)
	}
	var live string
	if err := harness.pool.QueryRow(ctx, `INSERT INTO personal_offers
		(user_id, promotion_id, title_ru, title_en, expires_at)
		VALUES ($1::uuid, $2::uuid, 'Предложение', 'Offer', now() + interval '1 day')
		RETURNING id::text`, customerID, otherPromotionID).Scan(&live); err != nil {
		t.Fatalf("create live offer: %v", err)
	}

	read := func(id string) (string, bool) {
		t.Helper()
		var status string
		var resolved bool
		if err := harness.pool.QueryRow(ctx, `SELECT status, resolved_at IS NOT NULL
			FROM personal_offers WHERE id = $1::uuid`, id).Scan(&status, &resolved); err != nil {
			t.Fatalf("read offer: %v", err)
		}
		return status, resolved
	}

	lifecycle := sweeper.New(harness.pool, nil, slog.New(slog.DiscardHandler), sweeper.Config{})
	for pass := 1; pass <= 2; pass++ {
		lifecycle.SweepLifecycle(ctx)
		if status, resolved := read(lapsed); status != "expired" || !resolved {
			t.Fatalf("pass %d: lapsed offer is %q (resolved=%v), want expired and resolved", pass, status, resolved)
		}
		for id, want := range map[string]string{dismissed: "dismissed", revoked: "revoked", live: "active"} {
			if status, _ := read(id); status != want {
				t.Fatalf("pass %d: offer %s is %q, want %q", pass, id, status, want)
			}
		}
	}

	var expiredCount int
	if err := harness.pool.QueryRow(ctx, `SELECT count(*) FROM personal_offers WHERE status = 'expired'`).
		Scan(&expiredCount); err != nil {
		t.Fatalf("count expired offers: %v", err)
	}
	if expiredCount != 1 {
		t.Fatalf("%d offers expired, want 1", expiredCount)
	}
}
