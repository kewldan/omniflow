package botapp

import (
	"context"
	"time"

	"github.com/omniflow/omniflow/internal/smartsub"
	"github.com/omniflow/omniflow/internal/smartsubpg"
)

// outageTrackInterval is how often node status is read for outages.
const outageTrackInterval = time.Minute

// WithSmartSubscription lets the notifier tell customers when a country they
// use goes down and comes back. Without it no such message is sent.
func (notifier *Notifier) WithSmartSubscription(service *smartsubpg.Service) *Notifier {
	notifier.smartSubscription = service
	return notifier
}

// deliverOutages records which countries are down and sends the queued
// messages, a batch per pass. A "down" for an outage that ended before it
// went out is dropped: telling somebody about a problem that is already
// over helps nobody.
func (notifier *Notifier) deliverOutages(ctx context.Context) int {
	if notifier.smartSubscription == nil {
		return 0
	}
	// The broadcast loop runs every half minute and may drain several times
	// in a row; asking the panel for node status once a minute is enough.
	if now := time.Now(); now.Sub(notifier.outagesTrackedAt) >= outageTrackInterval {
		notifier.outagesTrackedAt = now
		if err := notifier.smartSubscription.TrackOutages(ctx); err != nil {
			notifier.logger.Error("server outage tracking failed", "error", err)
		}
	}
	notices, err := notifier.smartSubscription.PendingOutageNotices(ctx, notificationBatch)
	if err != nil {
		notifier.logger.Error("server outage notice lookup failed", "error", err)
		return 0
	}
	lookup := notifier.candidateLookup()
	for _, notice := range notices {
		if ctx.Err() != nil || notifier.paused() {
			return len(notices)
		}
		if notice.Kind == "down" && !notice.Open {
			notifier.markOutage(ctx, notice)
			continue
		}
		candidate, found, lookupErr := lookup(ctx, notice.UserID)
		if lookupErr != nil {
			notifier.logger.Error("notification candidate lookup failed", "error", lookupErr)
			return len(notices)
		}
		if found {
			locale := localeFrom(candidate.Locale)
			notifier.deliver(ctx, candidate, "", "server", notice.OutageID+":"+notice.Kind, outageView(locale, notice))
		}
		notifier.markOutage(ctx, notice)
	}
	return len(notices)
}

func (notifier *Notifier) markOutage(ctx context.Context, notice smartsubpg.OutageNotice) {
	if err := notifier.smartSubscription.MarkOutageNotice(ctx, notice); err != nil {
		notifier.logger.Error("server outage bookkeeping failed", "error", err)
	}
}

// outageView is the message: the country's flag and name in the customer's
// language, and what it means for them.
func outageView(locale Locale, notice smartsubpg.OutageNotice) View {
	key := "server.down"
	if notice.Kind == "up" {
		key = "server.up"
	}
	name := smartsub.CountryName(notice.Country, string(locale))
	return View{
		Text:     text(locale, key, smartsub.Flag(notice.Country), name),
		Keyboard: keyboard(row(callbackButton(text(locale, "action.menu"), routeHome))),
	}
}
