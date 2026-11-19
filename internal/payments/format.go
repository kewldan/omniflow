package payments

import (
	"strconv"
	"strings"
)

// FormatAmount renders an integer minor-unit amount with its currency code for
// a message body: "149.50 RUB", "100 XTR". A currency with no known exponent
// is printed in minor units rather than guessed at.
func FormatAmount(amountMinor int64, currency string) string {
	currency = strings.ToUpper(strings.TrimSpace(currency))
	exponent, err := currencyExponent(currency)
	if err != nil || exponent == 0 {
		return strconv.FormatInt(amountMinor, 10) + " " + currency
	}
	sign := ""
	if amountMinor < 0 {
		sign, amountMinor = "-", -amountMinor
	}
	return sign + formatMinor(amountMinor, exponent) + " " + currency
}
