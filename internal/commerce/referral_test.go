package commerce

import "testing"

// A customer's "first order" is their first subscription order. Money moved
// into a wallet, a shop purchase, a gift for somebody else, or a code a
// distributor paid for is not the purchase a referral or a welcome offer is
// about, and must neither qualify one nor block one.
func TestOnlySubscriptionOrdersCountAsAFirstOrder(t *testing.T) {
	for operation, want := range map[string]bool{
		"purchase": true, "extension": true, "renewal": true, "upgrade": true, "downgrade": true,
		"topup": false, "goods": false, "gift": false, "code": false, "addon": false, "": false,
	} {
		if got := SubscriptionOperation(operation); got != want {
			t.Errorf("SubscriptionOperation(%q) = %v, want %v", operation, got, want)
		}
	}
}
