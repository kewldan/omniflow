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

func TestClientsForPlatformIsShared(t *testing.T) {
	platforms := ConnectPlatforms()
	if len(platforms) == 0 {
		t.Fatal("no platforms are documented")
	}
	for _, platform := range platforms {
		clients := ClientsForPlatform(platform)
		if len(clients) == 0 {
			t.Fatalf("platform %q documents no client", platform)
		}
		for _, client := range clients {
			if client.Name == "" || client.Scheme == "" {
				t.Fatalf("platform %q has an incomplete client entry", platform)
			}
			link := client.DeepLink("https://example.test/sub/abc")
			if !strings.HasPrefix(link, client.Scheme) {
				t.Fatalf("deep link %q does not use the client's scheme", link)
			}
		}
	}
}

func TestClientsForPlatformIsCaseInsensitiveAndSafe(t *testing.T) {
	if len(ClientsForPlatform("  IOS ")) == 0 {
		t.Fatal("a platform with different casing was not recognised")
	}
	if ClientsForPlatform("symbian") != nil {
		t.Fatal("an undocumented platform returned clients")
	}
}

func TestDeepLinkIsEmptyWithoutASubscriptionURL(t *testing.T) {
	// A subscription that is not provisioned yet has no link, and building
	// "happ://add/" out of nothing would hand the customer a broken button.
	clients := ClientsForPlatform("ios")
	if got := clients[0].DeepLink(""); got != "" {
		t.Fatalf("deep link = %q, want empty", got)
	}
}

func TestClientsForPlatformReturnsACopy(t *testing.T) {
	first := ClientsForPlatform("ios")
	first[0].Name = "mutated"
	if ClientsForPlatform("ios")[0].Name == "mutated" {
		t.Fatal("a caller mutated the shared client table")
	}
}
