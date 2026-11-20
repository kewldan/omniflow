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

// trafficPayload is the traffic a version sells beside its own allowance, as
// the checkout renders it: the allowance itself (null for unlimited) and a
// custom volume with its bounds and price per gigabyte, when there is one.
func trafficPayload(offer accountcheckout.TrafficOffer) map[string]any {
	payload := map[string]any{"ownBytes": offer.OwnBytes}
	if offer.Choosable() {
		payload["custom"] = map[string]any{
			"minGb": offer.MinGB, "maxGb": offer.MaxGB,
			"pricePerGb": map[string]any{"amountMinor": offer.PricePerGBMinor, "currency": offer.Currency},
		}
	}
	return payload
}
