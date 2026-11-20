package botapp

import (
	"testing"

	"github.com/omniflow/omniflow/internal/installsettings"
)

func TestRegistrationAccess(t *testing.T) {
	invite := installsettings.Document{"mode": "invite", "inviteCodes": "alpha,beta", "allowedCampaigns": []any{"spring"}}
	cases := []struct {
		access installsettings.Document
		start  string
		want   bool
	}{
		{installsettings.Document{}, "/start", true},
		{installsettings.Document{"mode": "closed"}, "/start ref_ABC", false},
		{invite, "/start", false},
		{invite, "/start ref_ABC", true},
		{installsettings.Document{"mode": "invite", "allowReferrals": false}, "/start ref_ABC", false},
		{invite, "/start c_spring", true},
		{invite, "/start c_autumn", false},
		{invite, "/start inv_beta", true},
		{invite, "/start inv_gamma", false},
	}
	for _, testCase := range cases {
		if got := admitted(testCase.access, testCase.start); got != testCase.want {
			t.Errorf("admitted(%v, %q) = %v", testCase.access, testCase.start, got)
		}
	}
}
