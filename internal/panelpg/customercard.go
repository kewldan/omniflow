package panelpg

import (
	"context"
	"time"
)

// CustomerCard is the short summary the panel shows when an operator hovers a
// customer anywhere a customer is named: who they are, whether they may use
// the service, how long they have been a customer, what they hold, and what
// they have spent. It carries only what the card shows; the profile page is
// where everything else is read.
type CustomerCard struct {
	ID         string `json:"id"`
	Name       string `json:"name,omitempty"`
	Username   string `json:"username,omitempty"`
	TelegramID *int64 `json:"telegramId,omitempty"`
	// Avatar says a picture can be asked for at /customers/{id}/avatar.
	Avatar    bool      `json:"avatar"`
	Status    string    `json:"status"`
	CreatedAt time.Time `json:"createdAt"`
	// SuspensionEndsAt is when a time-limited ban lifts on its own. Absent for
	// a customer who is not suspended or whose suspension has no end.
	SuspensionEndsAt *time.Time                 `json:"suspensionEndsAt,omitempty"`
	Subscriptions    []CustomerCardSubscription `json:"subscriptions"`
	Spent            []CustomerCardSpend        `json:"spent"`
}

// CustomerCardSubscription is one subscription as the card lists it.
type CustomerCardSubscription struct {
	ID                string     `json:"id"`
	Label             string     `json:"label"`
	Status            string     `json:"status"`
	EntitlementStatus string     `json:"entitlementStatus,omitempty"`
	EndsAt            *time.Time `json:"endsAt,omitempty"`
	PlanCode          string     `json:"planCode,omitempty"`
}

// CustomerCardSpend is what a customer has paid in one currency, less refunds,
// across their settled orders.
type CustomerCardSpend struct {
	Currency   string `json:"currency"`
	SpentMinor int64  `json:"spentMinor"`
	OrderCount int64  `json:"orderCount"`
}

// CustomerCard reads the hover summary for one customer. It reuses the
// profile header, the label lookup, and the subscription list the customer
// page reads, and adds the spend per currency.
func (service *Service) CustomerCard(ctx context.Context, customerID string) (CustomerCard, error) {
	id, err := parseUUID(customerID)
	if err != nil {
		return CustomerCard{}, err
	}
	overview, err := service.queries().GetCustomerOverview(ctx, id)
	if err != nil {
		return CustomerCard{}, notFound(err)
	}
	card := CustomerCard{
		ID:            uuidString(overview.User.ID),
		Status:        overview.User.Status,
		CreatedAt:     timeValue(overview.User.CreatedAt),
		Subscriptions: []CustomerCardSubscription{},
		Spent:         []CustomerCardSpend{},
	}
	if card.Status == "suspended" {
		card.SuspensionEndsAt = timePointer(overview.User.SuspensionEndsAt)
	}

	labels, err := service.CustomerLabels(ctx, []string{card.ID})
	if err != nil {
		return CustomerCard{}, err
	}
	if len(labels) == 1 {
		card.Name = labels[0].Name
		card.Username = labels[0].Username
		card.TelegramID = labels[0].TelegramID
	}

	subscriptions, err := service.queries().ListCustomerSubscriptionsDetailed(ctx, id)
	if err != nil {
		return CustomerCard{}, err
	}
	for _, row := range subscriptions {
		card.Subscriptions = append(card.Subscriptions, CustomerCardSubscription{
			ID:                uuidString(row.Subscription.ID),
			Label:             row.Subscription.Label,
			Status:            row.Subscription.Status,
			EntitlementStatus: row.EntitlementStatus,
			EndsAt:            timePointer(row.EntitlementEndsAt),
			PlanCode:          textValue(row.PlanCode),
		})
	}

	spend, err := service.queries().CustomerSpendByCurrency(ctx, id)
	if err != nil {
		return CustomerCard{}, err
	}
	for _, row := range spend {
		card.Spent = append(card.Spent, CustomerCardSpend{
			Currency: row.Currency, SpentMinor: row.SpentMinor, OrderCount: row.OrderCount,
		})
	}
	return card, nil
}
