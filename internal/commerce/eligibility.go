package commerce

import (
	"encoding/json"
	"errors"
	"math"
)

// EligibilityDiscountCooldownDays is the promotion eligibility key behind "a
// discount at most once in N days": a customer who had any discount in that
// many days — from any promotion, or from a previous bot before a migration —
// cannot redeem this one.
const EligibilityDiscountCooldownDays = "discountCooldownDays"

// MaxDiscountCooldownDays bounds the cooldown at ten years.
const MaxDiscountCooldownDays = 3650

// ErrEligibilityInvalid reports an eligibility document the checkout could not
// read. It is refused when the promotion is saved: a value the database cannot
// cast would otherwise fail the eligibility check for every customer.
var ErrEligibilityInvalid = errors.New("invalid promotion eligibility")

// ValidatePromotionEligibility checks the parts of a promotion's eligibility
// document that are read as numbers or flags. Keys it does not know are left
// alone, as the document always allowed.
func ValidatePromotionEligibility(raw []byte) error {
	if len(raw) == 0 {
		return nil
	}
	var document map[string]any
	if err := json.Unmarshal(raw, &document); err != nil {
		return ErrEligibilityInvalid
	}
	if value, ok := document[EligibilityDiscountCooldownDays]; ok {
		days, isNumber := value.(float64)
		if !isNumber || days != math.Trunc(days) || days < 1 || days > MaxDiscountCooldownDays {
			return ErrEligibilityInvalid
		}
	}
	if value, ok := document["minimumCompletedOrders"]; ok {
		count, isNumber := value.(float64)
		if !isNumber || count != math.Trunc(count) || count < 0 || count > math.MaxInt32 {
			return ErrEligibilityInvalid
		}
	}
	if value, ok := document["newCustomerOnly"]; ok {
		if _, isBool := value.(bool); !isBool {
			return ErrEligibilityInvalid
		}
	}
	return nil
}
