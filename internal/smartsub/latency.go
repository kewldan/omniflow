package smartsub

import (
	"math"
	"sort"
	"strings"
)

// The customer's screen times each server from the browser, which only
// reaches the server's entry. The estimate shown is the round trip a page
// sees through the tunnel on an open connection:
//
//	ping ≈ entry + chain + site + 1 ms per node passed
//
// entry comes from the browser's figure (see PingModel), chain is the hop
// between the node a host lands on and the node it leaves from, and site is
// how far the exit node is from the sites people open. Calibrated on
// 2026-11-24 against ICMP and TCP timings from Moscow and Germany: within a
// few milliseconds for TCP hosts, apart from addresses Russian filtering
// slows the TLS handshake to.
const (
	// TransportTCP hosts are reached over TCP and TLS (VLESS with Reality or
	// TLS, and the like); TransportUDP over QUIC (Hysteria).
	TransportTCP = "tcp"
	TransportUDP = "udp"

	// kmPerMs turns the great-circle distance between two nodes into round
	// trip time: light in fibre covers 100 km per millisecond of round trip,
	// and real routes run about 2.2 times the straight line (PL→NL 1093 km,
	// 24 ms; PL→CH 1268 km, 30 ms).
	kmPerMs = 45.0
	// nodeMs is what one proxy node adds to a round trip.
	nodeMs = 1.0
	// colocatedShare is how many of the sites that tell places apart must
	// answer two nodes within colocatedSlackMs (or 15%) for the nodes to count
	// as one data centre.
	colocatedShare   = 0.8
	colocatedSlackMs = 3.0
	colocatedMinimum = 5
	// informativeMs leaves out sites both nodes reach within it: a site with
	// a point of presence next to both says nothing about where they are.
	informativeMs = 5.0
)

// TransportOf is how a client reaches an inbound of the given protocol.
func TransportOf(protocol string) string {
	switch strings.ToLower(strings.TrimSpace(protocol)) {
	case "":
		return ""
	case "hysteria", "hysteria2", "tuic", "wireguard":
		return TransportUDP
	default:
		return TransportTCP
	}
}

// PingModel is what the customer's browser needs to turn its timing of a
// server into an estimate: how the server is reached, and the rest of the
// round trip past its entry.
//
// The browser times a failed image load. Over TCP that is the connection plus
// the TLS server's first answer, two round trips and a few milliseconds of
// Reality passing the handshake on: entry ≈ (t − 5) / 2. A UDP-only server
// refuses the TCP connection at once, one round trip: entry ≈ t.
type PingModel struct {
	Transport string `json:"transport"`
	// TailMs is chain + site + processing, rounded to a millisecond.
	TailMs int `json:"tailMs"`
}

// SiteMs is how far the node is from the sites people open: the median of
// its round trips to geocheck's reference sites.
func (check NodeCheck) SiteMs() (float64, bool) {
	if len(check.Latency) == 0 {
		return 0, false
	}
	values := make([]float64, 0, len(check.Latency))
	for _, value := range check.Latency {
		values = append(values, value)
	}
	sort.Float64s(values)
	middle := len(values) / 2
	if len(values)%2 == 1 {
		return values[middle], true
	}
	return (values[middle-1] + values[middle]) / 2, true
}

// HopMs estimates the round trip between two nodes. Nodes in one data centre
// answer the sites that tell places apart alike, and are a latency floor
// apart; others are as far apart as their places. It is false when neither
// says: no shared sites to compare and a node with no place.
func HopMs(a, b NodeCheck) (float64, bool) {
	informative, close := 0, 0
	for site, left := range a.Latency {
		right, ok := b.Latency[site]
		if !ok || math.Max(left, right) < informativeMs {
			continue
		}
		informative++
		if math.Abs(left-right) <= math.Max(colocatedSlackMs, 0.15*math.Max(left, right)) {
			close++
		}
	}
	if informative >= colocatedMinimum && float64(close) >= colocatedShare*float64(informative) {
		return math.Max(math.Max(a.Floor, b.Floor), nodeMs), true
	}
	if a.Place == nil || b.Place == nil {
		return 0, false
	}
	return distanceKm(*a.Place, *b.Place) / kmPerMs, true
}

// Ping builds the model for a server from its nodes' checks. It is nil when
// the estimate would be a guess: an unknown transport, an exit node never
// checked, or a chain whose hop cannot be told.
func Ping(server Server, checks map[string]NodeCheck) *PingModel {
	if server.Transport == "" || server.ExitNode == "" {
		return nil
	}
	exit, ok := checks[server.ExitNode]
	if !ok {
		return nil
	}
	site, ok := exit.SiteMs()
	if !ok {
		return nil
	}
	tail := site + nodeMs
	if server.EntryNode != "" && server.EntryNode != server.ExitNode {
		entry, ok := checks[server.EntryNode]
		if !ok {
			return nil
		}
		hop, ok := HopMs(entry, exit)
		if !ok {
			return nil
		}
		tail += hop + nodeMs
	} else if server.EntryNode == "" {
		// A host on several nodes with a chosen exit: where it lands is not
		// known, so neither is the hop.
		return nil
	}
	return &PingModel{Transport: server.Transport, TailMs: int(math.Round(tail))}
}
