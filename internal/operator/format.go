package operator

import (
	"fmt"
	"html"
	"math"
	"strconv"
	"strings"
	"time"

	"github.com/omniflow/omniflow/internal/payments"
)

// Formatting shared by every notice: money, periods, times, and names.

var monthsRU = []string{"янв", "фев", "мар", "апр", "мая", "июн", "июл", "авг", "сен", "окт", "ноя", "дек"}

// when is the notice's time in the installation's timezone.
func (input renderInput) when() string {
	location := input.Location
	if location == nil {
		location = time.UTC
	}
	local := input.At.In(location)
	zone, _ := local.Zone()
	if input.Locale == "ru" {
		return fmt.Sprintf("%d %s, %s %s", local.Day(), monthsRU[local.Month()-1], local.Format("15:04"), zone)
	}
	return local.Format("2 Jan, 15:04") + " " + zone
}

var currencySymbols = map[string]string{"RUB": "₽", "USD": "$", "EUR": "€", "UAH": "₴", "KZT": "₸", "BYN": "Br", "GBP": "£"}

// formatMoney renders minor units as an amount a person reads: "1 490 ₽",
// "4.99 $", "⭐ 250".
func formatMoney(minor int64, currency string) string {
	currency = strings.ToUpper(currency)
	sign := ""
	if minor < 0 {
		sign, minor = "−", -minor
	}
	if currency == "XTR" {
		return sign + "⭐ " + groupDigits(minor)
	}
	exponent, err := payments.CurrencyExponent(currency)
	if err != nil {
		exponent = 2
	}
	scale := int64(math.Pow10(exponent))
	amount := groupDigits(minor / scale)
	if fraction := minor % scale; exponent > 0 && fraction != 0 {
		amount += "." + fmt.Sprintf("%0*d", exponent, fraction)
	}
	symbol := currencySymbols[currency]
	if symbol == "" {
		symbol = currency
	}
	return sign + amount + " " + symbol
}

// period renders a duration in whole days, months, or years.
func (input renderInput) period(seconds int64) string {
	days := seconds / 86400
	if days <= 0 {
		return ""
	}
	if input.Locale == "ru" {
		switch {
		case days%365 == 0:
			return pluralRU(days/365, "год", "года", "лет")
		case days%30 == 0:
			return pluralRU(days/30, "месяц", "месяца", "месяцев")
		default:
			return pluralRU(days, "день", "дня", "дней")
		}
	}
	unit := func(count int64, one, many string) string {
		if count == 1 {
			return "1 " + one
		}
		return strconv.FormatInt(count, 10) + " " + many
	}
	switch {
	case days%365 == 0:
		return unit(days/365, "year", "years")
	case days%30 == 0:
		return unit(days/30, "month", "months")
	default:
		return unit(days, "day", "days")
	}
}

func pluralRU(count int64, one, few, many string) string {
	form := many
	switch remainder := count % 100; {
	case remainder >= 11 && remainder <= 14:
	case count%10 == 1:
		form = one
	case count%10 >= 2 && count%10 <= 4:
		form = few
	}
	return strconv.FormatInt(count, 10) + " " + form
}

// paymentCount is "first" / "3rd" — the customer's how-many-th payment.
func (input renderInput) paymentCount(count int64) string {
	if count <= 1 {
		return input.say(phrase{"first", "первая"})
	}
	if input.Locale == "ru" {
		return fmt.Sprintf("%d-я", count)
	}
	suffix := "th"
	if count%100 < 11 || count%100 > 13 {
		switch count % 10 {
		case 1:
			suffix = "st"
		case 2:
			suffix = "nd"
		case 3:
			suffix = "rd"
		}
	}
	return fmt.Sprintf("%d%s", count, suffix)
}

var providerNames = map[string]string{
	"yookassa": "YooKassa", "cryptobot": "CryptoBot", "telegram_stars": "Telegram Stars", "stars": "Telegram Stars",
	"heleket": "Heleket", "platega": "Platega", "mulenpay": "MulenPay", "pal24": "PayPalych", "wata": "WATA",
	"freekassa": "FreeKassa", "tribute": "Tribute", "cloudpayments": "CloudPayments", "manual": "manual",
	"stripe": "Stripe", "robokassa": "Robokassa", "lava": "Lava", "kassa_ai": "Kassa AI", "overpay": "Overpay",
	"antilopay": "Antilopay", "riopay": "RioPay", "severpay": "SeverPay", "paypear": "PayPear", "rollypay": "RollyPay",
	"aurapay": "AuraPay", "fragment_direct": "Fragment", "fragment": "Fragment", "wallet": "wallet",
	"cashera": "Cashera", "paritypay": "ParityPay", "payhot": "PayHot", "cispay": "CISPay", "anore": "Anore",
	"donut": "Donut", "etoplatezhi": "Это Платежи", "jupiter": "Jupiter", "tabpay": "TabPay",
}

var paymentMethodNames = map[string]phrase{
	"sbp": {"SBP", "СБП"}, "card": {"card", "карта"}, "bank_card": {"card", "карта"},
	"sberpay": {"SberPay", "SberPay"}, "sber_pay": {"SberPay", "SberPay"}, "tpay": {"T-Pay", "T-Pay"},
	"tinkoff_pay": {"T-Pay", "T-Pay"}, "yoo_money": {"YooMoney", "ЮMoney"}, "yoomoney": {"YooMoney", "ЮMoney"},
	"crypto": {"crypto", "криптовалюта"}, "usdt": {"USDT", "USDT"}, "ton": {"TON", "TON"}, "qr": {"QR", "QR"},
}

// via is "YooKassa · SBP": the provider and, when there is one, the method.
func (input renderInput) via(provider, method string) string {
	text := providerName(provider)
	if method != "" {
		if name, ok := paymentMethodNames[strings.ToLower(method)]; ok {
			text += " · " + input.say(name)
		} else {
			text += " · " + method
		}
	}
	return html.EscapeString(text)
}

func providerName(slug string) string {
	if name := providerNames[strings.ToLower(slug)]; name != "" {
		return name
	}
	return slug
}

// humanize turns an identifier like "payment_needs_attention" into words.
func humanize(value string) string {
	value = strings.NewReplacer("_", " ", ".", " ", "-", " ").Replace(value)
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	return strings.ToUpper(value[:1]) + value[1:]
}

func code(value string) string { return "<code>" + html.EscapeString(value) + "</code>" }

func shortID(id string) string {
	if len(id) > 8 {
		return id[:8]
	}
	return id
}
