package operator

import (
	"encoding/json"
	"fmt"
	"html"
	"math"
	"strings"
	"time"

	"github.com/go-telegram/bot/models"
	"github.com/omniflow/omniflow/internal/database/dbgen"
)

// goodsSale is the sale notice a delivered shop order queues: who bought what
// for whom, what it earned, and what is left in the wallet that paid for it.
//
// It is the fork's «Звёзды проданы» summary. It carries the recipient handle
// and the buyer's Telegram identifier — both identifiers an operator looks up
// in the panel — and never a name, a message, or anything the customer wrote.
type goodsSale struct {
	Event              string `json:"event"`
	OrderID            string `json:"orderId"`
	CustomerID         string `json:"customerId"`
	Provider           string `json:"provider"`
	Recipient          string `json:"recipient"`
	RecipientIsSelf    bool   `json:"recipientIsSelf"`
	BuyerTelegramID    int64  `json:"buyerTelegramId"`
	Stars              int64  `json:"stars"`
	AmountMinor        int64  `json:"amountMinor"`
	Currency           string `json:"currency"`
	PricePerStarMinor  *int64 `json:"pricePerStarMinor"`
	Source             string `json:"source"`
	Attempt            int64  `json:"attempt"`
	DeliverySeconds    *int64 `json:"deliverySeconds"`
	DryRun             bool   `json:"dryRun"`
	ProviderConfirmed  *bool  `json:"providerConfirmed"`
	TxHash             string `json:"txHash"`
	CostNano           *int64 `json:"costNano"`
	CostMinor          *int64 `json:"costMinor"`
	MarginMinor        *int64 `json:"marginMinor"`
	RateMinor          *int64 `json:"rateMinor"`
	RateSource         string `json:"rateSource"`
	WalletAddress      string `json:"walletAddress"`
	WalletBalanceNano  *int64 `json:"walletBalanceNano"`
	WalletBalanceMinor *int64 `json:"walletBalanceMinor"`
	WalletStarsCovered *int64 `json:"walletStarsCovered"`
	WalletLowStars     int64  `json:"walletLowStars"`
	WalletLow          bool   `json:"walletLow"`
}

// decodeGoodsSale reports whether a notification is a sale notice.
func decodeGoodsSale(notification dbgen.OperatorNotification) (goodsSale, bool) {
	var sale goodsSale
	if err := json.Unmarshal(notification.Payload, &sale); err != nil || sale.Event != "goods_sale" {
		return goodsSale{}, false
	}
	return sale, true
}

// renderGoodsSale writes the sale notice.
func renderGoodsSale(sale goodsSale, at time.Time) string {
	builder := &strings.Builder{}
	title := "⭐ Stars sold"
	if sale.DryRun {
		title += " (dry run)"
	}
	fmt.Fprintf(builder, "<b>%s</b>\n\n", title)

	buyer := "—"
	if sale.BuyerTelegramID != 0 {
		buyer = fmt.Sprintf("Telegram <code>%d</code>", sale.BuyerTelegramID)
	}
	fmt.Fprintf(builder, "👤 Buyer: %s · <code>%s</code>\n", buyer, html.EscapeString(sale.CustomerID))
	recipient := "@" + html.EscapeString(sale.Recipient)
	if sale.RecipientIsSelf {
		recipient += " <i>(self)</i>"
	}
	fmt.Fprintf(builder, "🎯 Recipient: %s\n", recipient)

	builder.WriteString("<blockquote>")
	fmt.Fprintf(builder, "⭐ <b>%s</b> Stars · %s\n", groupDigits(sale.Stars), html.EscapeString(sourceLabel(sale.Source)))
	paid := "💵 Paid: <b>" + money(sale.AmountMinor, sale.Currency) + "</b>"
	if sale.PricePerStarMinor != nil {
		paid += " (" + money(*sale.PricePerStarMinor, sale.Currency) + " per ⭐)"
	}
	builder.WriteString(paid + "\n")
	if sale.CostNano != nil && *sale.CostNano > 0 {
		line := "💎 Cost: " + ton(*sale.CostNano)
		if sale.CostMinor != nil {
			line += " ≈ " + money(*sale.CostMinor, sale.Currency)
		}
		builder.WriteString(line + "\n")
	} else if sale.CostMinor != nil && !sale.DryRun {
		builder.WriteString("💎 Cost: " + money(*sale.CostMinor, sale.Currency) + "\n")
	}
	if sale.MarginMinor != nil {
		percent := 0.0
		if sale.AmountMinor > 0 {
			percent = float64(*sale.MarginMinor) / float64(sale.AmountMinor) * 100
		}
		fmt.Fprintf(builder, "📈 Margin: <b>%s</b> (%.1f%%)\n", signedMoney(*sale.MarginMinor, sale.Currency), percent)
	}
	if sale.RateMinor != nil {
		fmt.Fprintf(builder, "💱 TON rate: %s (%s)\n", money(*sale.RateMinor, sale.Currency), html.EscapeString(sale.RateSource))
	}
	builder.WriteString("</blockquote>\n")

	var meta []string
	if sale.DeliverySeconds != nil {
		meta = append(meta, "delivered in "+duration(*sale.DeliverySeconds))
	}
	if sale.Attempt > 0 {
		meta = append(meta, fmt.Sprintf("attempt %d", sale.Attempt))
	}
	if len(meta) > 0 {
		builder.WriteString("⚡ " + strings.Join(meta, " · ") + "\n")
	}
	if sale.WalletBalanceNano != nil {
		line := "👛 Wallet: <b>" + ton(*sale.WalletBalanceNano) + "</b>"
		if sale.WalletBalanceMinor != nil {
			line += " ≈ " + money(*sale.WalletBalanceMinor, sale.Currency)
		}
		builder.WriteString(line + "\n")
		if sale.WalletStarsCovered != nil {
			if sale.WalletLow {
				fmt.Fprintf(builder, "⚠️ Covers about %s ⭐ — <b>top the wallet up</b> (warning below %s)\n",
					groupDigits(*sale.WalletStarsCovered), groupDigits(sale.WalletLowStars))
			} else {
				fmt.Fprintf(builder, "🔋 Covers about %s ⭐\n", groupDigits(*sale.WalletStarsCovered))
			}
		}
	}
	if sale.ProviderConfirmed != nil && !*sale.ProviderConfirmed {
		builder.WriteString("⚠️ Paid, but Fragment did not acknowledge the payment (confirmReq)\n")
	}
	if realTx(sale) {
		fmt.Fprintf(builder, "🧾 <code>%s</code>\n", html.EscapeString(sale.TxHash))
	}
	fmt.Fprintf(builder, "order: <code>%s</code>\n", html.EscapeString(sale.OrderID))
	fmt.Fprintf(builder, "at: <code>%s</code>", at.UTC().Format(time.RFC3339))
	return builder.String()
}

// goodsSaleMarkup links the transaction in two explorers, the order in the
// panel, and the wallet.
func goodsSaleMarkup(sale goodsSale, panelURL string) *models.InlineKeyboardMarkup {
	var rows [][]models.InlineKeyboardButton
	if realTx(sale) {
		rows = append(rows, []models.InlineKeyboardButton{
			{Text: "🔎 Tonscan", URL: "https://tonscan.org/tx/" + sale.TxHash},
			{Text: "🔎 Tonviewer", URL: "https://tonviewer.com/transaction/" + sale.TxHash},
		})
	}
	if base := strings.TrimSuffix(strings.TrimSpace(panelURL), "/"); strings.HasPrefix(base, "https://") {
		rows = append(rows, []models.InlineKeyboardButton{
			{Text: "⭐ Order", URL: base + "/admin/shop?order=" + sale.OrderID},
			{Text: "👤 Buyer", URL: base + "/admin/customers/" + sale.CustomerID},
		})
	}
	if sale.WalletAddress != "" {
		rows = append(rows, []models.InlineKeyboardButton{
			{Text: "👛 Wallet", URL: "https://tonscan.org/address/" + sale.WalletAddress},
		})
	}
	if len(rows) == 0 {
		return nil
	}
	return &models.InlineKeyboardMarkup{InlineKeyboard: rows}
}

func realTx(sale goodsSale) bool {
	return sale.TxHash != "" && !sale.DryRun && !strings.HasPrefix(sale.TxHash, "dry-run")
}

func sourceLabel(source string) string {
	switch source {
	case "bot":
		return "bot"
	case "web":
		return "web panel"
	case "auto_cart":
		return "after a top-up"
	default:
		return "unknown source"
	}
}

func money(minor int64, currency string) string {
	sign := ""
	if minor < 0 {
		sign, minor = "−", -minor
	}
	return fmt.Sprintf("%s%s.%02d %s", sign, groupDigits(minor/100), minor%100, currency)
}

func signedMoney(minor int64, currency string) string {
	if minor > 0 {
		return "+" + money(minor, currency)
	}
	return money(minor, currency)
}

func ton(nano int64) string {
	value := float64(nano) / 1e9
	text := strings.TrimRight(strings.TrimRight(fmt.Sprintf("%.4f", math.Abs(value)), "0"), ".")
	return text + " TON"
}

func groupDigits(value int64) string {
	text := fmt.Sprint(value)
	if value < 0 {
		return "-" + groupDigits(-value)
	}
	for index := len(text) - 3; index > 0; index -= 3 {
		text = text[:index] + " " + text[index:]
	}
	return text
}

func duration(seconds int64) string {
	switch {
	case seconds < 60:
		return fmt.Sprintf("%d s", seconds)
	case seconds < 3600:
		return fmt.Sprintf("%d min %d s", seconds/60, seconds%60)
	default:
		return fmt.Sprintf("%d h %d min", seconds/3600, (seconds%3600)/60)
	}
}
