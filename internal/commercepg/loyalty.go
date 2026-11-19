package commercepg

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/jackc/pgx/v5/pgtype"
	databaseutil "github.com/omniflow/omniflow/internal/database"
	"github.com/omniflow/omniflow/internal/database/dbgen"
	"github.com/omniflow/omniflow/internal/loyalty"
)

// ErrProviderRestricted reports a payment method the customer's loyalty group
// does not allow.
var ErrProviderRestricted = errors.New("provider_restricted")

// customerLoyaltyTier reads the tier that prices a customer's orders under the
// programme in force — the earned tier or a granted group, whichever has the
// highest priority — and reports false when there is no programme or the
// customer holds nothing in it. It is read inside the pricing transaction, so
// the discount a quote shows and the discount the order records are the same.
func (store *Store) customerLoyaltyTier(ctx context.Context, queries *dbgen.Queries, userID pgtype.UUID) (loyalty.Tier, bool, error) {
	rows, err := queries.GetCustomerLoyaltyTiers(ctx, userID)
	if err != nil {
		return loyalty.Tier{}, false, err
	}
	tiers := make([]loyalty.Tier, 0, len(rows))
	for _, row := range rows {
		tiers = append(tiers, LoyaltyTierFrom(row))
	}
	tier, ok := loyalty.Best(tiers)
	return tier, ok, nil
}

// LoyaltyTier is the tier that prices a customer's orders, for the surfaces
// that filter what they offer by it.
func (store *Store) LoyaltyTier(ctx context.Context, customerID string) (loyalty.Tier, bool, error) {
	userID, err := parseUUID(customerID)
	if err != nil {
		return loyalty.Tier{}, false, err
	}
	return store.customerLoyaltyTier(ctx, dbgen.New(store.pool), userID)
}

// ProviderAllowed reports whether the customer's loyalty group lets them pay
// through this provider. A customer in no group, or in one that restricts
// nothing, may use every configured provider.
func (store *Store) ProviderAllowed(ctx context.Context, customerID, provider string) (bool, error) {
	tier, held, err := store.LoyaltyTier(ctx, customerID)
	if err != nil || !held {
		return err == nil, err
	}
	return tier.AllowsProvider(provider), nil
}

// LoyaltyTierFrom maps a stored tier onto the domain.
func LoyaltyTierFrom(row dbgen.LoyaltyTier) loyalty.Tier {
	tier := loyalty.Tier{
		ID: uuidString(row.ID), Code: row.Code, NameEN: row.NameEn, NameRU: row.NameRu,
		Threshold: row.Threshold, DiscountBPS: int(row.DiscountBps),
		TrafficDiscountBPS: int(row.TrafficDiscountBps), DeviceDiscountBPS: int(row.DeviceDiscountBps),
		SquadDiscountBPS: int(row.SquadDiscountBps), Priority: int(row.Priority), ManualOnly: row.ManualOnly,
		AllowedPlanIDs:   databaseutil.UUIDStrings(row.AllowedPlanIds),
		AllowedSquadIDs:  databaseutil.UUIDStrings(row.AllowedSquadIds),
		AllowedProviders: row.AllowedProviders,
	}
	if len(row.PeriodDiscounts) > 0 {
		_ = json.Unmarshal(row.PeriodDiscounts, &tier.PeriodDiscountBPS)
	}
	return tier
}

// addonLoyaltyDiscount is the tier's discount on priced add-ons, line by line.
func addonLoyaltyDiscount(tier loyalty.Tier, held bool, priced []pricedAddon) int64 {
	if !held {
		return 0
	}
	var total int64
	for _, charge := range priced {
		total += loyalty.DiscountAt(charge.charge.ChargedMinor, tier.AddonDiscountBPS(charge.row.Kind))
	}
	return total
}
