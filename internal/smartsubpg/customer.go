package smartsubpg

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/omniflow/omniflow/internal/remnawave"
	"github.com/omniflow/omniflow/internal/smartsub"
)

// CustomerView is everything the customer's screen shows for one
// subscription.
type CustomerView struct {
	Servers     smartsub.View
	Preferences smartsub.Preferences
	// Customized is false while the customer is on the operator's defaults.
	Customized  bool
	Defaults    smartsub.Presets
	CustomRules bool
	MaxRules    int
	// DirectDomains and BlockDomains are the operator's rules for everyone,
	// shown so the customer sees what already applies before adding their own.
	DirectDomains []string
	BlockDomains  []string
}

// CustomerView reads the customer's servers from the panel and their saved
// choices.
func (service *Service) CustomerView(ctx context.Context, customerID, subscriptionID string) (CustomerView, error) {
	settings := service.Settings(ctx)
	if !settings.Enabled {
		return CustomerView{}, ErrDisabled
	}
	remnawaveID, err := service.ownedSubscription(ctx, customerID, subscriptionID)
	if err != nil {
		return CustomerView{}, err
	}
	// The list is built the way the panel builds a subscription — the hosts
	// the user's internal squads grant — rather than by asking the panel for
	// the subscription itself, which runs the device check and would answer a
	// request without a device with a notice instead of servers.
	user, err := service.panel.User(ctx, remnawaveID)
	if err != nil {
		if errors.Is(err, remnawave.ErrNotFound) {
			return CustomerView{}, ErrNotFound
		}
		return CustomerView{}, ErrPanelUnavailable
	}
	catalog := service.Catalog(ctx)
	if catalog.SquadInbounds == nil {
		return CustomerView{}, ErrPanelUnavailable
	}
	squads := make([]string, 0, len(user.ActiveInternalSquads))
	for _, squad := range user.ActiveInternalSquads {
		squads = append(squads, squad.UUID)
	}
	hosts := catalog.HostsFor(squads)
	entries := make([]smartsub.Entry, 0, len(hosts))
	for _, host := range hosts {
		entries = append(entries, smartsub.Entry{Remark: host.Remark, Address: host.Address, Port: host.Port})
	}
	view := CustomerView{
		Servers:  smartsub.BuildView(entries, catalog, settings, service.NodeChecks(ctx)),
		Defaults: settings.Defaults, CustomRules: settings.CustomRules, MaxRules: settings.MaxRules,
		DirectDomains: settings.DirectDomains, BlockDomains: settings.BlockDomains,
	}
	preferences, found, err := readPreferences(ctx, service.pool, subscriptionID)
	if err != nil {
		return CustomerView{}, err
	}
	view.Customized = found
	if found {
		view.Preferences = preferences
	} else {
		view.Preferences = settings.DefaultPreferences()
	}
	return view, nil
}

// SavePreferences validates and stores the customer's choices. They apply the
// next time a client refreshes the subscription.
func (service *Service) SavePreferences(
	ctx context.Context, customerID, subscriptionID string, preferences smartsub.Preferences,
) (smartsub.Preferences, error) {
	settings := service.Settings(ctx)
	if !settings.Enabled {
		return smartsub.Preferences{}, ErrDisabled
	}
	if _, err := service.ownedSubscription(ctx, customerID, subscriptionID); err != nil &&
		!errors.Is(err, ErrNotProvisioned) {
		return smartsub.Preferences{}, err
	}
	normalized, err := preferences.Normalize(settings)
	if err != nil {
		return smartsub.Preferences{}, err
	}
	rules, err := json.Marshal(normalized.Rules)
	if err != nil {
		return smartsub.Preferences{}, err
	}
	_, err = service.pool.Exec(ctx, `INSERT INTO smart_subscription_preferences
		(subscription_id, hidden_countries, auto_excluded, reserve, russia_direct, block_ads, block_quic, rules, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, now())
		ON CONFLICT (subscription_id) DO UPDATE SET
		  hidden_countries = EXCLUDED.hidden_countries, auto_excluded = EXCLUDED.auto_excluded,
		  reserve = EXCLUDED.reserve, russia_direct = EXCLUDED.russia_direct, block_ads = EXCLUDED.block_ads,
		  block_quic = EXCLUDED.block_quic, rules = EXCLUDED.rules, updated_at = now()`,
		subscriptionID, normalized.HiddenCountries, normalized.AutoExcluded, normalized.Reserve,
		normalized.Presets.RussiaDirect, normalized.Presets.BlockAds, normalized.Presets.BlockQUIC, rules)
	if err != nil {
		return smartsub.Preferences{}, err
	}
	return normalized, nil
}

// ResetPreferences puts the subscription back on the operator's defaults.
func (service *Service) ResetPreferences(ctx context.Context, customerID, subscriptionID string) error {
	if _, err := service.ownedSubscription(ctx, customerID, subscriptionID); err != nil &&
		!errors.Is(err, ErrNotProvisioned) {
		return err
	}
	_, err := service.pool.Exec(ctx, `DELETE FROM smart_subscription_preferences WHERE subscription_id = $1`, subscriptionID)
	return err
}

// ownedSubscription returns the panel user of a subscription that belongs to
// the customer. Someone else's subscription is reported as missing, not as
// forbidden: whether an identifier exists is not the asker's business.
func (service *Service) ownedSubscription(ctx context.Context, customerID, subscriptionID string) (int64, error) {
	var subscription, customer pgtype.UUID
	if subscription.Scan(subscriptionID) != nil || customer.Scan(customerID) != nil {
		return 0, ErrNotFound
	}
	var remnawaveID pgtype.Int8
	err := service.pool.QueryRow(ctx,
		`SELECT remnawave_user_id FROM subscriptions WHERE id = $1 AND user_id = $2`, subscription, customer,
	).Scan(&remnawaveID)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, ErrNotFound
	}
	if err != nil {
		return 0, err
	}
	if !remnawaveID.Valid || remnawaveID.Int64 <= 0 {
		return 0, ErrNotProvisioned
	}
	return remnawaveID.Int64, nil
}

// LinkedPanel is the Remnawave client with the subscription link swapped for
// the module's while the module is on. The customer panel, the bot, and its
// notices all read the link through Subscription, so this one place decides
// which link every one of them hands out.
type LinkedPanel struct {
	*remnawave.Client
	Links *Service
}

// Subscription reads the subscription and rewrites its link.
func (panel LinkedPanel) Subscription(ctx context.Context, userID int64) (remnawave.Subscription, error) {
	subscription, err := panel.Client.Subscription(ctx, userID)
	if err != nil || panel.Links == nil {
		return subscription, err
	}
	subscription.SubscriptionURL = panel.Links.Link(ctx, subscription.SubscriptionURL)
	return subscription, nil
}

var _ Panel = (*remnawave.Client)(nil)
