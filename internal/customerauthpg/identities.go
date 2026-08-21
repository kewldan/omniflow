package customerauthpg

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"
	"github.com/omniflow/omniflow/internal/customerauth"
	"github.com/omniflow/omniflow/internal/database/dbgen"
)

// SignInMethodView is one linked sign-in method as the customer sees it.
type SignInMethodView struct {
	ID       string
	Provider string
	// Label is the operator-configured display name for an OIDC provider, or
	// "Telegram". The external subject is never included: it identifies the
	// customer at somebody else's service and showing it back adds nothing.
	Label string
	// Removable is false for the only remaining method, so the panel can explain
	// why the control is disabled rather than failing the request afterwards.
	Removable bool
}

// HasTelegramIdentity reports whether the customer holds an active Telegram
// identity — the condition for anything that has to reach them in a chat, such
// as a Telegram Stars invoice.
func (service *Service) HasTelegramIdentity(ctx context.Context, customerID string) (bool, error) {
	userID, err := parseUUID(customerID)
	if err != nil {
		return false, err
	}
	rows, err := dbgen.New(service.pool).ListCustomerSignInIdentities(ctx, userID)
	if err != nil {
		return false, err
	}
	for _, row := range rows {
		if row.Provider == customerauth.ProviderTelegram {
			return true, nil
		}
	}
	return false, nil
}

// ListSignInMethods returns every way this customer can currently sign in.
func (service *Service) ListSignInMethods(
	ctx context.Context, customerID string,
) ([]SignInMethodView, error) {
	userID, err := parseUUID(customerID)
	if err != nil {
		return nil, err
	}
	queries := dbgen.New(service.pool)
	rows, err := queries.ListCustomerSignInIdentities(ctx, userID)
	if err != nil {
		return nil, err
	}

	// Provider display names are read once and reused, so a customer with three
	// linked providers does not cost three lookups.
	labels := map[string]string{}
	providers, err := queries.ListCustomerOIDCProviders(ctx)
	if err != nil {
		return nil, err
	}
	for _, provider := range providers {
		labels[provider.Slug] = provider.DisplayName
	}

	views := make([]SignInMethodView, 0, len(rows))
	for _, row := range rows {
		view := SignInMethodView{
			ID: uuidString(row.ID), Provider: row.Provider, Label: "Telegram",
			Removable: len(rows) > 1,
		}
		if slug, isOIDC := customerauth.OIDCSlug(row.Provider); isOIDC {
			view.Label = slug
			if name, known := labels[slug]; known && name != "" {
				view.Label = name
			}
		}
		views = append(views, view)
	}
	return views, nil
}

// UnlinkIdentity removes one sign-in method.
//
// The guard is the point of this function: a customer who removes their last
// method has locked themselves out of an account holding a paid subscription,
// and nothing in the panel can let them back in. The count is taken inside the
// transaction that performs the removal, so two tabs unlinking two different
// methods at once cannot both observe "two remain" and both succeed.
func (service *Service) UnlinkIdentity(
	ctx context.Context, customerID, identityID string, request RequestContext,
) error {
	userID, err := parseUUID(customerID)
	if err != nil {
		return err
	}
	target, err := parseUUID(identityID)
	if err != nil {
		return err
	}

	return service.inTx(ctx, func(queries *dbgen.Queries) error {
		rows, listErr := queries.ListCustomerSignInIdentities(ctx, userID)
		if listErr != nil {
			return listErr
		}
		methods := make([]customerauth.SignInMethod, 0, len(rows))
		for _, row := range rows {
			methods = append(methods, customerauth.SignInMethod{
				IdentityID: uuidString(row.ID), Provider: row.Provider,
			})
		}
		if guardErr := customerauth.CanUnlink(methods, identityID); guardErr != nil {
			if errors.Is(guardErr, customerauth.ErrMissingSubject) {
				return ErrNotFound
			}
			return guardErr
		}

		removed, revokeErr := queries.RevokeCustomerIdentity(ctx, dbgen.RevokeCustomerIdentityParams{
			IdentityID: target, UserID: userID,
		})
		if errors.Is(revokeErr, pgx.ErrNoRows) {
			return ErrNotFound
		}
		if revokeErr != nil {
			return revokeErr
		}

		metadata := map[string]any{"provider": removed.Provider}
		if slug, isOIDC := customerauth.OIDCSlug(removed.Provider); isOIDC {
			metadata["provider"] = slug
		}
		return service.appendSecurityEvent(ctx, queries, customerID, "identity_unlinked", request, metadata)
	})
}
