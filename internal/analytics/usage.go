package analytics

import (
	"context"
	"sort"
	"time"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/omniflow/omniflow/internal/database/dbgen"
	"github.com/omniflow/omniflow/internal/remnawave"
)

// UserLister is the part of the Remnawave adapter the usage census needs.
type UserLister interface {
	ListUsers(ctx context.Context, start, size int) ([]remnawave.User, int, error)
	AllDevices(ctx context.Context, start, size int) ([]remnawave.PanelDevice, int, error)
}

const censusPage = 500

// activeDeviceWindow is how recently a device must have connected to count
// against its user's limit, the same window the device overflow report uses.
const activeDeviceWindow = 30 * 24 * time.Hour

// CensusUsage records what Remnawave reports about every user's connections:
// first connection, last time online, last node, and devices against the
// limit. It reads the panel and writes only its own observation table, so a
// failed page leaves the previous reading in place.
func (service *Service) CensusUsage(ctx context.Context, panel UserLister) (int, error) {
	now := service.now()
	devices := map[int64]int32{}
	for start := 0; ; start += censusPage {
		page, total, err := panel.AllDevices(ctx, start, censusPage)
		if err != nil {
			return 0, err
		}
		for _, device := range page {
			if now.Sub(device.UpdatedAt) <= activeDeviceWindow {
				devices[device.UserID]++
			}
		}
		if len(page) < censusPage || start+len(page) >= total || ctx.Err() != nil {
			break
		}
	}
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	queries := service.queries()
	recorded := 0
	for start := 0; ; start += censusPage {
		users, total, err := panel.ListUsers(ctx, start, censusPage)
		if err != nil {
			return recorded, err
		}
		params := censusParams(users, devices)
		if len(params.RemnawaveUserIds) > 0 {
			if err := queries.UpsertVpnUsage(ctx, params); err != nil {
				return recorded, err
			}
			recorded += len(params.RemnawaveUserIds)
		}
		if len(users) < censusPage || start+len(users) >= total || ctx.Err() != nil {
			return recorded, ctx.Err()
		}
	}
}

// censusParams turns one page of panel users into the batch upsert. A user
// that never connected but has an online moment is taken to have first
// connected then: an older panel may not report the first connection itself.
func censusParams(users []remnawave.User, devices map[int64]int32) dbgen.UpsertVpnUsageParams {
	params := dbgen.UpsertVpnUsageParams{
		RemnawaveUserIds: []int64{}, FirstConnectedAts: []pgtype.Timestamptz{},
		OnlineAts: []pgtype.Timestamptz{}, LastNodeUuids: []string{},
		ActiveDevices: []int32{}, DeviceLimits: []int32{},
	}
	for _, user := range users {
		if user.ID <= 0 {
			continue
		}
		first := user.Traffic.FirstConnectedAt
		if first == nil {
			first = user.Traffic.OnlineAt
		}
		limit := int32(-1)
		if user.HWIDDeviceLimit != nil && *user.HWIDDeviceLimit >= 0 {
			limit = int32(*user.HWIDDeviceLimit)
		}
		params.RemnawaveUserIds = append(params.RemnawaveUserIds, user.ID)
		params.FirstConnectedAts = append(params.FirstConnectedAts, optionalTime(first))
		params.OnlineAts = append(params.OnlineAts, optionalTime(user.Traffic.OnlineAt))
		params.LastNodeUuids = append(params.LastNodeUuids, user.Traffic.LastConnectedNodeUUID)
		params.ActiveDevices = append(params.ActiveDevices, devices[user.ID])
		params.DeviceLimits = append(params.DeviceLimits, limit)
	}
	return params
}

func optionalTime(moment *time.Time) pgtype.Timestamptz {
	if moment == nil || moment.IsZero() {
		return pgtype.Timestamptz{}
	}
	return timestamp(*moment)
}

// TrialActivation is what became of the trials claimed in the window.
type TrialActivation struct {
	Trials             int64 `json:"trials"`
	Connected          int64 `json:"connected"`
	ConnectedWithinDay int64 `json:"connectedWithinDay"`
	// ConnectionRate is the share of trials whose customer connected.
	ConnectionRate *float64 `json:"connectionRate,omitempty"`
	// MedianHoursToConnect is the median time from claim to first
	// connection, for the customers who connected after claiming.
	MedianHoursToConnect *float64 `json:"medianHoursToConnect,omitempty"`
	// ConvertedConnected and ConvertedUnconnected are the trials whose
	// customer has paid since, split by whether they connected; the two
	// rates are the reason the screen exists.
	ConvertedConnected        int64    `json:"convertedConnected"`
	ConvertedUnconnected      int64    `json:"convertedUnconnected"`
	ConnectedConversionRate   *float64 `json:"connectedConversionRate,omitempty"`
	UnconnectedConversionRate *float64 `json:"unconnectedConversionRate,omitempty"`
}

// PurchaseActivation is whether the customers who paid for the first time in
// the window went on to connect.
type PurchaseActivation struct {
	Payers             int64    `json:"payers"`
	Connected          int64    `json:"connected"`
	ConnectedWithinDay int64    `json:"connectedWithinDay"`
	ConnectionRate     *float64 `json:"connectionRate,omitempty"`
}

// PayingUsage is how the customers paying now use the service.
type PayingUsage struct {
	Customers int64 `json:"customers"`
	// Observed is how many of them the census has a reading for.
	Observed        int64    `json:"observed"`
	OnlineWeek      int64    `json:"onlineWeek"`
	OnlineMonth     int64    `json:"onlineMonth"`
	NeverConnected  int64    `json:"neverConnected"`
	ActiveRate      *float64 `json:"activeRate,omitempty"`
	AtDeviceLimit   int64    `json:"atDeviceLimit"`
	OverDeviceLimit int64    `json:"overDeviceLimit"`
}

// NodeQualityLine is one node's paying customers, their renewals, and their
// support tickets.
type NodeQualityLine struct {
	NodeUUID        string   `json:"nodeUuid"`
	NodeName        string   `json:"nodeName"`
	CountryCode     string   `json:"countryCode"`
	PayingCustomers int64    `json:"payingCustomers"`
	PeriodsEnded    int64    `json:"periodsEnded"`
	PeriodsRenewed  int64    `json:"periodsRenewed"`
	RenewalRate     *float64 `json:"renewalRate,omitempty"`
	Tickets         int64    `json:"tickets"`
	// TicketsPerHundred is tickets per hundred paying customers.
	TicketsPerHundred *float64 `json:"ticketsPerHundred,omitempty"`
}

// UsageReport is the usage tab in one read.
type UsageReport struct {
	Since       time.Time `json:"since"`
	Until       time.Time `json:"until"`
	Timezone    string    `json:"timezone"`
	GeneratedAt time.Time `json:"generatedAt"`
	// ObservedAt is when the census last read the panel; absent before its
	// first pass, and every figure here is empty until then.
	ObservedAt *time.Time         `json:"observedAt,omitempty"`
	Trials     TrialActivation    `json:"trials"`
	Purchases  PurchaseActivation `json:"purchases"`
	Paying     PayingUsage        `json:"paying"`
	Nodes      []NodeQualityLine  `json:"nodes"`
}

// UsageReport reads activation, current use, and the state of each node's
// customers.
func (service *Service) UsageReport(ctx context.Context, period Period) (UsageReport, error) {
	queries := service.queries()
	report := UsageReport{
		Since: period.Since, Until: period.Until, Timezone: period.Timezone,
		GeneratedAt: service.now(), Nodes: []NodeQualityLine{},
	}
	observed, err := queries.UsageObservedAt(ctx)
	if err != nil {
		return report, err
	}
	if observed.Valid {
		moment := observed.Time.UTC()
		report.ObservedAt = &moment
	}
	since, until := timestamp(period.Since), timestamp(period.Until)

	trials, err := queries.TrialActivation(ctx, dbgen.TrialActivationParams{Since: since, Until: until})
	if err != nil {
		return report, err
	}
	report.Trials = TrialActivation{
		Trials: trials.Trials, Connected: trials.Connected, ConnectedWithinDay: trials.ConnectedWithinDay,
		ConnectionRate:            rate(trials.Connected, trials.Trials),
		ConvertedConnected:        trials.ConnectedPaid,
		ConvertedUnconnected:      trials.UnconnectedPaid,
		ConnectedConversionRate:   rate(trials.ConnectedPaid, trials.Connected),
		UnconnectedConversionRate: rate(trials.UnconnectedPaid, trials.Trials-trials.Connected),
	}
	if trials.MedianHours >= 0 {
		value := trials.MedianHours
		report.Trials.MedianHoursToConnect = &value
	}

	purchases, err := queries.PurchaseActivation(ctx, dbgen.PurchaseActivationParams{Since: since, Until: until})
	if err != nil {
		return report, err
	}
	report.Purchases = PurchaseActivation{
		Payers: purchases.Payers, Connected: purchases.Connected,
		ConnectedWithinDay: purchases.ConnectedWithinDay,
		ConnectionRate:     rate(purchases.Connected, purchases.Payers),
	}

	paying, err := queries.PayingBaseUsage(ctx)
	if err != nil {
		return report, err
	}
	report.Paying = PayingUsage{
		Customers: paying.Customers, Observed: paying.Observed,
		OnlineWeek: paying.OnlineWeek, OnlineMonth: paying.OnlineMonth,
		NeverConnected: paying.NeverConnected,
		ActiveRate:     rate(paying.OnlineWeek, paying.Observed),
		AtDeviceLimit:  paying.AtDeviceLimit, OverDeviceLimit: paying.OverDeviceLimit,
	}

	quality, err := queries.NodeQuality(ctx, dbgen.NodeQualityParams{
		Since: since, Until: until, GraceSeconds: RenewalGrace.Seconds(),
	})
	if err != nil {
		return report, err
	}
	names, err := queries.LatestNodeNames(ctx)
	if err != nil {
		return report, err
	}
	known := map[string]dbgen.LatestNodeNamesRow{}
	for _, row := range names {
		known[row.NodeUuid] = row
	}
	for _, row := range quality {
		line := NodeQualityLine{
			NodeUUID: row.NodeUuid, NodeName: known[row.NodeUuid].NodeName,
			CountryCode:     known[row.NodeUuid].CountryCode,
			PayingCustomers: row.PayingCustomers, PeriodsEnded: row.PeriodsEnded,
			PeriodsRenewed: row.PeriodsRenewed, Tickets: row.Tickets,
			RenewalRate: rate(row.PeriodsRenewed, row.PeriodsEnded),
		}
		if row.PayingCustomers > 0 {
			value := float64(row.Tickets) * 100 / float64(row.PayingCustomers)
			line.TicketsPerHundred = &value
		}
		// A node with neither paying customers nor ended periods says
		// nothing about quality.
		if line.PayingCustomers == 0 && line.PeriodsEnded == 0 {
			continue
		}
		report.Nodes = append(report.Nodes, line)
	}
	sort.SliceStable(report.Nodes, func(i, j int) bool {
		return report.Nodes[i].PayingCustomers > report.Nodes[j].PayingCustomers
	})
	return report, nil
}
