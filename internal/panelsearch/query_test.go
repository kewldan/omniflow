package panelsearch

import (
	"bytes"
	"slices"
	"testing"

	"github.com/omniflow/omniflow/internal/accesscode"
	"github.com/omniflow/omniflow/internal/rbac"
)

func TestParseRejectsTooShortAndTooLong(t *testing.T) {
	for _, input := range []string{"", " ", "a", " b "} {
		if _, ok := Parse(input); ok {
			t.Fatalf("%q was searchable", input)
		}
	}
	long := make([]byte, MaxQueryLength+1)
	for index := range long {
		long[index] = 'a'
	}
	if _, ok := Parse(string(long)); ok {
		t.Fatal("an over-long query was searchable")
	}
}

func TestParseRecognisesIdentifierPrefixes(t *testing.T) {
	cases := []struct {
		input, low, high string
		exact            bool
	}{
		{"3F2A9C", "3f2a9c00-0000-0000-0000-000000000000", "3f2a9cff-ffff-ffff-ffff-ffffffffffff", false},
		{"3f2a9c1b-12", "3f2a9c1b-1200-0000-0000-000000000000", "3f2a9c1b-12ff-ffff-ffff-ffffffffffff", false},
		{
			"3f2a9c1b-1234-4abc-8def-0123456789ab",
			"3f2a9c1b-1234-4abc-8def-0123456789ab", "3f2a9c1b-1234-4abc-8def-0123456789ab", true,
		},
		{
			"3f2a9c1b12344abc8def0123456789ab",
			"3f2a9c1b-1234-4abc-8def-0123456789ab", "3f2a9c1b-1234-4abc-8def-0123456789ab", true,
		},
	}
	for _, testCase := range cases {
		query, ok := Parse(testCase.input)
		if !ok || query.ID == nil {
			t.Fatalf("%q was not read as an identifier", testCase.input)
		}
		if query.ID.Low != testCase.low || query.ID.High != testCase.high || query.ID.Exact != testCase.exact {
			t.Fatalf("%q → %+v", testCase.input, *query.ID)
		}
	}
	for _, input := range []string{"3f2a9", "abc-def123", "3f2a9c1g", "3f2a9c1b-1234-4abc-8def-0123456789abc"} {
		if query, _ := Parse(input); query.ID != nil {
			t.Fatalf("%q was read as an identifier: %+v", input, *query.ID)
		}
	}
}

func TestParseRecognisesTelegramIdentifiers(t *testing.T) {
	query, _ := Parse("123456789")
	if query.TelegramID != 123456789 {
		t.Fatalf("telegram id = %d", query.TelegramID)
	}
	// All digits is also a valid hexadecimal prefix; both readings run.
	if query.ID == nil {
		t.Fatal("a digit string long enough to be a prefix was not also read as one")
	}
	for _, input := range []string{"1234", "12345678901234567", "12a45"} {
		if query, _ := Parse(input); query.TelegramID != 0 {
			t.Fatalf("%q was read as a Telegram id", input)
		}
	}
}

func TestParseSeparatesEmailsFromHandles(t *testing.T) {
	query, _ := Parse("  Ivan.Petrov@Example.COM ")
	if query.Email != "ivan.petrov@example.com" || query.Handle != "" {
		t.Fatalf("email parse = %+v", query)
	}
	if query.Username != "" || query.Reference != "" {
		t.Fatal("an address was also offered as a username or reference")
	}
	query, _ = Parse("@ivan_petrov")
	if query.Handle != "ivan_petrov" || query.Email != "" {
		t.Fatalf("handle parse = %+v", query)
	}
	for _, input := range []string{"a@b@c", "user@", "two words@x.y"} {
		if query, _ := Parse(input); query.Email != "" {
			t.Fatalf("%q was read as an email", input)
		}
	}
}

func TestParseOffersUsernamesAndReferences(t *testing.T) {
	query, _ := Parse("omniflow_3f2a9c1b12344abc")
	if query.Username != "omniflow_3f2a9c1b12344abc" || query.Reference == "" {
		t.Fatalf("username parse = %+v", query)
	}
	query, _ = Parse("2f9c1a3e-000f-5000-9000-1b2c3d4e5f6a")
	if query.Reference == "" {
		t.Fatal("a provider reference shaped like a UUID was not offered as one")
	}
	query, _ = Parse("refund please")
	if query.Username != "" || query.Reference != "" {
		t.Fatalf("free text was offered as an identifier: %+v", query)
	}
}

func TestParseEscapesSubjectWildcards(t *testing.T) {
	query, _ := Parse(`100%_off\`)
	if query.SubjectPattern != `%100\%\_off\\%` {
		t.Fatalf("pattern = %q", query.SubjectPattern)
	}
	if query, _ := Parse("ab"); query.SubjectPattern != "" {
		t.Fatal("a two-character fragment would read every ticket")
	}
}

func TestParseHashesCodesTheWayTheyAreStored(t *testing.T) {
	code, _, err := accesscode.New()
	if err != nil {
		t.Fatalf("new code: %v", err)
	}
	grouped := code[:4] + "-" + code[4:8] + "-" + code[8:12] + "-" + code[12:]
	query, _ := Parse(grouped)
	if !bytes.Equal(query.CodeHash, accesscode.Hash(code)) {
		t.Fatal("a grouped code did not hash to the stored digest")
	}
	if query, _ := Parse("not a code"); query.CodeHash != nil {
		t.Fatal("free text produced a code hash")
	}
}

func TestVisibleGroupsFollowTheRole(t *testing.T) {
	cases := map[rbac.Role][]Group{
		rbac.RoleOwner:     Groups,
		rbac.RoleSupport:   {GroupCustomers, GroupTickets, GroupGifts, GroupAccessCodes},
		rbac.RoleFinance:   {GroupCustomers, GroupOrders, GroupGifts, GroupAccessCodes},
		rbac.RoleMarketing: {GroupCustomers, GroupAccessCodes},
		rbac.RoleAuditor:   Groups,
	}
	for role, want := range cases {
		if got := Visible(rbac.NewGrant(role)); !slices.Equal(got, want) {
			t.Fatalf("%s sees %v, want %v", role, got, want)
		}
	}
	if got := Visible(rbac.NewGrant()); len(got) != 0 {
		t.Fatalf("an empty grant sees %v", got)
	}
}
