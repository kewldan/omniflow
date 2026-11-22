package analytics

import (
	"context"
	"regexp"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/omniflow/omniflow/internal/database/dbgen"
)

// Cost categories an operator files a running cost under.
const (
	CostServers  = "servers"
	CostSoftware = "software"
	CostStaff    = "staff"
	CostOther    = "other"
)

var (
	costCategories = map[string]bool{CostServers: true, CostSoftware: true, CostStaff: true, CostOther: true}
	nodeUUIDShape  = regexp.MustCompile(`^[0-9a-f-]{36}$`)
	providerShape  = regexp.MustCompile(`^[a-z0-9_]{1,32}$`)
)

// maxCostSpanDays bounds one cost entry: a two-year contract is entered as
// one cost, anything longer is two.
const maxCostSpanDays = 731

const gibibyte = 1 << 30

// OperatingCost is one recorded running cost.
type OperatingCost struct {
	ID          string    `json:"id"`
	Category    string    `json:"category"`
	NodeUUID    string    `json:"nodeUuid,omitempty"`
	CoversFrom  string    `json:"coversFrom"`
	CoversTo    string    `json:"coversTo"`
	Currency    string    `json:"currency"`
	AmountMinor int64     `json:"amountMinor"`
	Note        string    `json:"note"`
	CreatedBy   string    `json:"createdBy"`
	CreatedAt   time.Time `json:"createdAt"`
}

// OperatingCostInput is a new running cost.
type OperatingCostInput struct {
	Category    string `json:"category"`
	NodeUUID    string `json:"nodeUuid"`
	CoversFrom  string `json:"coversFrom"`
	CoversTo    string `json:"coversTo"`
	Currency    string `json:"currency"`
	AmountMinor int64  `json:"amountMinor"`
	Note        string `json:"note"`
}

// ProviderFee is the rate a payment provider charges from a date on.
type ProviderFee struct {
	ID            string    `json:"id"`
	Provider      string    `json:"provider"`
	EffectiveFrom string    `json:"effectiveFrom"`
	PercentBp     int32     `json:"percentBp"`
	FixedMinor    int64     `json:"fixedMinor"`
	FixedCurrency string    `json:"fixedCurrency,omitempty"`
	Note          string    `json:"note"`
	CreatedBy     string    `json:"createdBy"`
	CreatedAt     time.Time `json:"createdAt"`
}

// ProviderFeeInput is a rate to record.
type ProviderFeeInput struct {
	Provider      string `json:"provider"`
	EffectiveFrom string `json:"effectiveFrom"`
	PercentBp     int32  `json:"percentBp"`
	FixedMinor    int64  `json:"fixedMinor"`
	FixedCurrency string `json:"fixedCurrency"`
	Note          string `json:"note"`
}

// ProviderLine is the provider money one provider took in one currency and
// what it kept of it.
type ProviderLine struct {
	Provider  string `json:"provider"`
	Currency  string `json:"currency"`
	Payments  int64  `json:"payments"`
	PaidMinor int64  `json:"paidMinor"`
	FeeMinor  int64  `json:"feeMinor"`
	// WithoutRate counts payments with no rate entered for their provider
	// and day; their fee is unknown and not in FeeMinor.
	WithoutRate int64 `json:"withoutRate"`
	// EffectiveRate is the fee over the provider's money, present only when
	// every payment had a rate.
	EffectiveRate *float64 `json:"effectiveRate,omitempty"`
}

// CostLine is the part of the running costs of one category that falls in
// the window.
type CostLine struct {
	Category    string `json:"category"`
	Currency    string `json:"currency"`
	AmountMinor int64  `json:"amountMinor"`
}

// CurrencyEconomics is the profit and loss of the window in one currency.
// Every figure is in that currency; nothing is converted.
type CurrencyEconomics struct {
	Currency      string `json:"currency"`
	ProviderMinor int64  `json:"providerMinor"`
	RefundsMinor  int64  `json:"refundsMinor"`
	FeesMinor     int64  `json:"feesMinor"`
	// FeesEstimated is true while some payments have no rate entered: the
	// fee figure then covers only the payments that do.
	FeesEstimated    bool  `json:"feesEstimated"`
	NetRevenueMinor  int64 `json:"netRevenueMinor"`
	OperatingMinor   int64 `json:"operatingMinor"`
	AcquisitionMinor int64 `json:"acquisitionMinor"`
	ReferralMinor    int64 `json:"referralMinor"`
	// ContributionMinor is what is left after every cost Omniflow knows of.
	ContributionMinor int64 `json:"contributionMinor"`
	// Margin is the contribution over the money kept after refunds.
	Margin *float64 `json:"margin,omitempty"`
	// CostPerSubscriberMinor is running costs per average paying
	// subscription per thirty days.
	CostPerSubscriberMinor *int64 `json:"costPerSubscriberMinor,omitempty"`
	// RevenuePerSubscriberMinor is net revenue on the same basis, the figure
	// the cost per subscriber is read against.
	RevenuePerSubscriberMinor *int64 `json:"revenuePerSubscriberMinor,omitempty"`
}

// NodeEconomics is one node's traffic in the window and what it cost.
type NodeEconomics struct {
	NodeUUID        string  `json:"nodeUuid"`
	NodeName        string  `json:"nodeName"`
	CountryCode     string  `json:"countryCode"`
	TrafficBytes    int64   `json:"trafficBytes"`
	PayingCustomers int64   `json:"payingCustomers"`
	Costs           []Money `json:"costs"`
	// CostPerGibibyte is the node's cost over its traffic, per currency of
	// the cost, in minor units; absent where it carried nothing.
	CostPerGibibyte []Money `json:"costPerGibibyte"`
}

// EconomicsReport is the unit economics tab in one read.
type EconomicsReport struct {
	Since       time.Time `json:"since"`
	Until       time.Time `json:"until"`
	Timezone    string    `json:"timezone"`
	GeneratedAt time.Time `json:"generatedAt"`
	// Months is the window's length in thirty-day months.
	Months float64 `json:"months"`
	// AverageSubscribers is the mean daily number of paid and imported
	// subscriptions over the window, in every currency.
	AverageSubscribers *float64            `json:"averageSubscribers,omitempty"`
	Currencies         []CurrencyEconomics `json:"currencies"`
	Providers          []ProviderLine      `json:"providers"`
	Costs              []CostLine          `json:"costs"`
	Nodes              []NodeEconomics     `json:"nodes"`
	// TrafficBytes is what every node carried in the window, and
	// ServerCostPerGibibyte the server costs over it.
	TrafficBytes          int64   `json:"trafficBytes"`
	ServerCostPerGibibyte []Money `json:"serverCostPerGibibyte"`
	// ReferralDays is access the referral programme gave as days.
	ReferralDays int64 `json:"referralDays"`
}

// EconomicsReport reads what the window earned and what it cost.
func (service *Service) EconomicsReport(ctx context.Context, period Period) (EconomicsReport, error) {
	queries := service.queries()
	report := EconomicsReport{
		Since: period.Since, Until: period.Until, Timezone: period.Timezone,
		GeneratedAt: service.now(),
		Months:      period.Until.Sub(period.Since).Hours() / 24 / 30,
		Currencies:  []CurrencyEconomics{}, Providers: []ProviderLine{}, Costs: []CostLine{},
		Nodes: []NodeEconomics{}, ServerCostPerGibibyte: []Money{},
	}
	since, until := timestamp(period.Since), timestamp(period.Until)
	firstDay, lastDay := period.days()
	byCurrency := map[string]*CurrencyEconomics{}
	line := func(currency string) *CurrencyEconomics {
		entry, ok := byCurrency[currency]
		if !ok {
			entry = &CurrencyEconomics{Currency: currency}
			byCurrency[currency] = entry
		}
		return entry
	}

	providers, err := queries.ProviderMoneyAndFees(ctx, dbgen.ProviderMoneyAndFeesParams{Since: since, Until: until})
	if err != nil {
		return report, err
	}
	for _, row := range providers {
		provider := ProviderLine{
			Provider: row.Provider, Currency: row.Currency, Payments: row.Payments,
			PaidMinor: row.PaidMinor, FeeMinor: row.FeeMinor, WithoutRate: row.WithoutRate,
		}
		// The rate is read against all of the provider's money only when
		// every payment had one; otherwise it would read low.
		if row.WithoutRate == 0 && row.PaidMinor > 0 {
			value := float64(row.FeeMinor) / float64(row.PaidMinor)
			provider.EffectiveRate = &value
		}
		report.Providers = append(report.Providers, provider)
		entry := line(row.Currency)
		entry.ProviderMinor += row.PaidMinor
		entry.FeesMinor += row.FeeMinor
		if row.WithoutRate > 0 {
			entry.FeesEstimated = true
		}
	}
	refunds, err := queries.RefundsByCurrency(ctx, dbgen.RefundsByCurrencyParams{Since: since, Until: until})
	if err != nil {
		return report, err
	}
	for _, row := range refunds {
		line(row.Currency).RefundsMinor += row.AmountMinor
	}

	shares, err := queries.OperatingCostShares(ctx, dbgen.OperatingCostSharesParams{
		FromDay: date(firstDay), ToDay: date(lastDay),
	})
	if err != nil {
		return report, err
	}
	categories := map[[2]string]int64{}
	nodeCosts := map[string]map[string]int64{}
	serverCosts := map[string]int64{}
	for _, row := range shares {
		categories[[2]string{row.Category, row.Currency}] += row.AmountMinor
		line(row.Currency).OperatingMinor += row.AmountMinor
		if row.Category == CostServers {
			serverCosts[row.Currency] += row.AmountMinor
		}
		if row.NodeUuid != "" {
			add(nodeCosts, row.NodeUuid, row.Currency, row.AmountMinor)
		}
	}
	for key, amount := range categories {
		report.Costs = append(report.Costs, CostLine{Category: key[0], Currency: key[1], AmountMinor: amount})
	}
	sort.Slice(report.Costs, func(i, j int) bool {
		if report.Costs[i].Currency != report.Costs[j].Currency {
			return report.Costs[i].Currency < report.Costs[j].Currency
		}
		return report.Costs[i].AmountMinor > report.Costs[j].AmountMinor
	})

	spend, err := queries.AcquisitionSpendTotals(ctx, dbgen.AcquisitionSpendTotalsParams{
		FromDay: date(firstDay), ToDay: date(lastDay),
	})
	if err != nil {
		return report, err
	}
	for _, row := range spend {
		line(row.Currency).AcquisitionMinor += row.AmountMinor
	}
	rewards, err := queries.ReferralRewardCosts(ctx, dbgen.ReferralRewardCostsParams{Since: since, Until: until})
	if err != nil {
		return report, err
	}
	for _, row := range rewards {
		report.ReferralDays += row.Days
		if row.AmountMinor > 0 {
			line(row.Currency).ReferralMinor += row.AmountMinor
		}
	}

	snapshots, err := queries.SubscriptionSnapshotsBetween(ctx, dbgen.SubscriptionSnapshotsBetweenParams{
		FromDay: date(dateOf(period.Since)), ToDay: date(dateOf(period.Until.Add(-time.Nanosecond))),
	})
	if err != nil {
		return report, err
	}
	report.AverageSubscribers = averagePaying(snapshots)

	for _, entry := range byCurrency {
		entry.NetRevenueMinor = entry.ProviderMinor - entry.RefundsMinor - entry.FeesMinor
		entry.ContributionMinor = entry.NetRevenueMinor - entry.OperatingMinor - entry.AcquisitionMinor - entry.ReferralMinor
		if kept := entry.ProviderMinor - entry.RefundsMinor; kept > 0 {
			value := float64(entry.ContributionMinor) / float64(kept)
			entry.Margin = &value
		}
		if report.AverageSubscribers != nil && report.Months > 0 {
			units := *report.AverageSubscribers * report.Months
			if entry.OperatingMinor > 0 {
				value := int64(float64(entry.OperatingMinor) / units)
				entry.CostPerSubscriberMinor = &value
			}
			if entry.NetRevenueMinor > 0 {
				value := int64(float64(entry.NetRevenueMinor) / units)
				entry.RevenuePerSubscriberMinor = &value
			}
		}
		report.Currencies = append(report.Currencies, *entry)
	}
	sort.Slice(report.Currencies, func(i, j int) bool {
		return report.Currencies[i].ProviderMinor > report.Currencies[j].ProviderMinor
	})

	if err := report.readNodes(ctx, queries, period, nodeCosts, serverCosts); err != nil {
		return report, err
	}
	return report, nil
}

// readNodes prices each node's traffic with the costs entered against it.
func (report *EconomicsReport) readNodes(
	ctx context.Context, queries *dbgen.Queries, period Period,
	nodeCosts map[string]map[string]int64, serverCosts map[string]int64,
) error {
	traffic, err := queries.NodeTrafficDaily(ctx, dbgen.NodeTrafficDailyParams{
		Since: timestamp(period.Since), Until: timestamp(period.Until), Timezone: period.Timezone,
	})
	if err != nil {
		return err
	}
	nodes := map[string]*NodeEconomics{}
	node := func(uuid string) *NodeEconomics {
		entry, ok := nodes[uuid]
		if !ok {
			entry = &NodeEconomics{NodeUUID: uuid, Costs: []Money{}, CostPerGibibyte: []Money{}}
			nodes[uuid] = entry
		}
		return entry
	}
	for _, row := range traffic {
		entry := node(row.NodeUuid)
		entry.NodeName, entry.CountryCode = row.NodeName, row.CountryCode
		entry.TrafficBytes += row.UsedBytes
		report.TrafficBytes += row.UsedBytes
	}
	quality, err := queries.NodeQuality(ctx, dbgen.NodeQualityParams{
		Since: timestamp(period.Since), Until: timestamp(period.Until), GraceSeconds: RenewalGrace.Seconds(),
	})
	if err != nil {
		return err
	}
	for _, row := range quality {
		if _, known := nodes[row.NodeUuid]; known || nodeCosts[row.NodeUuid] != nil {
			node(row.NodeUuid).PayingCustomers = row.PayingCustomers
		}
	}
	for uuid, costs := range nodeCosts {
		entry := node(uuid)
		for currency, amount := range costs {
			entry.Costs = append(entry.Costs, Money{Currency: currency, AmountMinor: amount})
		}
		sort.Slice(entry.Costs, func(i, j int) bool { return entry.Costs[i].Currency < entry.Costs[j].Currency })
		entry.CostPerGibibyte = perGibibyte(entry.Costs, entry.TrafficBytes)
	}
	names, err := queries.LatestNodeNames(ctx)
	if err != nil {
		return err
	}
	for _, row := range names {
		if entry, ok := nodes[row.NodeUuid]; ok && entry.NodeName == "" {
			entry.NodeName, entry.CountryCode = row.NodeName, row.CountryCode
		}
	}
	for _, entry := range nodes {
		report.Nodes = append(report.Nodes, *entry)
	}
	sort.Slice(report.Nodes, func(i, j int) bool {
		if report.Nodes[i].TrafficBytes != report.Nodes[j].TrafficBytes {
			return report.Nodes[i].TrafficBytes > report.Nodes[j].TrafficBytes
		}
		return report.Nodes[i].NodeName < report.Nodes[j].NodeName
	})
	servers := make([]Money, 0, len(serverCosts))
	for currency, amount := range serverCosts {
		servers = append(servers, Money{Currency: currency, AmountMinor: amount})
	}
	sort.Slice(servers, func(i, j int) bool { return servers[i].Currency < servers[j].Currency })
	report.ServerCostPerGibibyte = perGibibyte(servers, report.TrafficBytes)
	return nil
}

// perGibibyte divides each cost by the traffic it paid for. Below a tenth of
// a gibibyte the figure would be a large number about nothing, and is left
// out.
func perGibibyte(costs []Money, bytes int64) []Money {
	lines := []Money{}
	if bytes < gibibyte/10 {
		return lines
	}
	for _, cost := range costs {
		lines = append(lines, Money{
			Currency: cost.Currency, AmountMinor: int64(float64(cost.AmountMinor) * gibibyte / float64(bytes)),
		})
	}
	return lines
}

// averagePaying is the mean daily count of paid and imported subscriptions
// over the snapshot days, or nil with no snapshot.
func averagePaying(rows []dbgen.SubscriptionSnapshotsBetweenRow) *float64 {
	days := map[string]bool{}
	var total int64
	for _, row := range rows {
		days[dayString(row.Day)] = true
		if row.Kind == "paid" || row.Kind == "imported" {
			total += int64(row.Active)
		}
	}
	if len(days) == 0 {
		return nil
	}
	value := float64(total) / float64(len(days))
	return &value
}

// ListOperatingCosts lists the costs whose span touches the period.
func (service *Service) ListOperatingCosts(ctx context.Context, period Period) ([]OperatingCost, error) {
	firstDay, lastDay := period.days()
	rows, err := service.queries().ListOperatingCosts(ctx, dbgen.ListOperatingCostsParams{
		FromDay: date(firstDay), ToDay: date(lastDay),
	})
	if err != nil {
		return nil, err
	}
	items := make([]OperatingCost, 0, len(rows))
	for _, row := range rows {
		items = append(items, OperatingCost{
			ID: uuidString(row.ID), Category: row.Category, NodeUUID: row.NodeUuid.String,
			CoversFrom: dayString(row.CoversFrom), CoversTo: dayString(row.CoversTo),
			Currency: row.Currency, AmountMinor: row.AmountMinor, Note: row.Note,
			CreatedBy: row.CreatedBy, CreatedAt: row.CreatedAt.Time.UTC(),
		})
	}
	return items, nil
}

// CreateOperatingCost records a running cost with an audit event in the same
// transaction.
func (service *Service) CreateOperatingCost(ctx context.Context, actor Actor, input OperatingCostInput) (OperatingCost, error) {
	params := dbgen.CreateOperatingCostParams{
		Category: strings.ToLower(strings.TrimSpace(input.Category)),
		Currency: strings.ToUpper(strings.TrimSpace(input.Currency)), AmountMinor: input.AmountMinor,
		Note: strings.TrimSpace(input.Note), CreatedBy: actor.AdminID,
		IdempotencyKey: text(strings.TrimSpace(actor.IdempotencyKey)),
	}
	if !costCategories[params.Category] {
		return OperatingCost{}, invalid("the category is servers, software, staff, or other")
	}
	if node := strings.ToLower(strings.TrimSpace(input.NodeUUID)); node != "" {
		if !nodeUUIDShape.MatchString(node) {
			return OperatingCost{}, invalid("the node is a Remnawave node UUID")
		}
		params.NodeUuid = pgtype.Text{String: node, Valid: true}
	}
	if !currencyShape.MatchString(params.Currency) {
		return OperatingCost{}, invalid("the currency is a three-letter code")
	}
	if params.AmountMinor <= 0 {
		return OperatingCost{}, invalid("the amount must be positive")
	}
	if utf8.RuneCountInString(params.Note) > 200 {
		return OperatingCost{}, invalid("the note is at most 200 characters")
	}
	from, err := time.Parse(time.DateOnly, strings.TrimSpace(input.CoversFrom))
	if err != nil {
		return OperatingCost{}, invalid("the first day is YYYY-MM-DD")
	}
	to, err := time.Parse(time.DateOnly, strings.TrimSpace(input.CoversTo))
	if err != nil {
		return OperatingCost{}, invalid("the last day is YYYY-MM-DD")
	}
	if to.Before(from) {
		return OperatingCost{}, invalid("the last day is before the first")
	}
	if to.Sub(from) > maxCostSpanDays*24*time.Hour {
		return OperatingCost{}, invalid("one cost covers at most two years")
	}
	params.CoversFrom, params.CoversTo = date(from), date(to)
	if actor.AdminID == "" {
		return OperatingCost{}, invalid("the change needs an operator")
	}
	if len(params.IdempotencyKey.String) > 128 {
		return OperatingCost{}, invalid("the idempotency key is at most 128 characters")
	}
	var created dbgen.CreateOperatingCostRow
	err = service.inTx(ctx, func(queries *dbgen.Queries) error {
		var txErr error
		created, txErr = queries.CreateOperatingCost(ctx, params)
		if txErr != nil || !created.Inserted {
			return txErr
		}
		return auditTarget(ctx, queries, actor, "operating_cost", "operating_cost.created", uuidString(created.ID), map[string]any{
			"category": created.Category, "currency": created.Currency, "amountMinor": created.AmountMinor,
			"coversFrom": dayString(created.CoversFrom), "coversTo": dayString(created.CoversTo),
			"nodeUuid": created.NodeUuid.String,
		})
	})
	if err != nil {
		return OperatingCost{}, err
	}
	return OperatingCost{
		ID: uuidString(created.ID), Category: created.Category, NodeUUID: created.NodeUuid.String,
		CoversFrom: dayString(created.CoversFrom), CoversTo: dayString(created.CoversTo),
		Currency: created.Currency, AmountMinor: created.AmountMinor, Note: created.Note,
		CreatedBy: created.CreatedBy, CreatedAt: created.CreatedAt.Time.UTC(),
	}, nil
}

// DeleteOperatingCost removes a cost entered by mistake. The audit trail
// keeps what it was.
func (service *Service) DeleteOperatingCost(ctx context.Context, actor Actor, id string) error {
	parsed, err := parseUUID(id)
	if err != nil {
		return err
	}
	return service.inTx(ctx, func(queries *dbgen.Queries) error {
		removed, err := queries.DeleteOperatingCost(ctx, parsed)
		if err != nil {
			return err
		}
		if removed == 0 {
			return ErrNotFound
		}
		return auditTarget(ctx, queries, actor, "operating_cost", "operating_cost.deleted", id, nil)
	})
}

// ListProviderFees lists every rate entered, newest first per provider.
func (service *Service) ListProviderFees(ctx context.Context) ([]ProviderFee, error) {
	rows, err := service.queries().ListProviderFees(ctx)
	if err != nil {
		return nil, err
	}
	items := make([]ProviderFee, 0, len(rows))
	for _, row := range rows {
		items = append(items, providerFee(row))
	}
	return items, nil
}

func providerFee(row dbgen.PaymentProviderFee) ProviderFee {
	return ProviderFee{
		ID: uuidString(row.ID), Provider: row.Provider, EffectiveFrom: dayString(row.EffectiveFrom),
		PercentBp: row.PercentBp, FixedMinor: row.FixedMinor, FixedCurrency: row.FixedCurrency.String,
		Note: row.Note, CreatedBy: row.CreatedBy, CreatedAt: row.CreatedAt.Time.UTC(),
	}
}

// SetProviderFee records a provider's rate from a date on. A rate for a date
// that already has one replaces it, and the audit trail keeps both.
func (service *Service) SetProviderFee(ctx context.Context, actor Actor, input ProviderFeeInput) (ProviderFee, error) {
	params := dbgen.UpsertProviderFeeParams{
		Provider:  strings.ToLower(strings.TrimSpace(input.Provider)),
		PercentBp: input.PercentBp, FixedMinor: input.FixedMinor,
		Note: strings.TrimSpace(input.Note), CreatedBy: actor.AdminID,
	}
	if !providerShape.MatchString(params.Provider) {
		return ProviderFee{}, invalid("choose the payment provider")
	}
	if params.PercentBp < 0 || params.PercentBp > 10000 {
		return ProviderFee{}, invalid("the share is between 0%% and 100%%")
	}
	if params.FixedMinor < 0 {
		return ProviderFee{}, invalid("the fixed amount cannot be negative")
	}
	if params.FixedMinor > 0 {
		currency := strings.ToUpper(strings.TrimSpace(input.FixedCurrency))
		if !currencyShape.MatchString(currency) {
			return ProviderFee{}, invalid("a fixed amount needs its three-letter currency")
		}
		params.FixedCurrency = pgtype.Text{String: currency, Valid: true}
	}
	if utf8.RuneCountInString(params.Note) > 200 {
		return ProviderFee{}, invalid("the note is at most 200 characters")
	}
	from, err := time.Parse(time.DateOnly, strings.TrimSpace(input.EffectiveFrom))
	if err != nil {
		return ProviderFee{}, invalid("the date is YYYY-MM-DD")
	}
	params.EffectiveFrom = date(from)
	if actor.AdminID == "" {
		return ProviderFee{}, invalid("the change needs an operator")
	}
	var saved dbgen.PaymentProviderFee
	err = service.inTx(ctx, func(queries *dbgen.Queries) error {
		var txErr error
		if saved, txErr = queries.UpsertProviderFee(ctx, params); txErr != nil {
			return txErr
		}
		return auditTarget(ctx, queries, actor, "payment_provider_fee", "payment_provider_fee.set", uuidString(saved.ID), map[string]any{
			"provider": saved.Provider, "effectiveFrom": dayString(saved.EffectiveFrom),
			"percentBp": saved.PercentBp, "fixedMinor": saved.FixedMinor,
			"fixedCurrency": saved.FixedCurrency.String,
		})
	})
	if err != nil {
		return ProviderFee{}, err
	}
	return providerFee(saved), nil
}

// DeleteProviderFee removes a rate entered by mistake.
func (service *Service) DeleteProviderFee(ctx context.Context, actor Actor, id string) error {
	parsed, err := parseUUID(id)
	if err != nil {
		return err
	}
	return service.inTx(ctx, func(queries *dbgen.Queries) error {
		removed, err := queries.DeleteProviderFee(ctx, parsed)
		if err != nil {
			return err
		}
		if removed == 0 {
			return ErrNotFound
		}
		return auditTarget(ctx, queries, actor, "payment_provider_fee", "payment_provider_fee.deleted", id, nil)
	})
}
