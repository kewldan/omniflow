package smartsub

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestDetectCountry(t *testing.T) {
	cases := map[string]string{
		"🇫🇮 Helsinki 1":     "FI",
		"Finland 2":         "FI",
		"Финляндия":         "FI",
		"DE 1":              "DE",
		"Modern server":     "",
		"Russia WHITE 1":    "RU",
		"🇳🇱 Amsterdam":      "NL",
		"New Jersey":        "US",
		"Nigeria":           "NG",
		"🇩🇪 but says Paris": "DE",
		"🌐 Инструкция: lk.example.xyz": "",
		"": "",
	}
	for remark, want := range cases {
		if got := DetectCountry(remark); got != want {
			t.Errorf("DetectCountry(%q) = %q, want %q", remark, got, want)
		}
	}
}

func TestNormalizeDomain(t *testing.T) {
	cases := map[string]string{
		"Example.COM":                      "example.com",
		"https://www.example.com/path?q=1": "example.com",
		"www.com":                          "www.com",
		"*.example.com":                    "example.com",
		"пример.рф":                        "xn--e1afmkfd.xn--p1ai",
		"example.com:443":                  "example.com",
	}
	for raw, want := range cases {
		got, err := NormalizeDomain(raw)
		if err != nil || got != want {
			t.Errorf("NormalizeDomain(%q) = %q, %v; want %q", raw, got, err, want)
		}
	}
	for _, bad := range []string{"", "localhost", "exa mple.com", "-bad.com", "a..b"} {
		if _, err := NormalizeDomain(bad); err == nil {
			t.Errorf("NormalizeDomain(%q) accepted a non-domain", bad)
		}
	}
}

func TestSettingsNormalize(t *testing.T) {
	settings := DefaultSettings()
	settings.LinkBase = "https://sub.example.com/v1/sub"
	settings.Rules = []ServiceRule{{ID: "banks", Name: "Banks", Action: ActionDirect, Mode: ModeLocked, Domains: []string{"B.com", "a.com", "b.com"}}}
	settings.Hosts = map[string]HostOverride{
		"0F0E0D0C-0000-4000-8000-000000000001": {Country: "fi", Role: RoleReserve},
		"0f0e0d0c-0000-4000-8000-000000000002": {},
	}
	normalized, err := settings.Normalize()
	if err != nil {
		t.Fatal(err)
	}
	if normalized.LinkBase != "https://sub.example.com/v1/sub/" {
		t.Errorf("link base = %q", normalized.LinkBase)
	}
	if banks, _ := normalized.Rule("banks"); strings.Join(banks.Domains, ",") != "a.com,b.com" {
		t.Errorf("rule domains = %v", banks.Domains)
	}
	if len(normalized.Rules) != 1+len(builtinRules) {
		t.Errorf("the built-in rules were not filled in: %d rules", len(normalized.Rules))
	}
	if len(normalized.Hosts) != 1 || normalized.Hosts["0f0e0d0c-0000-4000-8000-000000000001"].Country != "FI" {
		t.Errorf("hosts = %v", normalized.Hosts)
	}

	// An exit node is kept lowercased; an override that only names an empty
	// one is dropped like any other empty override.
	chained := DefaultSettings()
	chained.Hosts = map[string]HostOverride{
		"0f0e0d0c-0000-4000-8000-000000000001": {ExitNode: " 0F0E0D0C-0000-4000-8000-0000000000AA "},
		"0f0e0d0c-0000-4000-8000-000000000002": {ExitNode: " "},
	}
	normalized, err = chained.Normalize()
	if err != nil {
		t.Fatal(err)
	}
	if len(normalized.Hosts) != 1 ||
		normalized.Hosts["0f0e0d0c-0000-4000-8000-000000000001"].ExitNode != "0f0e0d0c-0000-4000-8000-0000000000aa" {
		t.Errorf("chained hosts = %v", normalized.Hosts)
	}

	for name, broken := range map[string]func(*Settings){
		"probe interval": func(s *Settings) { s.ProbeIntervalSeconds = 5 },
		"max rules":      func(s *Settings) { s.MaxRules = MaxRulesCeiling + 1 },
		"country": func(s *Settings) {
			s.Hosts = map[string]HostOverride{"0f0e0d0c-0000-4000-8000-000000000001": {Country: "XX"}}
		},
		"role": func(s *Settings) {
			s.Hosts = map[string]HostOverride{"0f0e0d0c-0000-4000-8000-000000000001": {Role: "vip"}}
		},
		"exit node": func(s *Settings) {
			s.Hosts = map[string]HostOverride{"0f0e0d0c-0000-4000-8000-000000000001": {ExitNode: "node-1"}}
		},
		"host key":  func(s *Settings) { s.Hosts = map[string]HostOverride{"not-a-uuid": {Role: RoleHidden}} },
		"link base": func(s *Settings) { s.LinkBase = "sub.example.com" },
	} {
		candidate := DefaultSettings()
		broken(&candidate)
		if _, err := candidate.Normalize(); !errors.Is(err, ErrInvalid) {
			t.Errorf("%s: accepted, err = %v", name, err)
		}
	}
}

func TestPreferencesNormalize(t *testing.T) {
	settings := DefaultSettings()
	settings.MaxRules = 2
	preferences, err := Preferences{
		HiddenCountries: []string{"ru", "RU"},
		AutoExcluded:    []string{"RU", "us"},
		Rules:           []Rule{{Domain: "Sberbank.ru", Action: ActionDirect}},
	}.Normalize(settings)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(preferences.HiddenCountries, ",") != "RU" || strings.Join(preferences.AutoExcluded, ",") != "US" {
		t.Errorf("countries = %v / %v", preferences.HiddenCountries, preferences.AutoExcluded)
	}
	if preferences.Rules[0].Domain != "sberbank.ru" {
		t.Errorf("rules = %v", preferences.Rules)
	}

	tooMany := Preferences{Rules: []Rule{{"a.com", ActionDirect}, {"b.com", ActionDirect}, {"c.com", ActionDirect}}}
	if _, err := tooMany.Normalize(settings); !errors.Is(err, ErrInvalid) {
		t.Error("accepted more rules than the limit")
	}
	duplicate := Preferences{Rules: []Rule{{"a.com", ActionDirect}, {"A.com", ActionBlock}}}
	if _, err := duplicate.Normalize(settings); !errors.Is(err, ErrInvalid) {
		t.Error("accepted a duplicate domain")
	}
	settings.CustomRules = false
	if _, err := (Preferences{Rules: []Rule{{"a.com", ActionDirect}}}).Normalize(settings); !errors.Is(err, ErrInvalid) {
		t.Error("accepted a rule with custom rules off")
	}
	if _, err := (Preferences{HiddenCountries: []string{"XX"}}).Normalize(DefaultSettings()); !errors.Is(err, ErrInvalid) {
		t.Error("accepted an unknown country")
	}
}

// panelConfig is one config of the panel's Xray JSON answer.
func panelConfig(remark, address string, port int) map[string]any {
	return map[string]any{
		"remarks": remark,
		"log":     map[string]any{"loglevel": "warning"},
		"dns":     map[string]any{"servers": []any{"1.1.1.1"}},
		"inbounds": []any{
			map[string]any{"tag": "socks", "port": 10808, "protocol": "socks"},
		},
		"outbounds": []any{
			map[string]any{
				"tag": "proxy", "protocol": "vless",
				"settings": map[string]any{"vnext": []any{map[string]any{"address": address, "port": port}}},
			},
			map[string]any{"tag": "direct", "protocol": "freedom"},
			map[string]any{"tag": "block", "protocol": "blackhole"},
		},
		"routing": map[string]any{
			"domainStrategy": "AsIs",
			"rules": []any{
				map[string]any{"type": "field", "protocol": []any{"bittorrent"}, "outboundTag": "direct"},
				map[string]any{"type": "field", "domain": []any{"domain:example.org"}, "outboundTag": "proxy"},
			},
		},
		"meta": map[string]any{"serverDescription": "panel", "supportUrl": "https://t.me/support"},
	}
}

func panelAnswer(t *testing.T, configs ...map[string]any) []byte {
	t.Helper()
	encoded, err := json.Marshal(configs)
	if err != nil {
		t.Fatal(err)
	}
	return encoded
}

const (
	finlandHost = "00000000-0000-4000-8000-000000000001"
	whiteHost   = "00000000-0000-4000-8000-000000000002"
	testHost    = "00000000-0000-4000-8000-000000000003"
)

func testCatalog() Catalog {
	return Catalog{Hosts: []Host{
		{UUID: finlandHost, Remark: "🇫🇮 Finland 1", Address: "fi1.example.net", Port: 443, Nodes: []string{"n1"}},
		{UUID: whiteHost, Remark: "Russia WHITE", Address: "ru.example.net", Port: 443, Nodes: []string{"n2"}},
		{UUID: testHost, Remark: "Germany test", Address: "de-test.example.net", Port: 443},
	}}
}

func testSettings() Settings {
	settings := DefaultSettings()
	settings.Enabled = true
	settings.Hosts = map[string]HostOverride{
		whiteHost: {Role: RoleReserve},
		testHost:  {Role: RoleHidden},
	}
	normalized, err := settings.Normalize()
	if err != nil {
		panic(err)
	}
	return normalized
}

// withRule sets one built-in rule's mode, or appends a domain rule.
func withRule(settings Settings, rule ServiceRule) Settings {
	for index, existing := range settings.Rules {
		if existing.ID == rule.ID {
			settings.Rules[index] = rule
			return settings
		}
	}
	settings.Rules = append([]ServiceRule{rule}, settings.Rules...)
	return settings
}

type built struct {
	Remarks string `json:"remarks"`
	Meta    struct {
		ServerDescription string `json:"serverDescription"`
		SupportURL        string `json:"supportUrl"`
	} `json:"meta"`
	Log       any `json:"log"`
	Outbounds []struct {
		Tag      string `json:"tag"`
		Protocol string `json:"protocol"`
	} `json:"outbounds"`
	Routing struct {
		DomainStrategy string `json:"domainStrategy"`
		Balancers      []struct {
			Tag         string   `json:"tag"`
			Selector    []string `json:"selector"`
			FallbackTag string   `json:"fallbackTag"`
		} `json:"balancers"`
		Rules []map[string]any `json:"rules"`
	} `json:"routing"`
	BurstObservatory struct {
		SubjectSelector []string `json:"subjectSelector"`
	} `json:"burstObservatory"`
}

func build(t *testing.T, request Request) []built {
	t.Helper()
	out, err := BuildXray(request)
	if err != nil {
		t.Fatalf("BuildXray: %v", err)
	}
	var configs []built
	if err := json.Unmarshal(out, &configs); err != nil {
		t.Fatal(err)
	}
	return configs
}

func remarksOf(configs []built) string {
	names := make([]string, 0, len(configs))
	for _, config := range configs {
		names = append(names, config.Remarks)
	}
	return strings.Join(names, " | ")
}

func standardAnswer(t *testing.T) []byte {
	return panelAnswer(t,
		panelConfig("🇫🇮 Finland 1", "fi1.example.net", 443),
		panelConfig("Finland 2", "fi2.example.net", 443),
		panelConfig("🇩🇪 Germany 1", "de1.example.net", 443),
		panelConfig("Russia WHITE", "ru.example.net", 443),
		panelConfig("Germany test", "de-test.example.net", 443),
	)
}

func TestBuildXrayGroups(t *testing.T) {
	settings := testSettings()
	configs := build(t, Request{
		Upstream: standardAnswer(t), Settings: settings, Catalog: testCatalog(),
		Preferences: settings.DefaultPreferences(), Locale: "ru",
	})
	if got := remarksOf(configs); got != "⚡ Самый быстрый | 🇫🇮 Финляндия | 🇩🇪 Германия" {
		t.Fatalf("groups = %s", got)
	}
	auto := configs[0]
	if auto.Log != nil {
		t.Error("the panel's log section was carried over")
	}
	if auto.Meta.SupportURL != "https://t.me/support" || auto.Meta.ServerDescription != "Серверов: 3" {
		t.Errorf("meta = %+v", auto.Meta)
	}
	if auto.Routing.DomainStrategy != "AsIs" {
		t.Errorf("domain strategy = %q", auto.Routing.DomainStrategy)
	}
	// main → reserve → everything, chained through loopbacks.
	balancers := map[string][]string{}
	fallbacks := map[string]string{}
	for _, balancer := range auto.Routing.Balancers {
		balancers[balancer.Tag] = balancer.Selector
		fallbacks[balancer.Tag] = balancer.FallbackTag
	}
	if len(balancers["g0-balancer"]) != 3 || fallbacks["g0-balancer"] != "g0-main-loopback" {
		t.Errorf("entry balancer = %v → %q", balancers["g0-balancer"], fallbacks["g0-balancer"])
	}
	if strings.Join(balancers["g0-reserve-balancer"], ",") != "Russia WHITE" || fallbacks["g0-reserve-balancer"] != "g0-reserve-loopback" {
		t.Errorf("reserve balancer = %v", balancers["g0-reserve-balancer"])
	}
	if len(balancers["g0-all-balancer"]) != 4 {
		t.Errorf("last resort = %v", balancers["g0-all-balancer"])
	}
	for _, outbound := range auto.Outbounds {
		if outbound.Tag == "Germany test" {
			t.Error("a hidden host reached the subscription")
		}
	}
	// The whitelist bypass has no group of its own: every group falls back
	// to it before trying everything.
	for _, config := range configs {
		found := false
		for _, balancer := range config.Routing.Balancers {
			if strings.HasSuffix(balancer.Tag, "-reserve-balancer") {
				found = strings.Join(balancer.Selector, ",") == "Russia WHITE"
			}
		}
		if !found {
			t.Errorf("%s has no whitelist bypass fallback", config.Remarks)
		}
	}
	// The template's own rules keep their targets: torrents direct, a proxied
	// domain into the group's balancer; the last rule catches everything.
	rules := auto.Routing.Rules
	if last := rules[len(rules)-1]; last["balancerTag"] != "g0-balancer" || last["network"] != "tcp,udp" {
		t.Errorf("catch-all = %v", last)
	}
	foundTemplate := false
	for _, rule := range rules {
		if domains, ok := rule["domain"].([]any); ok && len(domains) == 1 && domains[0] == "domain:example.org" {
			foundTemplate = rule["balancerTag"] == "g0-balancer"
		}
	}
	if !foundTemplate {
		t.Error("the panel template's proxy rule was not retargeted at the balancer")
	}
}

func TestBuildXrayPreferences(t *testing.T) {
	settings := testSettings()
	settings = withRule(settings, ServiceRule{ID: "banks", Kind: KindDomains, Name: "Banks", Action: ActionDirect, Mode: ModeLocked, Domains: []string{"bank.example"}})
	preferences := Preferences{
		HiddenCountries: []string{"DE"}, AutoExcluded: []string{"FI"}, Reserve: false,
		Toggles: map[string]bool{"quic": true, "ads": true, "russia": false},
		Rules:   []Rule{{Domain: "chatgpt.com", Action: ActionProxy}, {Domain: "sberbank.ru", Action: ActionDirect}},
	}
	configs := build(t, Request{
		Upstream: standardAnswer(t), Settings: settings, Catalog: testCatalog(),
		Preferences: preferences, Locale: "en",
	})
	// Germany is hidden, Finland is out of the automatic group (which then has
	// nothing left), and the bypass is off.
	if got := remarksOf(configs); got != "🇫🇮 Finland" {
		t.Fatalf("groups = %s", got)
	}
	finland := configs[0]
	if len(finland.Routing.Balancers) != 1 {
		t.Errorf("finland without a bypass should have one balancer, got %d", len(finland.Routing.Balancers))
	}
	encoded, _ := json.Marshal(finland.Routing.Rules)
	text := string(encoded)
	for _, want := range []string{
		`"port":"443"`, `domain:chatgpt.com`, `domain:sberbank.ru`, `domain:bank.example`, `geosite:category-ads-all`,
	} {
		if !strings.Contains(text, want) {
			t.Errorf("rules lack %s: %s", want, text)
		}
	}
	if strings.Contains(text, "geosite:category-ru") {
		t.Error("the Russia preset applied while off")
	}
	if strings.Index(text, "chatgpt.com") > strings.Index(text, "bank.example") {
		t.Error("the customer's rules must come before the operator's direct list")
	}
}

func TestBuildXrayDomesticDNS(t *testing.T) {
	settings := testSettings()
	settings = withRule(settings, ServiceRule{ID: "banks", Kind: KindDomains, Name: "Banks", Action: ActionDirect, Mode: ModeLocked, Domains: []string{"bank.example"}})
	preferences := settings.DefaultPreferences()
	preferences.Toggles["russia"] = true
	out, err := BuildXray(Request{
		Upstream: standardAnswer(t), Settings: settings, Catalog: testCatalog(), Preferences: preferences,
	})
	if err != nil {
		t.Fatal(err)
	}
	var configs []struct {
		DNS struct {
			Tag     string `json:"tag"`
			Servers []any  `json:"servers"`
		} `json:"dns"`
		Routing struct {
			Rules []map[string]any `json:"rules"`
		} `json:"routing"`
	}
	if err := json.Unmarshal(out, &configs); err != nil {
		t.Fatal(err)
	}
	config := configs[0]
	// The domains that go direct are resolved by the domestic resolver, ahead
	// of the panel template's own servers.
	if config.DNS.Tag != dnsTag || len(config.DNS.Servers) != 2 || config.DNS.Servers[1] != "1.1.1.1" {
		t.Fatalf("dns = %+v", config.DNS)
	}
	domestic, _ := config.DNS.Servers[0].(map[string]any)
	encoded, _ := json.Marshal(domestic)
	for _, want := range []string{domesticDNS, "domain:bank.example", "geosite:category-ru", "full:captive.apple.com", `"skipFallback":true`} {
		if !strings.Contains(string(encoded), want) {
			t.Errorf("domestic resolver lacks %s: %s", want, encoded)
		}
	}
	// Its queries leave direct, and the rule comes before anything that could
	// send them into the tunnel.
	dnsRule, torrents := -1, 0
	for index, rule := range config.Routing.Rules {
		if tags, _ := rule["inboundTag"].([]any); len(tags) == 1 && tags[0] == dnsTag {
			dnsRule = index
			if ips, _ := rule["ip"].([]any); len(ips) != 1 || ips[0] != domesticDNS || rule["outboundTag"] != "direct" {
				t.Errorf("dns rule = %v", rule)
			}
		}
		if protocols, _ := rule["protocol"].([]any); len(protocols) == 1 && protocols[0] == "bittorrent" {
			torrents++
		}
		if domains, _ := rule["domain"].([]any); len(domains) > 0 && domains[0] == connectivityChecks[0] && rule["outboundTag"] != "direct" {
			t.Errorf("connectivity checks rule = %v", rule)
		}
	}
	if dnsRule < 0 || dnsRule > 3 {
		t.Errorf("dns rule at %d", dnsRule)
	}
	// The panel template's torrent rule is not repeated after the service's.
	if torrents != 1 {
		t.Errorf("%d torrent rules", torrents)
	}
}

func TestBuildXrayWithoutGeosite(t *testing.T) {
	settings := testSettings()
	settings = withRule(settings, ServiceRule{ID: "blocked", Kind: KindDomains, Name: "Blocked", Action: ActionBlock, Mode: ModeLocked, Domains: []string{"max.ru"}})
	preferences := settings.DefaultPreferences()
	preferences.Toggles["ads"] = true
	answer := panelConfig("Finland 1", "a.example.net", 443)
	answer["routing"] = map[string]any{"rules": []any{
		map[string]any{"type": "field", "domain": []any{"geosite:google"}, "outboundTag": "direct"},
		map[string]any{"type": "field", "domain": []any{"geosite:openai", "domain:example.org"}, "outboundTag": "proxy"},
	}}
	request := Request{Upstream: panelAnswer(t, answer), Settings: settings, Preferences: preferences}
	with, _ := BuildXray(request)
	request.NoGeosite = true
	without, _ := BuildXray(request)
	if !strings.Contains(string(with), "geosite:category-ru") || !strings.Contains(string(with), "geosite:category-ads-all") {
		t.Error("a client that reads geosite lost the geosite rules")
	}
	// Russian sites go direct by geosite only: never whole zones or Russian
	// IP ranges, with or without geosite.
	for _, body := range [][]byte{with, without} {
		for _, wide := range []string{`"geoip:ru"`, `"domain:ru"`, `"domain:su"`, `"domain:xn--p1ai"`} {
			if strings.Contains(string(body), wide) {
				t.Errorf("the Russia preset sends %s direct", wide)
			}
		}
	}
	if strings.Contains(string(without), "geosite:") {
		t.Errorf("a client that stalls on geosite still got it: %s", without)
	}
	for _, body := range [][]byte{with, without} {
		if !strings.Contains(string(body), "domain:max.ru") || !strings.Contains(string(body), "domain:example.org") {
			t.Error("the operator's block list or a template domain went missing")
		}
	}
	if !StripsGeosite("INCY/2.1") || StripsGeosite("Happ/3") {
		t.Error("StripsGeosite picks the wrong clients")
	}
}

func TestBuildXrayKeepsOneServer(t *testing.T) {
	settings := testSettings()
	request := Request{
		Upstream: standardAnswer(t), Settings: settings, Catalog: testCatalog(),
		Preferences: settings.DefaultPreferences(), StickyKey: "abcDEF123456",
	}
	kept := func(config built) (string, string) {
		for _, balancer := range config.Routing.Balancers {
			if strings.HasSuffix(balancer.Tag, "-sticky-balancer") {
				return strings.Join(balancer.Selector, ","), balancer.FallbackTag
			}
		}
		return "", ""
	}
	first := build(t, request)
	server, fallback := kept(first[0])
	if server == "" || strings.Contains(server, ",") || fallback != "g0-sticky-loopback" {
		t.Fatalf("automatic group keeps %q → %q", server, fallback)
	}
	// Everything not routed elsewhere leaves through the kept server, and the
	// loopback hands it back to the group's own balancer.
	rules := first[0].Routing.Rules
	if last := rules[len(rules)-1]; last["balancerTag"] != "g0-sticky-balancer" {
		t.Errorf("catch-all = %v", last)
	}
	handedBack := false
	for _, rule := range rules {
		if inbound, ok := rule["inboundTag"].([]any); ok && len(inbound) == 1 && inbound[0] == "g0-sticky-lb-in" {
			handedBack = rule["balancerTag"] == "g0-balancer"
		}
	}
	if !handedBack {
		t.Error("the kept server does not fall back to the group's balancer")
	}
	// The same link keeps the same server on every refresh.
	if again, _ := kept(build(t, request)[0]); again != server {
		t.Errorf("refresh moved the server: %q → %q", server, again)
	}
	// Different links spread over the servers.
	seen := map[string]bool{}
	for index := range 40 {
		request.StickyKey = "key" + strconv.Itoa(index)
		chosen, _ := kept(build(t, request)[0])
		seen[chosen] = true
	}
	if len(seen) < 2 {
		t.Errorf("forty links all kept %v", seen)
	}
	// A country with one server needs no choice; without a key nothing is kept.
	germany := false
	for _, config := range first[1:] {
		if config.Remarks == "🇩🇪 Germany" {
			germany = true
			if got, _ := kept(config); got != "" {
				t.Errorf("one-server group keeps %q", got)
			}
		}
	}
	if !germany {
		t.Errorf("groups = %s", remarksOf(first))
	}
	request.StickyKey = ""
	if got, _ := kept(build(t, request)[0]); got != "" {
		t.Errorf("no key, kept %q", got)
	}
}

func TestBuildXrayHidingEverythingKeepsServers(t *testing.T) {
	settings := testSettings()
	configs := build(t, Request{
		Upstream: standardAnswer(t), Settings: settings, Catalog: testCatalog(),
		Preferences: Preferences{HiddenCountries: []string{"FI", "DE"}, Reserve: false},
	})
	if len(configs) == 0 || configs[0].Routing.Balancers == nil {
		t.Fatalf("groups = %s", remarksOf(configs))
	}
}

func TestBuildXrayDownNodes(t *testing.T) {
	settings := testSettings()
	catalog := testCatalog()
	catalog.DownNodes = map[string]bool{"n1": true}
	configs := build(t, Request{
		Upstream: standardAnswer(t), Settings: settings, Catalog: catalog,
		Preferences: settings.DefaultPreferences(),
	})
	for _, config := range configs {
		for _, tag := range config.BurstObservatory.SubjectSelector {
			if tag == "🇫🇮 Finland 1" {
				t.Errorf("%s still probes a server whose node is down", config.Remarks)
			}
		}
	}
}

func TestBuildXrayPrefixCollision(t *testing.T) {
	settings := testSettings()
	answer := panelAnswer(t,
		panelConfig("Finland 1", "a.example.net", 443),
		panelConfig("Finland 10", "b.example.net", 443),
	)
	configs := build(t, Request{Upstream: answer, Settings: settings, Preferences: settings.DefaultPreferences()})
	for _, tag := range configs[0].Routing.Balancers[0].Selector {
		if !strings.HasPrefix(tag, "s") || !strings.Contains(tag, "_") {
			t.Errorf("selector %q can still match by prefix", tag)
		}
	}
}

func TestBuildXrayPassesThrough(t *testing.T) {
	fake := panelAnswer(t, panelConfig("Subscription expired", "0.0.0.0", 1))
	for name, body := range map[string][]byte{
		"limit notice": fake,
		"html":         []byte("<html>blocked</html>"),
		"empty array":  []byte("[]"),
		"links":        []byte(base64.StdEncoding.EncodeToString([]byte("vless://a@b:1#x"))),
	} {
		if _, err := BuildXray(Request{Upstream: body, Settings: DefaultSettings()}); !errors.Is(err, ErrPassThrough) {
			t.Errorf("%s: err = %v", name, err)
		}
	}
}

func TestBuildXrayDialChain(t *testing.T) {
	config := panelConfig("Finland 1", "a.example.net", 443)
	outbounds := config["outbounds"].([]any)
	proxy := outbounds[0].(map[string]any)
	proxy["streamSettings"] = map[string]any{"sockopt": map[string]any{"dialerProxy": "fragment"}}
	config["outbounds"] = append(outbounds, map[string]any{"tag": "fragment", "protocol": "freedom"})
	settings := testSettings()
	out, err := BuildXray(Request{Upstream: panelAnswer(t, config), Settings: settings, Preferences: settings.DefaultPreferences()})
	if err != nil {
		t.Fatal(err)
	}
	text := string(out)
	if !strings.Contains(text, `"dialerProxy":"fragment~0"`) || !strings.Contains(text, `"tag":"fragment~0"`) {
		t.Errorf("the dial chain was not carried: %s", text)
	}
	// The source config is not mutated.
	if proxy["streamSettings"].(map[string]any)["sockopt"].(map[string]any)["dialerProxy"] != "fragment" {
		t.Error("the panel's config was mutated")
	}
}

func TestBuildXrayIsDeterministic(t *testing.T) {
	settings := testSettings()
	request := Request{Upstream: standardAnswer(t), Settings: settings, Catalog: testCatalog(), Preferences: settings.DefaultPreferences()}
	first, _ := BuildXray(request)
	for range 5 {
		again, _ := BuildXray(request)
		if string(again) != string(first) {
			t.Fatal("the same input produced a different body, which would change the ETag")
		}
	}
}

func TestFilterLinks(t *testing.T) {
	links := strings.Join([]string{
		"vless://id@fi1.example.net:443?security=reality#" + "%F0%9F%87%AB%F0%9F%87%AE%20Finland%201",
		"vless://id@de1.example.net:443#Germany%201",
		"vless://id@ru.example.net:443#Russia%20WHITE",
		"vless://id@de-test.example.net:443#Germany%20test",
	}, "\n")
	body := []byte(base64.StdEncoding.EncodeToString([]byte(links)))
	settings := testSettings()
	out, err := FilterLinks(Request{
		Upstream: body, Settings: settings, Catalog: testCatalog(),
		Preferences: Preferences{HiddenCountries: []string{"DE"}, Reserve: false},
	})
	if err != nil {
		t.Fatal(err)
	}
	decoded, _ := base64.StdEncoding.DecodeString(string(out))
	if got := string(decoded); strings.Count(got, "\n") != 0 || !strings.Contains(got, "fi1.example.net") {
		t.Errorf("filtered = %q", got)
	}
	if _, err := FilterLinks(Request{Upstream: []byte("mixed-port: 7890"), Settings: settings}); !errors.Is(err, ErrPassThrough) {
		t.Error("a YAML answer was not passed through")
	}
}

func TestDetectFormat(t *testing.T) {
	for agent, want := range map[string]Format{
		"Happ/3.2.1":          FormatXray,
		"v2rayTun/2.1 (ios)":  FormatXray,
		"Streisand":           FormatXray,
		"clash-verge/v2":      FormatLinks,
		"INCY/1.0":            FormatXray,
		"Mozilla/5.0 (Linux)": FormatLinks,
		"":                    FormatLinks,
	} {
		if got := DetectFormat(agent); got != want {
			t.Errorf("DetectFormat(%q) = %s, want %s", agent, got, want)
		}
	}
}

func TestHostsFor(t *testing.T) {
	catalog := Catalog{
		SquadInbounds: map[string][]string{"sq-a": {"in-1", "in-2"}, "sq-b": {"in-3"}},
		Hosts: []Host{
			{UUID: "h1", InboundUUID: "in-1", SquadMode: "EXCLUDE"},
			{UUID: "h2", InboundUUID: "in-2", SquadMode: "EXCLUDE", Squads: []string{"sq-a"}},
			{UUID: "h3", InboundUUID: "in-3", SquadMode: "EXCLUDE"},
			{UUID: "h4", InboundUUID: "in-1", SquadMode: "ALLOW_ONLY", Squads: []string{"sq-a"}},
			{UUID: "h5", InboundUUID: "in-1", SquadMode: "ALLOW_ONLY", Squads: []string{"sq-b"}},
			{UUID: "h6", InboundUUID: "in-1", SquadMode: "EXCLUDE", Disabled: true},
			{UUID: "h7", InboundUUID: "in-1", SquadMode: "EXCLUDE", Hidden: true},
		},
	}
	var got []string
	for _, host := range catalog.HostsFor([]string{"sq-a"}) {
		got = append(got, host.UUID)
	}
	if strings.Join(got, ",") != "h1,h4" {
		t.Errorf("hosts for sq-a = %v", got)
	}
	if len(catalog.HostsFor(nil)) != 0 {
		t.Error("a user in no squad got hosts")
	}
}

func TestBuildView(t *testing.T) {
	view := BuildView([]Entry{
		{Remark: "🇫🇮 Finland 1", Address: "fi1.example.net", Port: 443},
		{Remark: "Finland 2", Address: "fi2.example.net", Port: 443},
		{Remark: "Mystery", Address: "x.example.net", Port: 443},
		{Remark: "Russia WHITE", Address: "ru.example.net", Port: 443},
		{Remark: "Germany test", Address: "de-test.example.net", Port: 443},
		{Remark: "Panel hidden", Address: "h.example.net", Port: 443, Hidden: true},
		{Remark: "⚠️ Приложение не поддерживается", Address: "0.0.0.0", Port: 1},
	}, testCatalog(), testSettings(), nil)
	got := fmt.Sprintf("%d %s %d %s %d", len(view.Countries), view.Countries[0].Code, len(view.Countries[0].Servers),
		view.Countries[1].Code, len(view.Reserve))
	if got != "2 FI 2 ZZ 1" {
		t.Errorf("view = %s", got)
	}

	// A server shows the latest check of the node it exits from: its own
	// node, or the one the operator named for a chained host.
	settings := testSettings()
	checks := map[string]NodeCheck{
		"n1":    {Services: map[string]ServiceStatus{"gemini": {State: StateYes}}},
		"exit2": {Services: map[string]ServiceStatus{"gemini": {State: StateNo, Region: "RUS"}}},
	}
	marked := BuildView([]Entry{
		{Remark: "🇫🇮 Finland 1", Address: "fi1.example.net", Port: 443},
		{Remark: "Finland 2", Address: "fi2.example.net", Port: 443},
	}, testCatalog(), settings, checks)
	first, second := marked.Countries[0].Servers[0], marked.Countries[0].Servers[1]
	if first.Services["gemini"].State != StateYes || second.Services != nil {
		t.Errorf("services = %v, %v", first.Services, second.Services)
	}
	encoded, err := json.Marshal(second)
	if err != nil || strings.Contains(string(encoded), "services") {
		t.Errorf("unchecked server encodes as %s", encoded)
	}
	settings.Hosts[finlandHost] = HostOverride{ExitNode: "exit2"}
	chained := BuildView([]Entry{{Remark: "🇫🇮 Finland 1", Address: "fi1.example.net", Port: 443}},
		testCatalog(), settings, checks)
	if status := chained.Countries[0].Servers[0].Services["gemini"]; status.State != StateNo || status.Region != "RUS" {
		t.Errorf("chained host services = %v", chained.Countries[0].Servers[0].Services)
	}
}

// geocheckFixture is a trimmed Remnawave geocheck report, shaped like a real
// one from a node Google places in Russia and Reddit turns away.
const geocheckFixture = `{
 "schema": 1, "timestamp": "2026-11-24T07:29:45Z",
 "identity": {"ipv4": "192.0.2.10"},
 "geo": {"services": [
  {"id": "google", "kind": "country", "ipv4": {"value": "RU"}},
  {"id": "youtube", "kind": "country", "ipv4": {"value": "RU"}},
  {"id": "reddit", "kind": "country", "ipv4": {"error": "blocked by Reddit's network security"}},
  {"id": "reddit_guest", "kind": "availability", "ipv4": {"value": "no"}},
  {"id": "spotify", "kind": "country", "ipv4": {"value": "AT"}}
 ]},
 "stash_checks": [
  {"id": "chatgpt_web", "state": "available"},
  {"id": "netflix_access", "state": "available", "region": "AT", "detail": "originals only"},
  {"id": "claude_access", "state": "error", "detail": "Cloudflare challenged the request"},
  {"id": "tiktok_access", "state": "available"},
  {"id": "gemini_access", "state": "blocked", "region": "RUS", "detail": "not offered in this country"}
 ],
 "ai_endpoints": [{"id": "anthropic", "state": "reachable", "http_status": 401}],
 "reputation": {"city": "Vienna", "country_code": "AT"},
 "consensus": {"ipv4": [{"code": "AT", "percent": 93.1}, {"code": "RU", "percent": 6.9}]},
 "connectivity": {"latency_floor_ms": 5.76, "targets": [
  {"id": "google", "verdict": "direct", "rtt_ms": 6.0},
  {"id": "telegram", "verdict": "transit", "rtt_ms": 15.0},
  {"id": "telegram_dc5", "verdict": "detour", "rtt_ms": 155.0},
  {"id": "github", "verdict": "failed"}
 ]}
}`

func TestSummarizeGeocheck(t *testing.T) {
	check, err := SummarizeGeocheck([]byte(geocheckFixture), time.Unix(0, 0))
	if err != nil {
		t.Fatal(err)
	}
	if !check.CheckedAt.Equal(time.Date(2026, 11, 24, 7, 29, 45, 0, time.UTC)) {
		t.Errorf("checked at = %v", check.CheckedAt)
	}
	want := map[string]ServiceStatus{
		"gemini":  {State: StateNo, Region: "RUS"},
		"youtube": {State: StateLimited, Region: "RU"},
		"reddit":  {State: StateNo},
		"chatgpt": {State: StateYes},
		"claude":  {State: StateYes},
		"netflix": {State: StateLimited, Region: "AT"},
		"spotify": {State: StateYes, Region: "AT"},
		"tiktok":  {State: StateYes},
	}
	if len(check.Services) != len(want) {
		t.Errorf("services = %v", check.Services)
	}
	for service, status := range want {
		if check.Services[service] != status {
			t.Errorf("%s = %v, want %v", service, check.Services[service], status)
		}
	}

	if check.Place == nil || check.Place.City != "Vienna" || check.Floor != 5.76 ||
		len(check.Latency) != 2 || check.Latency["telegram"] != 15 {
		t.Errorf("place, floor, latency = %+v, %v, %v", check.Place, check.Floor, check.Latency)
	}

	// A check that settled nothing has no services, and a broken report is
	// an error rather than an empty success.
	empty, err := SummarizeGeocheck([]byte(`{"geo":{"services":[{"id":"youtube","ipv4":{"error":"timeout"}}]}}`), time.Unix(0, 0))
	if err != nil || len(empty.Services) != 0 {
		t.Errorf("empty = %v, %v", empty, err)
	}
	if _, err := SummarizeGeocheck([]byte(`not json`), time.Unix(0, 0)); err == nil {
		t.Error("a broken report was accepted")
	}
}

func TestLocate(t *testing.T) {
	for _, testCase := range []struct {
		city, cityCountry, consensus string
		want                         string
	}{
		{"Warsaw", "PL", "PL", "Warsaw/PL"},
		// No agreed country: the named city stands.
		{"Bolton", "GB", "", "Bolton/GB"},
		// The databases agree on another country: its usual city wins.
		{"Stockholm", "SE", "FI", "/FI"},
		// An unknown city in a known country.
		{"Nowhere", "NL", "NL", "/NL"},
	} {
		place := locate(testCase.city, testCase.cityCountry, testCase.consensus)
		if place == nil || place.City+"/"+place.Country != testCase.want {
			t.Errorf("locate(%q, %q, %q) = %+v, want %s", testCase.city, testCase.cityCountry, testCase.consensus, place, testCase.want)
		}
	}
	if place := locate("Nowhere", "ZZ", ""); place != nil {
		t.Errorf("an unknown place was placed: %+v", place)
	}
}

func TestHopMs(t *testing.T) {
	warsaw, amsterdam := locate("Warsaw", "PL", "PL"), locate("Amsterdam", "NL", "NL")
	// About 1100 km apart: two dozen milliseconds, as measured.
	hop, ok := HopMs(NodeCheck{Place: warsaw}, NodeCheck{Place: amsterdam})
	if !ok || hop < 22 || hop > 26 {
		t.Errorf("Warsaw→Amsterdam = %.1f, %v", hop, ok)
	}
	// Nodes answering the sites that tell places apart alike share a data
	// centre, whatever their cities say.
	stockholm, helsinki := locate("Stockholm", "SE", "SE"), locate("Helsinki", "FI", "FI")
	alike := map[string]float64{"google": 8.9, "cloudflare": 28.1, "telegram": 32.9, "discord": 28.2, "tiktok": 7.8, "apple": 8.1, "near": 1.1}
	twin := map[string]float64{"google": 8.9, "cloudflare": 28.1, "telegram": 29.7, "discord": 28.1, "tiktok": 7.7, "apple": 7.6, "near": 1.2}
	hop, ok = HopMs(NodeCheck{Place: stockholm, Floor: 1.5, Latency: alike}, NodeCheck{Place: helsinki, Floor: 1.2, Latency: twin})
	if !ok || hop != 1.5 {
		t.Errorf("co-located hop = %.1f, %v", hop, ok)
	}
	// Sites every node reaches in a millisecond or two prove nothing.
	near := map[string]float64{"a": 1.1, "b": 1.6, "c": 1.8, "d": 1.4, "e": 1.9, "f": 1.2}
	hop, ok = HopMs(NodeCheck{Place: warsaw, Latency: near}, NodeCheck{Place: amsterdam, Latency: near})
	if !ok || hop < 22 {
		t.Errorf("well-peered nodes read as co-located: %.1f, %v", hop, ok)
	}
	if _, ok := HopMs(NodeCheck{Place: warsaw}, NodeCheck{}); ok {
		t.Error("a hop to a node with no place was estimated")
	}
}

func TestPing(t *testing.T) {
	checks := map[string]NodeCheck{
		"pl": {Place: locate("Warsaw", "PL", "PL"), Latency: map[string]float64{"a": 1, "b": 2, "c": 3}},
		"nl": {Place: locate("Amsterdam", "NL", "NL"), Latency: map[string]float64{"a": 1, "b": 1.5}},
	}
	// One node: the exit's median site latency and one node's processing.
	if model := Ping(Server{EntryNode: "pl", ExitNode: "pl", Transport: TransportTCP}, checks); model == nil || model.TailMs != 3 {
		t.Errorf("single node = %+v", model)
	}
	// A chain adds the hop and a second node.
	model := Ping(Server{EntryNode: "pl", ExitNode: "nl", Transport: TransportUDP}, checks)
	if model == nil || model.Transport != TransportUDP || model.TailMs < 26 || model.TailMs > 30 {
		t.Errorf("chain = %+v", model)
	}
	for name, server := range map[string]Server{
		"unknown transport":  {EntryNode: "pl", ExitNode: "pl"},
		"exit never checked": {EntryNode: "pl", ExitNode: "de", Transport: TransportTCP},
		"entry not known":    {ExitNode: "nl", Transport: TransportTCP},
	} {
		if model := Ping(server, checks); model != nil {
			t.Errorf("%s: estimated %+v", name, model)
		}
	}
	if TransportOf("hysteria") != TransportUDP || TransportOf("vless") != TransportTCP || TransportOf("") != "" {
		t.Error("transport of an inbound protocol is wrong")
	}
}
