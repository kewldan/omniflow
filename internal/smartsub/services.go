package smartsub

import "strings"

// Service is a site whose answer depends on where the exit server's address
// is placed: Google refuses Gemini to an address it places in Russia, Reddit
// asks hosting-provider addresses to sign in, and so on. Whether one works
// through a server is what the operator measured, not something Omniflow
// can tell from the panel.
type Service string

const (
	ServiceGemini  Service = "gemini"
	ServiceYouTube Service = "youtube"
	ServiceReddit  Service = "reddit"
	ServiceChatGPT Service = "chatgpt"
	ServiceClaude  Service = "claude"
)

// Services are the ones a host can be marked for, in the order screens list
// them.
var Services = []Service{ServiceGemini, ServiceYouTube, ServiceReddit, ServiceChatGPT, ServiceClaude}

// ValidService reports whether the name is one of Services.
func ValidService(name string) bool {
	for _, service := range Services {
		if string(service) == name {
			return true
		}
	}
	return false
}

// normalizeServices validates one host's marks: true works, false does not,
// and a service left out was not checked. An empty set is returned as nil so
// an override that marks nothing compares as unchanged.
func normalizeServices(raw map[string]bool) (map[string]bool, error) {
	if len(raw) == 0 {
		return nil, nil
	}
	services := make(map[string]bool, len(raw))
	for name, works := range raw {
		name = strings.ToLower(strings.TrimSpace(name))
		if !ValidService(name) {
			return nil, invalid("hosts", "unknown service "+name)
		}
		services[name] = works
	}
	return services, nil
}

// copyServices hands out a host's marks without sharing the settings' map.
func copyServices(services map[string]bool) map[string]bool {
	if len(services) == 0 {
		return nil
	}
	copied := make(map[string]bool, len(services))
	for name, works := range services {
		copied[name] = works
	}
	return copied
}
