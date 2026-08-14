package httpapi

import (
	"errors"
	"net/http"
	"sort"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/omniflow/omniflow/internal/rbac"
	"github.com/omniflow/omniflow/internal/remnawave"
)

// Traffic reporting.
//
// Remnawave owns traffic, nodes, and connections. Nothing here is stored: both
// halves of this report are read live from the panel on each request, and there
// is no table in this repository for a node or for a byte a customer used.
// Caching it would be the first step towards Omniflow having an opinion about
// traffic, which decision 0004 exists to prevent.
//
// What Omniflow adds is the join — a Remnawave user identifier resolved to the
// customer who holds it — and that is the entire contribution.

// maxTrafficScan bounds how much of the user base one report reads.
//
// Finding the heaviest consumers means scanning every user, and the panel pages
// at five hundred. Ten pages is enough for most installations and short enough
// that the request finishes; a larger installation gets the top consumers of
// what was scanned, and the response says how many that was so the screen can
// say so too. A silently truncated ranking would read as a complete one.
const (
	trafficPageSize = 500
	maxTrafficPages = 10
	maxTrafficScan  = trafficPageSize * maxTrafficPages
	// topConsumers is how many rows the report returns. A ranking longer than a
	// screen is a list nobody reads to the end of.
	topConsumers = 50
)

// mountTraffic registers the traffic report.
//
// It needs `customers.read` rather than `system.read`, because the half of it
// that matters names customers and their consumption. The node figures travel
// with it; they are the less sensitive half.
func (handlers *AdminHandlers) mountTraffic(secure chi.Router) {
	if handlers.operations == nil {
		return
	}
	secure.With(handlers.requirePermission(rbac.PermissionCustomersRead)).Group(func(read chi.Router) {
		read.Get("/reports/traffic", handlers.trafficReport)
		read.Get("/reports/traffic/export", handlers.exportTrafficReport)
	})
}

// nodeLine is one node as the panel renders it.
type nodeLine struct {
	Name        string `json:"name"`
	CountryCode string `json:"countryCode,omitempty"`
	Connected   bool   `json:"connected"`
	Disabled    bool   `json:"disabled"`
	UsedBytes   int64  `json:"usedBytes"`
	LimitBytes  int64  `json:"limitBytes"`
	// UsedShare is used ÷ limit, absent when the node has no limit. A node with
	// no limit cannot be "filling up", and rendering 0% for it would put it at
	// the bottom of a list sorted by pressure when it belongs off that list.
	UsedShare   *float64 `json:"usedShare,omitempty"`
	UsersOnline *int     `json:"usersOnline,omitempty"`
}

// consumerLine is one heavy user, with whatever Omniflow knows about them.
type consumerLine struct {
	RemnawaveID int64  `json:"remnawaveId"`
	Username    string `json:"username"`
	UsedBytes   int64  `json:"usedBytes"`
	// LifetimeBytes is everything the user has ever used, against UsedBytes
	// which resets with the billing cycle. Both are shown because "this month"
	// and "ever" are different questions and the second explains the first.
	LifetimeBytes int64 `json:"lifetimeBytes"`
	LimitBytes    int64 `json:"limitBytes"`
	// CustomerID is empty for a Remnawave user Omniflow did not create, which is
	// a real state — provisioned directly in the panel, or an import that has
	// not run — and is shown as unattributed rather than given an owner.
	CustomerID string `json:"customerId,omitempty"`
	Label      string `json:"label,omitempty"`
	Status     string `json:"status,omitempty"`
}

type trafficReport struct {
	// Nodes is absent, not empty, when the panel does not expose the route. The
	// two look identical on a screen and mean opposite things.
	Nodes         []nodeLine     `json:"nodes"`
	NodesReported bool           `json:"nodesReported"`
	NodesDetail   string         `json:"nodesDetail,omitempty"`
	Consumers     []consumerLine `json:"consumers"`
	// Scanned and Total say how much of the user base the ranking covers. When
	// they differ the screen says so, because a truncated ranking presented as a
	// complete one is worse than no ranking.
	Scanned int `json:"scanned"`
	Total   int `json:"total"`
}

func (handlers *AdminHandlers) trafficReport(writer http.ResponseWriter, request *http.Request) {
	report, err := handlers.collectTraffic(request)
	handlers.respond(writer, request, report, err)
}

// collectTraffic reads both halves from the panel.
func (handlers *AdminHandlers) collectTraffic(request *http.Request) (trafficReport, error) {
	report := trafficReport{Nodes: []nodeLine{}, Consumers: []consumerLine{}}
	if handlers.remnawave == nil {
		// No panel is configured, so there is nothing to report and nothing to
		// apologise for beyond saying so.
		report.NodesDetail = "remnawave_not_configured"
		return report, nil
	}
	context := request.Context()

	nodes, err := handlers.remnawave.Nodes(context)
	switch {
	case err == nil:
		report.NodesReported = true
		report.Nodes = nodeLines(nodes)
	case errors.Is(err, remnawave.ErrNotFound):
		// A panel that does not expose the route is not a failure. It is an
		// answer, and the screen renders it as one.
		report.NodesDetail = "nodes_unsupported"
	default:
		handlers.logger.Warn("remnawave node listing failed", "error", err)
		report.NodesDetail = "nodes_unavailable"
	}

	consumers, scanned, total, err := handlers.topConsumers(request)
	if err != nil {
		return trafficReport{}, err
	}
	report.Consumers, report.Scanned, report.Total = consumers, scanned, total
	return report, nil
}

func nodeLines(nodes []remnawave.Node) []nodeLine {
	lines := make([]nodeLine, 0, len(nodes))
	for _, node := range nodes {
		line := nodeLine{
			Name: node.Name, CountryCode: node.CountryCode,
			Connected: node.IsConnected, Disabled: node.IsDisabled,
			UsedBytes: node.TrafficUsedBytes, LimitBytes: node.TrafficLimitBytes,
			UsersOnline: node.UsersOnline,
		}
		if node.TrafficLimitBytes > 0 {
			share := float64(node.TrafficUsedBytes) / float64(node.TrafficLimitBytes)
			line.UsedShare = &share
		}
		lines = append(lines, line)
	}
	// Nodes under pressure first, then the unlimited ones by absolute use. The
	// question this report exists to answer is "which node is filling up", and
	// the answer belongs at the top.
	sort.SliceStable(lines, func(first, second int) bool {
		left, right := lines[first], lines[second]
		if (left.UsedShare != nil) != (right.UsedShare != nil) {
			return left.UsedShare != nil
		}
		if left.UsedShare != nil && *left.UsedShare != *right.UsedShare {
			return *left.UsedShare > *right.UsedShare
		}
		return left.UsedBytes > right.UsedBytes
	})
	return lines
}

// topConsumers pages the panel's user list and ranks by traffic used.
func (handlers *AdminHandlers) topConsumers(
	request *http.Request,
) (lines []consumerLine, scanned, total int, err error) {
	context := request.Context()
	users := make([]remnawave.User, 0, trafficPageSize)

	for page := 0; page < maxTrafficPages; page++ {
		batch, reported, listErr := handlers.remnawave.ListUsers(
			context, page*trafficPageSize, trafficPageSize)
		if listErr != nil {
			handlers.logger.Warn("remnawave user listing failed", "error", listErr)
			// A partial scan is still a useful ranking, and refusing the whole
			// report because page seven timed out would be worse. What must not
			// happen is presenting it as complete, which `scanned` prevents.
			break
		}
		users = append(users, batch...)
		total = reported
		if len(batch) < trafficPageSize || len(users) >= maxTrafficScan {
			break
		}
	}
	scanned = len(users)
	if total < scanned {
		total = scanned
	}

	sort.SliceStable(users, func(first, second int) bool {
		return users[first].Traffic.UsedBytes > users[second].Traffic.UsedBytes
	})
	if len(users) > topConsumers {
		users = users[:topConsumers]
	}

	identifiers := make([]int64, 0, len(users))
	for _, user := range users {
		identifiers = append(identifiers, user.ID)
	}
	owners, err := handlers.operations.CustomersByRemnawaveIDs(context, identifiers)
	if err != nil {
		return nil, 0, 0, err
	}

	lines = make([]consumerLine, 0, len(users))
	for _, user := range users {
		line := consumerLine{
			RemnawaveID: user.ID, Username: user.Username,
			UsedBytes:     user.Traffic.UsedBytes,
			LifetimeBytes: user.Traffic.LifetimeBytes,
			LimitBytes:    user.TrafficLimitBytes,
		}
		if owner, known := owners[user.ID]; known {
			line.CustomerID, line.Label, line.Status = owner.CustomerID, owner.Label, owner.Status
		}
		lines = append(lines, line)
	}
	return lines, scanned, total, nil
}

// exportTrafficReport writes the same figures as CSV.
func (handlers *AdminHandlers) exportTrafficReport(writer http.ResponseWriter, request *http.Request) {
	report, err := handlers.collectTraffic(request)
	if err != nil {
		handlers.respond(writer, request, nil, err)
		return
	}

	writer.Header().Set("Content-Type", "text/csv; charset=utf-8")
	writer.Header().Set("Content-Disposition", `attachment; filename="traffic.csv"`)
	writer.WriteHeader(http.StatusOK)

	write := func(fields ...string) {
		encoded := make([]string, 0, len(fields))
		for _, field := range fields {
			encoded = append(encoded, csvField(field))
		}
		_, _ = writer.Write([]byte(strings.Join(encoded, ",") + "\n"))
	}

	// Bytes, unscaled. A gigabyte is 1000^3 to some readers and 1024^3 to
	// others, and picking one in an export would make the file wrong for half
	// of them.
	write("section", "name", "detail", "used_bytes", "lifetime_bytes", "limit_bytes",
		"customer_id", "status")
	for _, node := range report.Nodes {
		detail := "connected"
		if node.Disabled {
			detail = "disabled"
		} else if !node.Connected {
			detail = "offline"
		}
		write("node", node.Name, detail,
			strconv.FormatInt(node.UsedBytes, 10), "",
			strconv.FormatInt(node.LimitBytes, 10), "", "")
	}
	for _, consumer := range report.Consumers {
		write("consumer", consumer.Username, consumer.Label,
			strconv.FormatInt(consumer.UsedBytes, 10),
			strconv.FormatInt(consumer.LifetimeBytes, 10),
			strconv.FormatInt(consumer.LimitBytes, 10),
			consumer.CustomerID, consumer.Status)
	}
}
