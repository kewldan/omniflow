package customerauthpg

import (
	"context"
	"errors"
	"net/url"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/omniflow/omniflow/internal/customerauth"
	"github.com/omniflow/omniflow/internal/database/dbgen"
)

// ErrTelegramAlreadyLinked refuses a second Telegram account on one customer.
// The bot, notifications, and the Remnawave mapping all key on a single
// Telegram ID; a second one would leave it ambiguous which chat a renewal
// reminder goes to.
var ErrTelegramAlreadyLinked = errors.New("a telegram account is already linked")

// LinkTelegramIdentity attaches the Telegram account a Login Widget payload
// proves to the customer already signed in.
//
// It is the Telegram counterpart of LinkOIDCIdentity and follows the same
// rule: an identity that belongs to somebody else is refused, never moved.
// Joining two existing accounts is what the account merge does, with a
// preview; doing it here as a side effect would move one person's orders and
// wallet into another's account.
func (service *Service) LinkTelegramIdentity(
	ctx context.Context, customerID string, values url.Values, request RequestContext,
) error {
	if service.botToken == "" {
		return ErrTelegramUnset
	}
	identity, err := customerauth.VerifyLoginWidget(
		values, service.botToken, service.now(), customerauth.TelegramMaxAge,
	)
	if err != nil {
		return ErrSignInRejected
	}
	userID, err := parseUUID(customerID)
	if err != nil {
		return err
	}
	subject := identity.Subject()

	return service.inTx(ctx, func(queries *dbgen.Queries) error {
		// The same lock the bot and widget sign-in take, so a first /start from
		// this Telegram account cannot create a second customer while it is
		// being attached here.
		if err := queries.LockTelegramSubject(ctx, subject); err != nil {
			return err
		}
		existing, lookupErr := queries.GetIdentityBySubjectAnyStatus(ctx, dbgen.GetIdentityBySubjectAnyStatusParams{
			Provider: customerauth.ProviderTelegram, ProviderSubject: subject,
		})
		// Another customer's row is refused unless it is freed: unlinked by an
		// operator, or replaced by a different Telegram account on that
		// customer. A freed row is nobody's, and proving the Telegram account
		// here is what makes it this customer's.
		claim := lookupErr == nil && existing.UserID != userID && existing.Freed
		switch {
		case lookupErr == nil && existing.UserID != userID && !claim:
			return customerauth.ErrIdentityTaken
		case lookupErr == nil && existing.Status == "active":
			// Already this customer's: linking again is a harmless no-op.
			return nil
		case lookupErr != nil && !errors.Is(lookupErr, pgx.ErrNoRows):
			return lookupErr
		}

		// A Remnawave mapping already addressed by this Telegram ID belongs to
		// whoever holds it; it is not this customer's to take.
		mapped, mapErr := queries.GetCustomerByTelegramMapping(ctx, pgtype.Int8{Int64: identity.ID, Valid: true})
		switch {
		case mapErr == nil && mapped.ID != userID && !(claim && mapped.ID == existing.UserID):
			return customerauth.ErrIdentityTaken
		case mapErr != nil && !errors.Is(mapErr, pgx.ErrNoRows):
			return mapErr
		}

		linked, listErr := queries.ListCustomerIdentities(ctx, userID)
		if listErr != nil {
			return listErr
		}
		for _, row := range linked {
			if row.Provider == customerauth.ProviderTelegram && row.Status == "active" {
				return ErrTelegramAlreadyLinked
			}
		}

		verifiedAt := pgtype.Timestamptz{Time: service.now(), Valid: true}
		if claim {
			if _, err := queries.ClaimFreedIdentity(ctx, dbgen.ClaimFreedIdentityParams{
				UserID: userID, IdentityID: existing.ID, VerifiedAt: verifiedAt,
			}); err != nil {
				if errors.Is(err, pgx.ErrNoRows) {
					return customerauth.ErrIdentityTaken
				}
				return err
			}
		} else if lookupErr == nil {
			// This customer's own unlinked row comes back rather than a new
			// one being written beside it.
			if _, err := queries.ReactivateCustomerIdentity(ctx, dbgen.ReactivateCustomerIdentityParams{
				IdentityID: existing.ID, UserID: userID, VerifiedAt: verifiedAt,
			}); err != nil {
				if errors.Is(err, pgx.ErrNoRows) {
					return customerauth.ErrIdentityTaken
				}
				return err
			}
		} else if _, err := queries.LinkCustomerIdentity(ctx, dbgen.LinkCustomerIdentityParams{
			UserID: userID, Provider: customerauth.ProviderTelegram, ProviderSubject: subject,
			VerifiedAt: verifiedAt, Metadata: []byte("{}"),
		}); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return customerauth.ErrIdentityTaken
			}
			return err
		}
		if err := queries.EnsureBotPreferences(ctx, dbgen.EnsureBotPreferencesParams{
			UserID: userID, Locale: customerauth.ResolveSignUpLocale(identity.LanguageCode, request.AcceptLanguage),
		}); err != nil {
			return err
		}
		if err := queries.BackfillTelegramMapping(ctx, dbgen.BackfillTelegramMappingParams{
			TelegramID: pgtype.Int8{Int64: identity.ID, Valid: true}, UserID: userID,
		}); err != nil {
			return err
		}
		if err := queries.ReplaceTelegramMapping(ctx, dbgen.ReplaceTelegramMappingParams{
			TelegramID: pgtype.Int8{Int64: identity.ID, Valid: true}, UserID: userID,
		}); err != nil {
			return err
		}
		return service.appendSecurityEvent(ctx, queries, customerID, "identity_linked", request,
			map[string]any{"provider": customerauth.ProviderTelegram})
	})
}
