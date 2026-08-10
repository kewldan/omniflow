package payments

import (
	"errors"
	"strconv"
	"strings"
)

// CurrencyExponent reports how many minor units make up one major unit of a
// currency, so a customer surface can render an integer amount correctly.
func CurrencyExponent(currency string) (int, error) { return currencyExponent(currency) }

func currencyExponent(currency string) (int, error) {
	switch currency {
	case "JPY", "KRW", "XTR":
		return 0, nil
	case "BHD", "JOD", "KWD", "OMR", "TND":
		return 3, nil
	case "RUB", "USD", "EUR", "GBP", "AED", "AMD", "AUD", "AZN", "BYN", "CAD", "CHF", "CNY", "GEL", "INR", "KZT", "MDL", "NOK", "NZD", "PLN", "SEK", "TRY", "UAH", "UZS":
		return 2, nil
	default:
		return 0, errors.New("unsupported currency exponent")
	}
}

func formatMinor(amount int64, exponent int) string {
	if exponent == 0 {
		return strconv.FormatInt(amount, 10)
	}
	scale := int64(1)
	for range exponent {
		scale *= 10
	}
	return strconv.FormatInt(amount/scale, 10) + "." + leftPad(strconv.FormatInt(amount%scale, 10), exponent)
}

func parseMinor(value string, exponent int) (int64, error) {
	parts := strings.Split(value, ".")
	if len(parts) > 2 || len(parts) == 0 || strings.HasPrefix(value, "-") {
		return 0, errors.New("invalid provider amount")
	}
	whole, err := strconv.ParseInt(parts[0], 10, 64)
	if err != nil {
		return 0, errors.New("invalid provider amount")
	}
	fraction := ""
	if len(parts) == 2 {
		fraction = parts[1]
	}
	if len(fraction) > exponent {
		return 0, errors.New("provider amount has excessive precision")
	}
	fraction = fraction + strings.Repeat("0", exponent-len(fraction))
	minorFraction := int64(0)
	if fraction != "" {
		minorFraction, err = strconv.ParseInt(fraction, 10, 64)
		if err != nil {
			return 0, errors.New("invalid provider amount")
		}
	}
	scale := int64(1)
	for range exponent {
		scale *= 10
	}
	return whole*scale + minorFraction, nil
}

func leftPad(value string, width int) string {
	return strings.Repeat("0", max(0, width-len(value))) + value
}
