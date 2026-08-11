package customerauth

import (
	"errors"
	"strings"
)

// ProviderTelegram is the `identities.provider` value for a Telegram account.
// It is the same value the bot has always written, which is what makes signing
// in on the web land on the account the customer already has.
const ProviderTelegram = "telegram"

// oidcProviderPrefix namespaces external OIDC subjects inside the one identity
// table. Two providers may legitimately issue the same `sub`, so the slug has to
// be part of the stored provider for the table's UNIQUE (provider, subject) to
// mean "one external account, one customer".
const oidcProviderPrefix = "oidc:"

var (
	// ErrUnverifiedEmail reports a provider that asserted an address it has not
	// verified, where the provider is configured to require verification.
	ErrUnverifiedEmail = errors.New("identity provider did not verify the email address")
	// ErrMissingSubject reports an ID token with no usable subject claim.
	ErrMissingSubject = errors.New("identity provider returned no subject")
	// ErrIdentityTaken reports an external identity already linked to a
	// different customer.
	ErrIdentityTaken = errors.New("identity is already linked to another account")
	// ErrLastSignInMethod reports an unlink that would leave the customer with
	// no way back in.
	ErrLastSignInMethod = errors.New("cannot remove the last sign-in method")
)

// OIDCProvider returns the `identities.provider` value for one configured OIDC
// provider slug.
func OIDCProvider(slug string) string { return oidcProviderPrefix + slug }

// OIDCSlug reverses OIDCProvider, reporting false for any other provider.
func OIDCSlug(provider string) (string, bool) {
	slug, found := strings.CutPrefix(provider, oidcProviderPrefix)
	return slug, found && slug != ""
}

// SignInMethod is one way a customer can currently authenticate.
type SignInMethod struct {
	IdentityID string
	Provider   string
	// Label is what the customer sees: "Telegram", or the provider's configured
	// display name. It is never the subject, which is an external identifier
	// with no meaning to the person reading it.
	Label string
}

// CanUnlink reports whether removing one sign-in method leaves the customer able
// to sign in.
//
// The rule is deliberately about the count of remaining methods rather than
// about which method is being removed: a customer with Telegram plus Google may
// drop either, and a customer with only one may drop neither. Refusing here is
// the difference between an account the owner can still reach and one only an
// operator can recover.
func CanUnlink(methods []SignInMethod, identityID string) error {
	remaining := 0
	found := false
	for _, method := range methods {
		if method.IdentityID == identityID {
			found = true
			continue
		}
		remaining++
	}
	if !found {
		return ErrMissingSubject
	}
	if remaining == 0 {
		return ErrLastSignInMethod
	}
	return nil
}

// Claims is the subset of an OIDC ID token this application acts on.
type Claims struct {
	Subject       string
	Email         string
	EmailVerified bool
	Name          string
	Locale        string
}

// ResolveClaims applies a provider's configured trust rules to its claims.
//
// Two things are deliberately not done here. The email is never used to find an
// existing customer — only the subject is, because matching on an address would
// let any provider willing to assert someone else's address take over their
// account. And an address that changes upstream is simply recorded anew: since
// the subject is the key, an email change is not an identity change and needs no
// recovery path at all.
func ResolveClaims(claims Claims, requireVerifiedEmail bool) (Claims, error) {
	claims.Subject = strings.TrimSpace(claims.Subject)
	if claims.Subject == "" {
		return Claims{}, ErrMissingSubject
	}
	claims.Email = strings.ToLower(strings.TrimSpace(claims.Email))
	if requireVerifiedEmail && (claims.Email == "" || !claims.EmailVerified) {
		return Claims{}, ErrUnverifiedEmail
	}
	return claims, nil
}
