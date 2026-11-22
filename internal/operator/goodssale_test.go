package operator

import (
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/omniflow/omniflow/internal/database/dbgen"
)

func saleNotification(payload string) dbgen.OperatorNotification {
	return dbgen.OperatorNotification{
		Kind: "purchase", Payload: []byte(payload),
		CreatedAt: pgtype.Timestamptz{Time: time.Date(2026, 11, 19, 12, 0, 0, 0, time.UTC), Valid: true},
	}
}

// The sale notice is the fork's summary: who, how many, what it earned, and
// how much wallet is left — with a top-up warning below the threshold.
func TestASaleNoticeCarriesTheMarginAndTheWallet(t *testing.T) {
	notification := saleNotification(`{"event":"goods_sale","orderId":"o-1","customerId":"c-1",
		"recipient":"alice_stars","buyerTelegramId":42,"stars":250,"amountMinor":36000,"currency":"RUB",
		"pricePerStarMinor":144,"source":"auto_cart","attempt":1,"deliverySeconds":4,
		"txHash":"ab12","costNano":1000000000,"costMinor":30000,"marginMinor":6000,
		"rateMinor":30000,"rateSource":"tonapi","walletAddress":"UQwallet",
		"walletBalanceNano":2000000000,"walletBalanceMinor":60000,"walletStarsCovered":500,
		"walletLowStars":1000,"walletLow":true,"providerConfirmed":true}`)
	sale, ok := decodeGoodsSale(notification)
	if !ok {
		t.Fatal("a sale payload was not recognised")
	}
	text, markup := renderGoodsSale(sale, renderInput{
		At: notification.CreatedAt.Time, Locale: "ru", PanelURL: "https://panel.example",
	})
	for _, want := range []string{
		"⭐ ЗВЁЗДЫ ПРОДАНЫ", "<b>Получатель:</b> @alice_stars", "<b>Звёзд:</b> <b>250</b> · после пополнения",
		"<b>Оплачено:</b> <b>360 ₽</b> (1.44 ₽ за ⭐)", "1 TON ≈ 300 ₽", "<b>+60 ₽</b> (16.7%)", "tonapi",
		"4 s", "2 TON", "500 ⭐", "пополните кошелёк", "ab12", "<b>Telegram ID покупателя:</b> <code>42</code>",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("the notice lacks %q:\n%s", want, text)
		}
	}
	if strings.Contains(text, "blockquote") {
		t.Fatalf("the notice still quotes:\n%s", text)
	}
	if markup == nil || len(markup.InlineKeyboard) != 3 {
		t.Fatalf("expected explorer, panel, and wallet rows: %+v", markup)
	}
	if markup.InlineKeyboard[0][0].URL != "https://tonscan.org/tx/ab12" ||
		markup.InlineKeyboard[0][1].URL != "https://tonviewer.com/transaction/ab12" {
		t.Fatalf("explorer links = %+v", markup.InlineKeyboard[0])
	}
}

// A dry run has no transaction to link and says it was one.
func TestADryRunSaleHasNoExplorerLinks(t *testing.T) {
	sale, _ := decodeGoodsSale(saleNotification(`{"event":"goods_sale","orderId":"o","customerId":"c",
		"recipient":"bob_stars","stars":50,"amountMinor":7000,"currency":"RUB","dryRun":true,
		"txHash":"dry-run-tx-o","costNano":0,"costMinor":0,"marginMinor":7000}`))
	text, markup := renderGoodsSale(sale, renderInput{At: time.Now(), PanelURL: "http://insecure"})
	if !strings.Contains(text, "DRY RUN") || strings.Contains(text, "dry-run-tx") {
		t.Fatalf("dry run notice:\n%s", text)
	}
	if markup != nil {
		t.Fatalf("a dry run on a non-HTTPS panel has nothing to link: %+v", markup)
	}
}

// Every other notice keeps its allowlisted rendering.
func TestOtherNoticesAreNotSales(t *testing.T) {
	if _, ok := decodeGoodsSale(saleNotification(`{"orderId":"o","status":"paid"}`)); ok {
		t.Fatal("an ordinary purchase notice was read as a sale")
	}
}
