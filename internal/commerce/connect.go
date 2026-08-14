package commerce

import (
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"regexp"
	"strings"
)

// Connection guidance is domain data rather than presentation: which client
// applications an installation documents, and how each one imports a
// subscription. The bot and the customer web panel both render it, and they must
// not be able to disagree — a customer who reads "install Happ" in the chat and
// something else in the browser has been given two different products.
//
// It used to be a table compiled into this file, which guaranteed that property
// and made adding a client a release. It now lives in the database, read by both
// surfaces through one query; what stays here is the part that is a rule rather
// than a value.

// ConnectPlatform is one platform an installation documents, already resolved to
// the reader's language.
type ConnectPlatform struct {
	Slug  string `json:"slug"`
	Label string `json:"label"`
}

// ClientApp is one recommended client for a platform, together with the scheme
// that imports a subscription into it.
type ClientApp struct {
	Name   string `json:"name"`
	Scheme string `json:"-"`
	// DownloadURL is where a customer gets the application. Empty when the
	// operator has not said.
	DownloadURL string `json:"downloadUrl,omitempty"`
	// Instructions are the operator's own words for this client on this
	// platform, in the reader's language. Empty means the generic steps both
	// surfaces already render, which is what every shipped entry uses.
	Instructions string `json:"instructions,omitempty"`
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

// schemePattern is the shape an import scheme may take: a scheme name, `://`,
// and a conservative path alphabet. It is anchored at both ends, so nothing
// follows the path.
var schemePattern = regexp.MustCompile(`^[a-z][a-z0-9+.-]{1,30}://[A-Za-z0-9/_.~-]{0,60}$`)

// dangerousSchemes never appear in a link, whatever the pattern would allow.
//
// This value is concatenated with the subscription link and rendered as the
// href of an anchor in the customer web panel. `javascript:` in an href
// executes; `data:`, `vbscript:`, and `file:` are the neighbours it travels
// with. The same four are refused by a constraint on the table, so a script
// writing directly to the database is refused too — this check exists so an
// operator gets a message naming the field rather than a constraint violation.
var dangerousSchemes = map[string]bool{
	"javascript": true, "data": true, "vbscript": true, "file": true,
}

// ValidateClientScheme reports whether an operator-supplied import scheme is
// safe to render.
func ValidateClientScheme(scheme string) error {
	normalised := strings.ToLower(strings.TrimSpace(scheme))
	if !schemePattern.MatchString(normalised) {
		return fmt.Errorf(
			"%q is not an import scheme; it must look like happ://add/", scheme,
		)
	}
	name, _, _ := strings.Cut(normalised, ":")
	if dangerousSchemes[name] {
		return fmt.Errorf("%q cannot be used in a link", name)
	}
	return nil
}

// NormaliseClientScheme returns the scheme as it should be stored.
func NormaliseClientScheme(scheme string) string {
	return strings.ToLower(strings.TrimSpace(scheme))
}

// ValidateDownloadURL reports whether a download address is safe to offer.
//
// HTTPS only, because this is a link handed to somebody who is about to install
// software on their own device.
func ValidateDownloadURL(address string) error {
	trimmed := strings.TrimSpace(address)
	if trimmed == "" {
		return nil
	}
	if !strings.HasPrefix(strings.ToLower(trimmed), "https://") || len(trimmed) < 11 {
		return fmt.Errorf("a download address must be an https:// URL")
	}
	if strings.ContainsAny(trimmed, " \t\r\n") {
		return fmt.Errorf("a download address cannot contain whitespace")
	}
	return nil
}

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
