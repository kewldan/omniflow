package accountcheckout

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/omniflow/omniflow/internal/channelgate"
	"github.com/omniflow/omniflow/internal/database/dbgen"
)

// ErrChannelRequired reports a confirmation refused because the customer has
// not joined a channel the operator requires before a purchase. It carries the
// channels so the panel can say which, with a way to join each.
var ErrChannelRequired = errors.New("channel_required")

// ChannelRequirement is one channel the customer must join, as the panel
// renders it.
type ChannelRequirement struct {
	Title     string
	InviteURL string
}

// ChannelRefusal is the error value behind ErrChannelRequired.
type ChannelRefusal struct {
	Missing []ChannelRequirement
}

func (refusal ChannelRefusal) Error() string {
	return fmt.Sprintf("%v: %d channel(s) must be joined first", ErrChannelRequired, len(refusal.Missing))
}

// Is lets errors.Is(err, ErrChannelRequired) recognise the refusal.
func (ChannelRefusal) Is(target error) bool { return target == ErrChannelRequired }

// channelGateWindow is how long a recorded membership answer is trusted at
// confirmation. It is longer than the bot's five minutes because the web has
// no live check to fall back on: after it, an answer is treated as unknown,
// and unknown never blocks.
const channelGateWindow = 24 * time.Hour

// ChannelGate decides whether a confirmation may proceed against the required
// channels, for a customer the bot can reach.
//
// This is the same rule the bot applies before "Pay" — channelgate.Evaluate,
// with the purchase purpose — over the memberships the worker keeps recorded.
// What differs is the source of the answer. The bot asks Telegram live when
// its cache is stale; the API process holds no Telegram transport, so it
// reads only what has been recorded, and a channel nobody has checked this
// customer against recently is unknown rather than absent. The package's own
// rule covers that: unknown never blocks. The cost is that a customer refused
// here who then joins is let through once the worker's next sweep — or a tap
// on "I joined" in the bot — records the new state; the web page says so.
//
// A customer with no Telegram identity cannot be in a Telegram channel and
// cannot be asked to join one, so the gate does not apply to them at all.
func (service *Service) ChannelGate(ctx context.Context, customerID string) error {
	linked, err := service.store.HasTelegramIdentity(ctx, customerID)
	if err != nil {
		return err
	}
	if !linked {
		return nil
	}
	userID, err := parseUUID(customerID)
	if err != nil {
		return err
	}
	queries := dbgen.New(service.store.pool)
	channels, err := queries.ListEnabledChannels(ctx)
	if err != nil {
		return err
	}
	if len(channels) == 0 {
		return nil
	}
	exempt, err := queries.IsChannelExempt(ctx, userID)
	if err != nil {
		return err
	}
	recorded, err := queries.ListCustomerMemberships(ctx, userID)
	if err != nil {
		return err
	}

	rules := make([]channelgate.Channel, 0, len(channels))
	invites := make(map[string]ChannelRequirement, len(channels))
	for _, channel := range channels {
		invite := channelInvite(channel)
		if invite == "" {
			// A requirement the customer cannot satisfy is a wall. The bot skips
			// it too; the operator sees the channel without a link in the panel.
			continue
		}
		id := UUIDText(channel.ID)
		rules = append(rules, channelgate.Channel{
			ID: id, Enabled: channel.Enabled,
			RequireForPurchase:   channel.RequireForPurchase,
			RequireForActivation: channel.RequireForActivation,
		})
		invites[id] = ChannelRequirement{Title: channel.Title, InviteURL: invite}
	}
	memberships := make([]channelgate.Membership, 0, len(recorded))
	stale := channelgate.StaleBefore(service.clock(), channelGateWindow)
	for _, membership := range recorded {
		state := membership.State
		if !membership.CheckedAt.Valid || membership.CheckedAt.Time.Before(stale) {
			state = channelgate.StateUnknown
		}
		memberships = append(memberships, channelgate.Membership{
			ChannelID: UUIDText(membership.ChannelID), State: state,
			CheckedAt: membership.CheckedAt.Time,
		})
	}

	status := channelgate.Evaluate(rules, memberships, channelgate.PurposePurchase, exempt)
	if status.Compliant() {
		return nil
	}
	refusal := ChannelRefusal{Missing: make([]ChannelRequirement, 0, len(status.Missing))}
	for _, id := range status.Missing {
		refusal.Missing = append(refusal.Missing, invites[id])
	}
	return refusal
}

func channelInvite(channel dbgen.RequiredChannel) string {
	if channel.InviteUrl.Valid && channel.InviteUrl.String != "" {
		return channel.InviteUrl.String
	}
	if channel.Username.Valid && channel.Username.String != "" {
		return "https://t.me/" + channel.Username.String
	}
	return ""
}
