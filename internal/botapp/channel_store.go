package botapp

import (
	"context"
	"time"

	"github.com/omniflow/omniflow/internal/channelgate"
	databaseutil "github.com/omniflow/omniflow/internal/database"
	"github.com/omniflow/omniflow/internal/database/dbgen"
)

// Purchase-time channel verification.
//
// The periodic worker in `internal/channelworker` answers "is this customer
// still a member?" and takes access away when they are not. This answers the
// other half: "may this purchase proceed?", at the moment it is attempted.
//
// They are separate because they fail differently and must. The worker can
// afford to be cautious and slow — an unknown answer waits for the next pass.
// A customer with their card out cannot wait, so this checks live, and an
// unanswerable check lets the purchase through rather than blocking it.
//
// That last decision is the one worth stating plainly. Refusing a payment
// because Telegram was briefly unreachable would cost a real sale to enforce a
// marketing requirement, and the worker catches a non-member within the hour
// anyway. The failure mode is a customer who joins a channel late, not a
// customer charged for something they never receive.

// MembershipVerifier answers whether one Telegram account is in one chat.
//
// It is the same shape `internal/channelworker` takes, and for the same reason:
// the implementation lives where the bot token does, and a test can answer
// without Telegram.
type MembershipVerifier interface {
	IsMember(ctx context.Context, chatID, telegramID int64) (bool, error)
}

// UseMembershipVerifier attaches the verifier used at checkout.
//
// The bot works without one: an installation with no verifier simply does not
// gate purchases, which is what an installation that has configured no channels
// wanted anyway.
func (app *App) UseMembershipVerifier(verifier MembershipVerifier) {
	app.membership = verifier
}

// ChannelRequirement is one channel a customer must join before buying.
type ChannelRequirement struct {
	Title string
	// InviteURL is what the customer taps. A requirement with no way to satisfy
	// it is a wall, so a channel with neither an invite link nor a username is
	// not enforced.
	InviteURL string
}

// PurchaseGate is the answer to "may this purchase proceed?".
type PurchaseGate struct {
	// Missing lists the channels the customer has not joined. Empty means the
	// purchase may proceed.
	Missing []ChannelRequirement
}

// Allowed reports whether the purchase may go ahead.
func (gate PurchaseGate) Allowed() bool { return len(gate.Missing) == 0 }

const (
	// purchaseCacheWindow is how long a cached membership answer is trusted at
	// checkout. Five minutes is short enough that somebody who has just joined
	// is re-asked, and long enough that clicking through a flow does not
	// trigger a Telegram call per screen.
	purchaseCacheWindow = 5 * time.Minute
	// purchaseCheckTimeout bounds the live call. A slow answer is treated as no
	// answer, and no answer permits the purchase.
	purchaseCheckTimeout = 3 * time.Second
)

// checkPurchaseChannels verifies membership before a purchase.
//
// It permits the purchase whenever it cannot answer: no verifier, no configured
// channels, an exemption, or a Telegram call that failed. Each of those is a
// case where blocking would punish a customer for the installation's problem.
func (app *App) checkPurchaseChannels(
	ctx context.Context, customerID string, telegramID int64,
) PurchaseGate {
	if app.customers == nil || app.membership == nil || telegramID == 0 {
		return PurchaseGate{}
	}
	queries := app.customers.queries

	channels, err := queries.ListEnabledChannels(ctx)
	if err != nil || len(channels) == 0 {
		return PurchaseGate{}
	}

	parsed, err := databaseutil.ParseUUIDs([]string{customerID})
	if err != nil || len(parsed) != 1 {
		return PurchaseGate{}
	}
	customer := parsed[0]

	// An exempt customer is exempt everywhere, including here. Checking it
	// first also saves the Telegram calls.
	if exempt, err := queries.IsChannelExempt(ctx, customer); err == nil && exempt {
		return PurchaseGate{}
	}

	cached, err := queries.ListCustomerMemberships(ctx, customer)
	if err != nil {
		cached = nil
	}
	known := make(map[string]dbgen.ChannelMembership, len(cached))
	for _, membership := range cached {
		known[uuidText(membership.ChannelID)] = membership
	}

	gate := PurchaseGate{}
	now := time.Now().UTC()
	for _, channel := range channels {
		if !channel.RequireForPurchase {
			continue
		}
		invite := inviteFor(channel)
		if invite == "" {
			// A requirement the customer cannot satisfy is a wall. It is skipped
			// rather than enforced; the operator sees the channel listed without
			// an invite link in the panel.
			continue
		}

		member, answered := app.membershipNow(ctx, channel, known, telegramID, now)
		if answered && !member {
			gate.Missing = append(gate.Missing, ChannelRequirement{
				Title: channel.Title, InviteURL: invite,
			})
		}
	}
	return gate
}

// membershipNow resolves one channel's membership, preferring a recent cached
// answer and asking Telegram when there is not one.
func (app *App) membershipNow(
	ctx context.Context, channel dbgen.RequiredChannel,
	known map[string]dbgen.ChannelMembership, telegramID int64, now time.Time,
) (member bool, answered bool) {
	// The periodic worker keeps the cache warm, and re-asking Telegram on every
	// checkout is how a bot gets rate-limited at exactly the wrong moment.
	if cached, present := known[uuidText(channel.ID)]; present && cached.CheckedAt.Valid {
		if now.Sub(cached.CheckedAt.Time) < purchaseCacheWindow {
			switch cached.State {
			case channelgate.StateMember:
				return true, true
			case channelgate.StateAbsent:
				return false, true
			}
		}
	}

	callCtx, cancel := context.WithTimeout(ctx, purchaseCheckTimeout)
	defer cancel()

	member, err := app.membership.IsMember(callCtx, channel.TelegramChatID, telegramID)
	if err != nil {
		app.logger.Warn("purchase channel check failed",
			"channel", channel.Title, "error", err)
		return false, false
	}
	return member, true
}

func inviteFor(channel dbgen.RequiredChannel) string {
	if channel.InviteUrl.Valid && channel.InviteUrl.String != "" {
		return channel.InviteUrl.String
	}
	if channel.Username.Valid && channel.Username.String != "" {
		return "https://t.me/" + channel.Username.String
	}
	return ""
}
