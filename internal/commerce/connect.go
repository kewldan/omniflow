package commerce

import (
	"crypto/sha256"
	"encoding/base64"
	"strings"
)

// Connection guidance is domain data rather than presentation: which client
// applications an installation documents, and how each one imports a
// subscription. The bot and the customer web panel both render it, and they must
// not be able to disagree — a customer who reads "install Happ" in the chat and
// something else in the browser has been given two different products.

// ClientApp is one recommended client for a platform, together with the scheme
// that imports a subscription into it.
type ClientApp struct {
	Name   string `json:"name"`
	Scheme string `json:"-"`
}

// DeepLink builds the import URL for one client.
//
// It is offered as copyable text as well as a link because Telegram inline
// buttons accept only http, https, and tg URLs, and because a desktop browser
// may have no handler registered for the scheme. Copying is the documented
// fallback and works everywhere.
func (app ClientApp) DeepLink(subscriptionURL string) string {
	if subscriptionURL == "" {
		return ""
	}
	return app.Scheme + subscriptionURL
}

// platformClients maps a platform onto the clients Omniflow documents. Ordering
// is the recommendation order shown to the customer.
var platformClients = map[string][]ClientApp{
	"ios":     {{"Happ", "happ://add/"}, {"v2RayTun", "v2raytun://import/"}, {"Streisand", "streisand://import/"}},
	"android": {{"Happ", "happ://add/"}, {"v2RayTun", "v2raytun://import/"}, {"Hiddify", "hiddify://import/"}},
	"windows": {{"Hiddify", "hiddify://import/"}, {"v2RayTun", "v2raytun://import/"}},
	"macos":   {{"Happ", "happ://add/"}, {"Streisand", "streisand://import/"}},
	"linux":   {{"Hiddify", "hiddify://import/"}},
}

// platformOrder is the order the platforms are offered in.
var platformOrder = []string{"ios", "android", "windows", "macos", "linux"}

// DeviceHandle is the reference a customer surface uses to name one connected
// device.
//
// A hardware ID identifies a person's machine and must never leave the server —
// not in a page, not in callback data, not in a log. But removing a device needs
// some way to say which one, and an array index is not it: the list can change
// between the read and the removal, so an index can point at a different device
// by the time it is used.
//
// A truncated digest of the HWID is stable for as long as the device is, reveals
// nothing about it, and is resolved back by hashing the current list rather than
// by storing a mapping. 128 bits is far beyond what a per-customer device list
// needs to avoid collisions.
func DeviceHandle(hwid string) string {
	sum := sha256.Sum256([]byte("omniflow.device." + hwid))
	return base64.RawURLEncoding.EncodeToString(sum[:16])
}

// ConnectPlatforms returns the supported platform keys in presentation order.
func ConnectPlatforms() []string {
	platforms := make([]string, len(platformOrder))
	copy(platforms, platformOrder)
	return platforms
}

// ClientsForPlatform returns the documented clients for a platform, or nil when
// the platform is not one this installation documents.
func ClientsForPlatform(platform string) []ClientApp {
	apps, known := platformClients[strings.ToLower(strings.TrimSpace(platform))]
	if !known {
		return nil
	}
	clients := make([]ClientApp, len(apps))
	copy(clients, apps)
	return clients
}
