package analytics

import (
	"context"
	"strings"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/omniflow/omniflow/internal/database/dbgen"
)

// Occasions on which a customer is asked why they are leaving.
const (
	// OccasionAutoRenewOff is the customer switching automatic renewal off.
	OccasionAutoRenewOff = "auto_renew_off"
	// OccasionLapsed is the customer answering the message that their
	// subscription expired.
	OccasionLapsed = "lapsed"
)

// ChurnReasons is the closed list a customer chooses from, in the order the
// question offers them. A closed list keeps anything a customer types out of
// the database and lets the answers be counted.
var ChurnReasons = []string{"price", "blocked", "speed", "not_needed", "switched", "technical", "other"}

// Surfaces an answer can come from.
const (
	SurfaceBot = "bot"
	SurfaceWeb = "web"
)

// RecordChurnReason stores a customer's answer. subscriptionID may be empty;
// when given it must be the customer's own, and an answer about somebody
// else's subscription is refused. Answering again for the same occasion on
// the same day replaces the answer.
func (service *Service) RecordChurnReason(ctx context.Context, customerID, subscriptionID, occasion, reason, surface string) error {
	if service == nil {
		return invalid("analytics is not available")
	}
	user, err := parseUUID(customerID)
	if err != nil {
		return err
	}
	if occasion != OccasionAutoRenewOff && occasion != OccasionLapsed {
		return invalid("%q is not an occasion", occasion)
	}
	reason = strings.TrimSpace(reason)
	known := false
	for _, candidate := range ChurnReasons {
		if candidate == reason {
			known = true
			break
		}
	}
	if !known {
		return invalid("%q is not a reason", reason)
	}
	if surface != SurfaceBot && surface != SurfaceWeb {
		return invalid("%q is not a surface", surface)
	}
	var subscription pgtype.UUID
	if subscriptionID = strings.TrimSpace(subscriptionID); subscriptionID != "" {
		if subscription, err = parseUUID(subscriptionID); err != nil {
			return err
		}
	}
	written, err := service.queries().RecordChurnFeedback(ctx, dbgen.RecordChurnFeedbackParams{
		UserID: user, SubscriptionID: subscription, Occasion: occasion, Reason: reason, Surface: surface,
	})
	if err != nil {
		return err
	}
	if written == 0 {
		return ErrNotFound
	}
	return nil
}

// ChurnReasonLine is how often one reason was given on one occasion.
type ChurnReasonLine struct {
	Occasion string `json:"occasion"`
	Reason   string `json:"reason"`
	Answers  int64  `json:"answers"`
}
