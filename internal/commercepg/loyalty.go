package commercepg

import (
	"context"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/omniflow/omniflow/internal/database/dbgen"
	"github.com/omniflow/omniflow/internal/loyalty"
)

// customerLoyaltyTier reads the tier a customer is priced with under the
// programme in force, and reports false when there is no programme or the
// customer holds no tier in it. It is read inside the pricing transaction, so
// the discount a quote shows and the discount the order records are the same.
func (store *Store) customerLoyaltyTier(ctx context.Context, queries *dbgen.Queries, userID pgtype.UUID) (loyalty.Tier, bool, error) {
	rows, err := queries.GetCustomerLoyaltyTiers(ctx, userID)
	if err != nil {
		return loyalty.Tier{}, false, err
	}
	tiers := make([]loyalty.Tier, 0, len(rows))
	for _, row := range rows {
		tiers = append(tiers, loyaltyTierFrom(row))
	}
	tier, ok := loyalty.Best(tiers)
	return tier, ok, nil
}

func loyaltyTierFrom(row dbgen.LoyaltyTier) loyalty.Tier {
	return loyalty.Tier{
		ID: uuidString(row.ID), Code: row.Code, NameEN: row.NameEn, NameRU: row.NameRu,
		Threshold: row.Threshold, DiscountBPS: int(row.DiscountBps),
	}
}
