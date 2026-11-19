package botapp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/omniflow/omniflow/internal/database/dbgen"
	"github.com/omniflow/omniflow/internal/remnawave"
)

// panelEventBatch bounds one pass over the webhook queue.
const panelEventBatch = 100

// panelEventDeadline is how long an event may wait for its notice to settle
// — a quiet-hours deferral, a Telegram pause — before it is given up on.
// A "you connected" message two days late is noise, not news.
const panelEventDeadline = 48 * time.Hour

// panelNotice is the notice one panel event becomes.
type panelNotice struct {
	Kind   string
	Dedupe string
	View   View
}

// panelEventNotice words one event, or reports that it has nothing to say to
// the customer.
func panelEventNotice(
	set notices, locale Locale, id int64, event string, details map[string]any, label string,
) (panelNotice, bool) {
	prefix := subscriptionPrefix(locale, label)
	dedupe := "panel:" + strconv.FormatInt(id, 10)
	connect := row(callbackButton(text(locale, "connect.title.short"), routeConnect))
	menu := row(callbackButton(text(locale, "action.menu"), routeHome))
	devices := row(callbackButton(text(locale, "panel.devicesButton"), routeDevices))
	switch event {
	case remnawave.EventFirstConnected:
		return panelNotice{Kind: "connection", Dedupe: dedupe, View: View{
			Text: prefix + text(locale, "panel.firstConnected"), Keyboard: keyboard(menu),
		}}, true
	case remnawave.EventNotConnected:
		return panelNotice{Kind: "connection", Dedupe: dedupe, View: View{
			Text: prefix + text(locale, "panel.notConnected"), Keyboard: keyboard(connect, menu),
		}}, true
	case remnawave.EventBandwidthReached:
		percent := detailNumber(details, "thresholdPercent")
		if percent <= 0 {
			return panelNotice{}, false
		}
		// The key matches the lifecycle pass's own traffic warning, so a panel
		// threshold that coincides with an Omniflow one is sent once.
		limit := detailNumber(details, "trafficLimitBytes")
		expires, _ := details["expireAt"].(string)
		if parsed, err := time.Parse(time.RFC3339, expires); err == nil {
			expires = parsed.UTC().Format(time.DateOnly)
		}
		return panelNotice{
			Kind: "traffic", Dedupe: fmt.Sprintf("%d:%d:%s", percent, limit, expires),
			View: trafficAlertView(set, locale, percent, label),
		}, true
	case remnawave.EventTrafficReset:
		return panelNotice{Kind: "access", Dedupe: dedupe, View: View{
			Text: prefix + text(locale, "panel.trafficReset"), Keyboard: keyboard(menu),
		}}, true
	case remnawave.EventRevoked:
		return panelNotice{Kind: "access", Dedupe: dedupe, View: View{
			Text: prefix + text(locale, "panel.revoked"), Keyboard: keyboard(connect, menu),
		}}, true
	case remnawave.EventTorrentBlocked:
		return panelNotice{Kind: "access", Dedupe: dedupe, View: View{
			Text: prefix + text(locale, "panel.torrent"), Keyboard: keyboard(menu),
		}}, true
	case remnawave.EventDeviceAdded, remnawave.EventDeviceDeleted:
		key := "panel.deviceAdded"
		if event == remnawave.EventDeviceDeleted {
			key = "panel.deviceDeleted"
		}
		return panelNotice{Kind: "device", Dedupe: dedupe, View: View{
			Text:     prefix + text(locale, key, html.EscapeString(deviceLabel(locale, details))),
			Keyboard: keyboard(devices, menu),
		}}, true
	}
	return panelNotice{}, false
}

// deviceLabel names a device the way the customer will recognise it: their
// own alias when they gave one, then what the client reported about itself.
func deviceLabel(locale Locale, details map[string]any) string {
	alias, _ := details["alias"].(string)
	model, _ := details["deviceModel"].(string)
	platform, _ := details["platform"].(string)
	switch {
	case alias != "":
		return alias
	case model != "" && platform != "" && !strings.EqualFold(model, platform):
		return model + " (" + platform + ")"
	case model != "":
		return model
	case platform != "":
		return platform
	}
	return text(locale, "panel.deviceUnnamed")
}

func detailNumber(details map[string]any, key string) int64 {
	switch value := details[key].(type) {
	case float64:
		return int64(value)
	case int64:
		return value
	}
	return 0
}

// DeliverPanelEvents tells customers what the panel reported about their
// subscriptions. An event stays pending while its notice is deferred or
// retrying, so a quiet-hours deferral is delivered when the window ends
// rather than dropped, and is given up on after panelEventDeadline.
func (notifier *Notifier) DeliverPanelEvents(ctx context.Context) {
	if notifier.store == nil || notifier.store.pool == nil {
		return
	}
	queries := dbgen.New(notifier.store.pool)
	events, err := queries.ListPendingRemnawaveCustomerEvents(ctx, panelEventBatch)
	if err != nil {
		notifier.logger.Warn("panel event lookup failed", "error", err)
		return
	}
	lookup := notifier.candidateLookup()
	for _, event := range events {
		if ctx.Err() != nil || notifier.paused() {
			return
		}
		status := notifier.deliverPanelEvent(ctx, queries, lookup, event)
		if status == "" {
			continue
		}
		if err := queries.CompleteRemnawaveWebhookEvent(ctx, dbgen.CompleteRemnawaveWebhookEventParams{
			ID: event.ID, Status: status,
		}); err != nil {
			notifier.logger.Warn("panel event bookkeeping failed", "error", err)
		}
	}
}

// deliverPanelEvent returns the event's final status, or empty while its
// notice has not settled.
func (notifier *Notifier) deliverPanelEvent(
	ctx context.Context, queries *dbgen.Queries,
	lookup func(ctx context.Context, userID string) (notificationCandidate, bool, error),
	event dbgen.ListPendingRemnawaveCustomerEventsRow,
) string {
	if !event.UserID.Valid {
		// A panel user Omniflow does not know — created in the panel by hand
		// and not imported. There is nobody to tell.
		return "ignored"
	}
	userID := uuidText(event.UserID)
	candidate, found, err := lookup(ctx, userID)
	if err != nil {
		notifier.logger.Warn("panel event recipient lookup failed", "error", err)
		return ""
	}
	if !found {
		return "ignored"
	}
	details := map[string]any{}
	_ = json.Unmarshal(event.Details, &details)
	if event.Event == remnawave.EventRevoked {
		rotated, rotatedErr := queries.CustomerRotatedLinkRecently(ctx, event.UserID)
		if rotatedErr == nil && rotated {
			return "ignored"
		}
	}
	locale := localeFrom(candidate.Locale)
	label := ""
	if event.SubscriptionID.Valid {
		if count, countErr := queries.CountActiveSubscriptionsForCustomer(ctx, event.UserID); countErr == nil && count > 1 {
			label = event.SubscriptionLabel
		}
	}
	notice, ok := panelEventNotice(notifier.notices, locale, event.ID, event.Event, details, label)
	if !ok {
		return "ignored"
	}
	subscriptionID := uuidText(event.SubscriptionID)
	notifier.deliver(ctx, candidate, subscriptionID, notice.Kind, notice.Dedupe, notice.View)

	deliveryStatus, err := queries.GetNotificationDeliveryStatus(ctx, dbgen.GetNotificationDeliveryStatusParams{
		UserID: event.UserID, Kind: notice.Kind, SubscriptionID: event.SubscriptionID, DedupeKey: notice.Dedupe,
	})
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		// Switched off for the installation: deliver never claimed it.
		return "ignored"
	case err != nil:
		return ""
	case deliveryStatus == "sent":
		return "delivered"
	case deliveryStatus == "suppressed":
		return "ignored"
	}
	if time.Since(event.ReceivedAt.Time) > panelEventDeadline {
		return "failed"
	}
	return ""
}
