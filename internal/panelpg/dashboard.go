package panelpg

import (
	"context"
	"time"

	"github.com/omniflow/omniflow/internal/database/dbgen"
)

// Metric is one dashboard number together with everything needed to read it
// correctly.
//
// The definition travels with the value rather than living in a tooltip
// somewhere, because the failure mode of an operations dashboard is not a wrong
// number — it is a right number an operator understood to mean something else.
type Metric struct {
	Key string `json:"key"`
	// Definition is a stable identifier the panel resolves to localised copy
	// explaining exactly what was counted.
	Definition string `json:"definition"`
	Value      int64  `json:"value"`
	// Comparison is the same measure over the immediately preceding window of
	// the same length. It is nil for a point-in-time total, where comparing
	// against "the same total a month ago" would mean something the query did
	// not measure.
	Comparison *int64 `json:"comparison,omitempty"`
}

// compared attaches a previous-window figure to a windowed metric.
func compared(metric Metric, previous int64) Metric {
	value := previous
	metric.Comparison = &value
	return metric
}

// RevenueLine separates the three figures that are routinely and wrongly added
// together.
type RevenueLine struct {
	Currency string `json:"currency"`
	// PaidMinor is money that arrived through a payment provider.
	PaidMinor int64 `json:"paidMinor"`
	// WalletMinor is customer balance spent. It was already counted as revenue
	// when the balance was funded, so adding it to PaidMinor counts it twice.
	WalletMinor int64 `json:"walletMinor"`
	// RefundedMinor is money returned. It is reported beside the others rather
	// than subtracted, so a refund-heavy period is visible instead of merely
	// producing a smaller total.
	RefundedMinor int64 `json:"refundedMinor"`
	OrderCount    int64 `json:"orderCount"`
	// PreviousPaidMinor is provider money over the preceding window of the same
	// length, when there was any.
	PreviousPaidMinor *int64 `json:"previousPaidMinor,omitempty"`
}

// Dashboard is the operations overview.
type Dashboard struct {
	// Window is the length of the reporting period every windowed metric used.
	Window time.Duration `json:"-"`
	// GeneratedAt is when the figures were read. The panel shows it as the data
	// freshness, so an operator looking at a stale tab knows it is stale.
	GeneratedAt time.Time `json:"generatedAt"`
	// Timezone is always UTC. Every timestamp in this payload is UTC and the
	// panel converts for display, so two operators in different places never
	// compare numbers that silently covered different days.
	Timezone string `json:"timezone"`

	Customers     []Metric      `json:"customers"`
	Subscriptions []Metric      `json:"subscriptions"`
	Payments      []Metric      `json:"payments"`
	Revenue       []RevenueLine `json:"revenue"`
	Support       []Metric      `json:"support"`
	Operations    []Metric      `json:"operations"`

	// Attention is the short list of things that need somebody to act. It is
	// derived from the metrics above rather than measured separately, so the
	// panel cannot show an alert whose number contradicts the tile beside it.
	Attention []AttentionItem `json:"attention"`
}

// AttentionItem is one required action.
type AttentionItem struct {
	Key      string `json:"key"`
	Severity string `json:"severity"`
	Count    int64  `json:"count"`
	// Href deep-links into the ordinary panel surface that resolves it, rather
	// than offering a shortcut that bypasses the usual confirmation and audit.
	Href string `json:"href"`
}

// Dashboard windows. They are constants rather than parameters because a
// dashboard whose window moves under the operator is a dashboard whose numbers
// cannot be compared between two visits.
const (
	dashboardWindow = 30 * 24 * time.Hour
	// A payment intent that has not moved in this long is stuck rather than in
	// flight: every supported provider either settles or fails well inside it.
	stuckPaymentAfter = 2 * time.Hour
	// A fulfillment operation whose next attempt was due this long ago is not
	// merely queued.
	overdueJobAfter = 15 * time.Minute
	// An open ticket nobody has touched in this long is stale.
	staleTicketAfter = 24 * time.Hour
	// Outbox lag beyond this is a publisher that has stopped rather than a
	// queue that is busy.
	outboxLagAlert = 5 * time.Minute
)

// Dashboard reads the operations overview.
//
// Each aggregate is a separate query rather than one composite statement. They
// touch unrelated tables with unrelated indexes, and joining them would produce
// a plan whose cost is the product of the parts for no gain — the panel renders
// them as separate tiles regardless.
func (service *Service) Dashboard(ctx context.Context) (Dashboard, error) {
	queries := service.queries()
	window := interval(dashboardWindow)

	customers, err := queries.DashboardCustomerTotals(ctx, dbgen.DashboardCustomerTotalsParams{
		Lookback: window, Shift: interval(0),
	})
	if err != nil {
		return Dashboard{}, err
	}
	// The same statement, shifted back by its own length. Using one query for
	// both windows is what stops a comparison drifting from the number it is
	// compared against.
	previousCustomers, err := queries.DashboardCustomerTotals(ctx, dbgen.DashboardCustomerTotalsParams{
		Lookback: window, Shift: window,
	})
	if err != nil {
		return Dashboard{}, err
	}
	subscriptions, err := queries.DashboardSubscriptionTotals(ctx, window)
	if err != nil {
		return Dashboard{}, err
	}
	payments, err := queries.DashboardPaymentHealth(ctx, dbgen.DashboardPaymentHealthParams{
		StuckAfter: interval(stuckPaymentAfter), Lookback: window, Shift: interval(0),
	})
	if err != nil {
		return Dashboard{}, err
	}
	previousPayments, err := queries.DashboardPaymentHealth(ctx, dbgen.DashboardPaymentHealthParams{
		StuckAfter: interval(stuckPaymentAfter), Lookback: window, Shift: window,
	})
	if err != nil {
		return Dashboard{}, err
	}
	revenue, err := queries.DashboardRevenue(ctx, dbgen.DashboardRevenueParams{
		Lookback: window, Shift: interval(0),
	})
	if err != nil {
		return Dashboard{}, err
	}
	previousRevenue, err := queries.DashboardRevenue(ctx, dbgen.DashboardRevenueParams{
		Lookback: window, Shift: window,
	})
	if err != nil {
		return Dashboard{}, err
	}
	support, err := queries.DashboardSupportTotals(ctx, interval(staleTicketAfter))
	if err != nil {
		return Dashboard{}, err
	}
	jobs, err := queries.DashboardJobHealth(ctx, interval(overdueJobAfter))
	if err != nil {
		return Dashboard{}, err
	}
	webhooks, err := queries.DashboardWebhookHealth(ctx, window)
	if err != nil {
		return Dashboard{}, err
	}
	drifts, err := queries.DashboardDriftTotals(ctx)
	if err != nil {
		return Dashboard{}, err
	}
	outbox, err := queries.DashboardOutboxLag(ctx)
	if err != nil {
		return Dashboard{}, err
	}
	openSignals, err := queries.CountOpenAnomalySignals(ctx)
	if err != nil {
		return Dashboard{}, err
	}
	openFlags, err := queries.CountOpenBlocklistMatches(ctx)
	if err != nil {
		return Dashboard{}, err
	}

	dashboard := Dashboard{
		Window:      dashboardWindow,
		GeneratedAt: service.now(),
		Timezone:    "UTC",
		Customers: []Metric{
			{Key: "activeCustomers", Definition: "customer.status.active", Value: customers.ActiveCustomers},
			{Key: "suspendedCustomers", Definition: "customer.status.suspended", Value: customers.SuspendedCustomers},
			{Key: "deletedCustomers", Definition: "customer.status.deleted", Value: customers.DeletedCustomers},
			compared(
				Metric{Key: "newCustomers", Definition: "customer.created.window", Value: customers.NewCustomers},
				previousCustomers.NewCustomers,
			),
		},
		Subscriptions: []Metric{
			{Key: "activeEntitlements", Definition: "entitlement.active", Value: subscriptions.ActiveEntitlements},
			{Key: "limitedEntitlements", Definition: "entitlement.limited", Value: subscriptions.LimitedEntitlements},
			{Key: "lapsedEntitlements", Definition: "entitlement.lapsed", Value: subscriptions.LapsedEntitlements},
			{Key: "renewalsDue", Definition: "entitlement.renewal.window", Value: subscriptions.RenewalsDue},
			// Remnawave owns traffic; this is the counter Omniflow last
			// observed, which is why the definition says "observed".
			{Key: "observedTrafficBytes", Definition: "traffic.observed", Value: subscriptions.ObservedTrafficBytes},
		},
		Payments: []Metric{
			compared(
				Metric{Key: "paymentsSucceeded", Definition: "payment.succeeded.window", Value: payments.Succeeded},
				previousPayments.Succeeded,
			),
			{Key: "paymentsInFlight", Definition: "payment.inflight", Value: payments.InFlight},
			compared(
				Metric{Key: "paymentsFailed", Definition: "payment.failed.window", Value: payments.Failed},
				previousPayments.Failed,
			),
			{Key: "paymentsStuck", Definition: "payment.stuck", Value: payments.Stuck},
		},
		Support: []Metric{
			{Key: "openTickets", Definition: "support.open", Value: support.OpenTickets},
			{Key: "staleTickets", Definition: "support.stale", Value: support.StaleTickets},
		},
		Operations: []Metric{
			{Key: "jobsQueued", Definition: "job.queued", Value: jobs.Queued},
			{Key: "jobsFailed", Definition: "job.failed", Value: jobs.Failed},
			{Key: "jobsOverdue", Definition: "job.overdue", Value: jobs.Overdue},
			{Key: "webhooksUnprocessed", Definition: "webhook.unprocessed.window", Value: webhooks.Unprocessed},
			{Key: "webhooksFailed", Definition: "webhook.failed.window", Value: webhooks.Failed},
			{Key: "webhooksUnverified", Definition: "webhook.unverified.window", Value: webhooks.Unverified},
			{Key: "openDrifts", Definition: "drift.open", Value: drifts.OpenDrifts},
			{Key: "missingRemote", Definition: "drift.missing_remote", Value: drifts.MissingRemote},
			{Key: "outboxUnpublished", Definition: "outbox.unpublished", Value: outbox.Unpublished},
			{Key: "outboxOldestAgeSeconds", Definition: "outbox.oldest_age", Value: outbox.OldestAgeSeconds},
		},
	}

	previousPaid := make(map[string]int64, len(previousRevenue))
	for _, line := range previousRevenue {
		previousPaid[line.Currency] = line.PaidMinor
	}
	for _, line := range revenue {
		entry := RevenueLine{
			Currency:      line.Currency,
			PaidMinor:     line.PaidMinor,
			WalletMinor:   line.WalletMinor,
			RefundedMinor: line.RefundedMinor,
			OrderCount:    line.OrderCount,
		}
		// Only provider money is compared. Wallet spend and refunds move for
		// reasons that make a period-over-period arrow misleading rather than
		// informative.
		if previous, ok := previousPaid[line.Currency]; ok {
			entry.PreviousPaidMinor = &previous
		}
		dashboard.Revenue = append(dashboard.Revenue, entry)
	}

	dashboard.Attention = attentionFrom(payments.Stuck, jobs.Failed, webhooks.Failed,
		drifts.OpenDrifts, outbox.OldestAgeSeconds, openSignals, openFlags, support.StaleTickets)
	return dashboard, nil
}

// attentionFrom turns the measured numbers into the "needs action" list.
//
// A condition appears only when its count is non-zero, so an installation with
// nothing wrong shows an empty list rather than a row of zeroes an operator
// learns to skip past.
func attentionFrom(
	stuckPayments, failedJobs, failedWebhooks, openDrifts, outboxAge, openSignals, openFlags, staleTickets int64,
) []AttentionItem {
	items := make([]AttentionItem, 0, 7)
	add := func(key, severity string, count int64, href string) {
		if count > 0 {
			items = append(items, AttentionItem{Key: key, Severity: severity, Count: count, Href: href})
		}
	}

	// Money first: a stuck payment is a customer who has paid and is waiting.
	add("stuckPayments", "alert", stuckPayments, "/admin/finance?state=pending")
	add("failedJobs", "alert", failedJobs, "/admin/system/jobs?status=failed")
	add("failedWebhooks", "warning", failedWebhooks, "/admin/system/webhooks?status=failed")
	add("openDrifts", "warning", openDrifts, "/admin/system/drift")
	if outboxAge > int64(outboxLagAlert.Seconds()) {
		items = append(items, AttentionItem{
			Key: "outboxLag", Severity: "alert", Count: outboxAge, Href: "/admin/system/jobs",
		})
	}
	add("openAnomalies", "warning", openSignals, "/admin/risk/anomalies?status=open")
	add("openBlocklistMatches", "warning", openFlags, "/admin/risk/matches?status=open")
	add("staleTickets", "warning", staleTickets, "/admin/support?status=open")
	return items
}

// MaintenanceEvent is one recorded activation or recovery.
type MaintenanceEvent struct {
	ID         string    `json:"id"`
	Action     string    `json:"action"`
	Source     string    `json:"source"`
	Reason     string    `json:"reason"`
	ActorType  string    `json:"actorType"`
	ActorID    string    `json:"actorId,omitempty"`
	OccurredAt time.Time `json:"occurredAt"`
}

// RecentIncidents returns the maintenance history the dashboard shows beside
// the health tiles, so an operator can tell "this looks wrong" from "this
// looked wrong for twenty minutes last night and recovered".
func (service *Service) RecentIncidents(ctx context.Context, limit int32) ([]MaintenanceEvent, error) {
	rows, err := service.queries().ListRecentMaintenanceEvents(ctx, pageSize(limit))
	if err != nil {
		return nil, err
	}
	events := make([]MaintenanceEvent, 0, len(rows))
	for _, row := range rows {
		events = append(events, MaintenanceEvent{
			ID:         uuidString(row.ID),
			Action:     row.Action,
			Source:     row.Source,
			Reason:     row.Reason,
			ActorType:  row.ActorType,
			ActorID:    textValue(row.ActorID),
			OccurredAt: timeValue(row.OccurredAt),
		})
	}
	return events, nil
}
