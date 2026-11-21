package commerce

import "testing"

func TestValidatePromotionEligibility(t *testing.T) {
	for raw, valid := range map[string]bool{
		``:                                  true,
		`{}`:                                true,
		`{"discountCooldownDays": 60}`:      true,
		`{"discountCooldownDays": 3650}`:    true,
		`{"discountCooldownDays": 0}`:       false,
		`{"discountCooldownDays": 1.5}`:     false,
		`{"discountCooldownDays": "60"}`:    false,
		`{"discountCooldownDays": 3651}`:    false,
		`{"minimumCompletedOrders": 2}`:     true,
		`{"minimumCompletedOrders": "two"}`: false,
		`{"newCustomerOnly": "yes"}`:        false,
		`{"locales": ["ru"]}`:               true,
		`[1, 2]`:                            false,
	} {
		if err := ValidatePromotionEligibility([]byte(raw)); (err == nil) != valid {
			t.Errorf("%s: valid=%t, got %v", raw, valid, err)
		}
	}
}
