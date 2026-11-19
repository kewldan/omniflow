package remnawavesync

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/omniflow/omniflow/internal/commerce"
	"github.com/omniflow/omniflow/internal/database/dbgen"
)

var (
	// ErrRotationDisabled reports an installation that does not let
	// customers re-issue their link (APP_SUBSCRIPTION_REVOKE_ENABLED=false).
	ErrRotationDisabled = errors.New("re-issuing the subscription link is turned off")
	// ErrRotationUnknown reports a subscription that is not the customer's.
	ErrRotationUnknown = errors.New("subscription not found")
)

// RotationCooldownError reports a re-issue asked for before the cooldown
// since the last one has passed.
type RotationCooldownError struct {
	RetryAt time.Time
}

func (err *RotationCooldownError) Error() string {
	return fmt.Sprintf("the link was re-issued recently; try again after %s", err.RetryAt.UTC().Format(time.RFC3339))
}

// Revoker is the one Remnawave call a re-issue makes.
type Revoker interface {
	RevokeSubscription(ctx context.Context, userID int64) error
}

// RotateLink re-issues one subscription's access link on the customer's
// behalf, under the operator's switch and cooldown.
//
// The slot is claimed in the database before Remnawave is called, so two
// taps — in the bot and the browser at once — cannot both pass the cooldown.
// A Remnawave failure gives the slot back: a re-issue that did not happen
// must not hold the customer off for the cooldown.
func RotateLink(
	ctx context.Context, pool *pgxpool.Pool, panel Revoker, settings commerce.ConnectSettings,
	customerID, subscriptionID pgtype.UUID, remnawaveID int64,
) error {
	if !settings.RevokeAllowed() {
		return ErrRotationDisabled
	}
	queries := dbgen.New(pool)
	previous, err := queries.ClaimSubscriptionLinkRotation(ctx, dbgen.ClaimSubscriptionLinkRotationParams{
		CooldownSeconds: settings.RevokeCooldown.Seconds(),
		SubscriptionID:  subscriptionID, UserID: customerID,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		last, lookupErr := queries.GetSubscriptionLinkRotatedAt(ctx, dbgen.GetSubscriptionLinkRotatedAtParams{
			SubscriptionID: subscriptionID, UserID: customerID,
		})
		if errors.Is(lookupErr, pgx.ErrNoRows) || (lookupErr == nil && !last.Valid) {
			return ErrRotationUnknown
		}
		if lookupErr != nil {
			return lookupErr
		}
		return &RotationCooldownError{RetryAt: last.Time.Add(settings.RevokeCooldown)}
	}
	if err != nil {
		return err
	}
	if revokeErr := panel.RevokeSubscription(ctx, remnawaveID); revokeErr != nil {
		if restoreErr := queries.RestoreSubscriptionLinkRotation(ctx, dbgen.RestoreSubscriptionLinkRotationParams{
			RotatedAt: previous, SubscriptionID: subscriptionID, UserID: customerID,
		}); restoreErr != nil {
			return errors.Join(revokeErr, restoreErr)
		}
		return revokeErr
	}
	return nil
}
