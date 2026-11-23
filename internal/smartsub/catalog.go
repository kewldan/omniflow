package smartsub

import (
	"strconv"
	"strings"
	"unicode/utf8"
)

// Host is one Remnawave host as the module needs it.
type Host struct {
	UUID     string
	Remark   string
	Address  string
	Port     int
	Nodes    []string
	Disabled bool
	Hidden   bool
}

// Catalog is the panel's hosts and which of its nodes are down, read once a
// minute or so and shared by every request.
type Catalog struct {
	Hosts []Host
	// DownNodes are nodes that are disconnected or disabled.
	DownNodes map[string]bool
}

// Server is one server in a subscription, placed: which host it is, which
// country it belongs to, and what the operator made of it.
type Server struct {
	Remark   string
	Address  string
	Port     int
	HostUUID string
	// Country is an ISO code, or UnknownCountry.
	Country string
	// Detected is the country read from the remark, before any override.
	Detected string
	Role     Role
	// Down is true when every node behind the host is down.
	Down bool
}

// Classify places one server. The host is found by its remark first, which is
// what the subscription carries, and by its address and port when the remark
// was rendered from a template and no longer matches.
func (catalog Catalog) Classify(remark, address string, port int, settings Settings) Server {
	server := Server{Remark: remark, Address: address, Port: port, Role: RoleMain}
	host, found := catalog.find(remark, address, port)
	server.Detected = DetectCountry(remark)
	if found {
		server.HostUUID = host.UUID
		if server.Detected == "" {
			server.Detected = DetectCountry(host.Remark)
		}
		server.Down = catalog.down(host)
	}
	server.Country = server.Detected
	if found {
		if override, ok := settings.Hosts[strings.ToLower(host.UUID)]; ok {
			if override.Country != "" {
				server.Country = override.Country
			}
			if override.Role != "" {
				server.Role = override.Role
			}
		}
	}
	if server.Country == "" {
		server.Country = UnknownCountry
	}
	return server
}

func (catalog Catalog) find(remark, address string, port int) (Host, bool) {
	for _, host := range catalog.Hosts {
		if host.Remark == remark {
			return host, true
		}
	}
	if address == "" {
		return Host{}, false
	}
	var match Host
	matches := 0
	for _, host := range catalog.Hosts {
		if strings.EqualFold(host.Address, address) && host.Port == port {
			match = host
			matches++
		}
	}
	return match, matches == 1
}

func (catalog Catalog) down(host Host) bool {
	if len(host.Nodes) == 0 || len(catalog.DownNodes) == 0 {
		return false
	}
	for _, node := range host.Nodes {
		if !catalog.DownNodes[node] {
			return false
		}
	}
	return true
}

// CountryName is the country's name in the customer's language: Russian for
// Russian and Ukrainian speakers, English for everyone else.
func CountryName(code, locale string) string {
	russian := prefersRussian(locale)
	if code == UnknownCountry {
		if russian {
			return "Другие"
		}
		return "Other"
	}
	for _, row := range countryTable {
		if row.code == code {
			if russian {
				return row.russian
			}
			return row.english
		}
	}
	return code
}

// Flag is the flag emoji of an ISO code, or a globe for UnknownCountry.
func Flag(code string) string {
	if len(code) != 2 || code == UnknownCountry {
		return "🌐"
	}
	return string([]rune{0x1F1E6 + rune(code[0]-'A'), 0x1F1E6 + rune(code[1]-'A')})
}

func prefersRussian(locale string) bool {
	locale = strings.ToLower(locale)
	return strings.HasPrefix(locale, "ru") || strings.HasPrefix(locale, "uk")
}

func autoName(settings Settings, locale string) string {
	if settings.AutoName != "" {
		return settings.AutoName
	}
	if prefersRussian(locale) {
		return "⚡ Самый быстрый"
	}
	return "⚡ Fastest"
}

func reserveName(settings Settings, locale string) string {
	if settings.ReserveName != "" {
		return settings.ReserveName
	}
	if prefersRussian(locale) {
		return "🛡 Обход белых списков"
	}
	return "🛡 Whitelist bypass"
}

// describe is the line a client shows under a group's name, within the 30
// characters Happ allows.
func describe(count int, locale string) string {
	var text string
	if prefersRussian(locale) {
		text = "Серверов: " + strconv.Itoa(count)
	} else {
		text = "Servers: " + strconv.Itoa(count)
	}
	if utf8.RuneCountInString(text) > 30 {
		return string([]rune(text)[:30])
	}
	return text
}
