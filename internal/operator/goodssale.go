package operator

import (
	"encoding/json"
	"fmt"
	"html"
	"math"
	"net/url"
	"strconv"
	"strings"

	"github.com/go-telegram/bot/models"
	"github.com/omniflow/omniflow/internal/database/dbgen"
)

// goodsSale is the sale notice a delivered shop order queues: who bought what
// for whom, what it earned, and what is left in the wallet that paid for it.
//
// It is the fork's «Звёзды проданы» summary. It carries the recipient handle
// and the buyer's Telegram identifier — both identifiers an operator looks up
// in the panel — and never a message or anything the customer wrote.
type goodsSale struct {
	Event              string `json:"event"`
	Kind               string `json:"kind"`
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

// renderGoodsSale writes the sale notice in the layout every notice uses:
// who bought how many Stars for whom, what it earned, and what is left in the
// wallet that paid for it, with the transaction, the order, the buyer, and
// the wallet as buttons.
func renderGoodsSale(sale goodsSale, input renderInput) (string, *models.InlineKeyboardMarkup) {
	if sale.Kind == "steam_topup" {
		return renderSteamSale(sale, input)
	}
	n := &notice{input: input}
	n.title = input.say(phrase{"⭐ Stars sold", "⭐ Звёзды проданы"})
	if sale.DryRun {
		n.title += " " + input.say(phrase{"(dry run)", "(пробный прогон)"})
	}
	if sale.BuyerTelegramID != 0 {
		n.field("🆔", phrase{"Buyer's Telegram ID", "Telegram ID покупателя"}, code(strconv.FormatInt(sale.BuyerTelegramID, 10)))
	}
	recipient := "@" + html.EscapeString(sale.Recipient)
	if sale.RecipientIsSelf {
		recipient += " " + input.say(phrase{"(themselves)", "(себе)"})
	}
	n.field("🎯", phrase{"Recipient", "Получатель"}, recipient)
	n.field("⭐", phrase{"Stars", "Звёзд"}, "<b>"+groupDigits(sale.Stars)+"</b> · "+input.say(sourceLabel(sale.Source)))
	paid := "<b>" + formatMoney(sale.AmountMinor, sale.Currency) + "</b>"
	if sale.PricePerStarMinor != nil {
		paid += " (" + formatMoney(*sale.PricePerStarMinor, sale.Currency) + " " + input.say(phrase{"per ⭐", "за ⭐"}) + ")"
	}
	n.field("💵", phrase{"Paid", "Оплачено"}, paid)
	if sale.CostNano != nil && *sale.CostNano > 0 {
		cost := ton(*sale.CostNano)
		if sale.CostMinor != nil {
			cost += " ≈ " + formatMoney(*sale.CostMinor, sale.Currency)
		}
		n.field("💎", phrase{"Cost", "Себестоимость"}, cost)
	} else if sale.CostMinor != nil && !sale.DryRun {
		n.field("💎", phrase{"Cost", "Себестоимость"}, formatMoney(*sale.CostMinor, sale.Currency))
	}
	if sale.MarginMinor != nil {
		percent := 0.0
		if sale.AmountMinor > 0 {
			percent = float64(*sale.MarginMinor) / float64(sale.AmountMinor) * 100
		}
		margin := formatMoney(*sale.MarginMinor, sale.Currency)
		if *sale.MarginMinor > 0 {
			margin = "+" + margin
		}
		n.field("📈", phrase{"Margin", "Маржа"}, fmt.Sprintf("<b>%s</b> (%.1f%%)", margin, percent))
	}
	if sale.RateMinor != nil {
		n.field("💱", phrase{"TON rate", "Курс TON"}, formatMoney(*sale.RateMinor, sale.Currency)+" ("+html.EscapeString(sale.RateSource)+")")
	}
	if sale.DeliverySeconds != nil {
		n.field("⚡", phrase{"Delivered in", "Доставлено за"}, duration(*sale.DeliverySeconds))
	}
	if sale.Attempt > 1 {
		n.field("🔁", phrase{"Attempt", "Попытка"}, strconv.FormatInt(sale.Attempt, 10))
	}
	if sale.WalletBalanceNano != nil {
		wallet := "<b>" + ton(*sale.WalletBalanceNano) + "</b>"
		if sale.WalletBalanceMinor != nil {
			wallet += " ≈ " + formatMoney(*sale.WalletBalanceMinor, sale.Currency)
		}
		n.field("👛", phrase{"Wallet", "Кошелёк"}, wallet)
		if sale.WalletStarsCovered != nil {
			covers := groupDigits(*sale.WalletStarsCovered) + " ⭐"
			if sale.WalletLow {
				n.field("⚠️", phrase{"Covers about", "Хватит примерно на"}, covers+" — <b>"+
					input.say(phrase{"top the wallet up", "пополните кошелёк"})+"</b> ("+
					input.say(phrase{"warning below", "предупреждение ниже"})+" "+groupDigits(sale.WalletLowStars)+")")
			} else {
				n.field("🔋", phrase{"Covers about", "Хватит примерно на"}, covers)
			}
		}
	}
	if sale.ProviderConfirmed != nil && !*sale.ProviderConfirmed {
		n.line("⚠️", input.say(phrase{"Paid, but Fragment did not acknowledge the payment (confirmReq)",
			"Оплачено, но Fragment не подтвердил платёж (confirmReq)"}))
	}
	if realTx(sale) {
		n.field("🧾", phrase{"Transaction", "Транзакция"}, code(sale.TxHash))
		n.link("🔎 Tonscan", "https://tonscan.org/tx/"+sale.TxHash)
		n.link("🔎 Tonviewer", "https://tonviewer.com/transaction/"+sale.TxHash)
	}
	n.field("🛍", phrase{"Order", "Заказ"}, code(shortID(sale.OrderID)))
	n.button(input.say(phrase{"⭐ Order", "⭐ Заказ"}), "/admin/shop?order="+url.QueryEscape(sale.OrderID))
	n.button(input.say(phrase{"👤 Buyer", "👤 Покупатель"}), "/admin/customers/"+url.PathEscape(sale.CustomerID))
	if sale.WalletAddress != "" {
		n.link(input.say(phrase{"👛 Wallet", "👛 Кошелёк"}), "https://tonscan.org/address/"+sale.WalletAddress)
	}
	return n.finish()
}

func realTx(sale goodsSale) bool {
	return sale.TxHash != "" && !sale.DryRun && !strings.HasPrefix(sale.TxHash, "dry-run")
}

func sourceLabel(source string) phrase {
	switch source {
	case "bot":
		return phrase{"bot", "бот"}
	case "web":
		return phrase{"web panel", "веб-кабинет"}
	case "auto_cart":
		return phrase{"after a top-up", "после пополнения"}
	default:
		return phrase{"unknown source", "источник неизвестен"}
	}
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

// renderSteamSale is the sale notice for a Steam top-up: the login that was
// credited, how many dollars, what the customer paid, and what it earned.
func renderSteamSale(sale goodsSale, input renderInput) (string, *models.InlineKeyboardMarkup) {
	n := &notice{input: input}
	n.title = input.say(phrase{"🎮 Steam topped up", "🎮 Steam пополнен"})
	if sale.BuyerTelegramID != 0 {
		n.field("🆔", phrase{"Buyer's Telegram ID", "Telegram ID покупателя"}, code(strconv.FormatInt(sale.BuyerTelegramID, 10)))
	}
	n.field("🎯", phrase{"Steam login", "Логин Steam"}, code(html.EscapeString(sale.Recipient)))
	n.field("💲", phrase{"Top-up", "Пополнение"}, "<b>$"+groupDigits(sale.Stars)+"</b> · "+input.say(sourceLabel(sale.Source)))
	n.field("💵", phrase{"Paid", "Оплачено"}, "<b>"+formatMoney(sale.AmountMinor, sale.Currency)+"</b>")
	if sale.CostMinor != nil {
		n.field("💎", phrase{"Cost", "Себестоимость"}, formatMoney(*sale.CostMinor, sale.Currency))
	}
	if sale.MarginMinor != nil {
		percent := 0.0
		if sale.AmountMinor > 0 {
			percent = float64(*sale.MarginMinor) / float64(sale.AmountMinor) * 100
		}
		margin := formatMoney(*sale.MarginMinor, sale.Currency)
		if *sale.MarginMinor > 0 {
			margin = "+" + margin
		}
		n.field("📈", phrase{"Margin", "Маржа"}, fmt.Sprintf("<b>%s</b> (%.1f%%)", margin, percent))
	}
	if sale.DeliverySeconds != nil {
		n.field("⚡", phrase{"Delivered in", "Доставлено за"}, duration(*sale.DeliverySeconds))
	}
	if sale.Attempt > 1 {
		n.field("🔁", phrase{"Attempt", "Попытка"}, strconv.FormatInt(sale.Attempt, 10))
	}
	n.field("🛍", phrase{"Order", "Заказ"}, code(shortID(sale.OrderID)))
	n.button(input.say(phrase{"🎮 Order", "🎮 Заказ"}), "/admin/shop?order="+url.QueryEscape(sale.OrderID))
	n.button(input.say(phrase{"👤 Buyer", "👤 Покупатель"}), "/admin/customers/"+url.PathEscape(sale.CustomerID))
	return n.finish()
}
