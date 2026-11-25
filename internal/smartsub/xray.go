package smartsub

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"hash/fnv"
	"sort"
	"strconv"
	"strings"
)

// ErrPassThrough means the panel's answer is not something the module can
// rewrite — an error page, a limit notice, a format it does not handle — and
// it should reach the client exactly as the panel sent it.
var ErrPassThrough = errors.New("smart subscription passes this response through")

// Request is everything one rewrite needs.
type Request struct {
	Upstream    []byte
	Settings    Settings
	Preferences Preferences
	Catalog     Catalog
	// Locale picks the language group names are written in.
	Locale string
	// NoGeosite leaves out every rule that names the geosite database, for a
	// client that stalls on it (StripsGeosite).
	NoGeosite bool
	// Checks are the nodes' latest geocheck summaries by node UUID; the AI
	// rule routes each service through servers whose exit node it answers.
	Checks map[string]NodeCheck
	// StickyKey picks, in each group, the one server this subscription keeps
	// while it answers (the link's short UUID). Empty keeps no server.
	StickyKey string
}

// Tuning of the balancers each group gets. The numbers are the ones the
// subscription balancer this package replaces settled on in production.
const (
	loadExpected      = 3
	tolerance         = 0.5
	toleranceFallback = 0.8
	mainBaselineMS    = 2000
	singleBaselineMS  = 4000
	reserveBaselineMS = 4000
	// stickyBaselineMS is how slow the kept server may get before the group's
	// own balancer takes over. It is above the one second the subscription
	// balancer used, which dropped chained servers too readily.
	stickyBaselineMS = 2000
	probeSampling    = 1
	// probeTimeout is how long a server may take to answer the check before
	// the balancer counts it as down. A chain through a relay or the
	// whitelist bypass takes two to three and a half seconds from Russia; a
	// shorter limit marks those down for good, and a group then never falls
	// back to the bypass.
	probeTimeout        = "5s"
	defaultDomainPolicy = "IPIfNonMatch"
)

// systemProtocols are outbounds that are not servers.
var systemProtocols = map[string]bool{"freedom": true, "blackhole": true, "dns": true, "loopback": true}

// managedFields are the template keys a group config writes itself; every
// other key of the panel's first config is carried into each group as is.
var managedFields = map[string]bool{
	"remarks": true, "meta": true, "dns": true, "inbounds": true, "outbounds": true,
	"routing": true, "burstObservatory": true, "observatory": true, "log": true,
}

// privateRanges always go direct: a tunnel to the local network is never
// what anyone wants, and listing them as ranges needs no geo database.
var privateRanges = []any{
	"127.0.0.0/8", "10.0.0.0/8", "172.16.0.0/12", "192.168.0.0/16", "169.254.0.0/16",
	"::1/128", "fc00::/7", "fe80::/10",
}

// proxy is one server outbound found in the panel's answer.
type proxy struct {
	outbound map[string]any
	// aux are outbounds it dials through (a fragment or mux chain), renamed
	// so two servers' chains cannot collide once they share a config.
	aux    []map[string]any
	server Server
	order  int
}

type template struct {
	inherited map[string]any
	dns       any
	inbounds  any
	meta      map[string]any
	// rules are the panel template's own routing rules, with the target
	// rewritten: "proxy" for the group's balancer, "direct" or "block".
	rules          []templateRule
	direct, block  map[string]any
	domainStrategy string
}

type templateRule struct {
	rule   map[string]any
	target string // "balancer", "direct", or "block"
}

// BuildXray rewrites the panel's Xray JSON subscription — an array with one
// config per server — into one config per group.
//
// The groups are the automatic group over every country the customer left in
// it and one group per visible country. Each keeps one server per
// subscription in front of a leastLoad balancer over its servers, and falls
// back onto the whitelist bypass servers when none of its own answers, through
// a loopback chain because Xray's fallbackTag takes an outbound, not a
// balancer.
func BuildXray(request Request) ([]byte, error) {
	configs, err := decodeConfigs(request.Upstream)
	if err != nil {
		return nil, ErrPassThrough
	}
	proxies, fakes := collectProxies(configs, request.Catalog, request.Settings)
	if len(proxies) == 0 || fakes == len(proxies) {
		// A panel answering with a limit or expiry notice sends servers at
		// 0.0.0.0:1. The notice is the message; it must reach the client.
		return nil, ErrPassThrough
	}
	tpl := templateOf(configs[0])
	preferences := request.Preferences
	// A mode the operator no longer offers is ignored, even if it was saved.
	if !request.Settings.WhitelistMode {
		preferences.WhitelistMode = false
	}
	groups := plan(proxies, preferences)
	if len(groups) == 0 {
		return nil, ErrPassThrough
	}
	builder := groupBuilder{request: request, tpl: tpl, ai: aiPools(groups, request)}
	out := make([]any, 0, len(groups))
	for index, group := range groups {
		out = append(out, builder.build(group, "g"+strconv.Itoa(index)))
	}
	encoded, err := json.Marshal(out)
	if err != nil {
		return nil, fmt.Errorf("encode smart subscription: %w", err)
	}
	return encoded, nil
}

func decodeConfigs(body []byte) ([]map[string]any, error) {
	trimmed := bytes.TrimSpace(body)
	if len(trimmed) == 0 || trimmed[0] != '[' {
		return nil, ErrPassThrough
	}
	decoder := json.NewDecoder(bytes.NewReader(trimmed))
	decoder.UseNumber()
	var configs []map[string]any
	if err := decoder.Decode(&configs); err != nil || len(configs) == 0 {
		return nil, ErrPassThrough
	}
	return configs, nil
}

func collectProxies(configs []map[string]any, catalog Catalog, settings Settings) ([]*proxy, int) {
	var proxies []*proxy
	fakes := 0
	for index, config := range configs {
		remark, _ := config["remarks"].(string)
		outbounds, _ := config["outbounds"].([]any)
		byTag := map[string]map[string]any{}
		for _, raw := range outbounds {
			if outbound, ok := raw.(map[string]any); ok {
				if tag, _ := outbound["tag"].(string); tag != "" {
					byTag[tag] = outbound
				}
			}
		}
		for _, raw := range outbounds {
			outbound, ok := raw.(map[string]any)
			if !ok {
				continue
			}
			protocol, _ := outbound["protocol"].(string)
			tag, _ := outbound["tag"].(string)
			if tag == "" || systemProtocols[protocol] {
				continue
			}
			address, port := endpoint(outbound)
			if address == "0.0.0.0" && port == 1 {
				fakes++
			}
			name := remark
			if name == "" || tag != "proxy" {
				name = strings.TrimSpace(remark + " " + tag)
			}
			copied := shallow(outbound)
			copied["tag"] = name
			found := &proxy{
				outbound: copied, order: len(proxies),
				server: catalog.Classify(remark, address, port, settings),
			}
			found.aux = dialChain(copied, byTag, index)
			proxies = append(proxies, found)
		}
	}
	return proxies, fakes
}

// endpoint reads a server's address and port from the shapes Xray accepts:
// vnext (VLESS, VMess), servers (Trojan, Shadowsocks), or the flat form.
func endpoint(outbound map[string]any) (string, int) {
	settings, _ := outbound["settings"].(map[string]any)
	if settings == nil {
		return "", 0
	}
	for _, key := range []string{"vnext", "servers"} {
		if list, ok := settings[key].([]any); ok && len(list) > 0 {
			if first, ok := list[0].(map[string]any); ok {
				return stringOf(first["address"]), intOf(first["port"])
			}
		}
	}
	return stringOf(settings["address"]), intOf(settings["port"])
}

// dialChain carries the outbounds a server dials through. Xray names them in
// streamSettings.sockopt.dialerProxy; once several servers share one config
// their chains would collide by tag, so each is renamed for its config and the
// reference rewritten — on copies, since the nested objects are shared.
func dialChain(outbound map[string]any, byTag map[string]map[string]any, configIndex int) []map[string]any {
	stream, _ := outbound["streamSettings"].(map[string]any)
	sockopt, _ := stream["sockopt"].(map[string]any)
	through, _ := sockopt["dialerProxy"].(string)
	target, ok := byTag[through]
	if through == "" || !ok {
		return nil
	}
	renamed := fmt.Sprintf("%s~%d", through, configIndex)
	newSockopt := shallow(sockopt)
	newSockopt["dialerProxy"] = renamed
	newStream := shallow(stream)
	newStream["sockopt"] = newSockopt
	outbound["streamSettings"] = newStream
	chained := shallow(target)
	chained["tag"] = renamed
	return []map[string]any{chained}
}

func templateOf(config map[string]any) template {
	tpl := template{inherited: map[string]any{}, domainStrategy: defaultDomainPolicy}
	for key, value := range config {
		if !managedFields[key] {
			tpl.inherited[key] = value
		}
	}
	tpl.dns = config["dns"]
	if tpl.dns == nil {
		tpl.dns = map[string]any{"servers": []any{"1.1.1.1", "1.0.0.1"}, "queryStrategy": "UseIP"}
	}
	tpl.inbounds = config["inbounds"]
	if tpl.inbounds == nil {
		tpl.inbounds = []any{
			map[string]any{
				"tag": "socks", "port": 10808, "listen": "127.0.0.1", "protocol": "socks",
				"settings": map[string]any{"udp": true, "auth": "noauth"},
				"sniffing": map[string]any{"enabled": true, "routeOnly": false, "destOverride": []any{"http", "tls", "quic"}},
			},
			map[string]any{
				"tag": "http", "port": 10809, "listen": "127.0.0.1", "protocol": "http",
				"sniffing": map[string]any{"enabled": true, "routeOnly": false, "destOverride": []any{"http", "tls", "quic"}},
			},
		}
	}
	if meta, ok := config["meta"].(map[string]any); ok {
		tpl.meta = meta
	}

	kinds := map[string]string{}
	outbounds, _ := config["outbounds"].([]any)
	for _, raw := range outbounds {
		outbound, _ := raw.(map[string]any)
		tag, _ := outbound["tag"].(string)
		switch protocol, _ := outbound["protocol"].(string); protocol {
		case "freedom":
			kinds[tag] = "direct"
			if tpl.direct == nil {
				tpl.direct = outbound
			}
		case "blackhole":
			kinds[tag] = "block"
			if tpl.block == nil {
				tpl.block = outbound
			}
		case "dns", "loopback":
		default:
			if tag != "" {
				kinds[tag] = "balancer"
			}
		}
	}
	routing, _ := config["routing"].(map[string]any)
	if strategy, _ := routing["domainStrategy"].(string); strategy != "" {
		tpl.domainStrategy = strategy
	}
	rules, _ := routing["rules"].([]any)
	for _, raw := range rules {
		rule, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		tag, _ := rule["outboundTag"].(string)
		target := kinds[tag]
		// A rule that names a balancer, or an outbound the group configs will
		// not carry, would make Xray refuse the whole config.
		if target == "" || rule["balancerTag"] != nil {
			continue
		}
		cleaned := shallow(rule)
		delete(cleaned, "outboundTag")
		tpl.rules = append(tpl.rules, templateRule{rule: cleaned, target: target})
	}
	return tpl
}

// planned is a group before it is named.
type planned struct {
	kind     string // "auto", "country", "pinned"
	country  string
	main     []*proxy
	fallback []*proxy
}

// plan decides the groups. It is separate from naming so the rules about
// which server goes where read in one place.
func plan(proxies []*proxy, preferences Preferences) []planned {
	var main, reserve []*proxy
	anyUp := false
	for _, candidate := range proxies {
		if candidate.server.Role != RoleHidden && !candidate.server.Down {
			anyUp = true
		}
	}
	for _, candidate := range proxies {
		// A server whose nodes are all down is left out while anything else
		// answers; when nothing does, the panel may know better than its own
		// node status, and an empty subscription helps nobody.
		if candidate.server.Role == RoleHidden || candidate.server.Down && anyUp {
			continue
		}
		if candidate.server.Role == RoleReserve {
			reserve = append(reserve, candidate)
		} else {
			main = append(main, candidate)
		}
	}

	hidden := setOf(preferences.HiddenCountries)
	excluded := setOf(preferences.AutoExcluded)
	var visible []*proxy
	for _, candidate := range main {
		if !hidden[candidate.server.Country] {
			visible = append(visible, candidate)
		}
	}
	// Hiding every country would leave a subscription that connects nowhere.
	// That is never what somebody meant, so the choice is set aside.
	if len(visible) == 0 {
		visible = main
	}
	var fallback []*proxy
	if preferences.Reserve {
		fallback = reserve
	}

	var groups []planned
	var auto []*proxy
	for _, candidate := range visible {
		if !excluded[candidate.server.Country] {
			auto = append(auto, candidate)
		}
	}
	// In the whitelist mode the automatic group starts on the bypass servers
	// and falls back to the ordinary ones, so the app keeps working on a
	// mobile network that lets only an allowlist through.
	if preferences.WhitelistMode && len(reserve) > 0 {
		groups = append(groups, planned{kind: "auto", main: reserve, fallback: auto})
	} else if len(auto) > 0 {
		groups = append(groups, planned{kind: "auto", main: auto, fallback: fallback})
	}
	order := []string{}
	byCountry := map[string][]*proxy{}
	for _, candidate := range visible {
		country := candidate.server.Country
		if _, seen := byCountry[country]; !seen {
			order = append(order, country)
		}
		byCountry[country] = append(byCountry[country], candidate)
	}
	// The unknown group goes last, after every named country.
	sort.SliceStable(order, func(i, j int) bool {
		return order[i] != UnknownCountry && order[j] == UnknownCountry
	})
	for _, country := range order {
		groups = append(groups, planned{kind: "country", country: country, main: byCountry[country], fallback: fallback})
	}
	// The customer's own server goes first: one exit address the app keeps,
	// falling back to the rest of its country and then the bypass servers.
	if preferences.Pinned != "" {
		for _, candidate := range append(append([]*proxy{}, visible...), fallback...) {
			if !strings.EqualFold(candidate.server.HostUUID, preferences.Pinned) {
				continue
			}
			var rest []*proxy
			for _, other := range byCountry[candidate.server.Country] {
				if other != candidate {
					rest = append(rest, other)
				}
			}
			pinned := planned{kind: "pinned", main: []*proxy{candidate}, fallback: append(rest, fallback...)}
			groups = append([]planned{pinned}, groups...)
			break
		}
	}
	return groups
}

type groupBuilder struct {
	request Request
	tpl     template
	// ai are, for each AI service, the servers in this subscription whose
	// exit node the service answers from. Empty when the AI rule is off.
	ai map[Service][]*proxy
}

// aiPools picks, for each AI service, the servers the customer has whose
// exit node that service answers from — only when the AI rule applies.
func aiPools(groups []planned, request Request) map[Service][]*proxy {
	rule, found := request.Settings.Rule(string(KindAI))
	if !found || !request.Preferences.Applies(rule) || len(request.Checks) == 0 {
		return nil
	}
	var candidates []*proxy
	for _, group := range groups {
		candidates = append(candidates, group.main...)
		candidates = append(candidates, group.fallback...)
	}
	candidates = dedupe(candidates, nil)
	pools := map[Service][]*proxy{}
	for _, service := range AIServices {
		for _, candidate := range candidates {
			check, ok := request.Checks[candidate.server.ExitNode]
			if !ok || candidate.server.ExitNode == "" {
				continue
			}
			if status, known := check.Services[string(service)]; known && status.State == StateYes {
				pools[service] = append(pools[service], candidate)
			}
		}
	}
	return pools
}

func (builder groupBuilder) name(group planned) string {
	switch group.kind {
	case "auto":
		return autoName(builder.request.Settings, builder.request.Locale)
	case "pinned":
		return "📌 " + group.main[0].server.Remark
	default:
		return Flag(group.country) + " " + CountryName(group.country, builder.request.Locale)
	}
}

func (builder groupBuilder) build(group planned, prefix string) map[string]any {
	main := dedupe(group.main, nil)
	fallback := dedupe(group.fallback, main)
	all := append(append([]*proxy{}, main...), fallback...)
	// The AI rule's servers ride along in every group, so its balancers can
	// select them whichever group the app is on.
	for _, service := range AIServices {
		all = append(all, dedupe(builder.ai[service], all)...)
	}
	others := []string{"direct", "block",
		prefix + "-reserve-loopback", prefix + "-main-loopback", prefix + "-sticky-loopback"}
	for _, candidate := range all {
		for _, aux := range candidate.aux {
			others = append(others, aux["tag"].(string))
		}
	}
	tags := safeTags(all, others)
	mainTags := make([]any, 0, len(main))
	for _, candidate := range main {
		mainTags = append(mainTags, tags[candidate])
	}
	fallbackTags := make([]any, 0, len(fallback))
	for _, candidate := range fallback {
		fallbackTags = append(fallbackTags, tags[candidate])
	}
	allTags := make([]any, 0, len(all))
	for _, candidate := range all {
		allTags = append(allTags, tags[candidate])
	}

	entry := prefix + "-balancer"
	var balancers []any
	var extra []any
	var loopRules []any
	if len(fallback) > 0 {
		lastResort := prefix + "-all-balancer"
		balancers = append(balancers, balancer(lastResort, allTags, toleranceFallback, 0, ""))
		toAll := loopback(prefix, "reserve", lastResort)
		extra, loopRules = append(extra, toAll.outbound), append(loopRules, toAll.rule)
		reserveTag := prefix + "-reserve-balancer"
		balancers = append(balancers, balancer(reserveTag, fallbackTags, toleranceFallback, reserveBaselineMS, toAll.tag))
		toReserve := loopback(prefix, "main", reserveTag)
		extra, loopRules = append(extra, toReserve.outbound), append(loopRules, toReserve.rule)
		balancers = append(balancers, balancer(entry, mainTags, tolerance, mainBaselineMS, toReserve.tag))
	} else {
		balancers = append(balancers, balancer(entry, mainTags, toleranceFallback, singleBaselineMS, ""))
	}
	// The kept server goes in front: every connection leaves through it while
	// it answers, so sites see one address instead of a new country on each
	// connection. When it stops answering the group's balancer takes over.
	if kept := keptServer(main, builder.request.StickyKey); kept != nil {
		toEntry := loopback(prefix, "sticky", entry)
		extra, loopRules = append(extra, toEntry.outbound), append(loopRules, toEntry.rule)
		entry = prefix + "-sticky-balancer"
		balancers = append(balancers, map[string]any{
			"tag": entry, "selector": []any{tags[kept]}, "fallbackTag": toEntry.tag,
			"strategy": map[string]any{"type": "leastLoad", "settings": map[string]any{
				"expected": 1, "tolerance": 0, "baselines": []any{strconv.Itoa(stickyBaselineMS) + "ms"},
			}},
		})
	}
	aiBalancers := map[Service]string{}
	for _, service := range AIServices {
		pool := builder.ai[service]
		if len(pool) == 0 {
			continue
		}
		selector := make([]any, 0, len(pool))
		for _, candidate := range pool {
			selector = append(selector, tags[candidate])
		}
		tag := prefix + "-ai-" + string(service)
		aiBalancers[service] = tag
		balancers = append(balancers, balancer(tag, selector, toleranceFallback, 0, ""))
	}

	outbounds := make([]any, 0, len(all)*2+len(extra)+2)
	chained := map[string]bool{}
	for _, candidate := range all {
		copied := shallow(candidate.outbound)
		copied["tag"] = tags[candidate]
		outbounds = append(outbounds, copied)
		for _, aux := range candidate.aux {
			// Two servers of one panel config dial through the same chain.
			if tag := aux["tag"].(string); !chained[tag] {
				chained[tag] = true
				outbounds = append(outbounds, aux)
			}
		}
	}
	outbounds = append(outbounds, extra...)
	outbounds = append(outbounds, systemOutbound(builder.tpl.direct, "direct", "freedom"))
	outbounds = append(outbounds, systemOutbound(builder.tpl.block, "block", "blackhole"))

	config := map[string]any{}
	for key, value := range builder.tpl.inherited {
		config[key] = value
	}
	config["remarks"] = builder.name(group)
	meta := map[string]any{}
	for key, value := range builder.tpl.meta {
		meta[key] = value
	}
	meta["serverDescription"] = describe(len(main), builder.request.Locale)
	config["meta"] = meta
	config["dns"] = builder.tpl.dns
	config["inbounds"] = builder.tpl.inbounds
	config["outbounds"] = outbounds
	config["burstObservatory"] = map[string]any{
		"subjectSelector": allTags,
		"pingConfig": map[string]any{
			"destination": builder.request.Settings.ProbeURL,
			"interval":    strconv.Itoa(builder.request.Settings.ProbeIntervalSeconds) + "s",
			"sampling":    probeSampling,
			"timeout":     probeTimeout,
		},
	}
	rules := append(loopRules, builder.rules(entry, aiBalancers)...)
	config["routing"] = map[string]any{
		"domainStrategy": builder.tpl.domainStrategy,
		"domainMatcher":  "hybrid",
		"balancers":      balancers,
		"rules":          rules,
	}
	return config
}

// rules are the routing rules after the loopback chain, in the order they
// must win: QUIC, the customer's own domains, the service's blocks, the AI
// services, the service's proxied and direct domains, torrents, Russia, the
// panel template's rules, and finally everything else into the group's
// balancer.
func (builder groupBuilder) rules(entry string, ai map[Service]string) []any {
	request := builder.request
	applies := map[RuleKind]ServiceRule{}
	var serviceDomains = map[Action][]any{}
	for _, rule := range request.Settings.Rules {
		if !request.Preferences.Applies(rule) {
			continue
		}
		if rule.Kind != KindDomains {
			applies[rule.Kind] = rule
			continue
		}
		for _, domain := range rule.Domains {
			serviceDomains[rule.Action] = append(serviceDomains[rule.Action], "domain:"+domain)
		}
	}
	field := func(key string, values any, target string, balancer bool) map[string]any {
		rule := map[string]any{"type": "field", key: values}
		if balancer {
			rule["balancerTag"] = target
		} else {
			rule["outboundTag"] = target
		}
		return rule
	}

	var rules []any
	rules = append(rules, field("ip", privateRanges, "direct", false))
	if _, on := applies[KindQUIC]; on {
		rules = append(rules, map[string]any{"type": "field", "network": "udp", "port": "443", "outboundTag": "block"})
	}
	byAction := map[Action][]any{}
	for _, rule := range request.Preferences.Rules {
		byAction[rule.Action] = append(byAction[rule.Action], "domain:"+rule.Domain)
	}
	if domains := byAction[ActionBlock]; len(domains) > 0 {
		rules = append(rules, field("domain", domains, "block", false))
	}
	if domains := byAction[ActionProxy]; len(domains) > 0 {
		rules = append(rules, field("domain", domains, entry, true))
	}
	if domains := byAction[ActionDirect]; len(domains) > 0 {
		rules = append(rules, field("domain", domains, "direct", false))
	}
	if domains := serviceDomains[ActionBlock]; len(domains) > 0 {
		rules = append(rules, field("domain", domains, "block", false))
	}
	if _, on := applies[KindAds]; on && !request.NoGeosite {
		rules = append(rules, field("domain", []any{"geosite:category-ads-all"}, "block", false))
	}
	for _, service := range AIServices {
		tag, ok := ai[service]
		if !ok {
			continue
		}
		domains := make([]any, 0, len(AIDomains[service]))
		for _, domain := range AIDomains[service] {
			domains = append(domains, "domain:"+domain)
		}
		rules = append(rules, field("domain", domains, tag, true))
	}
	if domains := serviceDomains[ActionProxy]; len(domains) > 0 {
		rules = append(rules, field("domain", domains, entry, true))
	}
	if domains := serviceDomains[ActionDirect]; len(domains) > 0 {
		rules = append(rules, field("domain", domains, "direct", false))
	}
	// BitTorrent never goes through the VPN: it runs direct, which is the
	// fastest way, while a rule that sends it direct applies, and is blocked
	// otherwise — the choice of someone who would rather not show their own
	// address to a swarm.
	torrents := "block"
	if rule, on := applies[KindTorrents]; on && rule.Action == ActionDirect {
		torrents = "direct"
	}
	rules = append(rules, field("protocol", []any{"bittorrent"}, torrents, false))
	if _, on := applies[KindRussia]; on {
		rules = append(rules,
			field("domain", russianDomains(request.NoGeosite), "direct", false),
			field("ip", []any{"geoip:ru"}, "direct", false),
		)
	}
	for _, templated := range builder.tpl.rules {
		rule := shallow(templated.rule)
		if request.NoGeosite && !dropGeosite(rule) {
			continue
		}
		switch templated.target {
		case "balancer":
			rule["balancerTag"] = entry
		default:
			rule["outboundTag"] = templated.target
		}
		rules = append(rules, rule)
	}
	return append(rules, map[string]any{"type": "field", "network": "tcp,udp", "balancerTag": entry})
}

// russianDomains are the Russia preset's domains: the national zones, and
// the geosite category that also covers Russian services on other zones.
func russianDomains(noGeosite bool) []any {
	domains := []any{"domain:ru", "domain:su", "domain:xn--p1ai"}
	if !noGeosite {
		domains = append(domains, "geosite:category-ru")
	}
	return domains
}

// dropGeosite removes geosite entries from a rule's domain list, in place on
// the rule's own copy. It reports false when nothing is left of a rule that
// only named geosite, which must then be dropped rather than widened.
func dropGeosite(rule map[string]any) bool {
	domains, ok := rule["domain"].([]any)
	if !ok {
		return true
	}
	kept := make([]any, 0, len(domains))
	for _, domain := range domains {
		if text, _ := domain.(string); !strings.HasPrefix(text, "geosite:") {
			kept = append(kept, domain)
		}
	}
	if len(kept) == 0 {
		return false
	}
	rule["domain"] = kept
	return true
}

// keptServer is the server of a group this subscription stays on, by
// rendezvous hashing: each server scores hash(key, host) and the highest wins.
// The choice is the same on every refresh, and when a server leaves the
// group only the subscriptions that were on it move. Nil for a group of one
// server, which needs no choice, or without a key.
func keptServer(main []*proxy, key string) *proxy {
	if key == "" || len(main) < 2 {
		return nil
	}
	var kept *proxy
	var best uint64
	for _, candidate := range main {
		id := candidate.server.HostUUID
		if id == "" {
			id = candidate.server.Address + ":" + strconv.Itoa(candidate.server.Port) + " " + candidate.server.Remark
		}
		digest := fnv.New64a()
		digest.Write([]byte(key))
		digest.Write([]byte{0})
		digest.Write([]byte(id))
		if score := digest.Sum64(); kept == nil || score > best {
			kept, best = candidate, score
		}
	}
	return kept
}

func balancer(tag string, selector []any, tolerance float64, baselineMS int, fallbackTag string) map[string]any {
	expected := loadExpected
	if len(selector) < expected {
		expected = len(selector)
	}
	if expected < 1 {
		expected = 1
	}
	settings := map[string]any{"expected": expected, "tolerance": tolerance}
	if baselineMS > 0 {
		settings["baselines"] = []any{strconv.Itoa(baselineMS) + "ms"}
	}
	result := map[string]any{
		"tag": tag, "selector": selector,
		"strategy": map[string]any{"type": "leastLoad", "settings": settings},
	}
	if fallbackTag != "" {
		result["fallbackTag"] = fallbackTag
	}
	return result
}

type loopbackLink struct {
	tag      string
	outbound map[string]any
	rule     map[string]any
}

// loopback chains one balancer onto the next: the first balancer's fallback
// is a loopback outbound, which re-enters routing under an inbound tag that a
// rule sends to the next balancer.
func loopback(prefix, kind, target string) loopbackLink {
	tag := prefix + "-" + kind + "-loopback"
	inbound := prefix + "-" + kind + "-lb-in"
	return loopbackLink{
		tag:      tag,
		outbound: map[string]any{"tag": tag, "protocol": "loopback", "settings": map[string]any{"inboundTag": inbound}},
		rule:     map[string]any{"type": "field", "inboundTag": []any{inbound}, "balancerTag": target},
	}
}

func systemOutbound(fromTemplate map[string]any, tag, protocol string) map[string]any {
	if fromTemplate == nil {
		return map[string]any{"tag": tag, "protocol": protocol}
	}
	copied := shallow(fromTemplate)
	copied["tag"] = tag
	return copied
}

// safeTags gives every server a tag that is neither a prefix of another tag
// in the config nor has one as its prefix. Xray's balancer selector matches
// tags by prefix, so "Finland 1" would also select "Finland 10", or a server
// called "g0" every loopback outbound; when any such pair exists, every server
// tag is numbered.
func safeTags(proxies []*proxy, others []string) map[*proxy]string {
	names := make([]string, 0, len(proxies)+len(others))
	for _, candidate := range proxies {
		names = append(names, candidate.outbound["tag"].(string))
	}
	names = append(names, others...)
	sort.Strings(names)
	collision := false
	for index := 1; index < len(names); index++ {
		if strings.HasPrefix(names[index], names[index-1]) {
			collision = true
			break
		}
	}
	tags := make(map[*proxy]string, len(proxies))
	for index, candidate := range proxies {
		tag := candidate.outbound["tag"].(string)
		if collision {
			tag = "s" + strconv.Itoa(index) + "_" + tag
		}
		tags[candidate] = tag
	}
	return tags
}

func dedupe(proxies []*proxy, already []*proxy) []*proxy {
	seen := make(map[*proxy]bool, len(already))
	for _, candidate := range already {
		seen[candidate] = true
	}
	kept := make([]*proxy, 0, len(proxies))
	for _, candidate := range proxies {
		if !seen[candidate] {
			seen[candidate] = true
			kept = append(kept, candidate)
		}
	}
	return kept
}

func shallow(source map[string]any) map[string]any {
	copied := make(map[string]any, len(source))
	for key, value := range source {
		copied[key] = value
	}
	return copied
}

func stringOf(value any) string {
	text, _ := value.(string)
	return text
}

func intOf(value any) int {
	switch typed := value.(type) {
	case json.Number:
		parsed, _ := strconv.Atoi(typed.String())
		return parsed
	case float64:
		return int(typed)
	case string:
		parsed, _ := strconv.Atoi(typed)
		return parsed
	}
	return 0
}
