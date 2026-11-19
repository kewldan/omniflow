package customerauthpg

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/omniflow/omniflow/internal/customerauth"
	"github.com/omniflow/omniflow/internal/database/dbgen"
	"github.com/omniflow/omniflow/internal/mailer"
)

// ContactSealer encrypts a contact value and derives its lookup fingerprint.
// customerpg.Service implements it; it is the one place that decides how a
// contact is stored, so an address sealed here matches one stored anywhere
// else, the Bedolaga import included.
type ContactSealer interface {
	SealContact(kind, value string) (ciphertext, fingerprint []byte, err error)
}

// EmailSignInOptions configures sign-in by e-mailed link.
type EmailSignInOptions struct {
	Contacts ContactSealer
	Mailer   mailer.Sender
}

type emailSignIn struct {
	contacts ContactSealer
	mailer   mailer.Sender
}

var (
	// ErrEmailInvalid reports input that is not an e-mail address.
	ErrEmailInvalid = errors.New("that is not an e-mail address")
	emailPattern    = regexp.MustCompile(`^[^@\s]+@[^@\s]+\.[^@\s]+$`)
)

// EmailSignInEnabled reports whether the route is offered.
func (service *Service) EmailSignInEnabled() bool { return service.emailSignIn != nil }

// RequestEmailSignIn e-mails a one-time sign-in link.
//
// It answers the same way whether or not the address belongs to a customer:
// the form is public, and any difference would tell a stranger who has an
// account here. A known address gets a link to its customer; an unknown one
// gets a link that creates the customer when it is redeemed, so typing
// somebody else's address creates nothing. Both are throttled per address.
func (service *Service) RequestEmailSignIn(
	ctx context.Context, email, locale string, request RequestContext,
) error {
	if service.emailSignIn == nil {
		return customerauth.ErrMagicLinkUnavailable
	}
	email = strings.TrimSpace(email)
	if len(email) > 254 || !emailPattern.MatchString(email) {
		return ErrEmailInvalid
	}
	ciphertext, fingerprint, err := service.emailSignIn.contacts.SealContact("email", email)
	if err != nil {
		return err
	}
	queries := dbgen.New(service.pool)
	recent, err := queries.CountRecentEmailMagicLinks(ctx, dbgen.CountRecentEmailMagicLinksParams{
		EmailFingerprint: fingerprint, Lookback: interval(customerauth.MagicLinkRequestWindow),
	})
	if err != nil {
		return err
	}
	if recent >= customerauth.MagicLinkRequestLimit {
		return customerauth.ErrMagicLinkThrottled
	}
	owner := pgtype.UUID{}
	customer, err := queries.GetCustomerByEmailFingerprint(ctx, fingerprint)
	switch {
	case err == nil:
		if customer.Status != "active" {
			// Silently nothing: the answer must not differ from a sent link.
			return nil
		}
		owner = customer.ID
	case !errors.Is(err, pgx.ErrNoRows):
		return err
	}
	token, digest, err := customerauth.NewMagicLinkToken()
	if err != nil {
		return err
	}
	if _, err = queries.CreateCustomerEmailMagicLink(ctx, dbgen.CreateCustomerEmailMagicLinkParams{
		UserID: owner, TokenHash: digest, RequestedIp: request.IP,
		Lifetime: interval(customerauth.MagicLinkLifetime), EmailFingerprint: fingerprint, EmailCiphertext: ciphertext,
	}); err != nil {
		return err
	}
	link := service.publicURL + "/v1/account/auth/link?token=" + url.QueryEscape(token)
	if locale == "" {
		locale = customerauth.ResolveSignUpLocale("", request.AcceptLanguage)
	}
	return service.emailSignIn.mailer.Send(ctx, signInMessage(email, link, locale))
}

func signInMessage(to, link, locale string) mailer.Message {
	minutes := int(customerauth.MagicLinkLifetime.Minutes())
	if locale == "ru" {
		return mailer.Message{
			To: to, Subject: "Вход в личный кабинет",
			Text: fmt.Sprintf("Чтобы войти, откройте ссылку:\n\n%s\n\nСсылка действует %d минут и работает один раз. "+
				"Если вы не запрашивали вход, просто проигнорируйте это письмо.\n", link, minutes),
		}
	}
	return mailer.Message{
		To: to, Subject: "Sign in to your account",
		Text: fmt.Sprintf("Open this link to sign in:\n\n%s\n\nIt works once and expires in %d minutes. "+
			"If you did not ask to sign in, ignore this e-mail.\n", link, minutes),
	}
}

// emailLinkCustomer resolves who an e-mailed link signs in, creating the
// customer for an address nobody held when the link was issued. It runs in the
// redeeming transaction, so the link is spent only if the customer exists.
func (service *Service) emailLinkCustomer(
	ctx context.Context, queries *dbgen.Queries, link dbgen.CustomerMagicLink, request RequestContext,
) (dbgen.User, error) {
	if link.UserID.Valid {
		user, err := queries.GetCustomer(ctx, link.UserID)
		if err == nil && len(link.EmailFingerprint) > 0 {
			err = queries.MarkEmailContactVerified(ctx, dbgen.MarkEmailContactVerifiedParams{
				UserID: user.ID, ValueFingerprint: link.EmailFingerprint,
			})
		}
		return user, err
	}
	// Somebody may have taken the address since the link was sent.
	if existing, err := queries.GetCustomerByEmailFingerprint(ctx, link.EmailFingerprint); err == nil {
		return existing, nil
	} else if !errors.Is(err, pgx.ErrNoRows) {
		return dbgen.User{}, err
	}
	locale := customerauth.ResolveSignUpLocale("", request.AcceptLanguage)
	created, err := queries.CreateCustomerForSignIn(ctx, dbgen.CreateCustomerForSignInParams{Locale: locale, Timezone: "UTC"})
	if err != nil {
		return dbgen.User{}, err
	}
	if err = queries.EnsureBotPreferences(ctx, dbgen.EnsureBotPreferencesParams{UserID: created.ID, Locale: locale}); err != nil {
		return dbgen.User{}, err
	}
	err = queries.CreateVerifiedEmailContact(ctx, dbgen.CreateVerifiedEmailContactParams{
		UserID: created.ID, ValueCiphertext: link.EmailCiphertext, ValueFingerprint: link.EmailFingerprint,
	})
	return created, err
}
