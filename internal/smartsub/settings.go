package smartsub

import (
	"errors"
	"fmt"
	"net/url"
	"sort"
	"strings"
	"unicode/utf8"

	"golang.org/x/net/idna"
)

// Role is what the operator made of one Remnawave host.
type Role string

const (
	// RoleMain is an ordinary server: grouped under its country and eligible
	// for the automatic group. It is the default for a host nobody marked.
	RoleMain Role = "main"
	// RoleReserve is the whitelist bypass: a server that still works when a
	// mobile network lets through only an allowlist of sites. It is kept out
	// of the country groups and used as the fallback when they stop answering,
	// and as a group of its own.
	RoleReserve Role = "reserve"
	// RoleHidden leaves the host out of every subscription Omniflow serves,
	// for a test server or one being retired.
	RoleHidden Role = "hidden"
)

// HostOverride is the operator's correction for one host, keyed by the host's
// Remnawave UUID. An empty country keeps the one read from the remark.
type HostOverride struct {
	Country string `json:"country,omitempty"`
	Role    Role   `json:"role,omitempty"`
	// ExitNode is the Remnawave node the host's traffic leaves from, when it
	// is not the node the host runs on: a host whose inbound forwards to a
	// second node exits there, and that node's geocheck is what customers
	// should see. Empty uses the host's own node.
	ExitNode string `json:"exitNode,omitempty"`
}

func (override HostOverride) isZero() bool {
	return override == HostOverride{}
}

// Presets are the ready-made routing choices.
type Presets struct {
	// RussiaDirect sends Russian sites and addresses around the tunnel: .ru,
	// .su, .рф, and Russian IP ranges.
	RussiaDirect bool `json:"russiaDirect"`
	// BlockAds drops advertising and tracking domains.
	BlockAds bool `json:"blockAds"`
	// BlockQUIC refuses UDP 443, which pushes browsers back onto TCP; it is
	// the usual cure for a slow video site behind a tunnel.
	BlockQUIC bool `json:"blockQuic"`
}

// Settings are the operator's settings for the module.
type Settings struct {
	Enabled bool `json:"enabled"`
	// LinkBase is the public prefix the link is built on, ending before the
	// short UUID. Empty uses the installation's public URL and /v1/sub/.
	LinkBase string `json:"linkBase"`
	// ProbeURL is what each client fetches through every server to time it.
	ProbeURL string `json:"probeUrl"`
	// ProbeIntervalSeconds is how often a client re-times the servers.
	ProbeIntervalSeconds int `json:"probeIntervalSeconds"`
	// AutoName and ReserveName label the automatic group and the whitelist
	// bypass group. Empty uses the customer's language's default.
	AutoName    string `json:"autoName"`
	ReserveName string `json:"reserveName"`
	// Defaults are the presets a customer starts with.
	Defaults Presets `json:"defaults"`
	// CustomRules lets customers add their own domains; MaxRules caps them.
	CustomRules bool `json:"customRules"`
	MaxRules    int  `json:"maxRules"`
	// DirectDomains always go around the tunnel for everyone: the
	// installation's own sites, a bank the operator knows misbehaves.
	DirectDomains []string `json:"directDomains"`
	// BlockDomains are refused for everyone.
	BlockDomains []string `json:"blockDomains"`
	// Hosts are the per-host corrections, keyed by Remnawave host UUID.
	Hosts map[string]HostOverride `json:"hosts"`
}

// Limits a setting may not exceed.
const (
	DefaultProbeURL      = "https://www.gstatic.com/generate_204"
	DefaultProbeInterval = 600
	DefaultMaxRules      = 50
	MaxRulesCeiling      = 200
	maxDirectDomains     = 200
	maxNameLength        = 40
	maxDomainLength      = 253
)

// DefaultSettings is the module as an installation first sees it: off, with
// the defaults a customer would start from once it is on.
func DefaultSettings() Settings {
	return Settings{
		ProbeURL: DefaultProbeURL, ProbeIntervalSeconds: DefaultProbeInterval,
		Defaults:    Presets{RussiaDirect: true},
		CustomRules: true, MaxRules: DefaultMaxRules,
		DirectDomains: []string{}, BlockDomains: []string{}, Hosts: map[string]HostOverride{},
	}
}

// FieldError names the setting or preference that failed and why, for the
// screen to put next to the field.
type FieldError struct {
	Field  string
	Reason string
}

func (err *FieldError) Error() string { return err.Field + ": " + err.Reason }

// ErrInvalid wraps every FieldError, so a caller can test for any of them.
var ErrInvalid = errors.New("smart subscription setting is invalid")

func (err *FieldError) Unwrap() error { return ErrInvalid }

func invalid(field, reason string) error { return &FieldError{Field: field, Reason: reason} }

// Normalize validates the settings and returns them in canonical form: names
// trimmed, domains lowercased and deduplicated, defaults filled in.
func (settings Settings) Normalize() (Settings, error) {
	settings.LinkBase = strings.TrimSpace(settings.LinkBase)
	if settings.LinkBase != "" {
		parsed, err := url.Parse(settings.LinkBase)
		if err != nil || parsed.Host == "" || (parsed.Scheme != "https" && parsed.Scheme != "http") ||
			parsed.RawQuery != "" || parsed.Fragment != "" {
			return Settings{}, invalid("linkBase", "must be an absolute http(s) URL without a query")
		}
		if !strings.HasSuffix(settings.LinkBase, "/") {
			settings.LinkBase += "/"
		}
	}
	settings.ProbeURL = strings.TrimSpace(settings.ProbeURL)
	if settings.ProbeURL == "" {
		settings.ProbeURL = DefaultProbeURL
	}
	if parsed, err := url.Parse(settings.ProbeURL); err != nil || parsed.Host == "" ||
		(parsed.Scheme != "https" && parsed.Scheme != "http") {
		return Settings{}, invalid("probeUrl", "must be an absolute http(s) URL")
	}
	switch {
	case settings.ProbeIntervalSeconds == 0:
		settings.ProbeIntervalSeconds = DefaultProbeInterval
	case settings.ProbeIntervalSeconds < 30 || settings.ProbeIntervalSeconds > 86400:
		return Settings{}, invalid("probeIntervalSeconds", "must be between 30 and 86400")
	}
	settings.AutoName = strings.TrimSpace(settings.AutoName)
	settings.ReserveName = strings.TrimSpace(settings.ReserveName)
	if utf8.RuneCountInString(settings.AutoName) > maxNameLength {
		return Settings{}, invalid("autoName", "is too long")
	}
	if utf8.RuneCountInString(settings.ReserveName) > maxNameLength {
		return Settings{}, invalid("reserveName", "is too long")
	}
	switch {
	case settings.MaxRules == 0:
		settings.MaxRules = DefaultMaxRules
	case settings.MaxRules < 0 || settings.MaxRules > MaxRulesCeiling:
		return Settings{}, invalid("maxRules", fmt.Sprintf("must be between 1 and %d", MaxRulesCeiling))
	}
	var err error
	if settings.DirectDomains, err = normalizeDomains("directDomains", settings.DirectDomains); err != nil {
		return Settings{}, err
	}
	if settings.BlockDomains, err = normalizeDomains("blockDomains", settings.BlockDomains); err != nil {
		return Settings{}, err
	}
	hosts := make(map[string]HostOverride, len(settings.Hosts))
	for id, override := range settings.Hosts {
		id = strings.ToLower(strings.TrimSpace(id))
		if !validUUID(id) {
			return Settings{}, invalid("hosts", "host keys must be Remnawave host UUIDs")
		}
		override.Country = strings.ToUpper(strings.TrimSpace(override.Country))
		if override.Country != "" && !ValidCountry(override.Country) {
			return Settings{}, invalid("hosts", "unknown country "+override.Country)
		}
		switch override.Role {
		case "", RoleMain:
			override.Role = ""
		case RoleReserve, RoleHidden:
		default:
			return Settings{}, invalid("hosts", "role must be main, reserve, or hidden")
		}
		override.ExitNode = strings.ToLower(strings.TrimSpace(override.ExitNode))
		if override.ExitNode != "" && !validUUID(override.ExitNode) {
			return Settings{}, invalid("hosts", "exit node must be a Remnawave node UUID")
		}
		// An override that changes nothing is not kept, so the document only
		// ever lists hosts somebody actually corrected.
		if !override.isZero() {
			hosts[id] = override
		}
	}
	settings.Hosts = hosts
	return settings, nil
}

// normalizeDomains validates an operator's domain list and returns it
// deduplicated and sorted.
func normalizeDomains(field string, raw []string) ([]string, error) {
	if len(raw) > maxDirectDomains {
		return nil, invalid(field, "too many domains")
	}
	domains := make([]string, 0, len(raw))
	seen := map[string]bool{}
	for _, entry := range raw {
		domain, err := NormalizeDomain(entry)
		if err != nil {
			return nil, invalid(field, err.Error())
		}
		if !seen[domain] {
			seen[domain] = true
			domains = append(domains, domain)
		}
	}
	sort.Strings(domains)
	return domains, nil
}

// Action is what one customer rule does with a domain.
type Action string

const (
	ActionDirect Action = "direct"
	ActionProxy  Action = "proxy"
	ActionBlock  Action = "block"
)

// Rule is one customer routing rule. A domain matches itself and every
// subdomain.
type Rule struct {
	Domain string `json:"domain"`
	Action Action `json:"action"`
}

// Preferences are one customer's choices for one subscription.
type Preferences struct {
	// HiddenCountries are left out of the subscription altogether.
	HiddenCountries []string `json:"hiddenCountries"`
	// AutoExcluded are shown as their own group but never picked by the
	// automatic group.
	AutoExcluded []string `json:"autoExcluded"`
	// Reserve keeps the whitelist bypass servers: as the fallback behind
	// every group and as a group of their own.
	Reserve bool    `json:"reserve"`
	Presets Presets `json:"presets"`
	Rules   []Rule  `json:"rules"`
}

// DefaultPreferences are what a customer who never opened the screen gets.
func (settings Settings) DefaultPreferences() Preferences {
	return Preferences{
		HiddenCountries: []string{}, AutoExcluded: []string{}, Reserve: true,
		Presets: settings.Defaults, Rules: []Rule{},
	}
}

// Normalize validates a customer's preferences against the operator's
// settings and returns them in canonical form.
func (preferences Preferences) Normalize(settings Settings) (Preferences, error) {
	hidden, err := normalizeCountries("hiddenCountries", preferences.HiddenCountries)
	if err != nil {
		return Preferences{}, err
	}
	excluded, err := normalizeCountries("autoExcluded", preferences.AutoExcluded)
	if err != nil {
		return Preferences{}, err
	}
	// A hidden country is not in the subscription at all, so excluding it from
	// the automatic group as well would be a second record of the same choice.
	hiddenSet := setOf(hidden)
	kept := excluded[:0]
	for _, code := range excluded {
		if !hiddenSet[code] {
			kept = append(kept, code)
		}
	}
	preferences.HiddenCountries, preferences.AutoExcluded = hidden, kept

	if len(preferences.Rules) > 0 && !settings.CustomRules {
		return Preferences{}, invalid("rules", "custom rules are turned off")
	}
	limit := settings.MaxRules
	if limit <= 0 {
		limit = DefaultMaxRules
	}
	if len(preferences.Rules) > limit {
		return Preferences{}, invalid("rules", fmt.Sprintf("at most %d rules", limit))
	}
	rules := make([]Rule, 0, len(preferences.Rules))
	seen := map[string]bool{}
	for _, rule := range preferences.Rules {
		domain, err := NormalizeDomain(rule.Domain)
		if err != nil {
			return Preferences{}, invalid("rules", err.Error())
		}
		switch rule.Action {
		case ActionDirect, ActionProxy, ActionBlock:
		default:
			return Preferences{}, invalid("rules", "action must be direct, proxy, or block")
		}
		if seen[domain] {
			return Preferences{}, invalid("rules", "duplicate domain "+domain)
		}
		seen[domain] = true
		rules = append(rules, Rule{Domain: domain, Action: rule.Action})
	}
	preferences.Rules = rules
	return preferences, nil
}

func normalizeCountries(field string, codes []string) ([]string, error) {
	if len(codes) > len(countryTable) {
		return nil, invalid(field, "too many countries")
	}
	normalized := make([]string, 0, len(codes))
	seen := map[string]bool{}
	for _, code := range codes {
		code = strings.ToUpper(strings.TrimSpace(code))
		if code != UnknownCountry && !ValidCountry(code) {
			return nil, invalid(field, "unknown country "+code)
		}
		if !seen[code] {
			seen[code] = true
			normalized = append(normalized, code)
		}
	}
	sort.Strings(normalized)
	return normalized, nil
}

// UnknownCountry groups the servers whose remark names no country.
const UnknownCountry = "ZZ"

// NormalizeDomain turns what a person typed into the domain a routing rule
// matches: a pasted URL loses its scheme and path, a leading "*." or "." is
// dropped (a rule always covers subdomains), and an internationalized name is
// converted to its ASCII form, so "пример.рф" is stored as Xray will match it.
func NormalizeDomain(raw string) (string, error) {
	domain := strings.TrimSpace(strings.ToLower(raw))
	if index := strings.Index(domain, "://"); index >= 0 {
		domain = domain[index+3:]
	}
	if index := strings.IndexAny(domain, "/?#"); index >= 0 {
		domain = domain[:index]
	}
	if index := strings.LastIndexByte(domain, '@'); index >= 0 {
		domain = domain[index+1:]
	}
	if host, _, found := strings.Cut(domain, ":"); found {
		domain = host
	}
	domain = strings.TrimPrefix(domain, "*.")
	domain = strings.Trim(domain, ".")
	// "www." narrows a rule that already covers every subdomain to one of
	// them; somebody pasting a link never means that.
	if trimmed := strings.TrimPrefix(domain, "www."); strings.Contains(trimmed, ".") {
		domain = trimmed
	}
	if domain == "" {
		return "", errors.New("enter a domain such as example.com")
	}
	ascii, err := idna.Lookup.ToASCII(domain)
	if err != nil || len(ascii) > maxDomainLength || !strings.Contains(ascii, ".") {
		return "", fmt.Errorf("%q is not a domain", raw)
	}
	for _, label := range strings.Split(ascii, ".") {
		if label == "" || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return "", fmt.Errorf("%q is not a domain", raw)
		}
		for index := 0; index < len(label); index++ {
			character := label[index]
			if !(character >= 'a' && character <= 'z' || character >= '0' && character <= '9' || character == '-') {
				return "", fmt.Errorf("%q is not a domain", raw)
			}
		}
	}
	return ascii, nil
}

func validUUID(value string) bool {
	if len(value) != 36 {
		return false
	}
	for index := 0; index < len(value); index++ {
		character := value[index]
		switch index {
		case 8, 13, 18, 23:
			if character != '-' {
				return false
			}
		default:
			if !(character >= '0' && character <= '9' || character >= 'a' && character <= 'f') {
				return false
			}
		}
	}
	return true
}

func setOf(values []string) map[string]bool {
	set := make(map[string]bool, len(values))
	for _, value := range values {
		set[value] = true
	}
	return set
}
