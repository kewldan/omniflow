package accountpg

import (
	"context"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/omniflow/omniflow/internal/commerce"
	"github.com/omniflow/omniflow/internal/customer"
	"github.com/omniflow/omniflow/internal/database/dbgen"
)

// ConnectionClient is one documented client application with the link that
// imports this subscription into it.
type ConnectionClient struct {
	Name     string
	DeepLink string
}

// Connection is everything the "connect a device" screen needs.
//
// The subscription URL is included because the screen exists to hand it over —
// by QR code, by deep link, or by copy. What the panel must not do is let it
// leak sideways, which is a transport concern the API enforces with a no-store
// cache policy and a referrer policy on the response.
type Connection struct {
	SubscriptionURL string
	Platform        string
	Platforms       []string
	Clients         []ConnectionClient
}

// Connection builds the connection instructions for one platform.
//
// The client list comes from internal/commerce, which is the same table the bot
// renders from. That is deliberate: a customer who reads one recommendation in
// the chat and a different one in the browser has been handed two products.
func (service *Service) Connection(
	ctx context.Context, customerID, subscriptionID, platform string,
) (Connection, error) {
	subscription, err := service.SubscriptionURL(ctx, customerID, subscriptionID)
	if err != nil {
		return Connection{}, err
	}

	platforms := commerce.ConnectPlatforms()
	platform = strings.ToLower(strings.TrimSpace(platform))
	clients := commerce.ClientsForPlatform(platform)
	if len(clients) == 0 {
		platform = platforms[0]
		clients = commerce.ClientsForPlatform(platform)
	}

	connection := Connection{
		SubscriptionURL: subscription.SubscriptionURL,
		Platform:        platform,
		Platforms:       platforms,
		Clients:         make([]ConnectionClient, 0, len(clients)),
	}
	for _, client := range clients {
		connection.Clients = append(connection.Clients, ConnectionClient{
			Name: client.Name, DeepLink: client.DeepLink(subscription.SubscriptionURL),
		})
	}
	return connection, nil
}

// RotateSubscriptionLink issues a new access link and invalidates the old one.
//
// This is the most destructive thing a customer can do to their own working
// setup: every device stops connecting until the new link is imported. It is
// also the only remedy when a link has been shared or leaked, which is why it
// exists. The API gates it behind confirmation and a recent authentication; this
// function performs it and records that it happened.
func (service *Service) RotateSubscriptionLink(
	ctx context.Context, customerID, subscriptionID string, request SecurityRequest,
) (string, error) {
	row, err := service.subscriptionRecord(ctx, customerID, subscriptionID, "")
	if err != nil {
		return "", err
	}
	if row.RemnawaveID <= 0 {
		return "", ErrNotProvisioned
	}
	if service.remnawave == nil {
		return "", ErrRemnawaveUnavailable
	}

	if err = service.remnawave.RevokeSubscription(ctx, row.RemnawaveID); err != nil {
		return "", ErrRemnawaveUnavailable
	}
	// The new link is read back rather than assumed: Remnawave mints it, and
	// showing the customer the old one after a rotation would be worse than
	// showing none.
	rotated, err := service.remnawave.Subscription(ctx, row.RemnawaveID)
	if err != nil {
		return "", ErrRemnawaveUnavailable
	}

	// The event names the subscription and nothing else. Putting the link in the
	// customer's own security log would defeat the rotation it is recording.
	service.recordSecurity(ctx, customerID, "subscription_key_rotated", request, map[string]any{
		"subscription": row.Label,
	})
	return rotated.SubscriptionURL, nil
}

// UpdateProfile stores the customer's locale and timezone.
func (service *Service) UpdateProfile(
	ctx context.Context, customerID, locale, timezone string,
) (Customer, error) {
	if err := customer.ValidateLocaleTimezone(locale, timezone); err != nil {
		return Customer{}, fmt.Errorf("%w: %s", ErrInvalidInput, err.Error())
	}
	userID, err := parseUUID(customerID)
	if err != nil {
		return Customer{}, err
	}
	updated, err := dbgen.New(service.pool).UpdateAccountProfile(ctx, dbgen.UpdateAccountProfileParams{
		UserID: userID, Locale: locale, Timezone: timezone,
	})
	if err != nil {
		return Customer{}, ErrNotFound
	}
	return Customer{
		ID: customerID, Locale: updated.Locale, Timezone: updated.Timezone, Status: updated.Status,
	}, nil
}

// ---------------------------------------------------------------------------
// Shared helpers
// ---------------------------------------------------------------------------

func parseUUID(value string) (pgtype.UUID, error) {
	var id pgtype.UUID
	if err := id.Scan(strings.TrimSpace(value)); err != nil {
		return pgtype.UUID{}, fmt.Errorf("%w: malformed identifier", ErrNotFound)
	}
	return id, nil
}

func uuidString(id pgtype.UUID) string {
	if !id.Valid {
		return ""
	}
	value, err := id.Value()
	if err != nil {
		return ""
	}
	text, _ := value.(string)
	return text
}
