package accountpg

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/omniflow/omniflow/internal/commerce"
	"github.com/omniflow/omniflow/internal/database/dbgen"
	"github.com/omniflow/omniflow/internal/remnawave"
)

// Traffic is a subscription's allowance and what has been used of it.
//
// `Unlimited` is carried explicitly rather than being inferred from a zero
// limit. A progress bar with no ceiling is not a bar at 0%, and a screen that
// has to guess which of those a zero means will eventually guess wrong.
type Traffic struct {
	UsedBytes  int64
	LimitBytes int64
	Unlimited  bool
	// Percent is the used fraction, clamped to 0..100 and meaningless when
	// Unlimited. It is computed here rather than in React so the bar and the
	// textual equivalent can never disagree.
	Percent int
}

// DeviceUsage is the device slot count for a subscription.
type DeviceUsage struct {
	Used      int
	Limit     int
	Unlimited bool
}

// Subscription is one of the customer's subscriptions as the panel shows it.
type Subscription struct {
	ID    string
	Slot  int
	Label string
	Plan  string
	// Phase is the single customer-visible state the panel renders from, taken
	// from internal/commerce so the bot and the web agree on what "grace" means.
	Phase       commerce.SubscriptionPhase
	Status      string
	EndsAt      time.Time
	DaysLeft    int
	GracePeriod time.Duration
	Traffic     Traffic
	Devices     DeviceUsage
	Provisioned bool
	// Live reports whether the Remnawave figures in this record were actually
	// fetched. When false, traffic and devices are whatever Omniflow last
	// observed and the panel says so rather than presenting stale numbers as
	// current.
	Live bool
}

// Notice is a service-wide message the dashboard shows above everything else.
//
// It exists so an incident is explained where the customer is already looking,
// rather than being inferred from a subscription that has quietly stopped
// working. The wording is the operator's own, in the customer's language.
type Notice struct {
	Active bool
	// Source is how maintenance was entered — an operator, or an automatic
	// dependency probe. The panel does not show it; it is here so the same value
	// the operator panel reports is the one the customer surface saw.
	Source  string
	Message string
	// ExpectedReturnAt is zero when the operator gave no estimate, which the
	// panel renders as "we will update this" rather than as a fabricated time.
	ExpectedReturnAt time.Time
}

// Overview is everything the dashboard needs in one response.
type Overview struct {
	Customer      Customer
	Subscriptions []Subscription
	// Notice carries a maintenance or incident message when one is active.
	Notice Notice
	// ShowSwitcher is true only when the installation actually allows several
	// concurrent subscriptions. A single-subscription installation renders one
	// screen with no selection step, which is the behaviour v0.5 committed to.
	ShowSwitcher bool
	// Degraded reports that Remnawave could not be reached for at least one
	// subscription, so the page can explain the gap instead of silently showing
	// old numbers.
	Degraded bool
}

// Customer is the profile the panel shows and lets the customer edit.
type Customer struct {
	ID       string
	Locale   string
	Timezone string
	Status   string
}

// Overview reads the dashboard.
//
// Remnawave is queried once per provisioned subscription. A failure on any one
// of them degrades that subscription to Omniflow's own record rather than
// failing the page: a customer whose upstream panel is down still needs to see
// what they bought and when it ends.
func (service *Service) Overview(ctx context.Context, customerID, locale string) (Overview, error) {
	userID, err := parseUUID(customerID)
	if err != nil {
		return Overview{}, err
	}
	queries := dbgen.New(service.pool)

	user, err := queries.GetCustomer(ctx, userID)
	if err != nil {
		return Overview{}, err
	}
	rows, err := queries.ListAccountSubscriptions(ctx, dbgen.ListAccountSubscriptionsParams{
		UserID: userID, Locale: normalizeLocale(locale, user.Locale),
	})
	if err != nil {
		return Overview{}, err
	}

	overview := Overview{
		Customer: Customer{
			ID: customerID, Locale: user.Locale, Timezone: user.Timezone, Status: user.Status,
		},
		Subscriptions: make([]Subscription, 0, len(rows)),
	}
	for _, row := range rows {
		subscription, live := service.projectSubscription(ctx, subscriptionRow(row))
		overview.Subscriptions = append(overview.Subscriptions, subscription)
		if subscription.Provisioned && !live {
			overview.Degraded = true
		}
	}

	// The switcher follows the operator's configuration, not the number of rows:
	// a customer who happens to hold two subscriptions while the setting is off
	// is a migration state, and hiding the switcher there would strand one of
	// them.
	settings, err := queries.GetCommerceSettings(ctx)
	switch {
	case err == nil:
		overview.ShowSwitcher = settings.MultiSubscriptionEnabled
	case errors.Is(err, pgx.ErrNoRows):
		overview.ShowSwitcher = false
	default:
		return Overview{}, err
	}
	if len(overview.Subscriptions) > 1 {
		overview.ShowSwitcher = true
	}

	// A maintenance failure never fails the page: the dashboard is more useful
	// without the banner than not at all, and the banner is an explanation rather
	// than a control.
	if notice, noticeErr := service.notice(ctx, normalizeLocale(locale, user.Locale)); noticeErr == nil {
		overview.Notice = notice
	} else {
		service.logger.Warn("maintenance state unavailable", "error", noticeErr)
	}
	return overview, nil
}

// notice reads the maintenance banner in the customer's own language.
func (service *Service) notice(ctx context.Context, locale string) (Notice, error) {
	var (
		state    commerce.Maintenance
		expected pgtype.Timestamptz
	)
	err := service.pool.QueryRow(ctx,
		`SELECT active, source, reason, notice_ru, notice_en, expected_return_at
		 FROM maintenance_state WHERE singleton`,
	).Scan(&state.Active, &state.Source, &state.Reason, &state.NoticeRU, &state.NoticeEN, &expected)
	if errors.Is(err, pgx.ErrNoRows) {
		return Notice{}, nil
	}
	if err != nil {
		return Notice{}, err
	}
	if !state.Active {
		return Notice{}, nil
	}

	message := state.NoticeEN
	if locale == "ru" && strings.TrimSpace(state.NoticeRU) != "" {
		message = state.NoticeRU
	}
	// The reason is an operator-facing note and is deliberately not shown; an
	// empty notice leaves the panel to render its own localized wording.
	return Notice{
		Active: true, Source: state.Source, Message: strings.TrimSpace(message),
		ExpectedReturnAt: expected.Time.UTC(),
	}, nil
}

// Subscription reads one of the customer's own subscriptions.
func (service *Service) Subscription(
	ctx context.Context, customerID, subscriptionID, locale string,
) (Subscription, error) {
	row, err := service.subscriptionRecord(ctx, customerID, subscriptionID, locale)
	if err != nil {
		return Subscription{}, err
	}
	subscription, _ := service.projectSubscription(ctx, row)
	return subscription, nil
}

// record is the database half of a subscription, before Remnawave is consulted.
type record struct {
	ID     string
	Slot   int
	Label  string
	Plan   string
	Status string
	EndsAt time.Time
	// PausedAt is when the current pause began, zero when running. Every
	// remaining-time figure the customer sees is measured from it.
	PausedAt       time.Time
	GracePeriod    time.Duration
	RemnawaveID    int64
	TrafficLimit   int64
	TrafficLimited bool
	DeviceLimit    int
	DeviceLimited  bool
}

func subscriptionRow(row dbgen.ListAccountSubscriptionsRow) record {
	return record{
		ID: uuidString(row.ID), Slot: int(row.Slot), Label: row.Label, Plan: row.PlanName,
		Status: row.EntitlementStatus, EndsAt: row.EndsAt.Time.UTC(),
		PausedAt:     row.PausedAt.Time.UTC(),
		GracePeriod:  time.Duration(row.GracePeriodSeconds) * time.Second,
		RemnawaveID:  row.RemnawaveUserID.Int64,
		TrafficLimit: row.TrafficAllowanceBytes.Int64, TrafficLimited: row.TrafficAllowanceBytes.Valid,
		DeviceLimit: int(row.DeviceLimit.Int32), DeviceLimited: row.DeviceLimit.Valid,
	}
}

func (service *Service) subscriptionRecord(
	ctx context.Context, customerID, subscriptionID, locale string,
) (record, error) {
	userID, err := parseUUID(customerID)
	if err != nil {
		return record{}, err
	}
	target, err := parseUUID(subscriptionID)
	if err != nil {
		return record{}, ErrNotFound
	}
	queries := dbgen.New(service.pool)
	user, err := queries.GetCustomer(ctx, userID)
	if err != nil {
		return record{}, err
	}
	row, err := queries.GetAccountSubscription(ctx, dbgen.GetAccountSubscriptionParams{
		UserID: userID, SubscriptionID: target, Locale: normalizeLocale(locale, user.Locale),
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return record{}, ErrNotFound
	}
	if err != nil {
		return record{}, err
	}
	return subscriptionRow(dbgen.ListAccountSubscriptionsRow(row)), nil
}

// projectSubscription joins one database record to Remnawave's live state.
func (service *Service) projectSubscription(ctx context.Context, row record) (Subscription, bool) {
	subscription := Subscription{
		ID: row.ID, Slot: row.Slot, Label: row.Label, Plan: row.Plan,
		Status: row.Status, EndsAt: row.EndsAt, GracePeriod: row.GracePeriod,
		Provisioned: row.RemnawaveID > 0,
		Traffic:     Traffic{LimitBytes: row.TrafficLimit, Unlimited: !row.TrafficLimited || row.TrafficLimit <= 0},
		Devices:     DeviceUsage{Limit: row.DeviceLimit, Unlimited: !row.DeviceLimited},
	}

	live := false
	if subscription.Provisioned && service.remnawave != nil {
		if user, err := service.remnawave.User(ctx, row.RemnawaveID); err == nil {
			live = true
			subscription.Traffic.UsedBytes = user.Traffic.UsedBytes
			if user.TrafficLimitBytes > 0 {
				subscription.Traffic.LimitBytes = user.TrafficLimitBytes
				subscription.Traffic.Unlimited = false
			}
			if user.HWIDDeviceLimit != nil {
				subscription.Devices.Limit = *user.HWIDDeviceLimit
				subscription.Devices.Unlimited = false
			}
			if devices, deviceErr := service.remnawave.Devices(ctx, row.RemnawaveID); deviceErr == nil {
				subscription.Devices.Used = devices.Total
			}
		} else {
			service.logger.Warn("remnawave state unavailable for a customer subscription",
				"subscription", row.ID, "error", err)
		}
	}
	subscription.Live = live
	subscription.Traffic.Percent = trafficPercent(subscription.Traffic)

	now := service.now()
	state := commerce.Subscription{
		Status: row.Status, EndsAt: row.EndsAt, GracePeriod: row.GracePeriod,
		TrafficUsedBytes: subscription.Traffic.UsedBytes, TrafficLimitBytes: subscription.Traffic.LimitBytes,
		PausedAt: row.PausedAt,
	}
	subscription.Phase = commerce.EvaluatePhase(now, state)
	// Measured from the pause instant while paused, so the figure the customer
	// reads stays the figure they were promised instead of counting down while
	// nothing is being used.
	subscription.DaysLeft = daysLeft(commerce.ClockNow(now, state), row.EndsAt)
	return subscription, live
}

// trafficPercent is the used fraction as a whole number, clamped so an overage
// renders as a full bar rather than overflowing it.
func trafficPercent(traffic Traffic) int {
	if traffic.Unlimited || traffic.LimitBytes <= 0 {
		return 0
	}
	percent := int(traffic.UsedBytes * 100 / traffic.LimitBytes)
	if percent < 0 {
		return 0
	}
	if percent > 100 {
		return 100
	}
	return percent
}

// daysLeft rounds up, because a subscription with eight hours left has a day
// left from the customer's point of view, not zero.
func daysLeft(now, endsAt time.Time) int {
	if endsAt.IsZero() || !endsAt.After(now) {
		return 0
	}
	remaining := endsAt.Sub(now)
	days := int(remaining / (24 * time.Hour))
	if remaining%(24*time.Hour) > 0 {
		days++
	}
	return days
}

// normalizeLocale falls back to the customer's stored preference, then to the
// installation default, so a plan name is never blank because of a locale the
// catalogue has no localization for.
func normalizeLocale(requested, stored string) string {
	requested = strings.ToLower(strings.TrimSpace(requested))
	if requested == "ru" || requested == "en" {
		return requested
	}
	if stored == "ru" || stored == "en" {
		return stored
	}
	return "en"
}

// SubscriptionURL reads the access link for one subscription.
//
// It is fetched on demand rather than included in the overview: the link is the
// credential that grants access, and a dashboard response that always carried it
// would put it in every cache, log, and browser history entry that touches the
// page.
func (service *Service) SubscriptionURL(
	ctx context.Context, customerID, subscriptionID string,
) (remnawave.Subscription, error) {
	row, err := service.subscriptionRecord(ctx, customerID, subscriptionID, "")
	if err != nil {
		return remnawave.Subscription{}, err
	}
	if row.RemnawaveID <= 0 {
		return remnawave.Subscription{}, ErrNotProvisioned
	}
	if service.remnawave == nil {
		return remnawave.Subscription{}, ErrRemnawaveUnavailable
	}
	subscription, err := service.remnawave.Subscription(ctx, row.RemnawaveID)
	if err != nil {
		return remnawave.Subscription{}, ErrRemnawaveUnavailable
	}
	return subscription, nil
}
