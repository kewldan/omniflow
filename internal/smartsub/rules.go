package smartsub

import (
	"fmt"
	"strings"
	"unicode/utf8"
)

// RuleKind says what a service rule matches.
type RuleKind string

const (
	// KindDomains matches the operator's own domains.
	KindDomains RuleKind = "domains"
	// KindRussia sends Russian sites around the tunnel: the national zones,
	// geosite's Russian category, and Russian IP ranges.
	KindRussia RuleKind = "russia"
	// KindAds drops advertising and tracking domains (geosite).
	KindAds RuleKind = "ads"
	// KindQUIC refuses UDP 443, which pushes browsers back onto TCP.
	KindQUIC RuleKind = "quic"
	// KindTorrents sends BitTorrent traffic direct while it applies and
	// blocks it otherwise: it never goes through the VPN.
	KindTorrents RuleKind = "torrents"
	// KindAI sends ChatGPT, Gemini, and Claude through servers whose exit
	// node those services answer from, as the node's geocheck says.
	KindAI RuleKind = "ai"
)

// RuleMode is what a customer may do with a service rule.
type RuleMode string

const (
	// ModeLocked is always on; the customer sees it but cannot turn it off.
	ModeLocked RuleMode = "locked"
	// ModeOn is on until the customer turns it off.
	ModeOn RuleMode = "on"
	// ModeOff is off until the customer turns it on.
	ModeOff RuleMode = "off"
	// ModeDisabled is neither applied nor shown.
	ModeDisabled RuleMode = "disabled"
)

// ServiceRule is one card on the customer's Routing tab: a named group of
// domains (or one of the built-in matchers) with an action, an icon, a line
// of explanation, and what the customer may do with it.
type ServiceRule struct {
	// ID is stable: customers' choices are keyed by it. Built-in rules use
	// their kind.
	ID   string   `json:"id"`
	Kind RuleKind `json:"kind"`
	// Name and Caption are what the card says. Empty on a built-in rule uses
	// the panel's own words, in the customer's language.
	Name    string `json:"name"`
	Caption string `json:"caption"`
	// Icon is an uploaded icon's ID, or empty for none (built-in rules have
	// their own).
	Icon    string   `json:"icon"`
	Action  Action   `json:"action"`
	Mode    RuleMode `json:"mode"`
	Domains []string `json:"domains"`
}

// builtinRules are the rules every installation has, in the order they are
// offered, with the defaults an installation starts with.
var builtinRules = []ServiceRule{
	{ID: string(KindRussia), Kind: KindRussia, Action: ActionDirect, Mode: ModeOn},
	{ID: string(KindAI), Kind: KindAI, Action: ActionProxy, Mode: ModeOn},
	{ID: string(KindTorrents), Kind: KindTorrents, Action: ActionDirect, Mode: ModeOff},
	{ID: string(KindAds), Kind: KindAds, Action: ActionBlock, Mode: ModeOff},
	{ID: string(KindQUIC), Kind: KindQUIC, Action: ActionBlock, Mode: ModeOff},
}

// Limits on the operator's rules.
const (
	maxServiceRules     = 40
	maxRuleDomains      = 200
	maxRuleNameLength   = 60
	maxRuleCaptionRunes = 160
)

// allowedActions is the actions each kind can take.
var allowedActions = map[RuleKind][]Action{
	KindDomains:  {ActionDirect, ActionProxy, ActionBlock},
	KindRussia:   {ActionDirect},
	KindAds:      {ActionBlock},
	KindQUIC:     {ActionBlock},
	KindTorrents: {ActionDirect, ActionBlock},
	KindAI:       {ActionProxy},
}

// normalizeRules validates the operator's rules, fills in any built-in rule
// that is missing, and returns them in the operator's order.
func normalizeRules(rules []ServiceRule) ([]ServiceRule, error) {
	if len(rules) > maxServiceRules {
		return nil, invalid("rules", fmt.Sprintf("at most %d rules", maxServiceRules))
	}
	out := make([]ServiceRule, 0, len(rules)+len(builtinRules))
	seen := map[string]bool{}
	for index, rule := range rules {
		field := fmt.Sprintf("rules[%d]", index)
		if rule.Kind == "" {
			rule.Kind = KindDomains
		}
		actions, known := allowedActions[rule.Kind]
		if !known {
			return nil, invalid(field, "unknown kind "+string(rule.Kind))
		}
		if rule.Kind != KindDomains {
			rule.ID = string(rule.Kind)
			rule.Domains = nil
		} else {
			rule.ID = strings.ToLower(strings.TrimSpace(rule.ID))
			if !validRuleID(rule.ID) {
				return nil, invalid(field, "id must be 1–40 lowercase letters, digits, or dashes")
			}
			if builtin(rule.ID) {
				return nil, invalid(field, "id "+rule.ID+" is reserved")
			}
			domains, err := normalizeDomains(field, rule.Domains)
			if err != nil {
				return nil, err
			}
			if len(domains) == 0 {
				return nil, invalid(field, "add at least one domain")
			}
			if len(domains) > maxRuleDomains {
				return nil, invalid(field, "too many domains")
			}
			rule.Domains = domains
		}
		if seen[rule.ID] {
			return nil, invalid(field, "duplicate rule "+rule.ID)
		}
		seen[rule.ID] = true
		if rule.Action == "" {
			rule.Action = actions[0]
		}
		if !actionIn(rule.Action, actions) {
			return nil, invalid(field, "this rule cannot "+string(rule.Action))
		}
		switch rule.Mode {
		case ModeLocked, ModeOn, ModeOff, ModeDisabled:
		case "":
			rule.Mode = ModeOn
		default:
			return nil, invalid(field, "mode must be locked, on, off, or disabled")
		}
		// Blocked torrents have nothing to switch: off would block them too.
		if rule.Kind == KindTorrents && rule.Action == ActionBlock && rule.Mode != ModeDisabled {
			rule.Mode = ModeLocked
		}
		rule.Name = strings.TrimSpace(rule.Name)
		rule.Caption = strings.TrimSpace(rule.Caption)
		if rule.Kind == KindDomains && rule.Name == "" {
			return nil, invalid(field, "a rule needs a name")
		}
		if utf8.RuneCountInString(rule.Name) > maxRuleNameLength {
			return nil, invalid(field, "name is too long")
		}
		if utf8.RuneCountInString(rule.Caption) > maxRuleCaptionRunes {
			return nil, invalid(field, "caption is too long")
		}
		rule.Icon = strings.ToLower(strings.TrimSpace(rule.Icon))
		if rule.Icon != "" && !validUUID(rule.Icon) {
			return nil, invalid(field, "icon must be an uploaded icon")
		}
		out = append(out, rule)
	}
	for _, rule := range builtinRules {
		if !seen[rule.ID] {
			out = append(out, rule)
		}
	}
	return out, nil
}

func builtin(id string) bool {
	for _, rule := range builtinRules {
		if rule.ID == id {
			return true
		}
	}
	return false
}

func actionIn(action Action, actions []Action) bool {
	for _, candidate := range actions {
		if candidate == action {
			return true
		}
	}
	return false
}

func validRuleID(id string) bool {
	if id == "" || len(id) > 40 {
		return false
	}
	for index := 0; index < len(id); index++ {
		character := id[index]
		if !(character >= 'a' && character <= 'z' || character >= '0' && character <= '9' || character == '-') {
			return false
		}
	}
	return true
}

// upgradeLegacy turns the settings a version without service rules saved —
// the three default presets and the operator's direct and blocked domain
// lists — into rules, so an installation keeps behaving as it did.
func (settings *Settings) upgradeLegacy() {
	if settings.Rules != nil {
		settings.LegacyDefaults, settings.LegacyDirect, settings.LegacyBlock = nil, nil, nil
		return
	}
	rules := []ServiceRule{}
	if len(settings.LegacyBlock) > 0 {
		rules = append(rules, ServiceRule{
			ID: "blocked", Kind: KindDomains, Name: "Blocked sites", Action: ActionBlock, Mode: ModeLocked,
			Domains: settings.LegacyBlock,
		})
	}
	if len(settings.LegacyDirect) > 0 {
		rules = append(rules, ServiceRule{
			ID: "direct", Kind: KindDomains, Name: "Direct sites", Action: ActionDirect, Mode: ModeLocked,
			Domains: settings.LegacyDirect,
		})
	}
	defaults := Presets{RussiaDirect: true}
	if settings.LegacyDefaults != nil {
		defaults = *settings.LegacyDefaults
	}
	for _, rule := range builtinRules {
		switch rule.Kind {
		case KindRussia:
			rule.Mode = onOff(defaults.RussiaDirect)
		case KindAds:
			rule.Mode = onOff(defaults.BlockAds)
		case KindQUIC:
			rule.Mode = onOff(defaults.BlockQUIC)
		}
		rules = append(rules, rule)
	}
	settings.Rules = rules
	settings.LegacyDefaults, settings.LegacyDirect, settings.LegacyBlock = nil, nil, nil
}

func onOff(on bool) RuleMode {
	if on {
		return ModeOn
	}
	return ModeOff
}

// Rule returns the operator's rule with this ID.
func (settings Settings) Rule(id string) (ServiceRule, bool) {
	for _, rule := range settings.Rules {
		if rule.ID == id {
			return rule, true
		}
	}
	return ServiceRule{}, false
}

// Applies reports whether a rule is in force for a customer: locked rules
// always, disabled never, the rest by the customer's toggle or the rule's
// default.
func (preferences Preferences) Applies(rule ServiceRule) bool {
	switch rule.Mode {
	case ModeLocked:
		return true
	case ModeDisabled:
		return false
	}
	if on, chosen := preferences.Toggles[rule.ID]; chosen {
		return on
	}
	return rule.Mode == ModeOn
}

// AIDomains are the domains each AI service answers on.
var AIDomains = map[Service][]string{
	ServiceChatGPT: {"openai.com", "chatgpt.com", "oaistatic.com", "oaiusercontent.com", "sora.com"},
	ServiceGemini: {
		"gemini.google.com", "aistudio.google.com", "generativelanguage.googleapis.com",
		"notebooklm.google.com", "bard.google.com",
	},
	ServiceClaude: {"claude.ai", "claude.com", "anthropic.com", "claudeusercontent.com"},
}

// AIServices are the services the AI rule routes, in order.
var AIServices = []Service{ServiceChatGPT, ServiceGemini, ServiceClaude}
