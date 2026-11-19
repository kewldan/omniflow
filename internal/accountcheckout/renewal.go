package accountcheckout

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/omniflow/omniflow/internal/commerce"
	"github.com/omniflow/omniflow/internal/recurring"
	"github.com/omniflow/omniflow/internal/recurringpg"
)

// ErrRecurringUnavailable is returned when a customer asks for automatic
// renewal on a plan no configured adapter can charge again.
var ErrRecurringUnavailable = errors.New("automatic renewal is not available for this subscription")

// ErrNoSavedMethod is returned when a customer asks to renew from a card they
// have not saved.
var ErrNoSavedMethod = recurringpg.ErrNoSavedMethod

// ErrMethodNotFound is returned for a saved method that is not the caller's.
var ErrMethodNotFound = errors.New("payment method not found")

// SubscriptionRenewal is one subscription with its automatic-renewal setting.
type SubscriptionRenewal struct {
	Target    SubscriptionTarget
	Settings  recurringpg.RenewalSettings
	Supported bool
}

// RenewalOverview is everything the web panel's auto-renew screen shows: the
// saved methods and, per subscription, whether it renews automatically and
// from what.
type RenewalOverview struct {
	Available     bool
	Methods       []recurringpg.SavedMethod
	Subscriptions []SubscriptionRenewal
}

// RenewalInput is a change to one subscription's automatic renewal. A nil or
// empty field is left as stored.
type RenewalInput struct {
	Enabled  *bool
	Funding  string
	LeadTime time.Duration
}

func (service *Service) renewals() *recurringpg.Store { return recurringpg.New(service.store.pool) }

// Renewal reads the customer's automatic-renewal settings and saved methods.
// It is the same rows the bot's auto-renew screen reads, through the same
// store, so the two surfaces cannot disagree about one subscription.
func (service *Service) Renewal(ctx context.Context, customerID, locale string) (RenewalOverview, error) {
	overview := RenewalOverview{
		Available: commerce.SupportsAutoRenew(service.payments.Options(), service.settings.Currency),
	}
	methods, err := service.renewals().SavedMethods(ctx, customerID)
	if err != nil {
		return RenewalOverview{}, err
	}
	overview.Methods = methods
	targets, err := service.store.SubscriptionTargets(ctx, customerID, locale)
	if err != nil {
		return RenewalOverview{}, err
	}
	overview.Subscriptions = make([]SubscriptionRenewal, 0, len(targets))
	for _, target := range targets {
		settings, err := service.renewals().RenewalSettings(ctx, customerID, target.ID)
		if err != nil {
			return RenewalOverview{}, err
		}
		_, _, supported, err := service.recurringChoice(ctx, customerID, target.ID)
		if err != nil {
			return RenewalOverview{}, err
		}
		overview.Subscriptions = append(overview.Subscriptions, SubscriptionRenewal{
			Target: target, Settings: settings, Supported: supported,
		})
	}
	return overview, nil
}

// SetRenewal changes one subscription's automatic renewal. Turning it on is
// the moment the customer consents, which the store records; turning it off
// keeps saved methods, because withdrawing agreement to automatic charging is
// not the same as asking for a card to be forgotten.
func (service *Service) SetRenewal(ctx context.Context, customerID, subscriptionID string, input RenewalInput) error {
	owned, err := service.ownsSubscription(ctx, customerID, subscriptionID)
	if err != nil {
		return err
	}
	if !owned {
		return ErrOrderNotFound
	}
	store := service.renewals()
	if input.Enabled != nil {
		setting := recurringpg.AutoRenew{Enabled: *input.Enabled, SubscriptionID: subscriptionID}
		if *input.Enabled {
			planVersionID, choice, supported, err := service.recurringChoice(ctx, customerID, subscriptionID)
			if err != nil {
				return err
			}
			if !supported {
				return ErrRecurringUnavailable
			}
			setting.PlanVersionID, setting.Provider, setting.Currency = planVersionID, choice.Provider, choice.Currency
		}
		if err = store.SetAutoRenew(ctx, customerID, setting); err != nil {
			return err
		}
	}
	if input.Funding != "" {
		if input.Funding != recurring.FundingWallet && input.Funding != recurring.FundingSavedMethod {
			return invalidInput("unsupported renewal funding")
		}
		if err = store.SetRenewalFunding(ctx, customerID, subscriptionID, input.Funding); err != nil {
			return err
		}
	}
	if input.LeadTime > 0 {
		if err = store.SetRenewalLeadTime(ctx, customerID, subscriptionID, input.LeadTime); err != nil {
			return err
		}
	}
	return nil
}

// SetDefaultMethod makes one saved method the one automatic renewal charges.
func (service *Service) SetDefaultMethod(ctx context.Context, customerID, methodID string) error {
	if err := service.renewals().SetDefaultMethod(ctx, customerID, methodID); err != nil {
		return ErrMethodNotFound
	}
	return nil
}

// RemoveMethod revokes a saved method on the customer's own request.
func (service *Service) RemoveMethod(ctx context.Context, customerID, methodID string) error {
	if err := service.renewals().RemoveMethod(ctx, customerID, methodID); err != nil {
		return ErrMethodNotFound
	}
	return nil
}

// recurringChoice finds the plan a subscription renews onto and the first
// method that can charge it again, which is the rule the bot applies too.
func (service *Service) recurringChoice(ctx context.Context, customerID, subscriptionID string) (string, PaymentChoice, bool, error) {
	var planVersionID string
	err := service.store.pool.QueryRow(ctx, `SELECT e.plan_version_id::text
		FROM entitlements e
		WHERE e.user_id = $1::uuid AND e.subscription_id = $2::uuid AND e.status <> 'superseded'
		ORDER BY (e.starts_at <= now()) DESC, e.ends_at DESC LIMIT 1`, customerID, subscriptionID).Scan(&planVersionID)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", PaymentChoice{}, false, nil
	}
	if err != nil {
		return "", PaymentChoice{}, false, err
	}
	choices, err := service.PaymentChoices(ctx, planVersionID)
	if err != nil {
		return "", PaymentChoice{}, false, err
	}
	for _, choice := range choices {
		if choice.Recurring {
			return planVersionID, choice, true, nil
		}
	}
	return planVersionID, PaymentChoice{}, false, nil
}

func (service *Service) ownsSubscription(ctx context.Context, customerID, subscriptionID string) (bool, error) {
	if _, err := parseUUID(subscriptionID); err != nil {
		return false, nil
	}
	var owned bool
	err := service.store.pool.QueryRow(ctx, `SELECT EXISTS (
		SELECT 1 FROM subscriptions WHERE id = $2::uuid AND user_id = $1::uuid AND status = 'active')`,
		customerID, subscriptionID).Scan(&owned)
	return owned, err
}
