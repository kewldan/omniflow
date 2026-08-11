package panelpg

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/omniflow/omniflow/internal/database/dbgen"
)

// ReferralProgram is the operator-configured invite scheme.
//
// It is a singleton because a second programme would mean two answers to "what
// does an invite earn?", and the customer would already have been told one of
// them.
type ReferralProgram struct {
	Enabled  bool   `json:"enabled"`
	Currency string `json:"currency"`

	InviterRewardMinor int64 `json:"inviterRewardMinor"`
	InviteeRewardMinor int64 `json:"inviteeRewardMinor"`

	// Qualification is what has to happen before a reward is granted. It is a
	// closed set: every value here is a rule the commerce code implements, and
	// a free-text field would be a promise nothing enforces.
	Qualification string `json:"qualification"`

	// InviterRewardCap bounds how many rewards one inviter may earn. Nil means
	// no cap, which an owner chooses explicitly — an uncapped referral scheme is
	// the one that gets farmed.
	InviterRewardCap *int32 `json:"inviterRewardCap,omitempty"`
	// AttributionValidityDays is how long an invite stays attributable. Beyond
	// it the invitee is an ordinary customer, which is what stops a code from
	// earning on a purchase two years later.
	AttributionValidityDays int32 `json:"attributionValidityDays"`
	// RewardExpiryDays bounds how long a granted reward stays spendable.
	RewardExpiryDays *int32    `json:"rewardExpiryDays,omitempty"`
	TermsURL         string    `json:"termsUrl,omitempty"`
	UpdatedAt        time.Time `json:"updatedAt,omitzero"`

	// Record is what the current configuration has actually produced, so an
	// operator changing a reward can see what the last one did. It is named
	// separately from the per-customer ReferralSummary because they answer
	// different questions and conflating them would put one customer's figures
	// on a settings screen.
	Record ReferralRecord `json:"record"`
}

// ReferralRecord is the programme's totals to date.
type ReferralRecord struct {
	Attributed    int64 `json:"attributed"`
	Qualified     int64 `json:"qualified"`
	Rejected      int64 `json:"rejected"`
	RewardedMinor int64 `json:"rewardedMinor"`
}

// qualifications the commerce code implements. The panel offers exactly these.
var qualifications = map[string]bool{"first_paid_order": true}

// ReferralProgram reads the configuration and its record.
func (service *Service) ReferralProgram(ctx context.Context) (ReferralProgram, error) {
	queries := service.queries()

	row, err := queries.GetReferralProgram(ctx)
	program := ReferralProgram{
		// An installation that has never configured a programme gets the
		// disabled defaults rather than a missing-row error: the settings screen
		// has to render something, and "off" is the honest rendering.
		Enabled: false, Currency: "RUB",
		Qualification: "first_paid_order", AttributionValidityDays: 90,
	}
	if err == nil {
		program.Enabled = row.Enabled
		program.Currency = row.Currency
		program.InviterRewardMinor = row.InviterRewardMinor
		program.InviteeRewardMinor = row.InviteeRewardMinor
		program.Qualification = row.Qualification
		program.InviterRewardCap = int32Pointer(row.InviterRewardCap)
		program.AttributionValidityDays = row.AttributionValidityDays
		program.RewardExpiryDays = int32Pointer(row.RewardExpiryDays)
		program.TermsURL = row.TermsUrl.String
		program.UpdatedAt = row.UpdatedAt.Time
	} else if !errors.Is(err, pgx.ErrNoRows) {
		return ReferralProgram{}, err
	}

	summary, err := queries.ReferralProgramSummary(ctx)
	if err == nil {
		program.Record = ReferralRecord{
			Attributed: summary.Attributed, Qualified: summary.Qualified,
			Rejected: summary.Rejected, RewardedMinor: summary.RewardedMinor,
		}
	}
	return program, nil
}

// SaveReferralProgram stores the configuration.
//
// Enabling a programme with no reward on either side is refused. It would be a
// scheme that promises customers something and pays nothing, which is worse
// than having no scheme: the invites still go out.
func (service *Service) SaveReferralProgram(
	ctx context.Context, program ReferralProgram, actor Actor,
) (ReferralProgram, error) {
	if !qualifications[program.Qualification] {
		return ReferralProgram{}, ErrValidaton
	}
	if len(program.Currency) != 3 || program.Currency != strings.ToUpper(program.Currency) {
		return ReferralProgram{}, ErrValidaton
	}
	if program.InviterRewardMinor < 0 || program.InviteeRewardMinor < 0 {
		return ReferralProgram{}, ErrValidaton
	}
	if program.Enabled && program.InviterRewardMinor == 0 && program.InviteeRewardMinor == 0 {
		return ReferralProgram{}, ErrValidaton
	}
	if program.AttributionValidityDays <= 0 {
		return ReferralProgram{}, ErrValidaton
	}
	if program.InviterRewardCap != nil && *program.InviterRewardCap <= 0 {
		return ReferralProgram{}, ErrValidaton
	}
	if program.RewardExpiryDays != nil && *program.RewardExpiryDays <= 0 {
		return ReferralProgram{}, ErrValidaton
	}

	err := service.inTx(ctx, func(queries *dbgen.Queries) error {
		if _, err := queries.SaveReferralProgram(ctx, dbgen.SaveReferralProgramParams{
			Enabled: program.Enabled, Currency: program.Currency,
			InviterRewardMinor:      program.InviterRewardMinor,
			InviteeRewardMinor:      program.InviteeRewardMinor,
			Qualification:           program.Qualification,
			InviterRewardCap:        optionalInt4(program.InviterRewardCap),
			AttributionValidityDays: program.AttributionValidityDays,
			RewardExpiryDays:        optionalInt4(program.RewardExpiryDays),
			TermsUrl:                optionalText(program.TermsURL),
		}); err != nil {
			return err
		}
		return appendAudit(ctx, queries, actor.audit(
			"referral.program.saved", "marketing", "referral_program", "singleton",
			map[string]any{
				"enabled": program.Enabled, "currency": program.Currency,
				"inviterRewardMinor": program.InviterRewardMinor,
				"inviteeRewardMinor": program.InviteeRewardMinor,
				"cap":                program.InviterRewardCap,
			},
		))
	})
	if err != nil {
		return ReferralProgram{}, err
	}
	return service.ReferralProgram(ctx)
}
