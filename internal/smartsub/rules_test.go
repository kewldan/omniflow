package smartsub

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

func TestLegacySettingsBecomeRules(t *testing.T) {
	var settings Settings
	if err := json.Unmarshal([]byte(`{"enabled":true,"defaults":{"russiaDirect":false,"blockAds":true,"blockQuic":false},
		"directDomains":["Sberbank.ru"],"blockDomains":["max.ru"]}`), &settings); err != nil {
		t.Fatal(err)
	}
	normalized, err := settings.Normalize()
	if err != nil {
		t.Fatal(err)
	}
	direct, okDirect := normalized.Rule("direct")
	blocked, okBlocked := normalized.Rule("blocked")
	if !okDirect || direct.Mode != ModeLocked || direct.Domains[0] != "sberbank.ru" || direct.Action != ActionDirect {
		t.Errorf("direct = %+v", direct)
	}
	if !okBlocked || blocked.Action != ActionBlock || blocked.Domains[0] != "max.ru" {
		t.Errorf("blocked = %+v", blocked)
	}
	russia, _ := normalized.Rule("russia")
	ads, _ := normalized.Rule("ads")
	if russia.Mode != ModeOff || ads.Mode != ModeOn {
		t.Errorf("presets did not carry: russia %s, ads %s", russia.Mode, ads.Mode)
	}
	if normalized.LegacyDefaults != nil || normalized.LegacyDirect != nil || normalized.LegacyBlock != nil {
		t.Error("the legacy fields were kept after the upgrade")
	}
	encoded, _ := json.Marshal(normalized)
	if strings.Contains(string(encoded), "directDomains") {
		t.Errorf("a saved document still writes the legacy fields: %s", encoded)
	}
}

func TestRuleValidation(t *testing.T) {
	for name, rule := range map[string]ServiceRule{
		"no name":          {ID: "x", Kind: KindDomains, Action: ActionDirect, Domains: []string{"a.com"}},
		"no domains":       {ID: "x", Kind: KindDomains, Name: "X", Action: ActionDirect},
		"reserved id":      {ID: "russia", Kind: KindDomains, Name: "X", Action: ActionDirect, Domains: []string{"a.com"}},
		"bad id":           {ID: "Has Space", Kind: KindDomains, Name: "X", Action: ActionDirect, Domains: []string{"a.com"}},
		"ads cannot proxy": {Kind: KindAds, Action: ActionProxy},
		"bad mode":         {ID: "x", Kind: KindDomains, Name: "X", Mode: "maybe", Domains: []string{"a.com"}},
		"bad icon":         {ID: "x", Kind: KindDomains, Name: "X", Icon: "logo.png", Domains: []string{"a.com"}},
	} {
		settings := DefaultSettings()
		settings.Rules = []ServiceRule{rule}
		if _, err := settings.Normalize(); !errors.Is(err, ErrInvalid) {
			t.Errorf("%s: accepted, err = %v", name, err)
		}
	}
}

func TestToggles(t *testing.T) {
	settings := withRule(testSettings(), ServiceRule{ID: "banks", Kind: KindDomains, Name: "Banks", Action: ActionDirect, Mode: ModeLocked, Domains: []string{"bank.example"}})
	preferences, err := Preferences{Toggles: map[string]bool{
		"banks": false, "russia": true, "ads": true, "nope": true,
	}}.Normalize(settings)
	if err != nil {
		t.Fatal(err)
	}
	// A locked rule, an unknown one, and a choice equal to the default are
	// not kept.
	if len(preferences.Toggles) != 1 || !preferences.Toggles["ads"] {
		t.Errorf("toggles = %v", preferences.Toggles)
	}
	banks, _ := settings.Rule("banks")
	if !preferences.Applies(banks) {
		t.Error("a locked rule stopped applying")
	}
}

func rulesText(t *testing.T, configs []built) string {
	t.Helper()
	encoded, _ := json.Marshal(configs[0].Routing.Rules)
	return string(encoded)
}

func TestTorrentsAndQUIC(t *testing.T) {
	settings := testSettings()
	settings = withRule(settings, ServiceRule{ID: "torrents", Kind: KindTorrents, Action: ActionBlock, Mode: ModeLocked})
	preferences := settings.DefaultPreferences()
	preferences.Toggles["quic"] = true
	text := rulesText(t, build(t, Request{Upstream: standardAnswer(t), Settings: settings, Catalog: testCatalog(), Preferences: preferences}))
	if !strings.Contains(text, `"protocol":["bittorrent"],"type":"field"`) && !strings.Contains(text, `"outboundTag":"block","protocol":["bittorrent"]`) {
		t.Errorf("torrents are not blocked: %s", text)
	}
	if !strings.Contains(text, `"port":"443"`) {
		t.Errorf("QUIC is not blocked: %s", text)
	}
}

func TestTorrentsNeverGoThroughTheVPN(t *testing.T) {
	settings := withRule(testSettings(), ServiceRule{ID: "torrents", Kind: KindTorrents, Action: ActionDirect, Mode: ModeOn})
	request := Request{Upstream: standardAnswer(t), Settings: settings, Catalog: testCatalog(), Preferences: settings.DefaultPreferences()}
	if text := rulesText(t, build(t, request)); !strings.Contains(text, `{"outboundTag":"direct","protocol":["bittorrent"],"type":"field"}`) {
		t.Errorf("torrents on do not go direct: %s", text)
	}
	// Turned off, torrents are blocked rather than proxied.
	request.Preferences.Toggles = map[string]bool{"torrents": false}
	if text := rulesText(t, build(t, request)); !strings.Contains(text, `{"outboundTag":"block","protocol":["bittorrent"],"type":"field"}`) {
		t.Errorf("torrents turned off are not blocked: %s", text)
	}
	// Even a hidden rule leaves them blocked.
	hidden := withRule(testSettings(), ServiceRule{ID: "torrents", Kind: KindTorrents, Action: ActionDirect, Mode: ModeDisabled})
	request = Request{Upstream: standardAnswer(t), Settings: hidden, Catalog: testCatalog(), Preferences: hidden.DefaultPreferences()}
	if text := rulesText(t, build(t, request)); !strings.Contains(text, `{"outboundTag":"block","protocol":["bittorrent"],"type":"field"}`) {
		t.Errorf("hidden torrents rule let torrents through the VPN: %s", text)
	}
	// Blocked torrents have no switch.
	blocked := withRule(testSettings(), ServiceRule{ID: "torrents", Kind: KindTorrents, Action: ActionBlock, Mode: ModeOff})
	normalized, err := blocked.Normalize()
	if err != nil {
		t.Fatal(err)
	}
	if rule, _ := normalized.Rule("torrents"); rule.Mode != ModeLocked {
		t.Errorf("blocked torrents mode = %s", rule.Mode)
	}
}

func TestAIRoutesThroughServersWhereItWorks(t *testing.T) {
	settings := testSettings()
	// Finland 1 runs on node n1, where ChatGPT answers; Finland 2 and
	// Germany have no check.
	checks := map[string]NodeCheck{"n1": {Services: map[string]ServiceStatus{
		"chatgpt": {State: StateYes}, "gemini": {State: StateNo},
	}}}
	request := Request{
		Upstream: standardAnswer(t), Settings: settings, Catalog: testCatalog(),
		Preferences: settings.DefaultPreferences(), Checks: checks,
	}
	configs := build(t, request)
	germany := configs[2]
	var ai []string
	for _, balancer := range germany.Routing.Balancers {
		if strings.Contains(balancer.Tag, "-ai-") {
			ai = append(ai, balancer.Tag+"="+strings.Join(balancer.Selector, ","))
		}
	}
	if len(ai) != 1 || !strings.Contains(ai[0], "-ai-chatgpt=") || !strings.Contains(ai[0], "Finland 1") {
		t.Fatalf("AI balancers in the German group = %v", ai)
	}
	text := rulesText(t, []built{germany})
	if !strings.Contains(text, "domain:chatgpt.com") || strings.Contains(text, "domain:gemini.google.com") {
		t.Errorf("AI rules = %s", text)
	}
	// The AI server rides along in the German config, so its balancer can
	// reach it.
	found := false
	for _, outbound := range germany.Outbounds {
		if outbound.Tag == "🇫🇮 Finland 1" {
			found = true
		}
	}
	if !found {
		t.Error("the AI server is not in the group's outbounds")
	}

	request.Preferences.Toggles = map[string]bool{"ai": false}
	if text := rulesText(t, build(t, request)); strings.Contains(text, "chatgpt.com") {
		t.Error("the AI rule applied while turned off")
	}
}

func TestPinnedServerComesFirst(t *testing.T) {
	settings := testSettings()
	preferences := settings.DefaultPreferences()
	preferences.Pinned = finlandHost
	configs := build(t, Request{Upstream: standardAnswer(t), Settings: settings, Catalog: testCatalog(), Preferences: preferences, Locale: "ru"})
	if configs[0].Remarks != "📌 🇫🇮 Finland 1" {
		t.Fatalf("groups = %s", remarksOf(configs))
	}
	var entry []string
	for _, balancer := range configs[0].Routing.Balancers {
		if balancer.Tag == "g0-balancer" {
			entry = balancer.Selector
		}
	}
	if strings.Join(entry, ",") != "🇫🇮 Finland 1" {
		t.Errorf("the pinned group's main balancer = %v", entry)
	}
	// A pinned host the customer no longer has is ignored.
	preferences.Pinned = "00000000-0000-4000-8000-0000000000ff"
	if configs := build(t, Request{Upstream: standardAnswer(t), Settings: settings, Catalog: testCatalog(), Preferences: preferences}); strings.HasPrefix(configs[0].Remarks, "📌") {
		t.Error("a pin to a missing host made a group")
	}
}

func TestWhitelistModeStartsOnTheBypass(t *testing.T) {
	settings := testSettings()
	preferences := settings.DefaultPreferences()
	preferences.WhitelistMode = true
	configs := build(t, Request{Upstream: standardAnswer(t), Settings: settings, Catalog: testCatalog(), Preferences: preferences})
	var entry []string
	for _, balancer := range configs[0].Routing.Balancers {
		if balancer.Tag == "g0-balancer" {
			entry = balancer.Selector
		}
	}
	if strings.Join(entry, ",") != "Russia WHITE" {
		t.Errorf("in the whitelist mode the automatic group starts on %v", entry)
	}
	if normalized, _ := (Preferences{WhitelistMode: true}).Normalize(settings); !normalized.Reserve {
		t.Error("the whitelist mode left the bypass servers off")
	}
}
