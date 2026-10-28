// Package panelsearch decides what an operator's search string can mean and
// which result groups a role may see.
//
// It has no database or transport imports. The PostgreSQL adapter in
// internal/panelsearchpg runs exactly the lookups a parsed Query asks for, and
// each lookup is an index probe on an identifier rather than a scan, so what
// this package recognises is also the complete list of things the search can
// cost. A string that matches no recognised shape runs nothing at all except
// the ticket-subject lookup, which has a trigram index of its own.
package panelsearch

import (
	"regexp"
	"strconv"
	"strings"

	"github.com/omniflow/omniflow/internal/accesscode"
	"github.com/omniflow/omniflow/internal/rbac"
)

// Bounds on the input. Two characters is the shortest string worth running
// anything for; 128 covers every identifier the panel shows, with room.
const (
	MinQueryLength = 2
	MaxQueryLength = 128
	// MinUUIDPrefix is how much of an identifier an operator must paste. Six
	// hexadecimal characters is 24 bits: enough that a prefix almost always
	// names one record, short enough to read off a screenshot.
	MinUUIDPrefix = 6
	// MinSubjectLength is the shortest ticket-subject fragment searched. A
	// trigram index can only narrow a pattern of at least three characters;
	// anything shorter would read every ticket.
	MinSubjectLength = 3
	// GroupLimit bounds every group. The palette is a launcher, not a report:
	// five rows of each kind is what fits on a phone, and a search that needs
	// more is a search for the list screens.
	GroupLimit = 5
)

// Group is one kind of record the palette can return.
type Group string

const (
	GroupCustomers   Group = "customers"
	GroupOrders      Group = "orders"
	GroupTickets     Group = "tickets"
	GroupGifts       Group = "gifts"
	GroupAccessCodes Group = "accessCodes"
)

// Groups lists every group in the order the palette shows them.
var Groups = []Group{GroupCustomers, GroupOrders, GroupTickets, GroupGifts, GroupAccessCodes}

// groupPermissions is the read permission that governs each group. It is the
// same permission the group's own list screen and detail route require, so the
// palette can never show a record its result link would refuse to open.
var groupPermissions = map[Group]rbac.Permission{
	GroupCustomers:   rbac.PermissionCustomersRead,
	GroupOrders:      rbac.PermissionFinanceRead,
	GroupTickets:     rbac.PermissionSupportRead,
	GroupGifts:       rbac.PermissionGiftsRead,
	GroupAccessCodes: rbac.PermissionCatalogRead,
}

// Permission returns the permission a group requires.
func Permission(group Group) rbac.Permission { return groupPermissions[group] }

// Visible returns the groups a grant may search, in display order.
func Visible(grant rbac.Grant) []Group {
	visible := make([]Group, 0, len(Groups))
	for _, group := range Groups {
		if grant.Allows(groupPermissions[group]) {
			visible = append(visible, group)
		}
	}
	return visible
}

// UUIDRange is an inclusive range of identifiers sharing a prefix. Comparing a
// uuid column against two bounds is a plain primary-key range scan, which is
// why prefix search needs no index of its own.
type UUIDRange struct {
	Low  string
	High string
	// Exact is true when the input was a whole identifier.
	Exact bool
}

// Query is a parsed search string: every interpretation it plausibly has.
// A field left at its zero value means "not this".
type Query struct {
	// Raw is the trimmed input.
	Raw string
	// ID is set when the input is a whole identifier or a long enough prefix
	// of one, with or without hyphens.
	ID *UUIDRange
	// TelegramID is set for an all-digit input of Telegram's identifier length.
	TelegramID int64
	// Email is the normalised address when the input looks like one. It is
	// only ever matched exactly, through the keyed fingerprint stored beside
	// the encrypted value, so contact values cannot be trawled by prefix.
	Email string
	// Handle is a Telegram @username without the @. Omniflow does not store
	// handles, so the adapter matches nothing for it and the response says so.
	Handle string
	// Username is a candidate Remnawave username, matched exactly.
	Username string
	// Reference is a candidate payment-provider reference, matched exactly.
	Reference string
	// SubjectPattern is an ILIKE pattern for ticket subjects with every
	// wildcard in the input escaped, or empty when the input is too short.
	SubjectPattern string
	// CodeHash is the stored digest of a gift or access code the input
	// normalises to, so a code is found without its plaintext being stored.
	CodeHash []byte
}

var (
	telegramIDPattern = regexp.MustCompile(`^[0-9]{5,15}$`)
	handlePattern     = regexp.MustCompile(`^@[A-Za-z0-9_]{4,32}$`)
	usernamePattern   = regexp.MustCompile(`^[A-Za-z0-9_.-]{3,64}$`)
	referencePattern  = regexp.MustCompile(`^[A-Za-z0-9_.:/=+-]{4,128}$`)
)

// Parse classifies a search string. It returns ok=false for input too short or
// too long to search, which the caller answers with an empty result rather
// than an error: the palette sends whatever is typed.
func Parse(input string) (query Query, ok bool) {
	raw := strings.TrimSpace(input)
	length := len([]rune(raw))
	if length < MinQueryLength || length > MaxQueryLength {
		return Query{}, false
	}
	query.Raw = raw

	if id, isID := parseUUIDPrefix(raw); isID {
		query.ID = &id
	}
	if telegramIDPattern.MatchString(raw) {
		if value, err := strconv.ParseInt(raw, 10, 64); err == nil && value > 0 {
			query.TelegramID = value
		}
	}
	switch {
	case handlePattern.MatchString(raw):
		query.Handle = raw[1:]
	case strings.Count(raw, "@") == 1 && !strings.HasPrefix(raw, "@") && !strings.HasSuffix(raw, "@") &&
		!strings.ContainsAny(raw, " \t"):
		query.Email = strings.ToLower(raw)
	}
	if query.Email == "" && query.Handle == "" {
		if usernamePattern.MatchString(raw) {
			query.Username = raw
		}
		if referencePattern.MatchString(raw) {
			query.Reference = raw
		}
	}
	if code, err := accesscode.Normalize(raw); err == nil {
		query.CodeHash = accesscode.Hash(code)
	}
	if length >= MinSubjectLength {
		query.SubjectPattern = "%" + escapeLike(raw) + "%"
	}
	return query, true
}

// parseUUIDPrefix accepts a whole identifier or a prefix of one, with or
// without its hyphens, and returns the inclusive range of identifiers that
// start with it.
func parseUUIDPrefix(raw string) (UUIDRange, bool) {
	hex := strings.ToLower(strings.ReplaceAll(raw, "-", ""))
	if len(hex) < MinUUIDPrefix || len(hex) > 32 {
		return UUIDRange{}, false
	}
	for _, character := range hex {
		if (character < '0' || character > '9') && (character < 'a' || character > 'f') {
			return UUIDRange{}, false
		}
	}
	// A hyphen may only sit where the canonical form puts one, so a string
	// like "abc-def123" is not mistaken for an identifier.
	if strings.Contains(raw, "-") {
		canonical := formatUUID(hex + strings.Repeat("0", 32-len(hex)))
		if !strings.HasPrefix(canonical, strings.ToLower(raw)) {
			return UUIDRange{}, false
		}
	}
	return UUIDRange{
		Low:   formatUUID(hex + strings.Repeat("0", 32-len(hex))),
		High:  formatUUID(hex + strings.Repeat("f", 32-len(hex))),
		Exact: len(hex) == 32,
	}, true
}

func formatUUID(hex string) string {
	return hex[0:8] + "-" + hex[8:12] + "-" + hex[12:16] + "-" + hex[16:20] + "-" + hex[20:32]
}

// escapeLike neutralises the LIKE metacharacters, so a percent sign an
// operator types is searched for rather than treated as "anything".
func escapeLike(value string) string {
	replacer := strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`)
	return replacer.Replace(value)
}
