package botapp

import (
	"sync"
	"time"
)

// DefaultDeliveryRate is how many messages a second the bot sends when the
// operator has not said otherwise. Telegram's documented ceiling for one bot is
// about thirty a second across all chats; twenty-five leaves room for the
// screens customers are tapping through while a broadcast runs.
const DefaultDeliveryRate = 25

// deliveryPacer is the bot-wide budget every outbound message draws from.
//
// Telegram rate-limits a bot as a whole, not per chat, and says so with a 429
// and a retry_after. That answer used to be recorded against whichever
// customer happened to be on the receiving end — their delivery state became
// `failing` with a retry time, and their notifications were deferred — while
// the very next send, to somebody else, ran straight into the same limit. The
// pacer keeps the send rate under the ceiling to begin with, and when Telegram
// asks for a pause anyway it pauses everything, which is what the 429 means.
type deliveryPacer struct {
	mutex       sync.Mutex
	interval    time.Duration
	burst       int
	next        time.Time
	pausedUntil time.Time
	clock       func() time.Time
}

func newDeliveryPacer(perSecond int, clock func() time.Time) *deliveryPacer {
	pacer := &deliveryPacer{clock: clock}
	pacer.setRate(perSecond)
	return pacer
}

// setRate changes the budget. A rate of zero or less keeps the default.
func (pacer *deliveryPacer) setRate(perSecond int) {
	if perSecond <= 0 {
		perSecond = DefaultDeliveryRate
	}
	pacer.mutex.Lock()
	defer pacer.mutex.Unlock()
	pacer.interval = time.Second / time.Duration(perSecond)
	pacer.burst = perSecond
}

// reserve claims the next send slot. It returns how long to wait before
// sending, or — when Telegram has paused the bot for longer than a sender may
// wait inline — the instant the pause ends and no slot.
func (pacer *deliveryPacer) reserve() (time.Duration, time.Time) {
	pacer.mutex.Lock()
	defer pacer.mutex.Unlock()
	now := pacer.clock()
	start := now
	if pacer.pausedUntil.After(now) {
		if pacer.pausedUntil.Sub(now) > maxFloodWait {
			return 0, pacer.pausedUntil
		}
		start = pacer.pausedUntil
	}
	// A generic cell rate: `next` is when the send after this one would be due
	// at an even pace, and up to a second's worth of sends may go ahead of it,
	// so an idle pacer can send a short burst at once without banking more.
	due := pacer.next
	if due.Before(start) {
		due = start
	}
	allowed := due.Add(-time.Duration(pacer.burst-1) * pacer.interval)
	if allowed.Before(start) {
		allowed = start
	}
	pacer.next = due.Add(pacer.interval)
	return allowed.Sub(now), time.Time{}
}

// pause stops every send until the given instant.
func (pacer *deliveryPacer) pause(until time.Time) {
	pacer.mutex.Lock()
	defer pacer.mutex.Unlock()
	if until.After(pacer.pausedUntil) {
		pacer.pausedUntil = until
	}
}

// paused reports whether a pause longer than a sender may wait inline is in
// force, and until when. A delivery pass stops on it rather than walking the
// rest of its batch into the same refusal.
func (pacer *deliveryPacer) paused() (time.Time, bool) {
	pacer.mutex.Lock()
	defer pacer.mutex.Unlock()
	if pacer.pausedUntil.Sub(pacer.clock()) > maxFloodWait {
		return pacer.pausedUntil, true
	}
	return time.Time{}, false
}
