package accountreferral

import (
	"context"
	"regexp"
	"strings"

	"github.com/omniflow/omniflow/internal/database/dbgen"
)

// Attribution outcomes. The endpoint answers 200 with one of these rather than
// a problem, because none of them is something the customer did wrong: they
// followed a link, and the link either counted or it did not.
const (
	AttributionRecorded        = "recorded"
	AttributionAlreadyRecorded = "already_attributed"
	AttributionProgramOff      = "program_disabled"
	AttributionUnknownCode     = "unknown_code"
	AttributionSelf            = "self_referral"
	AttributionNotNew          = "not_new"
)

// referralCodePattern is the schema's own check on referral_codes.code.
var referralCodePattern = regexp.MustCompile(`^[A-Z0-9]{10}$`)

// AttributionResult reports what a referral code did for the signed-in
// customer.
type AttributionResult struct {
	Attributed bool
	Reason     string
}

// Attribute records the inviter behind a web sign-up, the way the bot records
// `/start ref_<code>`.
//
// The rule is the bot's — an immutable, first-write-wins pair keyed on the
// invited customer, self-referral impossible by construction — with two guards
// the bot does not apply and the programme's own terms imply:
//
//   - The programme has to be enabled. An attribution written while it is off
//     would sit waiting to pay out the day an operator turns it on for a
//     different audience.
//   - The customer has to be new: nobody who has already paid for anything is
//     "invited" by a link they followed afterwards. The qualification rule is
//     the first paid order, so such a row could never qualify honestly and
//     could only ever be an abuse path.
//
// The code is normalised the way the bot normalises it, so a code typed in
// lower case on either surface is the same code.
func (service *Service) Attribute(ctx context.Context, customerID, code string) (AttributionResult, error) {
	userID, err := parseUUID(customerID)
	if err != nil {
		return AttributionResult{}, err
	}
	code = strings.ToUpper(strings.TrimSpace(code))
	if !referralCodePattern.MatchString(code) {
		return AttributionResult{Reason: AttributionUnknownCode}, nil
	}

	program, err := service.program(ctx)
	if err != nil {
		return AttributionResult{}, err
	}
	if !program.Enabled {
		return AttributionResult{Reason: AttributionProgramOff}, nil
	}

	// One statement decides every reason and writes the row when none applies,
	// so two tabs racing on the same link produce one attribution and the same
	// answer for both.
	state, err := service.queries.AttributeAccountReferral(ctx, dbgen.AttributeAccountReferralParams{
		UserID: userID, Code: code,
	})
	if err != nil {
		return AttributionResult{}, err
	}

	switch {
	case state.Inserted:
		return AttributionResult{Attributed: true, Reason: AttributionRecorded}, nil
	case state.Already:
		return AttributionResult{Reason: AttributionAlreadyRecorded}, nil
	case !state.OwnerKnown:
		return AttributionResult{Reason: AttributionUnknownCode}, nil
	case state.IsSelf:
		return AttributionResult{Reason: AttributionSelf}, nil
	case state.HasPaid:
		return AttributionResult{Reason: AttributionNotNew}, nil
	default:
		// The insert found nothing to do for a reason the predicates above did
		// not name: a concurrent writer landed first. The row exists now.
		return AttributionResult{Reason: AttributionAlreadyRecorded}, nil
	}
}
