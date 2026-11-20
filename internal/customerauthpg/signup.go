package customerauthpg

import (
	"context"
	"errors"

	"github.com/omniflow/omniflow/internal/database/dbgen"
)

// ErrSignupClosed refuses to create a new customer while the access section
// admits new accounts by invitation only, or not at all. The web panel has no
// invitation link to carry, so only an existing customer can sign in there.
var ErrSignupClosed = errors.New("new accounts are by invitation only")

// createCustomer is the one place web sign-in creates a customer, so the
// access policy applies to every sign-in method alike.
func (service *Service) createCustomer(
	ctx context.Context, queries *dbgen.Queries, params dbgen.CreateCustomerForSignInParams,
) (dbgen.User, error) {
	if service.signupAllowed != nil && !service.signupAllowed(ctx) {
		return dbgen.User{}, ErrSignupClosed
	}
	return queries.CreateCustomerForSignIn(ctx, params)
}
