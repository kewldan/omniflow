package accountshop

import (
	"context"
	"errors"
	"strings"

	"github.com/omniflow/omniflow/internal/goods"
)

// Recipient is a handle that has been through the review step.
//
// It carries the normalised form and nothing else. No display name, no numeric
// identifier, no profile lookup: delivery is addressed by username and support
// needs to be able to answer "where did it go", and neither needs more. What is
// not collected cannot be retained, logged, or leaked.
type Recipient struct {
	// Username is the bare handle, without the @ or the profile URL a customer
	// may have pasted. It is what the confirmation screen shows and what the
	// gateway is given, and those being the same string is the point of the
	// review step.
	Username string
	// Checked reports that the product's own gateway confirmed the handle, not
	// merely that it is well-formed. Most adapters cannot do this, so a false
	// here is the normal case rather than a warning — the review step and the
	// recipient-invalid refund path exist precisely because the check is
	// usually unavailable.
	Checked bool
}

// Review normalises and validates what a customer typed.
//
// It answers with the exact handle that would be given to the gateway, so the
// customer confirms the string that will actually be used rather than the one
// they think they typed. A mistyped username is unrecoverable the moment a
// gateway has sent the goods, and this is the step that exists to catch it.
//
// The product is optional. When it names one, the adapter is asked to check the
// handle if it can; an adapter that cannot simply leaves `Checked` false, which
// is the state every adapter that ships today reports.
func (service *Service) Review(
	ctx context.Context, productID, input string,
) (Recipient, error) {
	if !service.Enabled() {
		return Recipient{}, ErrUnavailable
	}
	username, err := goods.NormalizeRecipient(input)
	if err != nil {
		return Recipient{}, ErrRecipientInvalid
	}
	if strings.TrimSpace(productID) == "" {
		return Recipient{Username: username}, nil
	}

	// A product that cannot be found is not a reason to refuse a handle that is
	// perfectly well-formed: the review step is about the username, and the
	// purchase will refuse the product on its own terms.
	product, err := service.product(ctx, productID, "")
	if err != nil {
		return Recipient{Username: username}, nil
	}
	provider, err := service.provider(ctx, product.ProviderSlug)
	if err != nil {
		return Recipient{Username: username}, nil
	}
	validator, ok := provider.(goods.RecipientValidator)
	if !ok {
		return Recipient{Username: username}, nil
	}
	if err = validator.ValidateRecipient(ctx, username); err != nil {
		if errors.Is(err, goods.ErrInvalidRecipient) {
			return Recipient{}, ErrRecipientInvalid
		}
		// The gateway could not answer. That is not the customer's fault and not
		// evidence against the handle, so the review proceeds unchecked rather
		// than refusing a username that is very likely fine.
		service.logger.Warn("recipient check was unavailable",
			"provider", product.ProviderSlug, "error", err)
		return Recipient{Username: username}, nil
	}
	return Recipient{Username: username, Checked: true}, nil
}

// reviewedRecipient reports the handle a purchase may use, or refuses.
//
// A purchase must present the normalised form the review step returned, not raw
// input. Requiring the two to be byte-identical is what makes the review step
// load-bearing rather than decorative: a client that skipped it submits
// something that does not match, and gets told to review instead of delivering
// on a single unconfirmed submission.
func reviewedRecipient(submitted string) (string, error) {
	trimmed := strings.TrimSpace(submitted)
	username, err := goods.NormalizeRecipient(trimmed)
	if err != nil {
		return "", ErrRecipientInvalid
	}
	if username != trimmed {
		return "", ErrRecipientNotReviewed
	}
	return username, nil
}
