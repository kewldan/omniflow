package smartsub

import (
	"encoding/json"
	"strings"
	"time"
)

// Service is a site whose answer depends on where the exit server's address
// is placed: Google refuses Gemini to an address it places in Russia, Reddit
// turns hosting ranges away unless the visitor signs in, and so on. Omniflow
// learns it from Remnawave's geocheck, which runs on the node itself.
type Service string

const (
	ServiceGemini  Service = "gemini"
	ServiceYouTube Service = "youtube"
	ServiceReddit  Service = "reddit"
	ServiceChatGPT Service = "chatgpt"
	ServiceClaude  Service = "claude"
	ServiceNetflix Service = "netflix"
	ServiceSpotify Service = "spotify"
	ServiceTikTok  Service = "tiktok"
)

// Services are the ones screens list, in order.
var Services = []Service{
	ServiceGemini, ServiceYouTube, ServiceReddit, ServiceChatGPT, ServiceClaude,
	ServiceNetflix, ServiceSpotify, ServiceTikTok,
}

// State is how a service answered from a node.
type State string

const (
	// StateYes opens normally.
	StateYes State = "yes"
	// StateNo refuses: not offered in the country, or Reddit asking to sign
	// in.
	StateNo State = "no"
	// StateLimited opens, but not as the server's country would: YouTube
	// treating the address as Russian, Netflix with its own shows only.
	StateLimited State = "limited"
)

// ServiceStatus is one service's answer. Region is the country the service
// placed the address in, when it says.
type ServiceStatus struct {
	State  State  `json:"state"`
	Region string `json:"region,omitempty"`
}

// NodeCheck is the summary of one node's latest geocheck. Services the check
// could not settle (a timeout, a Cloudflare challenge) are left out.
type NodeCheck struct {
	CheckedAt time.Time                `json:"checkedAt"`
	Services  map[string]ServiceStatus `json:"services"`
	// Place is where the node is; nil when the report did not say.
	Place *Place `json:"place,omitempty"`
	// Floor is the shortest round trip the node makes to anything, in ms.
	Floor float64 `json:"floor,omitempty"`
	// Latency is the node's round trip to each of geocheck's reference sites,
	// in ms, leaving out routes the check flagged as detours.
	Latency map[string]float64 `json:"latency,omitempty"`
}

// geocheckReport is the part of a Remnawave geocheck report Omniflow reads.
type geocheckReport struct {
	Timestamp  time.Time `json:"timestamp"`
	Reputation struct {
		City        string `json:"city"`
		CountryCode string `json:"country_code"`
	} `json:"reputation"`
	Consensus struct {
		IPv4 []struct {
			Code    string  `json:"code"`
			Percent float64 `json:"percent"`
		} `json:"ipv4"`
	} `json:"consensus"`
	Connectivity struct {
		Floor   float64 `json:"latency_floor_ms"`
		Targets []struct {
			ID      string  `json:"id"`
			Verdict string  `json:"verdict"`
			RTT     float64 `json:"rtt_ms"`
		} `json:"targets"`
	} `json:"connectivity"`
	Geo struct {
		Services []struct {
			ID   string `json:"id"`
			IPv4 struct {
				Value string `json:"value"`
				Error string `json:"error"`
			} `json:"ipv4"`
		} `json:"services"`
	} `json:"geo"`
	Stash []struct {
		ID     string `json:"id"`
		State  string `json:"state"`
		Region string `json:"region"`
		Detail string `json:"detail"`
	} `json:"stash_checks"`
	AI []struct {
		ID         string `json:"id"`
		State      string `json:"state"`
		HTTPStatus int    `json:"http_status"`
	} `json:"ai_endpoints"`
}

// SummarizeGeocheck reads a node's geocheck report into what the servers
// screen shows. checkedAt is used when the report carries no time.
func SummarizeGeocheck(report []byte, checkedAt time.Time) (NodeCheck, error) {
	var parsed geocheckReport
	if err := json.Unmarshal(report, &parsed); err != nil {
		return NodeCheck{}, err
	}
	check := NodeCheck{CheckedAt: checkedAt.UTC(), Services: map[string]ServiceStatus{}}
	if !parsed.Timestamp.IsZero() {
		check.CheckedAt = parsed.Timestamp.UTC()
	}
	consensus := ""
	if len(parsed.Consensus.IPv4) > 0 && parsed.Consensus.IPv4[0].Percent > 50 {
		consensus = parsed.Consensus.IPv4[0].Code
	}
	check.Place = locate(parsed.Reputation.City, parsed.Reputation.CountryCode, consensus)
	check.Floor = parsed.Connectivity.Floor
	for _, target := range parsed.Connectivity.Targets {
		// A detour (traffic sent round another continent) says how a route
		// is broken, not where the node is.
		if target.RTT <= 0 || target.Verdict == "detour" || target.Verdict == "failed" {
			continue
		}
		if check.Latency == nil {
			check.Latency = map[string]float64{}
		}
		check.Latency[target.ID] = target.RTT
	}
	set := func(service Service, status ServiceStatus) {
		status.Region = strings.ToUpper(strings.TrimSpace(status.Region))
		check.Services[string(service)] = status
	}

	// The stash checks open the service the way a person would and say
	// whether it is offered.
	for _, entry := range parsed.Stash {
		var service Service
		switch entry.ID {
		case "gemini_access":
			service = ServiceGemini
		case "chatgpt_web":
			service = ServiceChatGPT
		case "claude_access":
			service = ServiceClaude
		case "netflix_access":
			service = ServiceNetflix
		case "tiktok_access":
			service = ServiceTikTok
		default:
			continue
		}
		switch entry.State {
		case "available":
			state := StateYes
			// Netflix without its licensed catalogue still plays its own shows.
			if service == ServiceNetflix && strings.Contains(strings.ToLower(entry.Detail), "original") {
				state = StateLimited
			}
			set(service, ServiceStatus{State: state, Region: entry.Region})
		case "blocked":
			set(service, ServiceStatus{State: StateNo, Region: entry.Region})
		}
	}

	// Claude's page sits behind a Cloudflare challenge the node cannot pass;
	// its API answers 401 to an allowed country and 403 to a refused one.
	if _, settled := check.Services[string(ServiceClaude)]; !settled {
		for _, endpoint := range parsed.AI {
			if endpoint.ID != "anthropic" || endpoint.State != "reachable" {
				continue
			}
			switch endpoint.HTTPStatus {
			case 401:
				set(ServiceClaude, ServiceStatus{State: StateYes})
			case 403:
				set(ServiceClaude, ServiceStatus{State: StateNo})
			}
		}
	}

	for _, entry := range parsed.Geo.Services {
		value := strings.TrimSpace(entry.IPv4.Value)
		switch entry.ID {
		case "youtube":
			if value == "" {
				continue
			}
			// YouTube serving the Russian region means Russian ads and
			// recommendations, and no Premium.
			state := StateYes
			if strings.EqualFold(value, "RU") {
				state = StateLimited
			}
			set(ServiceYouTube, ServiceStatus{State: state, Region: value})
		case "reddit_guest":
			switch value {
			case "yes":
				set(ServiceReddit, ServiceStatus{State: StateYes})
			case "no":
				set(ServiceReddit, ServiceStatus{State: StateNo})
			}
		case "spotify":
			if value != "" {
				set(ServiceSpotify, ServiceStatus{State: StateYes, Region: value})
			}
		}
	}
	return check, nil
}
