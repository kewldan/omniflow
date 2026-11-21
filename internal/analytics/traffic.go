package analytics

import (
	"context"
	"sort"
	"time"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/omniflow/omniflow/internal/database/dbgen"
	"github.com/omniflow/omniflow/internal/remnawave"
)

// TrafficRetention is how long node samples are kept: a little over a year,
// so this month can be read against the same month last year.
const TrafficRetention = 400 * 24 * time.Hour

// NodeLister is the part of the Remnawave adapter the sampler needs.
type NodeLister interface {
	Nodes(ctx context.Context) ([]remnawave.Node, error)
}

// SampleNodes records every node's counters as Remnawave reports them now,
// truncated to the hour so two passes in one hour write one sample, and
// prunes samples older than TrafficRetention.
func (service *Service) SampleNodes(ctx context.Context, panel NodeLister) (int, error) {
	nodes, err := panel.Nodes(ctx)
	if err != nil {
		return 0, err
	}
	now := service.now()
	at := now.Truncate(time.Hour)
	queries := service.queries()
	for _, node := range nodes {
		if node.UUID == "" {
			continue
		}
		online := pgtype.Int4{}
		if node.UsersOnline != nil {
			online = pgtype.Int4{Int32: int32(*node.UsersOnline), Valid: true}
		}
		if err := queries.InsertNodeTrafficSample(ctx, dbgen.InsertNodeTrafficSampleParams{
			NodeUuid: node.UUID, SampledAt: timestamp(at), NodeName: node.Name,
			CountryCode: node.CountryCode, Connected: node.IsConnected && !node.IsDisabled,
			TrafficUsedBytes:  max(node.TrafficUsedBytes, 0),
			TrafficLimitBytes: max(node.TrafficLimitBytes, 0),
			UsersOnline:       online,
		}); err != nil {
			return 0, err
		}
	}
	if _, err := queries.PruneNodeTrafficSamples(ctx, timestamp(now.Add(-TrafficRetention))); err != nil {
		return len(nodes), err
	}
	return len(nodes), nil
}

// TrafficDay is one node's traffic on one day.
type TrafficDay struct {
	Day        string `json:"day"`
	UsedBytes  int64  `json:"usedBytes"`
	PeakOnline int32  `json:"peakOnline"`
}

// TrafficNode is one node across the window.
type TrafficNode struct {
	NodeUUID    string       `json:"nodeUuid"`
	NodeName    string       `json:"nodeName"`
	CountryCode string       `json:"countryCode"`
	LimitBytes  int64        `json:"limitBytes"`
	TotalBytes  int64        `json:"totalBytes"`
	PeakOnline  int32        `json:"peakOnline"`
	Days        []TrafficDay `json:"days"`
}

// TrafficTotal is every node's traffic on one day.
type TrafficTotal struct {
	Day       string `json:"day"`
	UsedBytes int64  `json:"usedBytes"`
}

// TrafficHistory is the traffic tab's history in one read.
type TrafficHistory struct {
	Since       time.Time      `json:"since"`
	Until       time.Time      `json:"until"`
	Timezone    string         `json:"timezone"`
	GeneratedAt time.Time      `json:"generatedAt"`
	Nodes       []TrafficNode  `json:"nodes"`
	Totals      []TrafficTotal `json:"totals"`
}

// TrafficHistory reads the stored samples as traffic per node per day.
func (service *Service) TrafficHistory(ctx context.Context, period Period) (TrafficHistory, error) {
	history := TrafficHistory{
		Since: period.Since, Until: period.Until, Timezone: period.Timezone,
		GeneratedAt: service.now(), Nodes: []TrafficNode{}, Totals: []TrafficTotal{},
	}
	rows, err := service.queries().NodeTrafficDaily(ctx, dbgen.NodeTrafficDailyParams{
		Since: timestamp(period.Since), Until: timestamp(period.Until), Timezone: period.Timezone,
	})
	if err != nil {
		return history, err
	}
	nodes := map[string]*TrafficNode{}
	totals := map[string]int64{}
	var order []string
	for _, row := range rows {
		node, ok := nodes[row.NodeUuid]
		if !ok {
			node = &TrafficNode{NodeUUID: row.NodeUuid, Days: []TrafficDay{}}
			nodes[row.NodeUuid] = node
			order = append(order, row.NodeUuid)
		}
		// The latest name and limit win: a node renamed in the window is
		// shown by what it is called now.
		node.NodeName, node.CountryCode, node.LimitBytes = row.NodeName, row.CountryCode, row.LimitBytes
		day := dayString(row.Day)
		node.Days = append(node.Days, TrafficDay{Day: day, UsedBytes: row.UsedBytes, PeakOnline: row.PeakOnline})
		node.TotalBytes += row.UsedBytes
		node.PeakOnline = max(node.PeakOnline, row.PeakOnline)
		totals[day] += row.UsedBytes
	}
	for _, id := range order {
		history.Nodes = append(history.Nodes, *nodes[id])
	}
	sort.SliceStable(history.Nodes, func(i, j int) bool { return history.Nodes[i].TotalBytes > history.Nodes[j].TotalBytes })
	for day, used := range totals {
		history.Totals = append(history.Totals, TrafficTotal{Day: day, UsedBytes: used})
	}
	sort.Slice(history.Totals, func(i, j int) bool { return history.Totals[i].Day < history.Totals[j].Day })
	return history, nil
}
