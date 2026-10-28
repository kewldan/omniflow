package httpapi

import "testing"

// The `next` value is sealed into the flow cookie and replayed as the
// post-sign-in Location. Every spelling a browser would resolve off-site, or
// onto the other surface, must come back empty.
func TestSafeNextPathRefusesEveryOffSiteSpelling(t *testing.T) {
	refused := []string{
		"", "admin", "https://evil.example/admin", "//evil.example",
		// A browser reads a backslash as a slash: this is //evil.example.
		"/\\evil.example", "/\\/evil.example", "/admin\\..\\..\\evil",
		// Tab and newline are stripped by browsers before parsing.
		"/\t/evil.example", "/\n/evil.example", "/admin\r\n",
		"/\x00/evil.example", "/\x7f",
		// Same site, wrong surface, including by dot segments.
		"/account", "/administrator", "/admin/../account", "/admin/../../evil",
		"/", "/api/v1",
	}
	for _, next := range refused {
		if got := safeNextPath(next, "/admin"); got != "" {
			t.Errorf("safeNextPath(%q, /admin) = %q, want empty", next, got)
		}
	}

	allowed := []string{"/admin", "/admin/", "/admin/finance", "/admin/customers?query=a%2Fb#top"}
	for _, next := range allowed {
		if got := safeNextPath(next, "/admin"); got != next {
			t.Errorf("safeNextPath(%q, /admin) = %q, want it unchanged", next, got)
		}
	}

	if got := safeNextPath("/account/security", "/account"); got != "/account/security" {
		t.Errorf("customer path refused: %q", got)
	}
	if got := safeNextPath("/admin", "/account"); got != "" {
		t.Errorf("an operator path was accepted on the customer surface: %q", got)
	}
}
