package httpapi

import "github.com/omniflow/omniflow/internal/accountcheckout"

// periodPayload is a version's other billing periods as the checkout renders
// them: listed choices, and custom days with their bounds and day price.
func periodPayload(offer accountcheckout.PeriodOffer) map[string]any {
	choices := make([]map[string]any, 0, len(offer.Choices))
	for _, choice := range offer.Choices {
		choices = append(choices, map[string]any{
			"days":  choice.Days,
			"price": map[string]any{"amountMinor": choice.AmountMinor, "currency": offer.Currency},
		})
	}
	payload := map[string]any{"choices": choices}
	if offer.MinDays > 0 {
		payload["custom"] = map[string]any{
			"minDays": offer.MinDays, "maxDays": offer.MaxDays,
			"pricePerDay": map[string]any{"amountMinor": offer.PricePerDayMinor, "currency": offer.Currency},
		}
	}
	return payload
}
