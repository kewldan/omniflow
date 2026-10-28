// Package egress builds the HTTP clients Omniflow uses to reach addresses an
// operator typed into the panel: AI provider base URLs, blocklist sources, and
// MCP servers.
//
// Every one of those is a server-side request to a destination someone other
// than the deployer chose. Without a guard, an operator with a settings
// permission can make the API or worker talk to the cloud metadata service, the
// database, Valkey, or anything else on the private network the process sits
// in. The guard has to run where the connection is made rather than where the
// URL is saved: a hostname can resolve to a public address when it is checked
// and to 169.254.169.254 a second later, and a public server can answer with a
// redirect to a private one. So the check lives in the dialer, after name
// resolution, and runs again for every connection a redirect opens.
//
// The package imports nothing from the rest of the application, so the three
// callers share one definition of "internal address" instead of each keeping
// its own list.
package egress

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strings"
	"syscall"
	"time"
)

var (
	// ErrBlockedDestination reports an address the policy does not admit.
	ErrBlockedDestination = errors.New("destination is not a public address")
	// ErrInsecureScheme reports a plain-HTTP URL where HTTPS is required.
	ErrInsecureScheme = errors.New("destination must use https")
	// ErrInvalidURL reports a URL that cannot name a destination at all.
	ErrInvalidURL = errors.New("destination is not a usable url")
	// ErrTooManyRedirects reports a redirect chain longer than maxRedirects.
	ErrTooManyRedirects = errors.New("destination redirected too many times")
)

// maxRedirects bounds a redirect chain. Each hop is dialled through the same
// guard, so the bound is about not looping forever rather than about safety.
const maxRedirects = 5

// Policy says which destinations a client may reach.
//
// The zero value is the strict one: HTTPS only, public addresses only. Each
// relaxation is a separate, explicit field so a caller cannot get one by
// accident while asking for the other.
type Policy struct {
	// AllowHTTP admits plain-HTTP URLs.
	AllowHTTP bool
	// AllowPrivate admits loopback, private, link-local, and the other
	// non-public ranges listed in blockedPrefixes.
	AllowPrivate bool
}

// blockedPrefixes are the ranges that are not public unicast on top of what
// netip classifies directly (loopback, RFC 1918 / ULA, link-local, multicast,
// unspecified).
var blockedPrefixes = []netip.Prefix{
	netip.MustParsePrefix("0.0.0.0/8"),       // "this network"
	netip.MustParsePrefix("100.64.0.0/10"),   // carrier-grade NAT, often internal
	netip.MustParsePrefix("192.0.0.0/24"),    // IETF protocol assignments
	netip.MustParsePrefix("198.18.0.0/15"),   // benchmarking
	netip.MustParsePrefix("240.0.0.0/4"),     // reserved, including broadcast
	netip.MustParsePrefix("64:ff9b::/96"),    // NAT64: embeds an IPv4 address
	netip.MustParsePrefix("64:ff9b:1::/48"),  // local-use NAT64
	netip.MustParsePrefix("2002::/16"),       // 6to4: embeds an IPv4 address
	netip.MustParsePrefix("100::/64"),        // discard-only
	netip.MustParsePrefix("2001:db8::/32"),   // documentation
	netip.MustParsePrefix("fec0::/10"),       // deprecated site-local
	netip.MustParsePrefix("::/128"),          // unspecified, for completeness
	netip.MustParsePrefix("::ffff:0:0/96"),   // IPv4-mapped, after unmapping fails
	netip.MustParsePrefix("2001::/32"),       // Teredo: embeds an IPv4 address
	netip.MustParsePrefix("198.51.100.0/24"), // documentation
	netip.MustParsePrefix("203.0.113.0/24"),  // documentation
	netip.MustParsePrefix("192.0.2.0/24"),    // documentation
}

// IsPublic reports whether an address is ordinary public unicast.
//
// An IPv4-mapped IPv6 address is judged by the IPv4 address it carries:
// `::ffff:127.0.0.1` is loopback however it is spelled.
func IsPublic(address netip.Addr) bool {
	if !address.IsValid() {
		return false
	}
	address = address.Unmap()
	if address.IsLoopback() || address.IsPrivate() || address.IsUnspecified() ||
		address.IsLinkLocalUnicast() || address.IsLinkLocalMulticast() ||
		address.IsInterfaceLocalMulticast() || address.IsMulticast() {
		return false
	}
	for _, prefix := range blockedPrefixes {
		if prefix.Contains(address) {
			return false
		}
	}
	return true
}

// CheckURL validates a URL against the policy without making a connection.
//
// It is what a save path calls so an operator hears about a bad address when
// they type it. It is not the enforcement: a hostname is only resolved at
// connect time, by the dialer, which is where a private destination is
// actually refused. A literal IP is checked here as well, because there is no
// later resolution that could change it.
func (policy Policy) CheckURL(raw string) error {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || parsed.Host == "" || parsed.Hostname() == "" {
		return ErrInvalidURL
	}
	if parsed.User != nil {
		// Credentials in the URL would travel to wherever the URL points and
		// be logged by anything that prints it.
		return fmt.Errorf("%w: credentials in the url are not accepted", ErrInvalidURL)
	}
	switch parsed.Scheme {
	case "https":
	case "http":
		if !policy.AllowHTTP {
			return ErrInsecureScheme
		}
	default:
		return ErrInvalidURL
	}
	if policy.AllowPrivate {
		return nil
	}
	if address, parseErr := netip.ParseAddr(parsed.Hostname()); parseErr == nil && !IsPublic(address) {
		return fmt.Errorf("%w: %s", ErrBlockedDestination, address)
	}
	if strings.EqualFold(parsed.Hostname(), "localhost") ||
		strings.HasSuffix(strings.ToLower(parsed.Hostname()), ".localhost") {
		return fmt.Errorf("%w: %s", ErrBlockedDestination, parsed.Hostname())
	}
	return nil
}

// control is the dialer hook. It runs after resolution, once per address the
// dialer tries, so it sees the address actually being connected to.
func (policy Policy) control(_, address string, _ syscall.RawConn) error {
	if policy.AllowPrivate {
		return nil
	}
	endpoint, err := netip.ParseAddrPort(address)
	if err != nil {
		return fmt.Errorf("%w: %s", ErrBlockedDestination, address)
	}
	if !IsPublic(endpoint.Addr()) {
		return fmt.Errorf("%w: %s", ErrBlockedDestination, endpoint.Addr())
	}
	return nil
}

// Transport returns an http.Transport whose every connection passes the policy.
//
// Environment proxies are deliberately not honoured. Through a proxy the dialer
// would only ever see the proxy's address, and the destination check would be
// checking the wrong thing.
func (policy Policy) Transport() *http.Transport {
	dialer := &net.Dialer{
		Timeout:   10 * time.Second,
		KeepAlive: 30 * time.Second,
		Control:   policy.control,
	}
	return &http.Transport{
		Proxy:                 nil,
		DialContext:           dialer.DialContext,
		ForceAttemptHTTP2:     true,
		MaxIdleConns:          32,
		IdleConnTimeout:       90 * time.Second,
		TLSHandshakeTimeout:   10 * time.Second,
		ExpectContinueTimeout: time.Second,
	}
}

// CheckRedirect is an http.Client redirect hook that applies the policy's URL
// rules to every hop and bounds the chain. The dialer still checks each hop's
// resolved address; this catches a downgrade to plain HTTP, which the dialer
// cannot see.
func (policy Policy) CheckRedirect(request *http.Request, via []*http.Request) error {
	if len(via) >= maxRedirects {
		return ErrTooManyRedirects
	}
	return policy.CheckURL(request.URL.String())
}

// Client returns an http.Client that enforces the policy on every connection
// and every redirect.
func (policy Policy) Client(timeout time.Duration) *http.Client {
	return &http.Client{
		Timeout:       timeout,
		Transport:     policy.Transport(),
		CheckRedirect: policy.CheckRedirect,
	}
}

// Resolve checks every address a hostname currently resolves to. It exists for
// callers that want to refuse a destination with a readable message before
// trying it; the dialer is still what enforces the policy.
func (policy Policy) Resolve(ctx context.Context, host string) error {
	if policy.AllowPrivate {
		return nil
	}
	if address, err := netip.ParseAddr(host); err == nil {
		if !IsPublic(address) {
			return fmt.Errorf("%w: %s", ErrBlockedDestination, address)
		}
		return nil
	}
	addresses, err := net.DefaultResolver.LookupNetIP(ctx, "ip", host)
	if err != nil {
		return fmt.Errorf("%w: %s does not resolve", ErrBlockedDestination, host)
	}
	for _, address := range addresses {
		if !IsPublic(address) {
			return fmt.Errorf("%w: %s resolves to an internal address", ErrBlockedDestination, host)
		}
	}
	return nil
}
