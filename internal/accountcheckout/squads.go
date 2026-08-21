package accountcheckout

import (
	"errors"
	"strings"

	"github.com/omniflow/omniflow/internal/commerce"
)

// SquadSelectionState reports whether the customer's server choice still
// stands between the checkout and a price.
//
// A plan whose squads are required, or optional with a minimum, opens with no
// selection. That is not an error in the checkout — it is the first thing the
// screen asks for — so the view carries the state instead of failing: the
// configurator renders, the quote is withheld, and Confirm stays disabled until
// the server says the selection resolves.
type SquadSelectionState struct {
	// Required is true while the selection does not resolve and no quote can
	// be computed.
	Required bool
	// Reason is the stable commerce reason — squad_selection_required,
	// squad_selection_too_few — that the panel explains in the customer's
	// language. Empty when Required is false.
	Reason string
}

// SquadSelectionIncomplete recognises a quote that failed only because the
// customer has not finished choosing servers, and returns the reason.
//
// Every other failure — including a selection that can never be valid, which
// Update refuses before it is stored — is reported false, so the caller treats
// it as the error it is.
func SquadSelectionIncomplete(err error) (string, bool) {
	if err == nil || !errors.Is(err, commerce.ErrSquadSelection) {
		return "", false
	}
	switch reason := squadReason(err); reason {
	case commerce.SquadSelectionRequired, commerce.SquadSelectionTooFew:
		return reason, true
	default:
		return "", false
	}
}

// ValidateSquadEdit refuses a selection that could never become valid, before
// it is stored against the checkout.
//
// It deliberately accepts an incomplete one. The panel sends the whole set on
// every tap, so a plan that wants two servers necessarily passes through a
// state with one; refusing that would make the minimum unreachable. What it
// refuses is a server the plan does not offer, more servers than the plan
// allows, and any selection at all on a plan that assigns its servers
// automatically — each of which the order would refuse later with no way for
// the customer to repair it from the screen.
func ValidateSquadEdit(offer SquadOffer, selected []string) error {
	policy := commerce.SquadPolicy{
		Selection: offer.Selection, Minimum: 0, Maximum: offer.Maximum,
		Offered: make([]string, 0, len(offer.Offered)),
	}
	for _, squad := range offer.Offered {
		policy.Offered = append(policy.Offered, squad.SquadID)
	}
	if policy.Selection == "" {
		policy.Selection = "automatic"
	}
	if policy.Selection == "automatic" && len(selected) == 0 {
		return nil
	}
	_, err := policy.ResolveSquads(selected)
	if err == nil {
		return nil
	}
	switch squadReason(err) {
	case commerce.SquadSelectionRequired, commerce.SquadSelectionTooFew:
		// Unreachable with Minimum zero, kept for the day the policy grows a
		// reason this function has not seen: an incomplete selection is stored.
		return nil
	default:
		return err
	}
}

// squadReason recovers the stable reason a wrapped commerce error carries
// after its final ": ".
func squadReason(err error) string {
	message := err.Error()
	if index := strings.LastIndex(message, ": "); index >= 0 {
		return message[index+2:]
	}
	return ""
}

// incompleteQuote is the quote shown while the selection is unresolved: the
// settlement currency and nothing else, so the screen knows what it will be
// priced in and the breakdown has nothing to mislead with.
func incompleteQuote(session Session) commerce.CheckoutQuote {
	return commerce.CheckoutQuote{
		Subtotal:       commerce.Money{Currency: session.Currency},
		PromoCode:      session.PromoCode,
		PromoRejection: session.PromoRejection,
	}
}
