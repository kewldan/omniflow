package botapp

import (
	"context"
	"time"

	"github.com/omniflow/omniflow/internal/recurringpg"
)

// The renewal settings and saved methods live in internal/recurringpg, which
// the customer web panel shares; the bot reads them through these names.
type (
	AutoRenew       = recurringpg.AutoRenew
	SavedMethod     = recurringpg.SavedMethod
	RenewalSettings = recurringpg.RenewalSettings
)

var errNoSavedMethod = recurringpg.ErrNoSavedMethod

func (store *PostgresStore) renewals() *recurringpg.Store { return recurringpg.New(store.pool) }

// AutoRenew reads the auto-renew intent for one subscription.
func (store *PostgresStore) AutoRenew(ctx context.Context, customerID, subscriptionID string) (AutoRenew, error) {
	return store.renewals().AutoRenew(ctx, customerID, subscriptionID)
}

// SetAutoRenew records or cancels the auto-renew intent for one subscription.
func (store *PostgresStore) SetAutoRenew(ctx context.Context, customerID string, setting AutoRenew) error {
	return store.renewals().SetAutoRenew(ctx, customerID, setting)
}

// SavedMethods lists the methods a customer can renew with.
func (store *PostgresStore) SavedMethods(ctx context.Context, customerID string) ([]SavedMethod, error) {
	return store.renewals().SavedMethods(ctx, customerID)
}

// SetDefaultMethod makes one saved method the one automatic renewal uses.
func (store *PostgresStore) SetDefaultMethod(ctx context.Context, customerID, methodID string) error {
	return store.renewals().SetDefaultMethod(ctx, customerID, methodID)
}

// RemoveMethod revokes a saved method and stops any renewal that depended on it.
func (store *PostgresStore) RemoveMethod(ctx context.Context, customerID, methodID string) error {
	return store.renewals().RemoveMethod(ctx, customerID, methodID)
}

// SetRenewalFunding chooses what one subscription's automatic renewal is paid from.
func (store *PostgresStore) SetRenewalFunding(ctx context.Context, customerID, subscriptionID, funding string) error {
	return store.renewals().SetRenewalFunding(ctx, customerID, subscriptionID, funding)
}

// SetRenewalLeadTime sets how far ahead of expiry a renewal is attempted.
func (store *PostgresStore) SetRenewalLeadTime(ctx context.Context, customerID, subscriptionID string, lead time.Duration) error {
	return store.renewals().SetRenewalLeadTime(ctx, customerID, subscriptionID, lead)
}

// RenewalSettings reads one subscription's auto-renew configuration.
func (store *PostgresStore) RenewalSettings(ctx context.Context, customerID, subscriptionID string) (RenewalSettings, error) {
	return store.renewals().RenewalSettings(ctx, customerID, subscriptionID)
}
