package smartsub

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
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
	settings.DirectDomains = []string{"B.com", "a.com", "b.com"}
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
	if strings.Join(normalized.DirectDomains, ",") != "a.com,b.com" {
		t.Errorf("direct domains = %v", normalized.DirectDomains)
	}
	if len(normalized.Hosts) != 1 || normalized.Hosts["0f0e0d0c-0000-4000-8000-000000000001"].Country != "FI" {
		t.Errorf("hosts = %v", normalized.Hosts)
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
	if got := remarksOf(configs); got != "⚡ Самый быстрый | 🇫🇮 Финляндия | 🇩🇪 Германия | 🛡 Обход белых списков" {
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
	// The reserve group stands alone, without a fallback chain.
	reserve := configs[len(configs)-1]
	if len(reserve.Routing.Balancers) != 1 || reserve.Routing.Balancers[0].FallbackTag != "" {
		t.Errorf("reserve group balancers = %+v", reserve.Routing.Balancers)
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
	settings.DirectDomains = []string{"bank.example"}
	preferences := Preferences{
		HiddenCountries: []string{"DE"}, AutoExcluded: []string{"FI"}, Reserve: false,
		Presets: Presets{BlockQUIC: true, BlockAds: true},
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
	if strings.Contains(text, "geoip:ru") {
		t.Error("the Russia preset applied while off")
	}
	if strings.Index(text, "chatgpt.com") > strings.Index(text, "bank.example") {
		t.Error("the customer's rules must come before the operator's direct list")
	}
}

func TestBuildXrayWithoutGeosite(t *testing.T) {
	settings := testSettings()
	settings.BlockDomains = []string{"max.ru"}
	preferences := settings.DefaultPreferences()
	preferences.Presets.BlockAds = true
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
	}, testCatalog(), testSettings())
	got := fmt.Sprintf("%d %s %d %s %d", len(view.Countries), view.Countries[0].Code, len(view.Countries[0].Servers),
		view.Countries[1].Code, len(view.Reserve))
	if got != "2 FI 2 ZZ 1" {
		t.Errorf("view = %s", got)
	}
}
