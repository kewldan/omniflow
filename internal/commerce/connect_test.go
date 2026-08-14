package commerce

import (
	"strings"
	"testing"
)

func TestDeviceHandleNeverContainsTheHardwareIdentifier(t *testing.T) {
	const hwid = "private-hwid-that-must-not-leave-the-server"
	handle := DeviceHandle(hwid)

	if strings.Contains(handle, hwid) {
		t.Fatal("the handle carries the hardware identifier")
	}
	// A short prefix would be enough to correlate two lists, so the check is that
	// nothing recognisable survives.
	if strings.Contains(handle, hwid[:12]) {
		t.Fatal("the handle carries a recognisable prefix of the hardware identifier")
	}
	if handle == "" {
		t.Fatal("the handle is empty")
	}
}

func TestDeviceHandleIsStableAndDistinct(t *testing.T) {
	// Stability is what lets a handle survive between the read that showed the
	// device and the request that removes it.
	if DeviceHandle("device-a") != DeviceHandle("device-a") {
		t.Fatal("the same device produced two different handles")
	}
	if DeviceHandle("device-a") == DeviceHandle("device-b") {
		t.Fatal("two devices produced the same handle")
	}
}

func TestDeepLinkIsEmptyWithoutASubscriptionURL(t *testing.T) {
	// A subscription that is not provisioned yet has no link, and building
	// "happ://add/" out of nothing would hand the customer a broken button.
	client := ClientApp{Name: "Happ", Scheme: "happ://add/"}
	if got := client.DeepLink(""); got != "" {
		t.Fatalf("deep link = %q, want empty", got)
	}
	if got := client.DeepLink("https://example.test/sub/abc"); !strings.HasPrefix(got, client.Scheme) {
		t.Fatalf("deep link %q does not use the client's scheme", got)
	}
}

// The scheme is now operator-supplied, and it ends up in the href of an anchor
// in the customer web panel. This is the test that stands between an operator
// and stored cross-site scripting on a page that holds a session cookie.
func TestValidateClientSchemeRefusesAnythingThatIsNotAnImportScheme(t *testing.T) {
	for _, scheme := range []string{
		"happ://add/", "v2raytun://import/", "hiddify://import/",
		"streisand://import/", "clash://install-config/",
	} {
		if err := ValidateClientScheme(scheme); err != nil {
			t.Errorf("a real import scheme was refused: %q: %v", scheme, err)
		}
	}

	for _, scheme := range []string{
		"", "happ", "happ:add/", "//add/",
		// The four that execute or read local state. javascript: in an href
		// runs; the others are the neighbours it travels with.
		"javascript://alert(1)", "JavaScript://x", "data://text/html,x",
		"vbscript://x", "file:///etc/passwd",
		// A scheme that closes the attribute and opens another one.
		`happ://add/" onclick="alert(1)`,
		"happ://add/><script>alert(1)</script>",
		// Whitespace, which a browser strips before parsing a URL.
		"java\tscript://x", "happ://add/ x",
	} {
		if err := ValidateClientScheme(scheme); err == nil {
			t.Errorf("%q was accepted as an import scheme", scheme)
		}
	}
}

func TestValidateDownloadURLRequiresHTTPS(t *testing.T) {
	// Absent is fine: an operator who has not said where to get the application
	// has not made a mistake.
	if err := ValidateDownloadURL(""); err != nil {
		t.Errorf("an empty download address was refused: %v", err)
	}
	if err := ValidateDownloadURL("https://apps.example.test/happ"); err != nil {
		t.Errorf("an https address was refused: %v", err)
	}
	for _, address := range []string{
		"http://apps.example.test/happ",
		"javascript:alert(1)",
		"//apps.example.test/happ",
		"https://apps.example.test/a b",
	} {
		if err := ValidateDownloadURL(address); err == nil {
			t.Errorf("%q was accepted as a download address", address)
		}
	}
}

func TestNormaliseClientSchemeIsCaseInsensitive(t *testing.T) {
	if got := NormaliseClientScheme("  HAPP://Add/ "); got != "happ://add/" {
		t.Fatalf("normalised to %q", got)
	}
}
