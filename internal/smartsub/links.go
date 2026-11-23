package smartsub

import (
	"bytes"
	"encoding/base64"
	"net/url"
	"regexp"
	"strconv"
	"strings"
)

// Format is what the module answers one client with.
type Format string

const (
	// FormatXray is the rewritten Xray JSON: groups, balancers, routing.
	FormatXray Format = "xray"
	// FormatLinks is the panel's own answer for the client, with the servers
	// the customer or the operator hid taken out when it is a list of links.
	FormatLinks Format = "links"
)

// xrayClients read an Xray JSON array with balancers.
var xrayClients = regexp.MustCompile(`(?i)^(?:happ|streisand|foxray|v2box|v2rayn|v2raytun|invisibleman|xray|neko)|happ|incy`)

// noGeositeClients stall on a config that names the geosite database, so the
// rules they get are built from domain suffixes and geoip alone.
var noGeositeClients = regexp.MustCompile(`(?i)incy`)

// StripsGeosite reports whether a client must get rules without geosite.
func StripsGeosite(userAgent string) bool {
	return noGeositeClients.MatchString(userAgent)
}

// DetectFormat picks the answer by User-Agent. A client the module does not
// know gets the panel's own answer for it.
func DetectFormat(userAgent string) Format {
	if xrayClients.MatchString(strings.TrimSpace(userAgent)) {
		return FormatXray
	}
	return FormatLinks
}

// FilterLinks takes the servers the customer hid, the operator hid, or the
// customer turned the whitelist bypass off for out of a base64 list of share
// links. Anything that is not such a list passes through.
func FilterLinks(request Request) ([]byte, error) {
	body := bytes.TrimSpace(request.Upstream)
	if len(body) == 0 || bytes.ContainsAny(body[:1], "{[<") {
		return nil, ErrPassThrough
	}
	decoded, err := decodeBase64(body)
	if err != nil || !bytes.Contains(decoded, []byte("://")) {
		return nil, ErrPassThrough
	}
	lines := strings.Split(strings.ReplaceAll(string(decoded), "\r\n", "\n"), "\n")
	hidden := setOf(request.Preferences.HiddenCountries)
	var kept []string
	removed := 0
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			continue
		}
		remark, address, port := shareLink(trimmed)
		server := request.Catalog.Classify(remark, address, port, request.Settings)
		drop := server.Role == RoleHidden ||
			server.Role == RoleReserve && !request.Preferences.Reserve ||
			server.Role == RoleMain && hidden[server.Country]
		if drop {
			removed++
			continue
		}
		kept = append(kept, trimmed)
	}
	if removed == 0 || len(kept) == 0 {
		return nil, ErrPassThrough
	}
	encoded := base64.StdEncoding.EncodeToString([]byte(strings.Join(kept, "\n")))
	return []byte(encoded), nil
}

func decodeBase64(body []byte) ([]byte, error) {
	compact := strings.Map(func(r rune) rune {
		if r == '\n' || r == '\r' || r == ' ' {
			return -1
		}
		return r
	}, string(body))
	for _, encoding := range []*base64.Encoding{
		base64.StdEncoding, base64.RawStdEncoding, base64.URLEncoding, base64.RawURLEncoding,
	} {
		if decoded, err := encoding.DecodeString(compact); err == nil {
			return decoded, nil
		}
	}
	return nil, ErrPassThrough
}

// shareLink reads the remark and the server out of one share link. VMess
// links carry a base64 JSON body instead of a URL; their remark is "ps".
func shareLink(link string) (remark, address string, port int) {
	if fragment := strings.LastIndexByte(link, '#'); fragment >= 0 {
		if unescaped, err := url.PathUnescape(link[fragment+1:]); err == nil {
			remark = unescaped
		} else {
			remark = link[fragment+1:]
		}
	}
	parsed, err := url.Parse(link)
	if err != nil {
		return remark, "", 0
	}
	port, _ = strconv.Atoi(parsed.Port())
	return remark, parsed.Hostname(), port
}
